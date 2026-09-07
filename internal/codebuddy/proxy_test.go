package codebuddy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewProfileUsesKnownContextWindows(t *testing.T) {
	profile := NewProfile("http://127.0.0.1:17878/codebuddy-proxy/v1", "ck_test")
	windows := map[string]int64{}
	for _, model := range profile.Models {
		windows[model.Name] = model.ContextWindow
	}
	if windows["hy3"] != 128000 {
		t.Fatalf("hy3 context = %d", windows["hy3"])
	}
	for _, id := range []string{"deepseek-v4-flash", "deepseek-v4-pro"} {
		if windows[id] != 1000000 {
			t.Fatalf("%s context = %d", id, windows[id])
		}
	}
}

func TestNewProfileDisablesStreamToolCalls(t *testing.T) {
	profile := NewProfile("http://127.0.0.1:17878/codebuddy-proxy/v1", "ck_test")
	if len(profile.Models) == 0 {
		t.Fatal("expected models")
	}
	for _, model := range profile.Models {
		if model.StreamToolCalls == nil || *model.StreamToolCalls {
			t.Fatalf("model %q StreamToolCalls = %#v, want false", model.Name, model.StreamToolCalls)
		}
	}
}

func TestExplicitEmptyOffersFailClosed(t *testing.T) {
	h := &Handler{Offers: []ModelOffer{}}
	if got := h.enabledOffers(); len(got) != 0 {
		t.Fatalf("enabled offers = %#v, want empty", got)
	}
	if _, ok := h.resolveUpstream("hy3"); ok {
		t.Fatal("explicit empty offers resolved fallback model")
	}
}

func TestOffersOutsideTrustedCatalogFailClosed(t *testing.T) {
	h := &Handler{
		AllowedModels: []string{"hy3"},
		Offers:        []ModelOffer{{ID: "hy3", Upstream: "hy3"}, {ID: "removed", Upstream: "removed-model"}},
	}
	offers := h.enabledOffers()
	if len(offers) != 1 || offers[0].Upstream != "hy3" {
		t.Fatalf("enabled offers = %#v", offers)
	}
	if _, ok := h.resolveUpstream("removed"); ok {
		t.Fatal("offer outside trusted catalog was resolved")
	}
}

func TestSanitizeSSELineEmptyFinishReason(t *testing.T) {
	in := `data: {"choices":[{"delta":{"content":"pong"},"finish_reason":""}]}`
	out := sanitizeSSELine(in, nil)
	if !strings.HasPrefix(out, "data: ") {
		t.Fatalf("expected data line, got %q", out)
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(out, "data:"))), &obj); err != nil {
		t.Fatal(err)
	}
	ch := obj["choices"].([]any)[0].(map[string]any)
	if ch["finish_reason"] != nil {
		t.Fatalf("finish_reason = %#v, want nil", ch["finish_reason"])
	}
}

func TestSanitizeSSELineStop(t *testing.T) {
	in := `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`
	out := sanitizeSSELine(in, nil)
	var obj map[string]any
	_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(out, "data:"))), &obj)
	ch := obj["choices"].([]any)[0].(map[string]any)
	if ch["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %#v", ch["finish_reason"])
	}
}

func TestSanitizeSSELineDone(t *testing.T) {
	if got := sanitizeSSELine("data: [DONE]", nil); got != "data: [DONE]" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizeSSELineReinjectsEmptyToolName(t *testing.T) {
	state := newToolCallStreamState()
	first := sanitizeSSELine(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"list_dir","arguments":""}}]}}]}`, state)
	second := sanitizeSSELine(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"{\""}}]}}]}`, state)

	var firstObj, secondObj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(first, "data:"))), &firstObj); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(second, "data:"))), &secondObj); err != nil {
		t.Fatal(err)
	}
	firstName := firstObj["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"]
	secondName := secondObj["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"]
	if firstName != "list_dir" {
		t.Fatalf("first name = %#v", firstName)
	}
	if secondName != "list_dir" {
		t.Fatalf("second name = %#v, want reinjected list_dir", secondName)
	}
	if state.names[0] != "list_dir" {
		t.Fatalf("remembered name = %#v", state.names[0])
	}
}

