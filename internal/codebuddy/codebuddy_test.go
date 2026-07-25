package codebuddy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParseModelsFromHelp(t *testing.T) {
	help := `  --model <model>  Model ID. Currently supported: (hy3, glm-5.2, deepseek-v4-pro)
  --fallback-model <model> fallback`
	got := ParseModelsFromHelp(help)
	want := []string{"hy3", "glm-5.2", "deepseek-v4-pro"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseModelsFromHelp() = %#v, want %#v", got, want)
	}
}

func TestInspectUsesHelpAndFallbackWithoutReadingAuth(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake POSIX executable")
	}
	dir := t.TempDir()
	auth := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(auth, []byte("do not read"), 0); err != nil {
		t.Fatal(err)
	}
	executable := fakeExecutable(t, `
case "$1" in
  --version) printf '2.125.5\n' ;;
  --help) printf 'Usage: codebuddy\n  --model <model> supported: (hy3, glm-5.2)\n' ;;
  *) exit 9 ;;
esac
`)
	status := Inspect(context.Background(), executable)
	if !status.Available || status.Version != "2.125.5" || status.Fallback {
		t.Fatalf("Inspect() = %#v", status)
	}
	if !reflect.DeepEqual(status.Models, []string{"hy3", "glm-5.2"}) {
		t.Fatalf("models = %#v", status.Models)
	}
	if err := os.Chmod(auth, 0o600); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(auth)
	if err != nil || string(contents) != "do not read" {
		t.Fatalf("authentication sentinel changed: contents=%q err=%v", contents, err)
	}

	fallbackExecutable := fakeExecutable(t, `
case "$1" in
  --version) printf '1.0.0\n' ;;
  --help) printf 'Usage changed\n' ;;
esac
`)
	fallback := Inspect(context.Background(), fallbackExecutable)
	if !fallback.Fallback || len(fallback.Models) == 0 {
		t.Fatalf("fallback Inspect() = %#v", fallback)
	}
}

func TestDefaultArgsAllowNativeToolExecution(t *testing.T) {
	args := DefaultArgs("hy3")
	joined := " " + strings.Join(args, " ") + " "
	for _, required := range []string{
		" --print ", " --output-format stream-json ", " --permission-mode acceptEdits ",
		" --tools default ", " --no-session-persistence ",
		" --setting-sources user ", " --strict-mcp-config ",
		` --mcp-config {"mcpServers":{}} `, " --model hy3 ",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("missing required args %q in %q", required, joined)
		}
	}
	for _, forbidden := range []string{" -y ", " --bg ", " --background ", "daemon"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("forbidden argument %q in %q", forbidden, joined)
		}
	}
}

func TestRunnerRejectsModelFlagInjection(t *testing.T) {
	err := (Runner{}).Run(context.Background(), RunRequest{Prompt: "hello", Model: "--bg"}, func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRunnerFakeExecutableReceivesSafeArgsAndPromptOnStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake POSIX executable")
	}
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args.jsonl")
	stdinPath := filepath.Join(dir, "stdin.txt")
	envPath := filepath.Join(dir, "env.txt")
	t.Setenv("FAKE_ARGS", argsPath)
	t.Setenv("FAKE_STDIN", stdinPath)
	t.Setenv("FAKE_ENV", envPath)
	executable := fakeExecutable(t, `
: > "$FAKE_ARGS"
for arg in "$@"; do printf '%s\n' "$arg" >> "$FAKE_ARGS"; done
printf '%s' "$CODEBUDDY_CODE_DISABLE_BACKGROUND_TASKS" > "$FAKE_ENV"
cat > "$FAKE_STDIN"
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","result":"done"}'
`)
	var events []Event
	err := (Runner{Executable: executable}).Run(context.Background(), RunRequest{
		Prompt: "secret prompt", Cwd: dir, Model: "hy3",
	}, func(event Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt, err := os.ReadFile(stdinPath)
	if err != nil || string(prompt) != "secret prompt" {
		t.Fatalf("stdin = %q, err = %v", prompt, err)
	}
	argData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Fields(string(argData))
	if !reflect.DeepEqual(args, DefaultArgs("hy3")) {
		t.Fatalf("args = %#v, want %#v", args, DefaultArgs("hy3"))
	}
	envData, err := os.ReadFile(envPath)
	if err != nil || string(envData) != "1" {
		t.Fatalf("background task guard = %q, err = %v", envData, err)
	}
	if len(events) != 2 || events[0].Kind != EventText || events[0].Text != "hello" || events[1].Kind != EventResult {
		t.Fatalf("events = %#v", events)
	}
}

func TestMessagesToPromptPreservesToolContext(t *testing.T) {
	raw := []byte(`[
		{"role":"system","content":"follow policy"},
		{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"secret"}}]},
		{"role":"assistant","content":"answer","tool_calls":[{"id":"call-1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"f.txt\"}"}}]},
		{"role":"tool","tool_call_id":"call-1","content":"file contents"}
	]`)
	prompt, err := MessagesJSONToPrompt(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"系统上下文（引用）：\nfollow policy",
		"用户：\nhello",
		"助手：\nanswer",
		"助手 请求工具调用：read_file",
		"工具结果（tool_use_id=call-1）：\nfile contents",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("prompt missing %q: %s", expected, prompt)
		}
	}
	for _, forbidden := range []string{"secret", "image_url"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("prompt contains %q: %s", forbidden, prompt)
		}
	}
}

