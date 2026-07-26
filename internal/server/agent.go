package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"grok_switch/internal/agentbridge"
	"grok_switch/internal/routing"
)

type AgentService interface {
	Status() agentbridge.Status
	Start(context.Context, agentbridge.StartOptions) error
	Stop() error
	NewSession(context.Context, string) error
	Prompt(string, []agentbridge.Attachment) error
	CancelPrompt() error
	Subscribe() (string, <-chan agentbridge.Event)
	Unsubscribe(string)
	RespondPermission(string, bool) error
	RespondPermissionEx(string, bool, bool) error
	SetSessionAutoApprove(bool)
	SetMcpServers([]acp.McpServer)
	McpServers() []acp.McpServer
	ListStoredSessions(string, int) ([]agentbridge.SessionSummary, error)
	StoredSessionHistory(string) (agentbridge.SessionHistory, error)
	RenameStoredSession(string, string) error
	DeleteStoredSession(string) error
	SetStoredSessionProvider(string, agentbridge.SessionProvider) error
	StoredSessionTransferText(string, int) (string, error)
}

func (s *Server) handleAgentStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.Agent == nil {
		writeError(w, errors.New("Agent 服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s.Agent.Status())
}

func (s *Server) handleAgentStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.Agent == nil {
		writeError(w, errors.New("Agent 服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	var opts agentbridge.StartOptions
	if err := decodeAgentJSON(r, &opts); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if strings.TrimSpace(opts.SessionID) == "" {
		s.providerMu.Lock()
		if handoff := s.providerHandoff; handoff != nil && sameProvider(handoff.Target, s.activeProviderIdentity()) {
			opts.SessionID = handoff.TargetSessionID
		}
		s.providerMu.Unlock()
	}

	var migration map[string]any
	if strings.TrimSpace(opts.SessionID) != "" {
		history, err := s.Agent.StoredSessionHistory(opts.SessionID)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		if reason := s.sessionMigrationReason(history); reason != "" {
			s.markDegradedHistory(history, reason)
			migration, err = s.startMigratedSession(ctx, opts, history, reason)
			if err != nil {
				writeAgentError(w, err)
				return
			}
			opts.SessionID = ""
		}
	}
	if migration == nil {
		if err := s.Agent.Start(ctx, opts); err != nil {
			if rollbackErr := s.rollbackPendingProviderHandoff(); rollbackErr != nil {
				err = fmt.Errorf("%v；恢复原供应商失败: %w", err, rollbackErr)
			}
			if agentbridge.IsSessionLoadOverflow(err) {
				writeSessionLoadError(w, err, s.Agent.Status())
				return
			}
			writeAgentError(w, err)
			return
		}
	}
	status := s.Agent.Status()
	s.rememberAgentCwd(status.Cwd)
	if err := s.markAgentSessionProvider(status); err != nil {
		_ = s.Agent.Stop()
		if rollbackErr := s.rollbackPendingProviderHandoff(); rollbackErr != nil {
			err = fmt.Errorf("%v；恢复原供应商失败: %w", err, rollbackErr)
		}
		writeAgentError(w, err)
		return
	}
	response := s.finishProviderHandoff(status)
	if migration != nil {
		response["provider_handoff"] = migration
	}
	writeJSON(w, response)
}

func (s *Server) rollbackPendingProviderHandoff() error {
	s.providerMu.Lock()
	handoff := s.providerHandoff
	s.providerHandoff = nil
	s.providerMu.Unlock()
	if handoff == nil {
		return nil
	}
	return s.rollbackProviderHandoff(handoff)
}

func (s *Server) markDegradedHistory(history agentbridge.SessionHistory, reason string) {
	if reason != "invalid_tool_history" && reason != "degraded_branch" {
		return
	}
	logicalID := logicalIDFromHistory(history)
	provider := providerIdentity{
		ID: history.Session.ProviderID, Name: history.Session.ProviderName,
		Backend: history.Session.ProviderBackend, Model: history.Session.Model,
		BaseURL: history.Session.ProviderBaseURL,
	}
	_ = s.Agent.SetStoredSessionProvider(history.Session.ID, agentbridge.SessionProvider{
		ID: provider.ID, Name: provider.Name, Backend: provider.Backend,
		Model: history.Session.Model, BaseURL: provider.BaseURL,
		LogicalSessionID: logicalID, ParentSessionID: history.Session.ParentSessionID,
		MigrationMode: history.Session.MigrationMode, Health: "degraded",
	})
	if graph := s.sessionGraphStore(); graph != nil {
		_ = graph.MarkHealth(logicalID, provider, "degraded")
	}
}

func (s *Server) sessionMigrationReason(history agentbridge.SessionHistory) string {
	if history.Session.BranchHealth == "degraded" {
		return "degraded_branch"
	}
	if history.Session.ProviderID == "" || history.Session.ProviderBackend == "" {
		return "legacy_provider_unknown"
	}
	active := s.activeProviderIdentity()
	stored := providerIdentity{
		ID: history.Session.ProviderID, Name: history.Session.ProviderName,
		Backend: history.Session.ProviderBackend, Model: history.Session.Model,
		BaseURL: history.Session.ProviderBaseURL,
	}
	if !sameProvider(stored, active) {
		return "provider_changed"
	}
	for _, message := range history.Messages {
		if message.Role == "tool" && message.Tool != nil && strings.TrimSpace(message.Tool.Title) == "" {
			return "invalid_tool_history"
		}
	}
	return ""
}

func (s *Server) startMigratedSession(ctx context.Context, opts agentbridge.StartOptions, history agentbridge.SessionHistory, reason string) (map[string]any, error) {
	transferText, err := s.Agent.StoredSessionTransferText(opts.SessionID, 48000)
	if err != nil {
		return nil, err
	}
	if s.Agent.Status().Running {
		if err := s.Agent.Stop(); err != nil {
			return nil, err
		}
	}
	fresh := opts
	fresh.SessionID = ""
	if fresh.Cwd == "" {
		fresh.Cwd = history.Session.Cwd
	}
	if err := s.Agent.Start(ctx, fresh); err != nil {
		return nil, err
	}
	status := s.Agent.Status()
	_ = s.markAgentSessionProvider(status)
	active := s.activeProviderIdentity()
	migration := map[string]any{
		"source_session_id": opts.SessionID,
		"source": providerIdentity{
			ID: history.Session.ProviderID, Name: history.Session.ProviderName,
			Backend: history.Session.ProviderBackend, Model: history.Session.Model,
			BaseURL: history.Session.ProviderBaseURL,
		},
		"target": active, "reason": reason, "migrated": false,
	}
	if strings.TrimSpace(transferText) == "" {
		migration["warning"] = "旧会话没有可迁移的用户/助手纯文本，已创建全新会话"
	} else if err := s.Agent.Prompt(transferText, nil); err != nil {
		migration["error"] = err.Error()
	} else {
		migration["migrated"] = true
	}
	return migration, nil
}

func (s *Server) markAgentSessionProvider(status agentbridge.Status) error {
	if s.Agent == nil || strings.TrimSpace(status.SessionID) == "" {
		return nil
	}
	provider := s.activeProviderIdentity()
	logicalID := status.SessionID
	parentID := ""
	migrationMode := "native"
	s.providerMu.Lock()
	if handoff := s.providerHandoff; handoff != nil && sameProvider(handoff.Target, provider) {
		if strings.TrimSpace(handoff.LogicalSessionID) != "" {
			logicalID = handoff.LogicalSessionID
		}
		parentID = handoff.SourceSessionID
		migrationMode = handoff.Mode
	}
	s.providerMu.Unlock()
	if err := s.Agent.SetStoredSessionProvider(status.SessionID, agentbridge.SessionProvider{
		ID: provider.ID, Name: provider.Name, Backend: provider.Backend,
		Model: status.Model, BaseURL: provider.BaseURL, Official: provider.Official,
		LogicalSessionID: logicalID, ParentSessionID: parentID,
		MigrationMode: migrationMode, Health: "healthy",
	}); err != nil {
		return err
	}
	if graph := s.sessionGraphStore(); graph != nil {
		_, err := graph.Record(logicalID, sessionBranch{
			Provider: provider, NativeSessionID: status.SessionID, Cwd: status.Cwd,
			Model: status.Model, Health: "healthy",
		})
		return err
	}
	return nil
}

func (s *Server) finishProviderHandoff(status agentbridge.Status) map[string]any {
	data := map[string]any{}
	raw, _ := json.Marshal(status)
	_ = json.Unmarshal(raw, &data)

	s.providerMu.Lock()
	handoff := s.providerHandoff
	if handoff == nil || !sameProvider(handoff.Target, s.activeProviderIdentity()) {
		s.providerMu.Unlock()
		return data
	}
	// The target session is already running at this point. Consume the pending
	// transaction exactly once; failures are reported without ever loading the
	// source provider's native session into the target provider.
	s.providerHandoff = nil
	s.providerMu.Unlock()

	result := map[string]any{
		"logical_session_id": handoff.LogicalSessionID,
		"source_session_id":  handoff.SourceSessionID,
		"target_session_id":  status.SessionID,
		"source":             handoff.Source,
		"target":             handoff.Target,
		"mode":               handoff.Mode,
		"created_at":         handoff.CreatedAt,
		"migrated":           false,
	}
	if handoff.Mode == "branch_resume" {
		result["resumed"] = true
		data["provider_handoff"] = result
		return data
	}
	if strings.TrimSpace(handoff.TransferText) == "" {
		result["warning"] = "旧会话没有可迁移的用户/助手纯文本，已创建全新会话"
		data["provider_handoff"] = result
		return data
	}
	if err := s.Agent.Prompt(handoff.TransferText, nil); err != nil {
		result["error"] = err.Error()
		data["provider_handoff"] = result
		return data
	}
	result["migrated"] = true
	data["provider_handoff"] = result
	// Async: analyze conversation and rename the new session after the transfer
	// text has been processed by the Agent.
	go s.analyzeAndRenameSession(handoff.Target.Name)
	return data
}

// analyzeAndRenameSession waits for the Agent to process the handoff transfer text,
// then calls the default model to analyze the conversation and generate a concise title.
func (s *Server) analyzeAndRenameSession(providerName string) {
	if s.Agent == nil || s.Routing == nil {
		return
	}
	// Wait for the Agent to finish processing the transfer text.
	// Poll until the Agent becomes idle or timeout.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		status := s.Agent.Status()
		if !status.Busy && status.Running && status.SessionID != "" {
			break
		}
		time.Sleep(2 * time.Second)
	}
	status := s.Agent.Status()
	if !status.Running || status.SessionID == "" {
		return
	}
	// Extra delay to ensure session history is flushed to disk.
	time.Sleep(3 * time.Second)
	// Get default route for LLM call (use hydrated snapshot).
	_, hydrated, err := s.currentRouting()
	if err != nil {
		return
	}
	route, ok := hydrated.Route(hydrated.Policy.Default)
	if !ok || route.Model == "" || route.BaseURL == "" {
		return
	}
	llmRoute := routing.ModelRoute{
		Model:      route.Model,
		APIBackend: route.APIBackend,
		BaseURL:    route.BaseURL,
		APIKey:     route.APIKey,
	}
	// Read session history.
	history, err := s.Agent.StoredSessionHistory(status.SessionID)
	if err != nil {
		return
	}
	// Build conversation summary (user + assistant only).
	var sb strings.Builder
	for _, msg := range history.Messages {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		sb.WriteString(msg.Role)
		sb.WriteString(": ")
		sb.WriteString(content)
		sb.WriteString("\n")
	}
	conversation := strings.TrimSpace(sb.String())
	if conversation == "" {
		return
	}
	// Truncate to avoid huge prompts.
	if len(conversation) > 4000 {
		conversation = conversation[:4000]
	}
	title, _, _ := s.generateSessionTitle(llmRoute, conversation, providerName)
	if title == "" {
		return
	}
	_ = s.Agent.RenameStoredSession(status.SessionID, title)
}

func (s *Server) handleAgentStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.Agent == nil {
		writeError(w, errors.New("Agent 服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	if err := s.Agent.Stop(); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, s.Agent.Status())
}

func (s *Server) handleAgentCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.Agent == nil {
		writeError(w, errors.New("Agent 服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	if err := s.Agent.CancelPrompt(); err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, s.Agent.Status())
}

func (s *Server) handleAgentSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.Agent == nil {
		writeError(w, errors.New("Agent 服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	var request struct {
		Cwd string `json:"cwd"`
	}
	if err := decodeAgentJSON(r, &request); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.Agent.NewSession(ctx, request.Cwd); err != nil {
		writeAgentError(w, err)
		return
	}
	status := s.Agent.Status()
	s.rememberAgentCwd(status.Cwd)
	_ = s.markAgentSessionProvider(status)
	writeJSON(w, status)
}

func (s *Server) handleAgentSessionLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.Agent == nil {
		writeError(w, errors.New("Agent 服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	s.sessionOperationMu.Lock()
	defer s.sessionOperationMu.Unlock()
	var opts agentbridge.StartOptions
	if err := decodeAgentJSON(r, &opts); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(opts.SessionID) == "" {
		writeError(w, errors.New("会话 ID 不能为空"), http.StatusBadRequest)
		return
	}
	history, err := s.Agent.StoredSessionHistory(opts.SessionID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrNotExist) {
			_ = s.sessionGraphStore().RemoveNativeSession(opts.SessionID)
			status = http.StatusGone
			err = errors.New("该分支的本地会话文件已不存在，失效记录已从会话图谱清理；请选择其他分支")
		}
		writeError(w, err, status)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if reason := s.sessionMigrationReason(history); reason != "" {
		s.markDegradedHistory(history, reason)
		migration, err := s.startMigratedSession(ctx, opts, history, reason)
		if err != nil {
			writeAgentError(w, err)
			return
		}
		status := s.Agent.Status()
		s.rememberAgentCwd(status.Cwd)
		response := map[string]any{}
		raw, _ := json.Marshal(status)
		_ = json.Unmarshal(raw, &response)
		response["provider_handoff"] = migration
		writeJSON(w, response)
		return
	}
	if err := s.Agent.Start(ctx, opts); err != nil {
		if agentbridge.IsSessionLoadOverflow(err) {
			writeSessionLoadError(w, err, s.Agent.Status())
			return
		}
		writeAgentError(w, err)
		return
	}
	status := s.Agent.Status()
	s.rememberAgentCwd(status.Cwd)
	_ = s.markAgentSessionProvider(status)
	writeJSON(w, status)
}

func (s *Server) handleAgentSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.Agent == nil {
		writeError(w, errors.New("Agent 服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	sessions, err := s.Agent.ListStoredSessions(r.URL.Query().Get("query"), limit)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, sessions)
}

func (s *Server) handleAgentSessionHistory(w http.ResponseWriter, r *http.Request) {
	if s.Agent == nil {
		writeError(w, errors.New("Agent 服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/agent/sessions/"), "/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, errors.New("会话 ID 无效"), http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		history, err := s.Agent.StoredSessionHistory(id)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, os.ErrNotExist) {
				status = http.StatusNotFound
			}
			writeError(w, err, status)
			return
		}
		writeJSON(w, history)
	case http.MethodDelete:
		s.sessionOperationMu.Lock()
		defer s.sessionOperationMu.Unlock()
		statusSnapshot := s.Agent.Status()
		if statusSnapshot.Running && strings.TrimSpace(statusSnapshot.SessionID) == id {
			writeError(w, errors.New("当前正在运行的会话不能删除，请先切换到其他对话"), http.StatusConflict)
			return
		}
		if err := s.Agent.DeleteStoredSession(id); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, os.ErrNotExist) {
				status = http.StatusNotFound
			}
			writeError(w, err, status)
			return
		}
		if graph := s.sessionGraphStore(); graph != nil {
			if err := graph.RemoveNativeSession(id); err != nil {
				writeError(w, fmt.Errorf("会话文件已删除，但更新逻辑会话图谱失败: %w", err), http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, map[string]any{"ok": true, "id": id})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleAgentRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.Agent == nil {
		writeError(w, errors.New("Agent 服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	var request struct {
		SessionID string `json:"session_id"`
		Title     string `json:"title"`
	}
	if err := decodeAgentJSON(r, &request); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(request.SessionID) == "" {
		writeError(w, errors.New("会话 ID 不能为空"), http.StatusBadRequest)
		return
	}
	if err := s.Agent.RenameStoredSession(request.SessionID, request.Title); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ——— Session Analysis (对话整理) ———

type sessionAnalysisTask struct {
	ID        string                  `json:"id"`
	Status    string                  `json:"status"` // pending, running, completed, failed
	Total     int                     `json:"total"`
	Completed int                     `json:"completed"`
	Results   []sessionAnalysisResult `json:"results"`
	Error     string                  `json:"error,omitempty"`
	CreatedAt time.Time               `json:"created_at"`
}

type sessionAnalysisResult struct {
	ID             string `json:"id"`
	CurrentTitle   string `json:"current_title"`
	SuggestedTitle string `json:"suggested_title"`
	ShouldDelete   bool   `json:"should_delete"`
	Reason         string `json:"reason,omitempty"`
	MessageCount   int    `json:"message_count"`
	Model          string `json:"model"`
	UpdatedAt      string `json:"updated_at"`
}

var (
	analysisTasks   = make(map[string]*sessionAnalysisTask)
	analysisTasksMu sync.Mutex
	analysisCounter uint64
)

func (s *Server) handleAgentSessionsAnalyze(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.startSessionAnalysis(w, r)
	case http.MethodGet:
		s.getSessionAnalysis(w, r)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) startSessionAnalysis(w http.ResponseWriter, r *http.Request) {
	if s.Agent == nil || s.Routing == nil {
		writeError(w, errors.New("服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	// Get all sessions.
	sessions, err := s.Agent.ListStoredSessions("", 200)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	// Get default route for LLM calls (use hydrated snapshot).
	_, hydrated, err := s.currentRouting()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	route, ok := hydrated.Route(hydrated.Policy.Default)
	if !ok || route.Model == "" || route.BaseURL == "" {
		writeError(w, errors.New("默认路由未配置，无法分析"), http.StatusServiceUnavailable)
		return
	}
	llmRoute := routing.ModelRoute{
		Model:      route.Model,
		APIBackend: route.APIBackend,
		BaseURL:    route.BaseURL,
		APIKey:     route.APIKey,
	}
	// Create task.
	analysisTasksMu.Lock()
	atomic.AddUint64(&analysisCounter, 1)
	taskID := fmt.Sprintf("analysis-%d", analysisCounter)
	task := &sessionAnalysisTask{
		ID:        taskID,
		Status:    "running",
		Total:     len(sessions),
		CreatedAt: time.Now(),
		Results:   make([]sessionAnalysisResult, 0, len(sessions)),
	}
	analysisTasks[taskID] = task
	analysisTasksMu.Unlock()

	// Start background analysis.
	go s.runSessionAnalysis(task, sessions, llmRoute)

	writeJSON(w, map[string]any{"task_id": taskID, "total": len(sessions)})
}

func (s *Server) getSessionAnalysis(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	if taskID == "" {
		writeError(w, errors.New("task_id 不能为空"), http.StatusBadRequest)
		return
	}
	analysisTasksMu.Lock()
	task, ok := analysisTasks[taskID]
	analysisTasksMu.Unlock()
	if !ok {
		writeError(w, errors.New("任务不存在"), http.StatusNotFound)
		return
	}
	writeJSON(w, task)
}

func (s *Server) runSessionAnalysis(task *sessionAnalysisTask, sessions []agentbridge.SessionSummary, route routing.ModelRoute) {
	defer func() {
		if r := recover(); r != nil {
			task.Status = "failed"
			task.Error = fmt.Sprintf("分析 panic: %v", r)
		}
	}()

	for i, session := range sessions {
		// Read session history.
		history, err := s.Agent.StoredSessionHistory(session.ID)
		if err != nil {
			// Session might have been deleted, skip.
			task.Completed = i + 1
			continue
		}
		// Build conversation summary.
		var sb strings.Builder
		for _, msg := range history.Messages {
			if msg.Role != "user" && msg.Role != "assistant" {
				continue
			}
			content := strings.TrimSpace(msg.Content)
			if content == "" {
				continue
			}
			sb.WriteString(msg.Role)
			sb.WriteString(": ")
			sb.WriteString(content)
			sb.WriteString("\n")
		}
		conversation := strings.TrimSpace(sb.String())

		result := sessionAnalysisResult{
			ID:           session.ID,
			CurrentTitle: session.Title,
			MessageCount: session.MessageCount,
			Model:        session.Model,
			UpdatedAt:    session.UpdatedAt.Format(time.RFC3339),
		}

		if conversation == "" {
			// Empty session.
			result.SuggestedTitle = "空会话"
			result.ShouldDelete = true
			result.Reason = "无对话内容"
		} else {
			// Truncate for LLM.
			if len(conversation) > 3000 {
				conversation = conversation[:3000]
			}
			title, shouldDelete, reason := s.analyzeSessionConversation(route, conversation)
			result.SuggestedTitle = title
			result.ShouldDelete = shouldDelete
			result.Reason = reason
		}

		task.Results = append(task.Results, result)
		task.Completed = i + 1
	}
	task.Status = "completed"
}

// analyzeSessionConversation calls the default model to analyze a single session.
// It looks up the profile to get connection info (BaseURL, APIKey).
func (s *Server) analyzeSessionConversation(route routing.ModelRoute, conversation string) (title string, shouldDelete bool, reason string) {
	// Route is already hydrated with BaseURL and APIKey. Use route.Name as provider name.
	return s.generateSessionTitle(route, conversation, route.Name)
}

// generateSessionTitle calls the LLM to analyze the conversation and generate a title.
// Returns (title, shouldDelete, reason).
func (s *Server) generateSessionTitle(route routing.ModelRoute, conversation, providerName string) (string, bool, string) {
	prompt := fmt.Sprintf(
		"Analyze this conversation and respond in JSON format with three fields:\n"+
			"- title: a concise session title (max 10 words, in the language of the conversation)\n"+
			"- should_delete: true if this is a one-time/blank/test/abandoned conversation\n"+
			"- reason: one short sentence explaining the deletion recommendation\n\n"+
			"A session should be deleted if it has very little content, is clearly a test, or was abandoned.\n\n"+
			"Conversation:\n%s",
		conversation,
	)
	backend := strings.TrimSpace(route.APIBackend)
	if backend == "" {
		backend = "chat_completions"
	}
	body := map[string]any{"model": route.Model}
	endpoint := strings.TrimRight(route.BaseURL, "/") + "/chat/completions"
	switch backend {
	case "responses":
		endpoint = strings.TrimRight(route.BaseURL, "/") + "/responses"
		body["instructions"] = "You are a JSON-only response assistant. Always respond with valid JSON."
		body["input"] = prompt
		body["max_output_tokens"] = 1000
	case "messages":
		endpoint = strings.TrimRight(route.BaseURL, "/") + "/messages"
		body["system"] = "You are a JSON-only response assistant. Always respond with valid JSON."
		body["messages"] = []map[string]string{{"role": "user", "content": prompt}}
		body["max_tokens"] = 1000
	default:
		body["messages"] = []map[string]string{
			{"role": "system", "content": "You are a JSON-only response assistant. Always respond with valid JSON."},
			{"role": "user", "content": prompt},
		}
		body["max_tokens"] = 1000
		body["temperature"] = 0.3
	}
	if route.SupportsReasoningEffort && backend != "messages" {
		body["reasoning_effort"] = "none"
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[organize] marshal error: %v\n", err)
		return "", false, ""
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		fmt.Fprintf(os.Stderr, "[organize] request error: %v\n", err)
		return "", false, ""
	}
	req.Header.Set("Content-Type", "application/json")
	if route.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+route.APIKey)
	}
	for k, v := range route.ExtraHeaders {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[organize] LLM call error: %v\n", err)
		return "", false, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		preview := respBody[:min(len(respBody), 200)]
		fmt.Fprintf(os.Stderr, "[organize] LLM status %d: %s\n", resp.StatusCode, string(preview))
		return "", false, ""
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[organize] read error: %v\n", err)
		return "", false, ""
	}
	content, err := organizeResponseContent(backend, respBody)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[organize] response error: %v\n", err)
		return "", false, ""
	}
	content = strings.TrimSpace(content)
	fmt.Fprintf(os.Stderr, "[organize] LLM raw response: %q\n", content)
	fmt.Fprintf(os.Stderr, "[organize] LLM full response: %s\n", string(respBody)[:min(len(respBody), 500)])
	// Parse JSON response.
	var parsed struct {
		Title        string `json:"title"`
		ShouldDelete bool   `json:"should_delete"`
		Reason       string `json:"reason"`
	}
	// Try to extract JSON from response (may be wrapped in markdown code block).
	if idx := strings.Index(content, "{"); idx >= 0 {
		content = content[idx:]
	}
	if lastIdx := strings.LastIndex(content, "}"); lastIdx >= 0 {
		content = content[:lastIdx+1]
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		// Fallback: use raw content as title.
		return strings.Trim(content, "\"' \n\t"), false, ""
	}
	title := strings.TrimSpace(parsed.Title)
	if title == "" {
		return "", parsed.ShouldDelete, strings.TrimSpace(parsed.Reason)
	}
	// Prefix with provider name for handoff title.
	if providerName != "" {
		title = fmt.Sprintf("%s: %s", providerName, title)
	}
	return title, parsed.ShouldDelete, strings.TrimSpace(parsed.Reason)
}

func organizeResponseContent(backend string, data []byte) (string, error) {
	switch backend {
	case "responses":
		var response struct {
			OutputText string `json:"output_text"`
			Output     []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return "", err
		}
		if strings.TrimSpace(response.OutputText) != "" {
			return response.OutputText, nil
		}
		for _, item := range response.Output {
			for _, block := range item.Content {
				if strings.TrimSpace(block.Text) != "" {
					return block.Text, nil
				}
			}
		}
	case "messages":
		var response struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return "", err
		}
		for _, block := range response.Content {
			if strings.TrimSpace(block.Text) != "" {
				return block.Text, nil
			}
		}
	default:
		var response struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(data, &response); err != nil {
			return "", err
		}
		if len(response.Choices) > 0 && strings.TrimSpace(response.Choices[0].Message.Content) != "" {
			return response.Choices[0].Message.Content, nil
		}
	}
	return "", errors.New("模型响应中没有文本内容")
}

// ——— Bulk Operations ———

func (s *Server) handleAgentSessionsBulkRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.Agent == nil {
		writeError(w, errors.New("Agent 服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	var request struct {
		Items []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"items"`
	}
	if err := decodeAgentJSON(r, &request); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	var renamed int
	for _, item := range request.Items {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Title) == "" {
			continue
		}
		if err := s.Agent.RenameStoredSession(item.ID, item.Title); err == nil {
			renamed++
		}
	}
	writeJSON(w, map[string]any{"ok": true, "renamed": renamed})
}

func (s *Server) handleAgentSessionsBulkDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.Agent == nil {
		writeError(w, errors.New("Agent 服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	var request struct {
		IDs []string `json:"ids"`
	}
	if err := decodeAgentJSON(r, &request); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	var deleted int
	statusSnapshot := s.Agent.Status()
	currentID := ""
	if statusSnapshot.Running {
		currentID = strings.TrimSpace(statusSnapshot.SessionID)
	}
	for _, id := range request.IDs {
		id = strings.TrimSpace(id)
		if id == "" || id == currentID {
			continue
		}
		if err := s.Agent.DeleteStoredSession(id); err == nil {
			deleted++
			if graph := s.sessionGraphStore(); graph != nil {
				_ = graph.RemoveNativeSession(id)
			}
		}
	}
	writeJSON(w, map[string]any{"ok": true, "deleted": deleted})
}

type agentSocketMessage struct {
	Type        string                   `json:"type"`
	Text        string                   `json:"text,omitempty"`
	RequestID   string                   `json:"request_id,omitempty"`
	Allow       bool                     `json:"allow,omitempty"`
	Remember    bool                     `json:"remember,omitempty"`
	Attachments []agentbridge.Attachment `json:"attachments,omitempty"`
}

func (s *Server) handleAgentWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.Agent == nil {
		writeError(w, errors.New("Agent 服务未初始化"), http.StatusServiceUnavailable)
		return
	}
	if !agentWebSocketOriginAllowed(r) {
		http.Error(w, "请求来源不受信任", http.StatusForbidden)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(64 << 10)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	subscriberID, events := s.Agent.Subscribe()
	defer s.Agent.Unsubscribe(subscriberID)
	replies := make(chan agentbridge.Event, 16)
	go s.readAgentSocket(ctx, cancel, conn, replies)

	status := s.Agent.Status()
	auto := status.SessionAutoApprove
	if err := wsjson.Write(ctx, conn, agentbridge.Event{
		Type: "agent_status", SessionID: status.SessionID, Status: status.State,
		Model: status.Model, Error: status.Error, SessionAutoApprove: &auto,
	}); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-events:
			if err := wsjson.Write(ctx, conn, event); err != nil {
				return
			}
		case event := <-replies:
			if err := wsjson.Write(ctx, conn, event); err != nil {
				return
			}
		}
	}
}

func (s *Server) readAgentSocket(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn, replies chan<- agentbridge.Event) {
	defer cancel()
	for {
		var message agentSocketMessage
		if err := wsjson.Read(ctx, conn, &message); err != nil {
			return
		}
		var err error
		switch message.Type {
		case "user_message":
			err = s.Agent.Prompt(message.Text, message.Attachments)
		case "cancel":
			err = s.Agent.CancelPrompt()
		case "permission_response":
			err = s.Agent.RespondPermissionEx(message.RequestID, message.Allow, message.Remember)
		case "set_session_auto_approve":
			s.Agent.SetSessionAutoApprove(message.Allow || message.Remember)
			// Status broadcast is emitted by SetSessionAutoApprove.
			continue
		case "ping":
			replies <- agentbridge.Event{Type: "pong"}
			continue
		default:
			err = fmt.Errorf("不支持的消息类型: %s", message.Type)
		}
		if err != nil {
			select {
			case replies <- agentbridge.Event{Type: "error", Error: err.Error()}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (s *Server) rememberAgentCwd(cwd string) {
	if s.Settings == nil || strings.TrimSpace(cwd) == "" {
		return
	}
	current, err := s.Settings.Get()
	if err != nil || current.AgentDefaultCwd == cwd {
		return
	}
	current.AgentDefaultCwd = cwd
	_, _ = s.Settings.Update(current)
}

func decodeAgentJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("请求内容无效: %w", err)
	}
	return nil
}

func writeAgentError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, agentbridge.ErrBusy) {
		status = http.StatusConflict
	} else if errors.Is(err, agentbridge.ErrNotRunning) {
		status = http.StatusServiceUnavailable
	} else if strings.Contains(err.Error(), "工作目录") || strings.Contains(err.Error(), "消息不能为空") ||
		strings.Contains(err.Error(), "没有正在生成") {
		status = http.StatusBadRequest
	}
	writeError(w, err, status)
}

func writeSessionLoadError(w http.ResponseWriter, err error, status agentbridge.Status) {
	restarted := false
	var loadErr *agentbridge.SessionLoadError
	if errors.As(err, &loadErr) {
		restarted = loadErr.Recovered()
	}
	writeJSONStatus(w, struct {
		Error           string             `json:"error"`
		Code            string             `json:"code"`
		ReadonlyHistory bool               `json:"readonly_history"`
		Recoverable     bool               `json:"recoverable"`
		EngineLoaded    bool               `json:"engine_loaded"`
		AgentRestarted  bool               `json:"agent_restarted"`
		Status          agentbridge.Status `json:"status"`
	}{
		Error:           err.Error(),
		Code:            agentbridge.SessionLoadOverflowCode,
		ReadonlyHistory: true,
		Recoverable:     true,
		EngineLoaded:    false,
		AgentRestarted:  restarted,
		Status:          status,
	}, http.StatusConflict)
}

func agentWebSocketOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return isLoopbackRequest(r)
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host != r.Host {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}
