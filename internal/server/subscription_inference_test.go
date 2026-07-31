package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRepairMalformedResponsesHistoryDropsToolProtocol(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5",
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"请修复项目"}]},
			{"type":"function_call","call_id":"call-bad","name":"","arguments":"{}"},
			{"type":"function_call_output","call_id":"call-bad","output":"secret result"},
			{"role":"assistant","content":[{"type":"output_text","text":"我会检查文件"}]}
		],
		"tools":[{"type":"function","name":"read_file"}]
	}`)

	repaired, changed, err := repairMalformedToolHistory(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected malformed request to be repaired")
	}
	text := string(repaired)
	for _, forbidden := range []string{`"name":""`, "call-bad", "secret result"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("repaired request still contains %q: %s", forbidden, text)
		}
	}
	for _, wanted := range []string{"请修复项目", "我会检查文件", "read_file"} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("repaired request lost %q: %s", wanted, text)
		}
	}
}

func TestRepairMalformedChatHistoryDropsCallsAndResults(t *testing.T) {
	raw := []byte(`{
		"messages":[
			{"role":"user","content":"继续"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"bad","type":"function","function":{"name":"","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"bad","content":"private output"},
			{"role":"assistant","content":"已完成检查"}
		]
	}`)
	repaired, changed, err := repairMalformedToolHistory(raw)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	var body map[string]any
	if err := json.Unmarshal(repaired, &body); err != nil {
		t.Fatal(err)
	}
	messages := body["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages=%#v", messages)
	}
	text := string(repaired)
	if strings.Contains(text, "private output") || strings.Contains(text, "tool_calls") {
		t.Fatalf("tool protocol leaked: %s", text)
	}
}

func TestRepairMalformedToolDefinitionsAndLegacyFunctionResult(t *testing.T) {
	raw := []byte(`{
		"metadata":{"name":""},
		"messages":[
			{"role":"assistant","content":null,"function_call":{"name":"","arguments":"{}"}},
			{"role":"function","name":"","content":"bad legacy result"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"good","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"good","content":"valid result"}
		],
		"tools":[
			{"type":"function","function":{"name":"","description":"bad chat definition"}},
			{"type":"function","name":"","description":"bad responses definition"},
			{"type":"function","function":{"name":"read_file"}}
		]
	}`)
	repaired, changed, err := repairMalformedToolHistory(raw)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	text := string(repaired)
	for _, forbidden := range []string{"bad legacy result", "bad chat definition", "bad responses definition"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("malformed protocol retained %q: %s", forbidden, text)
		}
	}
	for _, wanted := range []string{`"metadata":{"name":""}`, "read_file", "valid result", `"tool_call_id":"good"`} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("unrelated or valid data lost %q: %s", wanted, text)
		}
	}
}

func TestRepairValidRequestIsBytePreserving(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"read_file"}}]}`)
	repaired, changed, err := repairMalformedToolHistory(raw)
	if err != nil {
		t.Fatal(err)
	}
	if changed || string(repaired) != string(raw) {
		t.Fatalf("valid request changed: changed=%v body=%s", changed, repaired)
	}
}
