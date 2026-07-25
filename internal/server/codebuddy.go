package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"grok_switch/internal/codebuddy"
)

const (
	codeBuddyModelPrefix = "codebuddy/"
	codeBuddyMaxBody     = 32 << 20
)

// CodeBuddyRunner is replaceable so the HTTP bridge can be tested without
// starting a real CodeBuddy process.
type CodeBuddyRunner interface {
	Inspect(context.Context) codebuddy.Status
	Run(context.Context, codebuddy.RunRequest, func(codebuddy.Event) error) error
}

type defaultCodeBuddyRunner struct{}

func (defaultCodeBuddyRunner) Inspect(ctx context.Context) codebuddy.Status {
	return codebuddy.Inspect(ctx, "")
}

func (defaultCodeBuddyRunner) Run(ctx context.Context, request codebuddy.RunRequest, emit func(codebuddy.Event) error) error {
	return (codebuddy.Runner{}).Run(ctx, request, emit)
}

func (s *Server) codeBuddyRunner() CodeBuddyRunner {
	if s.CodeBuddy != nil {
		return s.CodeBuddy
	}
	return defaultCodeBuddyRunner{}
}

func (s *Server) handleCodeBuddyStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	status := publicCodeBuddyStatus(s.codeBuddyRunner().Inspect(r.Context()))
	writeJSON(w, status)
}

func (s *Server) handleCodeBuddyInference(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeCodeBuddyError(w, "仅允许本机访问 CodeBuddy 推理端", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/codebuddy/v1/models":
		s.handleCodeBuddyModels(w, r)
	case "/codebuddy/v1/chat/completions":
		s.handleCodeBuddyChatCompletions(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) handleCodeBuddyModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	status := s.codeBuddyRunner().Inspect(r.Context())
	if !status.Available {
		writeCodeBuddyError(w, "CodeBuddy 不可用", http.StatusServiceUnavailable)
		return
	}
	models := externalCodeBuddyModels(status.Models)
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		data = append(data, map[string]any{
			"id":       model,
			"object":   "model",
			"created":  0,
			"owned_by": "codebuddy",
		})
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}

type codeBuddyChatRequest struct {
	Model             string              `json:"model"`
	Messages          []codebuddy.Message `json:"messages"`
	Stream            bool                `json:"stream,omitempty"`
	Tools             json.RawMessage     `json:"tools,omitempty"`
	ToolChoice        json.RawMessage     `json:"tool_choice,omitempty"`
	Functions         json.RawMessage     `json:"functions,omitempty"`
	FunctionCall      json.RawMessage     `json:"function_call,omitempty"`
	ParallelToolCalls json.RawMessage     `json:"parallel_tool_calls,omitempty"`
}

func (s *Server) handleCodeBuddyChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var request codeBuddyChatRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, codeBuddyMaxBody))
	if err := decoder.Decode(&request); err != nil {
		status := http.StatusBadRequest
		message := "请求 JSON 无效"
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
			message = "请求体超过 32MiB 限制"
		}
		writeCodeBuddyError(w, message, status)
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeCodeBuddyError(w, "请求 JSON 无效", http.StatusBadRequest)
		return
	}
	// Grok may keep its tool schema attached when /model switches an existing
	// session to CodeBuddy. CodeBuddy remains text-only: protocol fields and tool
	// messages are deliberately ignored by MessagesToPrompt instead of being
	// forwarded to the CLI harness or rejected with a 400 response.
	model, err := normalizeCodeBuddyModel(request.Model)
	if err != nil {
		writeCodeBuddyError(w, err.Error(), http.StatusBadRequest)
		return
	}
	status := s.codeBuddyRunner().Inspect(r.Context())
	if !status.Available {
		writeCodeBuddyError(w, "CodeBuddy 不可用", http.StatusServiceUnavailable)
		return
	}
	if !codeBuddyModelListed(model, status.Models) {
		writeCodeBuddyError(w, "CodeBuddy model 不在可用列表中", http.StatusBadRequest)
		return
	}
	if len(request.Messages) == 0 {
		writeCodeBuddyError(w, "messages 不能为空", http.StatusBadRequest)
		return
	}
	prompt, err := codebuddy.MessagesToPrompt(request.Messages)
	if err != nil {
		writeCodeBuddyError(w, "messages 仅支持文本内容", http.StatusBadRequest)
		return
	}
	if !promptHasConversation(prompt) {
		writeCodeBuddyError(w, "messages 不包含可用文本", http.StatusBadRequest)
		return
	}
	cwd, err := codeBuddyWorkingDirectory(r)
	if err != nil {
		writeCodeBuddyError(w, err.Error(), http.StatusBadRequest)
		return
	}

	runRequest := codebuddy.RunRequest{Prompt: prompt, Cwd: cwd, Model: model}
	if request.Stream {
		s.streamCodeBuddyCompletion(w, r, runRequest, codeBuddyModelPrefix+model)
		return
	}
	s.writeCodeBuddyCompletion(w, r, runRequest, codeBuddyModelPrefix+model)
}

