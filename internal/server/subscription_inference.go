package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const subscriptionProxyUpstream = "http://127.0.0.1:8317"

// SubscriptionProxyBaseURL returns the in-process inference endpoint. Keeping
// this hop inside grok_switch lets us repair malformed legacy tool history
// without launching another proxy process.
func (s *Server) SubscriptionProxyBaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/subscription-proxy/v1", s.ActualPort)
}

// EnsureSubscriptionProxyRoutes migrates profiles created by older versions,
// which pointed Grok directly at CLIProxyAPI and therefore bypassed request
// repair. Routing-aware servers rebuild and reapply the complete catalog;
// legacy servers re-apply the active single profile when its route changed.
func (s *Server) EnsureSubscriptionProxyRoutes() error {
	if s.Profiles == nil || s.Switcher == nil || s.ActualPort == 0 {
		return nil
	}
	list, err := s.Profiles.List()
	if err != nil {
		return err
	}
	target := s.SubscriptionProxyBaseURL()
	changed := false
	for _, profile := range list {
		if !strings.HasPrefix(profile.Source, "subscription-proxy:") {
			continue
		}
		profileChanged := profile.BaseURL != target
		profile.BaseURL = target
		for i := range profile.Models {
			if profile.Models[i].BaseURL != target {
				profile.Models[i].BaseURL = target
				profileChanged = true
			}
		}
		if !profileChanged {
			continue
		}
		_, updateErr := s.Profiles.Update(profile.ID, profile)
		if updateErr != nil {
			return updateErr
		}
		changed = true
	}
	if changed && s.Routing != nil {
		if err := s.ApplyCurrentRouting(); err != nil {
			return err
		}
	}
	if changed {
		s.changed()
	}
	return nil
}

func (s *Server) handleSubscriptionInference(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		http.Error(w, "仅允许本机访问", http.StatusForbidden)
		return
	}
	upstreamPath := strings.TrimPrefix(r.URL.Path, "/subscription-proxy")
	if !strings.HasPrefix(upstreamPath, "/v1/") && upstreamPath != "/v1" {
		http.NotFound(w, r)
		return
	}

	var body io.Reader = r.Body
	if r.Body != nil && requestMayContainJSON(r) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil {
			http.Error(w, "读取请求失败", http.StatusBadRequest)
			return
		}
		if len(raw) > 0 {
			repaired, _, repairErr := repairMalformedToolHistory(raw)
			if repairErr != nil {
				http.Error(w, "请求 JSON 无效", http.StatusBadRequest)
				return
			}
			body = bytes.NewReader(repaired)
		}
	}

	upstreamURL := subscriptionProxyUpstream + upstreamPath
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, body)
	if err != nil {
		http.Error(w, "创建订阅代理请求失败", http.StatusBadGateway)
		return
	}
	copyProxyHeaders(req.Header, r.Header)
	req.Host = "127.0.0.1:8317"

	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		http.Error(w, "CLIProxyAPI 不可用", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyProxyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if flusher, ok := w.(http.Flusher); ok && isStreamingResponse(resp) {
		buffer := make([]byte, 32<<10)
		for {
			n, readErr := resp.Body.Read(buffer)
			if n > 0 {
				if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
					return
				}
				flusher.Flush()
			}
			if readErr != nil {
				return
			}
		}
	}
	_, _ = io.Copy(w, resp.Body)
}

func isStreamingResponse(resp *http.Response) bool {
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	return strings.Contains(contentType, "text/event-stream") ||
		strings.EqualFold(resp.Header.Get("X-Accel-Buffering"), "no")
}

func requestMayContainJSON(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return false
	}
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	return contentType == "" || strings.Contains(contentType, "json")
}

var hopByHopHeaders = map[string]bool{
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true,
	"Proxy-Authenticate": true, "Proxy-Authorization": true, "Te": true,
	"Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

func copyProxyHeaders(dst, src http.Header) {
	for key, values := range src {
		if hopByHopHeaders[http.CanonicalHeaderKey(key)] {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// repairMalformedToolHistory preserves normal requests exactly. If any empty
// tool/function name is found, legacy tool calls and results are removed from
// the conversation while user/assistant text and current tool definitions are
// retained. This is the request-level equivalent of a safe provider handoff.
func repairMalformedToolHistory(raw []byte) ([]byte, bool, error) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, false, err
	}
	if !containsEmptyName(root) {
		return raw, false, nil
	}
	if object, ok := root.(map[string]any); ok {
		if messages, exists := object["messages"].([]any); exists {
			object["messages"] = cleanChatMessages(messages)
		}
		if input, exists := object["input"].([]any); exists {
			object["input"] = cleanResponsesInput(input)
		}
		if tools, exists := object["tools"].([]any); exists {
			object["tools"] = cleanToolDefinitions(tools)
		}
	}
	fillEmptyNames(root)
	repaired, err := json.Marshal(root)
	return repaired, true, err
}

func containsEmptyName(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		if name, ok := value["name"].(string); ok && strings.TrimSpace(name) == "" {
			return true
		}
		for _, child := range value {
			if containsEmptyName(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if containsEmptyName(child) {
				return true
			}
		}
	}
	return false
}

func cleanChatMessages(messages []any) []any {
	out := make([]any, 0, len(messages))
	for _, item := range messages {
		message, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		role, _ := message["role"].(string)
		if role == "tool" || role == "function" {
			continue
		}
		delete(message, "tool_calls")
		delete(message, "function_call")
		if role == "assistant" && !hasTextContent(message["content"]) {
			continue
		}
		out = append(out, message)
	}
	return out
}

func cleanResponsesInput(input []any) []any {
	out := make([]any, 0, len(input))
	for _, item := range input {
		object, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		typeName := strings.ToLower(stringValue(object["type"]))
		if isToolProtocolType(typeName) {
			continue
		}
		if content, ok := object["content"].([]any); ok {
			cleaned := make([]any, 0, len(content))
			for _, part := range content {
				partObject, isObject := part.(map[string]any)
				if isObject && isToolProtocolType(strings.ToLower(stringValue(partObject["type"]))) {
					continue
				}
				cleaned = append(cleaned, part)
			}
			object["content"] = cleaned
		}
		out = append(out, object)
	}
	return out
}

func cleanToolDefinitions(tools []any) []any {
	out := make([]any, 0, len(tools))
	for _, item := range tools {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, exists := object["name"].(string); exists && strings.TrimSpace(name) == "" {
			continue
		}
		if function, exists := object["function"].(map[string]any); exists {
			if name, named := function["name"].(string); named && strings.TrimSpace(name) == "" {
				continue
			}
		}
		out = append(out, object)
	}
	return out
}

func isToolProtocolType(value string) bool {
	return strings.Contains(value, "function_call") || strings.Contains(value, "tool_call") ||
		strings.Contains(value, "tool_result") || strings.Contains(value, "function_result")
}

func hasTextContent(value any) bool {
	switch value := value.(type) {
	case string:
		return strings.TrimSpace(value) != ""
	case []any:
		for _, item := range value {
			if object, ok := item.(map[string]any); ok {
				if text := stringValue(object["text"]); strings.TrimSpace(text) != "" {
					return true
				}
			}
		}
	}
	return false
}

func fillEmptyNames(value any) {
	switch value := value.(type) {
	case map[string]any:
		if name, ok := value["name"].(string); ok && strings.TrimSpace(name) == "" {
			value["name"] = "grok_switch_recovered_tool"
		}
		for _, child := range value {
			fillEmptyNames(child)
		}
	case []any:
		for _, child := range value {
			fillEmptyNames(child)
		}
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