func TestEventDecoder(t *testing.T) {
	decoder := NewEventDecoder(strings.NewReader("\n" +
		`{"type":"content_block_delta","delta":{"text":"a"}}` + "\n" +
		`{"type":"result","subtype":"success","result":"ok","session_id":"s1"}` + "\n"))
	first, err := decoder.Next()
	if err != nil || first.Kind != EventText || first.Text != "a" {
		t.Fatalf("first = %#v, err = %v", first, err)
	}
	second, err := decoder.Next()
	if err != nil || second.Kind != EventResult || second.Text != "ok" || second.SessionID != "s1" {
		t.Fatalf("second = %#v, err = %v", second, err)
	}
	if _, err := decoder.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("EOF err = %v", err)
	}
	var acc toolAccumulator
	if _, err := ParseEvent([]byte("not-json"), &acc); err == nil {
		t.Fatal("ParseEvent accepted invalid JSON")
	}
}

func TestParseEventToolUseAndResult(t *testing.T) {
	var acc toolAccumulator

	// content_block_start for tool_use
	start := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-123","name":"Edit","input":{}}}`)
	event, err := ParseEvent(start, &acc)
	if err != nil || event.Kind != EventMeta {
		t.Fatalf("start: event=%#v err=%v", event, err)
	}

	// content_block_delta with partial JSON
	delta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/f\"}"}}`)
	event, err = ParseEvent(delta, &acc)
	if err != nil || event.Kind != EventMeta {
		t.Fatalf("delta: event=%#v err=%v", event, err)
	}

	// content_block_stop emits the accumulated tool_use
	stop := []byte(`{"type":"content_block_stop","index":0}`)
	event, err = ParseEvent(stop, &acc)
	if err != nil || event.Kind != EventToolUse {
		t.Fatalf("stop: event=%#v err=%v", event, err)
	}
	if event.Tool == nil || event.Tool.ID != "call-123" || event.Tool.Name != "Edit" {
		t.Fatalf("tool event = %#v", event.Tool)
	}
	if !strings.Contains(event.Tool.InputJSON, "/f") {
		t.Fatalf("tool input = %q", event.Tool.InputJSON)
	}

	// tool_result
	result := []byte(`{"type":"tool_result","tool_use_id":"call-123","content":[{"type":"text","text":"file updated"}],"is_error":false}`)
	event, err = ParseEvent(result, &acc)
	if err != nil || event.Kind != EventToolResult {
		t.Fatalf("result: event=%#v err=%v", event, err)
	}
	if event.Tool == nil || event.Tool.UseID != "call-123" || event.Tool.IsError {
		t.Fatalf("result tool = %#v", event.Tool)
	}
	if event.Text != "file updated" {
		t.Fatalf("result text = %q", event.Text)
	}
}

func TestMessageContentRejectsMalformedShape(t *testing.T) {
	_, err := MessagesToPrompt([]Message{{Role: "user", Content: json.RawMessage(`{"unexpected":true}`)}})
	if err == nil {
		t.Fatal("MessagesToPrompt accepted object content")
	}
}

func fakeExecutable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codebuddy")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