func TestSanitizeSSELineRepairsEmptyToolCallIDs(t *testing.T) {
	state := newToolCallStreamState()
	first := sanitizeSSELine(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_valid","type":"function","function":{"name":"read_file","arguments":"{}"}},{"index":1,"id":"","type":"function","function":{"name":"list_dir","arguments":""}}]}}]}`, state)
	second := sanitizeSSELine(`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"name":"","arguments":"{}"}}]}}]}`, state)

	firstCalls := toolCallsFromSSE(t, first)
	secondCalls := toolCallsFromSSE(t, second)
	if firstCalls[0]["id"] != "call_valid" {
		t.Fatalf("valid id changed to %#v", firstCalls[0]["id"])
	}
	generated, _ := firstCalls[1]["id"].(string)
	if generated == "" {
		t.Fatal("empty tool call id was not repaired")
	}
	if secondCalls[0]["id"] != generated {
		t.Fatalf("incremental id = %#v, want %q", secondCalls[0]["id"], generated)
	}
	fn := secondCalls[0]["function"].(map[string]any)
	if fn["name"] != "list_dir" {
		t.Fatalf("incremental name = %#v", fn["name"])
	}
}

func TestSanitizeSSELineUsesPositionWhenIndexesAreMissing(t *testing.T) {
	state := newToolCallStreamState()
	line := sanitizeSSELine(`data: {"choices":[{"delta":{"tool_calls":[{"id":"","type":"function","function":{"name":"read_file","arguments":"{}"}},{"id":"","type":"function","function":{"name":"list_dir","arguments":"{}"}}]}}]}`, state)
	calls := toolCallsFromSSE(t, line)
	first, _ := calls[0]["id"].(string)
	second, _ := calls[1]["id"].(string)
	if first == "" || second == "" || first == second {
		t.Fatalf("generated ids = %q, %q", first, second)
	}
}

func TestNormalizeToolHistoryRepairsSingleEmptyPair(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "", "type": "function", "function": map[string]any{"name": "read_file", "arguments": "{}"}}}},
			map[string]any{"role": "tool", "tool_call_id": "", "content": "ok"},
		},
	}
	if err := normalizeToolHistory(payload); err != nil {
		t.Fatal(err)
	}
	messages := payload["messages"].([]any)
	call := messages[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	result := messages[1].(map[string]any)
	id, _ := call["id"].(string)
	if id == "" || result["tool_call_id"] != id {
		t.Fatalf("call id = %#v, result id = %#v", call["id"], result["tool_call_id"])
	}
}

func TestNormalizeToolHistoryRepairsMultipleResultsByExistingIDs(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{"id": "call_1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": "{}"}},
				map[string]any{"id": "", "type": "function", "function": map[string]any{"name": "list_dir", "arguments": "{}"}},
			}},
			map[string]any{"role": "tool", "tool_call_id": "call_1", "content": "one"},
			map[string]any{"role": "tool", "tool_call_id": "", "content": "two"},
		},
	}
	if err := normalizeToolHistory(payload); err != nil {
		t.Fatal(err)
	}
	messages := payload["messages"].([]any)
	calls := messages[0].(map[string]any)["tool_calls"].([]any)
	generated := calls[1].(map[string]any)["id"]
	if generated == "" || messages[2].(map[string]any)["tool_call_id"] != generated {
		t.Fatalf("generated = %#v result = %#v", generated, messages[2].(map[string]any)["tool_call_id"])
	}
}

func TestNormalizeToolHistoryRejectsAmbiguousEmptyResult(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{"id": "", "type": "function", "function": map[string]any{"name": "read_file", "arguments": "{}"}},
				map[string]any{"id": "", "type": "function", "function": map[string]any{"name": "list_dir", "arguments": "{}"}},
			}},
			map[string]any{"role": "tool", "tool_call_id": "", "content": "unknown"},
		},
	}
	if err := normalizeToolHistory(payload); err == nil || !strings.Contains(err.Error(), "2 possible") {
		t.Fatalf("error = %v", err)
	}
}

func TestCollectStreamRepairsEmptyToolCallID(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"model":"hy4-preview","choices":[{"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"run_terminal_command","arguments":"{\"command\":"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"\"pwd\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n")
	completion, err := collectStream(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	choices := completion["choices"].([]any)
	message := choices[0].(map[string]any)["message"].(map[string]any)
	call := message["tool_calls"].([]any)[0].(map[string]any)
	id, _ := call["id"].(string)
	if id == "" {
		t.Fatal("aggregated tool call id is empty")
	}
	fn := call["function"].(map[string]any)
	if fn["name"] != "run_terminal_command" || fn["arguments"] != `{"command":"pwd"}` {
		t.Fatalf("function = %#v", fn)
	}
}

func toolCallsFromSSE(t *testing.T, line string) []map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &obj); err != nil {
		t.Fatal(err)
	}
	raw := obj["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["tool_calls"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(map[string]any))
	}
	return out
}
