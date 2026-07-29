package agentbridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type SessionSummary struct {
	ID               string    `json:"id"`
	Title            string    `json:"title"`
	Cwd              string    `json:"cwd"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Model            string    `json:"model,omitempty"`
	AgentName        string    `json:"agent_name,omitempty"`
	MessageCount     int       `json:"message_count"`
	ProviderID       string    `json:"provider_id,omitempty"`
	ProviderName     string    `json:"provider_name,omitempty"`
	ProviderBackend  string    `json:"provider_backend,omitempty"`
	ProviderBaseURL  string    `json:"provider_base_url,omitempty"`
	LogicalSessionID string    `json:"logical_session_id,omitempty"`
	ParentSessionID  string    `json:"parent_session_id,omitempty"`
	MigrationMode    string    `json:"migration_mode,omitempty"`
	BranchHealth     string    `json:"branch_health,omitempty"`
}

// SessionProvider is grok_switch-owned metadata. Grok CLI does not currently
// record the active profile ID in summary.json, so a sidecar is required to
// distinguish safe same-provider resume from cross-provider migration.
type SessionProvider struct {
	ID               string `json:"id,omitempty"`
	Name             string `json:"name,omitempty"`
	Backend          string `json:"backend,omitempty"`
	Model            string `json:"model,omitempty"`
	BaseURL          string `json:"base_url,omitempty"`
	Official         bool   `json:"official,omitempty"`
	LogicalSessionID string `json:"logical_session_id,omitempty"`
	ParentSessionID  string `json:"parent_session_id,omitempty"`
	MigrationMode    string `json:"migration_mode,omitempty"`
	Health           string `json:"health,omitempty"`
	UpdatedAt        string `json:"updated_at,omitempty"`
}

type HistoryMessage struct {
	Role    string     `json:"role"`
	Content string     `json:"content,omitempty"`
	Model   string     `json:"model,omitempty"`
	Tool    *ToolEvent `json:"tool,omitempty"`
}

type SessionHistory struct {
	Session  SessionSummary   `json:"session"`
	Messages []HistoryMessage `json:"messages"`
}

type storedSummary struct {
	Info struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"info"`
	SessionSummary string    `json:"session_summary"`
	GeneratedTitle string    `json:"generated_title"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	LastActiveAt   time.Time `json:"last_active_at"`
	CurrentModelID string    `json:"current_model_id"`
	AgentName      string    `json:"agent_name"`
	NumChatMessage int       `json:"num_chat_messages"`

	// CustomTitle and Provider are loaded from grok_switch sidecar files. They
	// are not part of Grok CLI's summary.json.
	CustomTitle string          `json:"-"`
	Provider    SessionProvider `json:"-"`
}

func (b *Bridge) ListStoredSessions(query string, limit int) ([]SessionSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 80
	}
	query = strings.ToLower(strings.TrimSpace(query))
	root := filepath.Join(b.grokHome, "sessions")
	cwdEntries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []SessionSummary{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取 Grok 会话目录失败: %w", err)
	}
	sessions := make([]SessionSummary, 0, min(limit*2, 200))
	for _, cwdEntry := range cwdEntries {
		if !cwdEntry.IsDir() {
			continue
		}
		cwdPath := filepath.Join(root, cwdEntry.Name())
		sessionEntries, readErr := os.ReadDir(cwdPath)
		if readErr != nil {
			continue
		}
		for _, sessionEntry := range sessionEntries {
			if !sessionEntry.IsDir() {
				continue
			}
			dir := filepath.Join(cwdPath, sessionEntry.Name())
			summary, readErr := readStoredSummary(filepath.Join(dir, "summary.json"))
			if readErr != nil || summary.Info.ID == "" || summary.Info.Cwd == "" {
				continue
			}
			if strings.TrimSpace(summary.GeneratedTitle) == "" && strings.TrimSpace(summary.SessionSummary) == "" {
				continue
			}
			if info, statErr := os.Stat(summary.Info.Cwd); statErr != nil || !info.IsDir() {
				continue
			}
			item := summary.toSessionSummary()
			if query != "" && !strings.Contains(strings.ToLower(item.Title+" "+item.Cwd+" "+item.Model), query) {
				continue
			}
			sessions = append(sessions, item)
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt) })
	if len(sessions) > limit {
		sessions = sessions[:limit]
	}
	return sessions, nil
}

func (b *Bridge) StoredSessionHistory(id string) (SessionHistory, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, `/\\`) {
		return SessionHistory{}, errors.New("会话 ID 无效")
	}
	dir, summary, err := b.findStoredSession(id)
	if err != nil {
		return SessionHistory{}, err
	}
	messages, err := readChatHistory(filepath.Join(dir, "chat_history.jsonl"))
	if err != nil {
		return SessionHistory{}, err
	}
	return SessionHistory{Session: summary.toSessionSummary(), Messages: messages}, nil
}

func (b *Bridge) findStoredSession(id string) (string, storedSummary, error) {
	root := filepath.Join(b.grokHome, "sessions")
	cwdEntries, err := os.ReadDir(root)
	if err != nil {
		return "", storedSummary{}, err
	}
	for _, cwdEntry := range cwdEntries {
		if !cwdEntry.IsDir() {
			continue
		}
		dir := filepath.Join(root, cwdEntry.Name(), id)
		summary, readErr := readStoredSummary(filepath.Join(dir, "summary.json"))
		if readErr == nil && summary.Info.ID == id {
			return dir, summary, nil
		}
	}
	return "", storedSummary{}, os.ErrNotExist
}

func readStoredSummary(path string) (storedSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return storedSummary{}, err
	}
	var summary storedSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return storedSummary{}, err
	}
	// Prefer a user-defined title stored in a sidecar file next to summary.json,
	// so renaming survives Grok regenerating its own summary metadata.
	dir := filepath.Dir(path)
	if side, sideErr := os.ReadFile(filepath.Join(dir, "grok_switch_title.json")); sideErr == nil {
		var override struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(side, &override) == nil {
			summary.CustomTitle = strings.TrimSpace(override.Title)
		}
	}
	if side, sideErr := os.ReadFile(filepath.Join(dir, "grok_switch_provider.json")); sideErr == nil {
		_ = json.Unmarshal(side, &summary.Provider)
	}
	return summary, nil
}

func (b *Bridge) findStoredSessionDir(id string) (string, error) {
	root := filepath.Join(b.grokHome, "sessions")
	for attempt := 0; attempt < 20; attempt++ {
		cwdEntries, err := os.ReadDir(root)
		if err == nil {
			for _, cwdEntry := range cwdEntries {
				if !cwdEntry.IsDir() {
					continue
				}
				dir := filepath.Join(root, cwdEntry.Name(), id)
				if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
					return dir, nil
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		time.Sleep(25 * time.Millisecond)
	}
	return "", os.ErrNotExist
}

func (b *Bridge) SetStoredSessionProvider(id string, provider SessionProvider) error {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, `/\\`) {
		return errors.New("会话 ID 无效")
	}
	dir, err := b.findStoredSessionDir(id)
	if err != nil {
		return err
	}
	provider.ID = strings.TrimSpace(provider.ID)
	provider.Name = strings.TrimSpace(provider.Name)
	provider.Backend = strings.TrimSpace(provider.Backend)
	provider.Model = strings.TrimSpace(provider.Model)
	provider.BaseURL = strings.TrimSpace(provider.BaseURL)
	provider.LogicalSessionID = strings.TrimSpace(provider.LogicalSessionID)
	provider.ParentSessionID = strings.TrimSpace(provider.ParentSessionID)
	provider.MigrationMode = strings.TrimSpace(provider.MigrationMode)
	provider.Health = strings.TrimSpace(provider.Health)
	provider.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(provider, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "grok_switch_provider.json"), data, 0o600); err != nil {
		return fmt.Errorf("写入会话供应商元数据失败: %w", err)
	}
	return nil
}

// RenameStoredSession persists a user-chosen title for a stored session in a
// sidecar file. An empty title clears the override and restores the original.
func (b *Bridge) RenameStoredSession(id, title string) error {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, `/\\`) {
		return errors.New("会话 ID 无效")
	}
	title = strings.TrimSpace(title)
	dir, _, err := b.findStoredSession(id)
	if err != nil {
		return err
	}
	sidecar := filepath.Join(dir, "grok_switch_title.json")
	if title == "" {
		_ = os.Remove(sidecar)
		return nil
	}
	data, err := json.MarshalIndent(map[string]string{"title": title}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(sidecar, data, 0o600); err != nil {
		return fmt.Errorf("写入会话标题失败: %w", err)
	}
	return nil
}

// DeleteStoredSession removes a stored session directory under ~/.grok/sessions
// (summary, chat history, terminals, and grok_switch sidecars).
func (b *Bridge) DeleteStoredSession(id string) error {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, `/\\`) {
		return errors.New("会话 ID 无效")
	}
	dir, _, err := b.findStoredSession(id)
	if err != nil {
		return err
	}
	// Safety: only delete under <grokHome>/sessions/<cwd>/<id>
	root := filepath.Clean(filepath.Join(b.grokHome, "sessions"))
	cleanDir := filepath.Clean(dir)
	rel, err := filepath.Rel(root, cleanDir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("会话路径无效，拒绝删除")
	}
	if filepath.Base(cleanDir) != id {
		return fmt.Errorf("会话目录与 ID 不匹配，拒绝删除")
	}
	if err := os.RemoveAll(cleanDir); err != nil {
		return fmt.Errorf("删除会话文件失败: %w", err)
	}
	// Best-effort: drop empty cwd bucket directories.
	parent := filepath.Dir(cleanDir)
	if entries, readErr := os.ReadDir(parent); readErr == nil && len(entries) == 0 {
		_ = os.Remove(parent)
	}
	return nil
}

func (s storedSummary) toSessionSummary() SessionSummary {
	title := strings.TrimSpace(s.CustomTitle)
	if title == "" {
		title = strings.TrimSpace(s.GeneratedTitle)
	}
	if title == "" {
		title = strings.TrimSpace(s.SessionSummary)
	}
	if title == "" {
		title = "未命名会话"
	}
	updated := s.UpdatedAt
	if s.LastActiveAt.After(updated) {
		updated = s.LastActiveAt
	}
	return SessionSummary{
		ID: s.Info.ID, Title: title, Cwd: s.Info.Cwd, CreatedAt: s.CreatedAt,
		UpdatedAt: updated, Model: s.CurrentModelID, AgentName: s.AgentName,
		MessageCount: s.NumChatMessage, ProviderID: s.Provider.ID,
		ProviderName: s.Provider.Name, ProviderBackend: s.Provider.Backend,
		ProviderBaseURL: s.Provider.BaseURL, LogicalSessionID: s.Provider.LogicalSessionID,
		ParentSessionID: s.Provider.ParentSessionID, MigrationMode: s.Provider.MigrationMode,
		BranchHealth: s.Provider.Health,
	}
}

// StoredSessionTransferText produces a provider-neutral handoff. It deliberately
// excludes reasoning, tool calls, tool results, IDs, and protocol metadata.
// The resulting text can be sent as a normal user message to a fresh session.
func (b *Bridge) StoredSessionTransferText(id string, maxChars int) (string, error) {
	history, err := b.StoredSessionHistory(id)
	if err != nil {
		return "", err
	}
	if maxChars <= 0 || maxChars > 120000 {
		maxChars = 48000
	}
	parts := make([]string, 0, len(history.Messages)+4)
	parts = append(parts,
		"以下是从另一供应商迁移的旧会话纯文本上下文。",
		"请继续完成原任务；不要假设旧工具调用仍然存在，需要时重新检查文件和环境。",
	)
	if title := strings.TrimSpace(history.Session.Title); title != "" {
		parts = append(parts, "旧会话标题："+title)
	}
	messageParts := make([]string, 0, len(history.Messages))
	for _, message := range history.Messages {
		content := strings.TrimSpace(message.Content)
		if content == "" || (message.Role != "user" && message.Role != "assistant") {
			continue
		}
		label := "用户"
		if message.Role == "assistant" {
			label = "助手"
		}
		messageParts = append(messageParts, label+"："+content)
	}
	if len(messageParts) == 0 {
		return "", nil
	}
	parts = append(parts, messageParts...)
	text := strings.Join(parts, "\n\n")
	if len([]rune(text)) > maxChars {
		runes := []rune(text)
		text = "（较早内容已截断）\n\n" + string(runes[len(runes)-maxChars:])
	}
	return text, nil
}

func readChatHistory(path string) ([]HistoryMessage, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []HistoryMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	messages := make([]HistoryMessage, 0, 32)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		var entry struct {
			Type       string          `json:"type"`
			Content    json.RawMessage `json:"content"`
			ModelID    string          `json:"model_id"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"tool_calls"`
			Summary []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"summary"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		switch entry.Type {
		case "user":
			if text := cleanStoredUserText(contentTextFromJSON(entry.Content)); text != "" {
				messages = append(messages, HistoryMessage{Role: "user", Content: text})
			}
		case "assistant":
			if text := strings.TrimSpace(contentTextFromJSON(entry.Content)); text != "" {
				messages = append(messages, HistoryMessage{Role: "assistant", Content: text, Model: entry.ModelID})
			}
			for _, call := range entry.ToolCalls {
				var input any
				if json.Unmarshal([]byte(call.Arguments), &input) != nil {
					input = call.Arguments
				}
				messages = append(messages, HistoryMessage{Role: "tool", Tool: &ToolEvent{ID: call.ID, Title: call.Name, Status: "completed", RawInput: input}})
			}
		case "tool_result":
			messages = append(messages, HistoryMessage{Role: "tool_result", Content: contentTextFromJSON(entry.Content), Tool: &ToolEvent{ID: entry.ToolCallID, Status: "completed"}})
		case "reasoning":
			parts := make([]string, 0, len(entry.Summary))
			for _, part := range entry.Summary {
				if part.Text != "" {
					parts = append(parts, part.Text)
				}
			}
			if len(parts) > 0 {
				messages = append(messages, HistoryMessage{Role: "thought", Content: strings.Join(parts, "\n")})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func contentTextFromJSON(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		parts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func cleanStoredUserText(text string) string {
	text = strings.TrimSpace(text)
	if start := strings.Index(text, "<user_query>"); start >= 0 {
		start += len("<user_query>")
		if end := strings.Index(text[start:], "</user_query>"); end >= 0 {
			return strings.TrimSpace(text[start : start+end])
		}
	}
	for _, marker := range []string{"<user_info>", "<git_status>", "<system-reminder>"} {
		if strings.Contains(text, marker) {
			return ""
		}
	}
	return text
}
