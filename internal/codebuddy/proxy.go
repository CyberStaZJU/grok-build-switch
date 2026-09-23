// Package codebuddy adapts Tencent CodeBuddy / WorkBuddy subscription chat
// completions into a Grok-compatible OpenAI Chat Completions surface.
//
// Upstream quirks handled here:
//   - only stream=true is accepted (non-stream returns code 11101)
//   - intermediate SSE chunks use finish_reason:"" which Grok rejects
package codebuddy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultUpstream is the China-site CodeBuddy chat endpoint.
	DefaultUpstream = "https://copilot.tencent.com/v2/chat/completions"
	// SourceTag identifies managed profiles created for this proxy.
	SourceTag = "codebuddy-proxy"
	// DefaultModel is the profile default requested by the user.
	DefaultModel = "hy3"
)

// KnownModels are the subscription models we expose through the Switch catalog.
var KnownModels = []string{
	"auto",
	"hy4-preview",
	"hy3",
	"glm-5.3",
	"glm-5.3-flash",
	"kimi-k3-2",
	"deepseek-v4-flash",
	"deepseek-v4-pro",
	"glm-5.2",
	"glm-5.1",
	"kimi-k2.7",
	"kimi-k2.6",
	"kimi-k2.5",
}

// ModelOffer is one CodeBuddy model as shown to Grok (/m / config alias) and
// the upstream id forwarded to CodeBuddy chat.
type ModelOffer struct {
	ID       string // advertised id (may include "@Provider" disambiguation)
	Upstream string // bare CodeBuddy model id
}

// Handler proxies OpenAI-compatible /v1 requests to CodeBuddy.
type Handler struct {
	Upstream string
	// FallbackKey is used when the client did not send Authorization.
	FallbackKey string
	Client      *http.Client
	// Offers is the enabled catalog advertised on GET /v1/models. Nil means
	// the full fallback list for the standalone helper; an explicit empty slice
	// fails closed.
	Offers []ModelOffer
	// AllowedModels is the trusted catalog. Nil uses the fallback list.
	AllowedModels []string
}

func (h *Handler) upstream() string {
	if strings.TrimSpace(h.Upstream) != "" {
		return h.Upstream
	}
	return DefaultUpstream
}

func (h *Handler) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return &http.Client{Timeout: 0} // streaming; no overall timeout
}

// ServeHTTP serves /v1/models and /v1/chat/completions under any mount prefix
// that ends with those paths.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case strings.HasSuffix(path, "/v1/models") || strings.HasSuffix(path, "/models"):
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.serveModels(w)
	case strings.HasSuffix(path, "/v1/chat/completions") || strings.HasSuffix(path, "/chat/completions"):
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.serveChat(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) enabledOffers() []ModelOffer {
	allowed := h.AllowedModels
	if allowed == nil {
		allowed = KnownModels
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		if IsValidModelID(id) {
			allowedSet[id] = true
		}
	}
	if h.Offers != nil {
		out := make([]ModelOffer, 0, len(h.Offers))
		seen := map[string]bool{}
		for _, offer := range h.Offers {
			id := strings.TrimSpace(offer.ID)
			upstream := strings.TrimSpace(offer.Upstream)
			if upstream == "" {
				upstream = id
			}
			if id == "" || seen[id] || !allowedSet[upstream] {
				continue
			}
			seen[id] = true
			out = append(out, ModelOffer{ID: id, Upstream: upstream})
		}
		return out
	}
	out := make([]ModelOffer, 0, len(allowed))
	for _, id := range allowed {
		if allowedSet[id] {
			out = append(out, ModelOffer{ID: id, Upstream: id})
		}
	}
	return out
}

func (h *Handler) resolveUpstream(id string) (string, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", false
	}
	for _, offer := range h.enabledOffers() {
		if offer.ID == id || offer.Upstream == id {
			return offer.Upstream, true
		}
	}
	return "", false
}

