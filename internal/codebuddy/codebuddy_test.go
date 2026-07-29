package codebuddy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
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

func TestDefaultArgsEnforceReadOnlyTools(t *testing.T) {
	args := DefaultArgs("hy3", CapabilityReadOnly)
	joined := " " + strings.Join(args, " ") + " "
	for _, required := range []string{
		" --print ", " --output-format stream-json ", " --permission-mode default ",
		" --tools Read,Grep,Glob ", " --no-session-persistence ",
		" --setting-sources user ", " --strict-mcp-config ",
		` --mcp-config {"mcpServers":{}} `, " --model hy3 ",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("missing required args %q in %q", required, joined)
		}
	}
	for _, forbidden := range []string{" acceptEdits ", " --tools default ", " -y ", " --bg ", " --background ", "daemon", " Bash ", " Write ", " Edit "} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("forbidden argument %q in %q", forbidden, joined)
		}
	}
}

func TestDefaultArgsExposeRequestedCapabilityWithoutBypass(t *testing.T) {
	webJoined := " " + strings.Join(DefaultArgs("hy3", CapabilityWeb), " ") + " "
	if !strings.Contains(webJoined, " --tools Read,Grep,Glob,WebSearch,WebFetch ") || !strings.Contains(webJoined, " --allowedTools WebSearch,WebFetch ") {
		t.Fatalf("web capability args = %q", webJoined)
	}
	for _, test := range []struct {
		capability Capability
		tools      string
	}{
		{CapabilityExecute, "Read,Grep,Glob,Bash"},
		{CapabilityAll, "Read,Grep,Glob,Bash,Write,Edit"},
	} {
		joined := " " + strings.Join(DefaultArgs("hy3", test.capability), " ") + " "
		if !strings.Contains(joined, " --tools "+test.tools+" ") || !strings.Contains(joined, " --permission-mode acceptEdits ") || !strings.Contains(joined, " --allowedTools ") {
			t.Errorf("capability %s args = %q", test.capability, joined)
		}
		for _, forbidden := range []string{" bypassPermissions ", " -y ", " --bg ", " --background ", " --tools default "} {
			if strings.Contains(joined, forbidden) {
				t.Errorf("capability %s contains forbidden argument %q: %s", test.capability, forbidden, joined)
			}
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
	if err != nil {
		t.Fatal(err)
	}
	promptText := string(prompt)
	for _, expected := range []string{
		"当前只提供 Read、Grep、Glob 三种只读工具",
		"Bash、Write、Edit、后台任务和 MCP 均不可用",
		"不要从工作区根目录并行发起多个宽泛的递归 Glob",
		"若搜索超时，应缩小到相关目录",
		"--- 对话记录开始 ---\nsecret prompt",
	} {
		if !strings.Contains(promptText, expected) {
			t.Errorf("stdin missing guidance or original prompt %q: %s", expected, promptText)
		}
	}
	if strings.Count(promptText, "secret prompt") != 1 {
		t.Fatalf("original prompt count = %d, stdin=%q", strings.Count(promptText, "secret prompt"), promptText)
	}
	argData, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Fields(string(argData))
	if !reflect.DeepEqual(args, DefaultArgs("hy3", CapabilityReadOnly)) {
		t.Fatalf("args = %#v, want %#v", args, DefaultArgs("hy3", CapabilityReadOnly))
	}
	envData, err := os.ReadFile(envPath)
	if err != nil || string(envData) != "1" {
		t.Fatalf("background task guard = %q, err = %v", envData, err)
	}
	if len(events) != 2 || events[0].Kind != EventText || events[0].Text != "hello" || events[1].Kind != EventResult {
		t.Fatalf("events = %#v", events)
	}
}

func TestRunnerAllCapabilityUsesDetachedWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake POSIX executable")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repo}, args...)...)
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	git("init")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked.txt")
	git("commit", "-m", "base")

	cwdPath := filepath.Join(t.TempDir(), "cwd.txt")
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	t.Setenv("FAKE_CWD", cwdPath)
	t.Setenv("FAKE_ARGS", argsPath)
	t.Setenv("FAKE_STDIN", stdinPath)
	executable := fakeExecutable(t, `
pwd > "$FAKE_CWD"
printf '%s\n' "$@" > "$FAKE_ARGS"
cat > "$FAKE_STDIN"
printf '%s\n' '{"type":"result","subtype":"success","result":"done"}'
`)
	var worktree string
	err := (Runner{Executable: executable}).Run(context.Background(), RunRequest{
		Prompt: "modify and test", Cwd: repo, Model: "hy3", Capability: CapabilityAll,
	}, func(event Event) error {
		if event.Type == "grok_switch_worktree" {
			worktree = event.Text
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if worktree == "" || worktree == repo {
		t.Fatalf("worktree = %q, repo=%q", worktree, repo)
	}
	cwdData, err := os.ReadFile(cwdPath)
	if err != nil || strings.TrimSpace(string(cwdData)) != worktree {
		t.Fatalf("runner cwd=%q err=%v, want %q", cwdData, err, worktree)
	}
	if _, err := os.Stat(filepath.Join(worktree, "tracked.txt")); err != nil {
		t.Fatalf("isolated worktree missing tracked file: %v", err)
	}
	argsData, err := os.ReadFile(argsPath)
	if err != nil || !strings.Contains(string(argsData), "Read,Grep,Glob,Bash,Write,Edit") {
		t.Fatalf("args=%q err=%v", argsData, err)
	}
	stdinData, err := os.ReadFile(stdinPath)
	if err != nil || !strings.Contains(string(stdinData), "独立的 detached Git worktree") || !strings.Contains(string(stdinData), worktree) {
		t.Fatalf("stdin=%q err=%v", stdinData, err)
	}
}

func TestRunnerRejectsDirtyRepositoryForIsolatedExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires git")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repo}, args...)...)
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	git("init")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked.txt")
	git("commit", "-m", "base")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := fakeExecutable(t, `exit 0`)
	err := (Runner{Executable: executable}).Run(context.Background(), RunRequest{
		Prompt: "modify", Cwd: repo, Model: "hy3", Capability: CapabilityAll,
	}, func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "干净 Git 工作区") {
		t.Fatalf("Run() error = %v", err)
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