func (s *Server) writeCodeBuddyCompletion(w http.ResponseWriter, r *http.Request, request codebuddy.RunRequest, externalModel string) {
	var output codeBuddyOutput
	err := s.codeBuddyRunner().Run(r.Context(), request, output.consume)
	if err != nil {
		writeCodeBuddyError(w, "CodeBuddy 执行失败", http.StatusBadGateway)
		return
	}
	text := output.text() + output.toolSummary()
	if text == "" {
		writeCodeBuddyError(w, "CodeBuddy 未返回文本结果", http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{
		"id":      newCodeBuddyCompletionID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   externalModel,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]string{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
	})
}

func (s *Server) streamCodeBuddyCompletion(w http.ResponseWriter, r *http.Request, request codebuddy.RunRequest, externalModel string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeCodeBuddyError(w, "当前 HTTP writer 不支持流式响应", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	id := newCodeBuddyCompletionID()
	created := time.Now().Unix()
	var output codeBuddyOutput
	wroteContent := false
	emitText := func(text string) error {
		if text == "" {
			return nil
		}
		wroteContent = true
		if err := writeCodeBuddySSE(w, map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": externalModel,
			"choices": []map[string]any{{"index": 0, "delta": map[string]string{"content": text}, "finish_reason": nil}},
		}); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	err := s.codeBuddyRunner().Run(r.Context(), request, func(event codebuddy.Event) error {
		delta, consumeErr := output.consumeDelta(event)
		if consumeErr != nil {
			return consumeErr
		}
		return emitText(delta)
	})
	if err != nil {
		if !wroteContent {
			_ = writeCodeBuddySSE(w, map[string]any{"error": map[string]string{"message": "CodeBuddy 执行失败", "type": "codebuddy_error"}})
		}
	} else {
		// Append tool execution summary before finish
		if summary := output.toolSummary(); summary != "" {
			_ = emitText(summary)
		}
		_ = writeCodeBuddySSE(w, map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": externalModel,
			"choices": []map[string]any{{"index": 0, "delta": map[string]string{}, "finish_reason": "stop"}},
		})
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flusher.Flush()
}

type codeBuddyOutput struct {
	value string
	tools []codebuddy.Event
}

func (o *codeBuddyOutput) consume(event codebuddy.Event) error {
	_, err := o.consumeDelta(event)
	return err
}

func (o *codeBuddyOutput) consumeDelta(event codebuddy.Event) (string, error) {
	switch event.Kind {
	case codebuddy.EventText:
		o.value += event.Text
		return event.Text, nil
	case codebuddy.EventResult:
		delta := nonDuplicateCodeBuddyResult(o.value, event.Text)
		o.value += delta
		return delta, nil
	case codebuddy.EventToolUse, codebuddy.EventToolResult:
		o.tools = append(o.tools, event)
		return "", nil
	case codebuddy.EventError:
		return "", errors.New("CodeBuddy 返回错误事件")
	default:
		return "", nil
	}
}

func (o *codeBuddyOutput) text() string { return o.value }

func (o *codeBuddyOutput) toolSummary() string {
	if len(o.tools) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, event := range o.tools {
		if event.Tool == nil {
			continue
		}
		switch event.Kind {
		case codebuddy.EventToolUse:
			fmt.Fprintf(&sb, "\n[工具调用] %s", event.Tool.Name)
			if event.Tool.InputJSON != "" {
				fmt.Fprintf(&sb, " 输入=%s", event.Tool.InputJSON)
			}
		case codebuddy.EventToolResult:
			status := "完成"
			if event.Tool.IsError {
				status = "错误"
			}
			fmt.Fprintf(&sb, "\n[工具结果] %s tool_use_id=%s", status, event.Tool.UseID)
			if event.Text != "" {
				fmt.Fprintf(&sb, " 输出=%s", event.Text)
			}
		}
	}
	return sb.String()
}

func nonDuplicateCodeBuddyResult(current, result string) string {
	if result == "" || result == current || strings.HasSuffix(current, result) {
		return ""
	}
	if strings.HasPrefix(result, current) {
		return result[len(current):]
	}
	for overlap := min(len(current), len(result)); overlap > 0; overlap-- {
		if strings.HasSuffix(current, result[:overlap]) {
			return result[overlap:]
		}
	}
	return result
}

func normalizeCodeBuddyModel(value string) (string, error) {
	model := strings.TrimSpace(value)
	if strings.HasPrefix(model, codeBuddyModelPrefix) {
		model = strings.TrimPrefix(model, codeBuddyModelPrefix)
	}
	if !isSafeCodeBuddyModel(model) {
		return "", fmt.Errorf("无效的 CodeBuddy model")
	}
	return model, nil
}

func isSafeCodeBuddyModel(model string) bool {
	if model == "" || len(model) > 128 || model[0] == '-' || model[0] == '.' {
		return false
	}
	for _, character := range model {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune("._:-", character) {
			continue
		}
		return false
	}
	return true
}

func codeBuddyModelListed(model string, models []string) bool {
	for _, candidate := range models {
		normalized, err := normalizeCodeBuddyModel(candidate)
		if err == nil && normalized == model {
			return true
		}
	}
	return false
}

func externalCodeBuddyModels(models []string) []string {
	seen := make(map[string]bool, len(models))
	result := make([]string, 0, len(models))
	for _, model := range models {
		normalized, err := normalizeCodeBuddyModel(model)
		if err != nil || seen[normalized] {
			continue
		}
		seen[normalized] = true
		result = append(result, codeBuddyModelPrefix+normalized)
	}
	return result
}

func publicCodeBuddyStatus(status codebuddy.Status) codebuddy.Status {
	status.Path = ""
	status.Error = publicCodeBuddyStatusError(status)
	status.Models = externalCodeBuddyModels(status.Models)
	return status
}

func publicCodeBuddyStatusError(status codebuddy.Status) string {
	if status.Error == "" {
		return ""
	}
	if !status.Available {
		return "CodeBuddy 不可用"
	}
	if status.Version == "" {
		return "无法读取 CodeBuddy 版本"
	}
	return "无法完整读取 CodeBuddy 状态"
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("请求包含多个 JSON 值")
	}
	return nil
}

func promptHasConversation(prompt string) bool {
	return strings.Contains(prompt, "\n\n用户：\n") || strings.Contains(prompt, "\n\n助手：\n") ||
		strings.Contains(prompt, "\n\n系统上下文（引用）：\n")
}

func codeBuddyWorkingDirectory(r *http.Request) (string, error) {
	values := r.Header.Values("X-Grok-Working-Directory")
	if len(values) > 1 {
		return "", errors.New("X-Grok-Working-Directory 只能提供一次")
	}
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", errors.New("无法读取当前工作目录")
		}
		return cwd, nil
	}
	value := strings.TrimSpace(values[0])
	if len(value) > 4096 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", errors.New("X-Grok-Working-Directory 无效")
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", errors.New("X-Grok-Working-Directory 无效")
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", errors.New("X-Grok-Working-Directory 必须是现有目录")
	}
	return absolute, nil
}

func writeCodeBuddySSE(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

func writeCodeBuddyError(w http.ResponseWriter, message string, status int) {
	writeJSONStatus(w, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    status,
		},
	}, status)
}

var codeBuddyCompletionSequence atomic.Uint64

func newCodeBuddyCompletionID() string {
	return fmt.Sprintf("chatcmpl-codebuddy-%d-%d", time.Now().UnixNano(), codeBuddyCompletionSequence.Add(1))
}