func (h *Handler) serveModels(w http.ResponseWriter) {
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	offers := h.enabledOffers()
	out := make([]model, 0, len(offers))
	for _, offer := range offers {
		out = append(out, model{ID: offer.ID, Object: "model", Created: 1700000000, OwnedBy: "codebuddy"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": out})
}

func (h *Handler) serveChat(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	clientStream := truthy(payload["stream"])
	if err := normalizeToolHistory(payload); err != nil {
		http.Error(w, "invalid tool history: "+err.Error(), http.StatusBadRequest)
		return
	}
	// CodeBuddy requires stream; always force it upstream.
	payload["stream"] = true
	if _, ok := payload["stream_options"]; !ok {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	if _, ok := payload["model"]; !ok {
		payload["model"] = DefaultModel
	}
	modelID, _ := payload["model"].(string)
	upstream, ok := h.resolveUpstream(modelID)
	if !ok {
		http.Error(w, fmt.Sprintf("model %q is not enabled in CodeBuddy profile", modelID), http.StatusBadRequest)
		return
	}
	payload["model"] = upstream
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}

	key := bearerFromRequest(r)
	if key == "" {
		key = strings.TrimSpace(h.FallbackKey)
	}
	if key == "" {
		http.Error(w, "missing CodeBuddy API key", http.StatusUnauthorized)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.upstream(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "grok-build-switch-codebuddy/1.0")

	resp, err := h.client().Do(req)
	if err != nil {
		http.Error(w, "CodeBuddy upstream unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(errBody)
		return
	}

	if clientStream {
		h.pipeSanitizedStream(w, resp.Body)
		return
	}
	// Aggregate upstream SSE into one non-stream OpenAI completion for clients
	// that still issue stream=false.
	collected, err := collectStream(resp.Body)
	if err != nil {
		http.Error(w, "upstream stream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, collected)
}

func (h *Handler) pipeSanitizedStream(w http.ResponseWriter, body io.Reader) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	scanner := bufio.NewScanner(body)
	// Large tool-call chunks can exceed the default 64K token size.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	state := newToolCallStreamState()
	for scanner.Scan() {
		line := scanner.Text()
		out := sanitizeSSELine(line, state)
		if _, err := io.WriteString(w, out+"\n"); err != nil {
			return
		}
		// Preserve blank-line event separators from the scanner's stripped \n
		// by emitting an extra newline when the original line was empty — the
		// scanner already drops the trailing \n, so each WriteString ends one
		// logical line; blank lines become "\n" which is correct SSE framing
		// once we also write the final separator after data lines below.
		if flusher != nil {
			flusher.Flush()
		}
	}
	// Ensure a trailing blank line is not required; upstream usually ends with
	// data: [DONE] followed by newline.
}

// sanitizeSSELine rewrites one SSE line. Non-data lines pass through.
// State retains generated IDs and function names across incremental chunks.
func sanitizeSSELine(line string, state *toolCallStreamState) string {
	if !strings.HasPrefix(line, "data:") {
		return line
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" {
		return "data: " + data
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(data), &obj); err != nil {
		return line
	}
	if state == nil {
		state = newToolCallStreamState()
	}
	sanitizeChunk(obj, state)
	b, err := json.Marshal(obj)
	if err != nil {
		return line
	}
	return "data: " + string(b)
}

type toolCallStreamState struct {
	ids    map[int]string
	names  map[int]string
	prefix string
}

func newToolCallStreamState() *toolCallStreamState {
	return &toolCallStreamState{
		ids:    map[int]string{},
		names:  map[int]string{},
		prefix: fmt.Sprintf("call_cb_%x", time.Now().UnixNano()),
	}
}

func (s *toolCallStreamState) id(index int) string {
	if id := s.ids[index]; id != "" {
		return id
	}
	id := fmt.Sprintf("%s_%d", s.prefix, index)
	s.ids[index] = id
	return id
}

func sanitizeChunk(obj map[string]any, state *toolCallStreamState) {
	choices, _ := obj["choices"].([]any)
	for _, raw := range choices {
		ch, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if fr, exists := ch["finish_reason"]; exists {
			switch v := fr.(type) {
			case string:
				// Grok models finish_reason as a closed enum of
				// stop/length/tool_calls/content_filter/function_call. An
				// upstream that reports an out-of-band failure as
				// `finish_reason: "error"` aborts the turn with a
				// serialization error, so surface it as a normal stop and let
				// the error channel carry the failure.
				if v == "" || v == "error" {
					ch["finish_reason"] = nil
				}
			case bool:
				if !v {
					ch["finish_reason"] = nil
				}
			}
		}
		delta, _ := ch["delta"].(map[string]any)
		if delta == nil {
			continue
		}
		// Drop empty noise that confuses strict clients.
		if rc, ok := delta["reasoning_content"].(string); ok && rc == "" {
			delete(delta, "reasoning_content")
		}
		if tc, ok := delta["tool_calls"].([]any); ok && len(tc) == 0 {
			delete(delta, "tool_calls")
		} else if ok {
			repairStreamingToolCalls(tc, state)
		}
		if delta["function_call"] == nil {
			delete(delta, "function_call")
		}
		if delta["refusal"] == nil || delta["refusal"] == "" {
			delete(delta, "refusal")
		}
		if delta["extra_fields"] == nil {
			delete(delta, "extra_fields")
		}
	}
}

func repairStreamingToolCalls(tcs []any, state *toolCallStreamState) {
	if state == nil {
		return
	}
	for position, raw := range tcs {
		tc, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		idx := toolCallIndex(tc, position)
		if id := strings.TrimSpace(stringValue(tc["id"])); id != "" {
			state.ids[idx] = id
		} else {
			tc["id"] = state.id(idx)
		}
		if top, ok := tc["name"].(string); ok && strings.TrimSpace(top) != "" {
			state.names[idx] = top
		}
		fn, _ := tc["function"].(map[string]any)
		if fn == nil {
			if remembered := state.names[idx]; remembered != "" {
				tc["function"] = map[string]any{"name": remembered}
			}
			continue
		}
		if n, ok := fn["name"].(string); ok && strings.TrimSpace(n) != "" {
			state.names[idx] = n
			continue
		}
		if remembered := state.names[idx]; remembered != "" {
			fn["name"] = remembered
		} else if _, exists := fn["name"]; exists && fn["name"] == "" {
			// Drop a leading empty name so a later non-empty name can still land.
			delete(fn, "name")
		}
	}
}

func normalizeToolHistory(payload map[string]any) error {
	messages, ok := payload["messages"].([]any)
	if !ok {
		return nil
	}
	pending := make([]string, 0)
	for messageIndex, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch strings.TrimSpace(stringValue(message["role"])) {
		case "assistant":
			calls, _ := message["tool_calls"].([]any)
			for callIndex, rawCall := range calls {
				call, ok := rawCall.(map[string]any)
				if !ok {
					continue
				}
				id := strings.TrimSpace(stringValue(call["id"]))
				if id == "" {
					id = fmt.Sprintf("call_cb_history_%d_%d", messageIndex, callIndex)
					call["id"] = id
				}
				pending = append(pending, id)
			}
		case "tool":
			id := strings.TrimSpace(stringValue(message["tool_call_id"]))
			if id == "" {
				if len(pending) != 1 {
					return fmt.Errorf("messages[%d] has an empty tool_call_id with %d possible preceding tool calls", messageIndex, len(pending))
				}
				id = pending[0]
				message["tool_call_id"] = id
			}
			for i, candidate := range pending {
				if candidate == id {
					pending = append(pending[:i], pending[i+1:]...)
					break
				}
			}
		}
	}
	return nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func intValue(value any) int {
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	case json.Number:
		parsed, _ := number.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func toolCallIndex(call map[string]any, fallback int) int {
	if _, ok := call["index"]; !ok {
		return fallback
	}
	return intValue(call["index"])
}

func collectStream(body io.Reader) (map[string]any, error) {
	var content strings.Builder
	toolCalls := map[int]map[string]string{}
	state := newToolCallStreamState()
	var model any
	var finish any
	var usage any

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(data), &obj); err != nil {
			continue
		}
		if m := obj["model"]; m != nil {
			model = m
		}
		if u := obj["usage"]; u != nil {
			usage = u
		}
		choices, _ := obj["choices"].([]any)
		for _, raw := range choices {
			ch, _ := raw.(map[string]any)
			if ch == nil {
				continue
			}
			if fr := ch["finish_reason"]; fr != nil && fr != "" {
				finish = fr
			}
			delta, _ := ch["delta"].(map[string]any)
			if delta == nil {
				continue
			}
			if c, ok := delta["content"].(string); ok {
				content.WriteString(c)
			}
			if tcs, ok := delta["tool_calls"].([]any); ok {
				repairStreamingToolCalls(tcs, state)
				for position, tr := range tcs {
					tc, _ := tr.(map[string]any)
					if tc == nil {
						continue
					}
					idx := toolCallIndex(tc, position)
					slot := toolCalls[idx]
					if slot == nil {
						slot = map[string]string{}
						toolCalls[idx] = slot
					}
					if id := strings.TrimSpace(stringValue(tc["id"])); id != "" {
						slot["id"] = id
					}
					fn, _ := tc["function"].(map[string]any)
					if fn != nil {
						if n, ok := fn["name"].(string); ok && n != "" {
							slot["name"] = n
						}
						if a, ok := fn["arguments"].(string); ok {
							slot["arguments"] += a
						}
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	message := map[string]any{"role": "assistant", "content": nilString(content.String())}
	if len(toolCalls) > 0 {
		list := make([]any, 0, len(toolCalls))
		for i := 0; i < len(toolCalls); i++ {
			slot := toolCalls[i]
			if slot == nil {
				continue
			}
			list = append(list, map[string]any{
				"id":   valueOr(slot["id"], fmt.Sprintf("call_%d", i)),
				"type": "function",
				"function": map[string]any{
					"name":      slot["name"],
					"arguments": slot["arguments"],
				},
			})
		}
		message["tool_calls"] = list
		if finish == nil || finish == "" {
			finish = "tool_calls"
		}
	}
	if finish == nil || finish == "" {
		finish = "stop"
	}
	if usage == nil {
		usage = map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
	}
	if model == nil {
		model = "unknown"
	}
	return map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": finish,
			},
		},
		"usage": usage,
	}, nil
}

func bearerFromRequest(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if k := strings.TrimSpace(r.Header.Get("X-Api-Key")); k != "" {
		return k
	}
	return ""
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true" || t == "1"
	case float64:
		return t != 0
	default:
		return false
	}
}

func nilString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}
