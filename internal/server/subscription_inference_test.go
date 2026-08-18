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

func TestGeminiToolSchemaDropsEmptyEnumAndCollapsesNullType(t *testing.T) {
	raw := []byte(`{
		"model":"subscription/gemini/gemini-3.7-flash-high",
		"messages":[{"role":"user","content":"整理重复安装"}],
		"tools":[
			{
				"type":"function",
				"function":{
					"name":"todo_write",
					"parameters":{
						"type":"object",
						"properties":{
							"todos":{
								"type":"array",
								"items":{
									"type":"object",
									"properties":{
										"status":{
											"type":["string","null"],
											"enum":["pending","in_progress","completed","cancelled",null]
										}
									}
								}
							}
						}
					}
				}
			},
			{
				"type":"function",
				"function":{
					"name":"spawn_subagent",
					"parameters":{
						"type":"object",
						"properties":{
							"capability_mode":{
								"description":"Capability mode",
								"enum":["read-only","read-write","execute","all",null],
								"type":["string","null"]
							},
							"isolation":{
								"enum":["none","worktree",null],
								"type":["string","null"]
							}
						}
					}
				}
			}
		]
	}`)
	repaired, changed, err := repairMalformedToolHistory(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected Gemini tool schema to be sanitized")
	}
	var body map[string]any
	if err := json.Unmarshal(repaired, &body); err != nil {
		t.Fatal(err)
	}
	tools := body["tools"].([]any)
	todoStatus := nestedMap(t, tools[0], "function", "parameters", "properties", "todos", "items", "properties", "status")
	assertGeminiStringEnum(t, todoStatus, "pending", "in_progress", "completed", "cancelled")
	capability := nestedMap(t, tools[1], "function", "parameters", "properties", "capability_mode")
	assertGeminiStringEnum(t, capability, "read-only", "read-write", "execute", "all")
	isolation := nestedMap(t, tools[1], "function", "parameters", "properties", "isolation")
	assertGeminiStringEnum(t, isolation, "none", "worktree")
}

func TestGeminiResponsesAndTopLevelParameterSchemasAreSanitized(t *testing.T) {
	raw := []byte(`{
		"model":"subscription/gemini/gemini-3.7-flash-high",
		"tools":[
			{"type":"function","name":"read_file","parameters":{"properties":{"path":{"type":["string","null"],"enum":["a",null,""]}}}},
			{"type":"function","name":"write_file","input_schema":{"properties":{"mode":{"anyOf":[{"type":"string","enum":["create","append",null]},{"type":"null"}]}}}}
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
	tools := body["tools"].([]any)
	pathSchema := nestedMap(t, tools[0], "parameters", "properties", "path")
	assertGeminiStringEnum(t, pathSchema, "a")
	modeSchema := nestedMap(t, tools[1], "input_schema", "properties", "mode")
	assertGeminiStringEnum(t, modeSchema, "create", "append")
	if _, exists := modeSchema["anyOf"]; exists {
		t.Fatalf("anyOf was not flattened: %#v", modeSchema)
	}
}

func TestCodexRequestWithNullEnumIsBytePreserving(t *testing.T) {
	raw := []byte(`{"model":"subscription/codex/gpt-5.6-terra","tools":[{"type":"function","function":{"name":"todo_write","parameters":{"properties":{"status":{"type":["string","null"],"enum":["pending",null]}}}}}]}`)
	repaired, changed, err := repairMalformedToolHistory(raw)
	if err != nil {
		t.Fatal(err)
	}
	if changed || string(repaired) != string(raw) {
		t.Fatalf("codex request changed: changed=%v body=%s", changed, repaired)
	}
}

func TestGrokSubscriptionRequestWithNullEnumIsBytePreserving(t *testing.T) {
	raw := []byte(`{"model":"subscription/grok/grok-4","tools":[{"type":"function","function":{"name":"todo_write","parameters":{"properties":{"status":{"enum":["pending",null]}}}}}]}`)
	repaired, changed, err := repairMalformedToolHistory(raw)
	if err != nil {
		t.Fatal(err)
	}
	if changed || string(repaired) != string(raw) {
		t.Fatalf("grok request changed: changed=%v body=%s", changed, repaired)
	}
}

func TestCleanGeminiRequestWithoutDirtySchemaIsBytePreserving(t *testing.T) {
	raw := []byte(`{"model":"subscription/gemini/gemini-3.7-flash-high","tools":[{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}]}`)
	repaired, changed, err := repairMalformedToolHistory(raw)
	if err != nil {
		t.Fatal(err)
	}
	if changed || string(repaired) != string(raw) {
		t.Fatalf("clean gemini request changed: changed=%v body=%s", changed, repaired)
	}
}

func TestGeminiSchemaSanitizeStillDropsEmptyToolNames(t *testing.T) {
	raw := []byte(`{
		"model":"subscription/gemini/gemini-3.7-flash-high",
		"tools":[
			{"type":"function","function":{"name":"","parameters":{"properties":{"status":{"enum":["a",null]}}}}},
			{"type":"function","function":{"name":"todo_write","parameters":{"properties":{"status":{"type":["string","null"],"enum":["pending",null]}}}}}
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
	tools := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools=%#v", tools)
	}
	status := nestedMap(t, tools[0], "function", "parameters", "properties", "status")
	assertGeminiStringEnum(t, status, "pending")
}

func assertGeminiStringEnum(t *testing.T, schema map[string]any, want ...string) {
	t.Helper()
	if schema["type"] != "string" {
		t.Fatalf("type=%#v want string in %#v", schema["type"], schema)
	}
	if schema["nullable"] != true {
		t.Fatalf("nullable=%#v want true in %#v", schema["nullable"], schema)
	}
	got, ok := schema["enum"].([]any)
	if !ok {
		t.Fatalf("enum missing in %#v", schema)
	}
	if len(got) != len(want) {
		t.Fatalf("enum=%#v want %v", got, want)
	}
	for i, value := range got {
		text, _ := value.(string)
		if text != want[i] {
			t.Fatalf("enum[%d]=%#v want %q", i, value, want[i])
		}
		if strings.TrimSpace(text) == "" {
			t.Fatalf("enum still contains empty value: %#v", got)
		}
	}
}

func nestedMap(t *testing.T, root any, keys ...string) map[string]any {
	t.Helper()
	current := root
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("expected object at %q, got %#v", key, current)
		}
		current, ok = object[key]
		if !ok {
			t.Fatalf("missing %q in %#v", key, object)
		}
	}
	object, ok := current.(map[string]any)
	if !ok {
		t.Fatalf("expected object, got %#v", current)
	}
	return object
}
