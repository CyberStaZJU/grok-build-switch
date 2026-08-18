package codebuddy

import (
	"encoding/json"
	"strings"
	"testing"
)

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
	names := map[int]string{}
	first := sanitizeSSELine(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"list_dir","arguments":""}}]}}]}`, names)
	second := sanitizeSSELine(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"","arguments":"{\""}}]}}]}`, names)

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
	if names[0] != "list_dir" {
		t.Fatalf("remembered name = %#v", names[0])
	}
}
