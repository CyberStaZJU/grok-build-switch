package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"grok_switch/internal/codebuddy"
)

type fakeCodeBuddyRunner struct {
	mu       sync.Mutex
	status   codebuddy.Status
	events   []codebuddy.Event
	err      error
	requests []codebuddy.RunRequest
}

func (f *fakeCodeBuddyRunner) Inspect(context.Context) codebuddy.Status {
	return f.status
}

func (f *fakeCodeBuddyRunner) Run(_ context.Context, request codebuddy.RunRequest, emit func(codebuddy.Event) error) error {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	f.mu.Unlock()
	for _, event := range f.events {
		if err := emit(event); err != nil {
			return err
		}
	}
	return f.err
}

func (f *fakeCodeBuddyRunner) lastRequest(t *testing.T) codebuddy.RunRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 {
		t.Fatalf("Run calls = %d, want 1", len(f.requests))
	}
	return f.requests[0]
}

func TestCodeBuddyStatusAndModels(t *testing.T) {
	fake := &fakeCodeBuddyRunner{status: codebuddy.Status{
		Available: true,
		Path:      "/Users/private/.local/bin/codebuddy",
		Version:   "2.0.0",
		Models:    []string{"hy3", "codebuddy/glm-5.2", "--unsafe", "hy3"},
		Error:     "authentication token from /Users/private/.codebuddy/auth.json",
	}}
	s := &Server{CodeBuddy: fake}

	statusRequest := httptest.NewRequest(http.MethodGet, "/api/codebuddy/status", nil)
	statusResponse := httptest.NewRecorder()
	s.handleCodeBuddyStatus(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d, body=%s", statusResponse.Code, statusResponse.Body.String())
	}
	if body := statusResponse.Body.String(); !strings.Contains(body, `"codebuddy/hy3"`) || !strings.Contains(body, `"codebuddy/glm-5.2"`) || strings.Contains(body, "--unsafe") || strings.Contains(body, "private") || strings.Contains(body, "auth.json") || strings.Contains(body, "token") {
		t.Fatalf("unexpected status body: %s", body)
	}

	modelsRequest := loopbackRequest(http.MethodGet, "/codebuddy/v1/models", "")
	modelsResponse := httptest.NewRecorder()
	s.handleCodeBuddyInference(modelsResponse, modelsRequest)
	if modelsResponse.Code != http.StatusOK {
		t.Fatalf("models endpoint = %d, body=%s", modelsResponse.Code, modelsResponse.Body.String())
	}
	var response struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(modelsResponse.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Object != "list" || len(response.Data) != 2 || response.Data[0].ID != "codebuddy/hy3" || response.Data[1].ID != "codebuddy/glm-5.2" {
		t.Fatalf("models response = %#v", response)
	}
}

func TestCodeBuddyInferenceRejectsRemoteButNormalizesProtocolTools(t *testing.T) {
	fake := &fakeCodeBuddyRunner{status: codebuddy.Status{Available: true, Models: []string{"hy3"}}, events: []codebuddy.Event{{Kind: codebuddy.EventResult, Text: "text answer"}}}
	s := &Server{CodeBuddy: fake}

	remote := httptest.NewRequest(http.MethodPost, "/codebuddy/v1/chat/completions", strings.NewReader(`{"model":"hy3","messages":[{"role":"user","content":"hello"}]}`))
	remote.RemoteAddr = "192.168.1.9:1234"
	response := httptest.NewRecorder()
	s.handleCodeBuddyInference(response, remote)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote status = %d, body=%s", response.Code, response.Body.String())
	}

	body := `{
		"model":"hy3",
		"messages":[
			{"role":"user","content":"inspect this project"},
			{"role":"assistant","content":"I will inspect it","tool_calls":[{"id":"call-1","function":{"name":"read_file","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call-1","content":"file contents here"}
		],
		"tools":[{"type":"function","function":{"name":"read_file"}}],
		"tool_choice":"auto",
		"parallel_tool_calls":true
	}`
	request := loopbackRequest(http.MethodPost, "/codebuddy/v1/chat/completions", body)
	response = httptest.NewRecorder()
	s.handleCodeBuddyInference(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	run := fake.lastRequest(t)
	if !strings.Contains(run.Prompt, "inspect this project") || !strings.Contains(run.Prompt, "I will inspect it") {
		t.Fatalf("normalized prompt lost text: %s", run.Prompt)
	}
	// Tool history is preserved as context for CodeBuddy's native tool execution
	for _, expected := range []string{"file contents here", "read_file", "工具结果", "工具调用"} {
		if !strings.Contains(run.Prompt, expected) {
			t.Fatalf("normalized prompt missing tool context %q: %s", expected, run.Prompt)
		}
	}
	// Safety notice must be present to prevent re-execution
	if !strings.Contains(run.Prompt, "都只是引用文本") {
		t.Fatalf("normalized prompt missing safety notice: %s", run.Prompt)
	}
}

func TestCodeBuddyNonStreamingCompletionUsesPromptModelAndCwd(t *testing.T) {
	cwd := t.TempDir()
	fake := &fakeCodeBuddyRunner{status: codebuddy.Status{Available: true, Models: []string{"hy3"}}, events: []codebuddy.Event{
		{Kind: codebuddy.EventText, Text: "hello "},
		{Kind: codebuddy.EventText, Text: "world"},
		{Kind: codebuddy.EventResult, Text: "hello world"},
	}}
	s := &Server{CodeBuddy: fake}
	body := `{
		"model":"codebuddy/hy3",
		"messages":[
			{"role":"system","content":"context"},
			{"role":"user","content":[{"type":"text","text":"question"},{"type":"image_url","image_url":{"url":"secret"}}]},
			{"role":"tool","content":"tool secret"}
		]
	}`
	request := loopbackRequest(http.MethodPost, "/codebuddy/v1/chat/completions", body)
	request.Header.Set("X-Grok-Working-Directory", cwd)
	response := httptest.NewRecorder()
	s.handleCodeBuddyInference(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	var completion struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &completion); err != nil {
		t.Fatal(err)
	}
	if completion.Object != "chat.completion" || completion.Model != "codebuddy/hy3" || len(completion.Choices) != 1 || completion.Choices[0].Message.Content != "hello world" || completion.Choices[0].FinishReason != "stop" {
		t.Fatalf("completion = %#v", completion)
	}
	run := fake.lastRequest(t)
	if run.Model != "hy3" || run.Cwd != cwd {
		t.Fatalf("run request = %#v", run)
	}
	for _, expected := range []string{"系统上下文（引用）：\ncontext", "用户：\nquestion"} {
		if !strings.Contains(run.Prompt, expected) {
			t.Errorf("prompt missing %q: %s", expected, run.Prompt)
		}
	}
	// image_url content must still be stripped; tool results are preserved as context
	for _, forbidden := range []string{"image_url"} {
		if strings.Contains(run.Prompt, forbidden) {
			t.Errorf("prompt contains %q: %s", forbidden, run.Prompt)
		}
	}
	// Tool result is now preserved as context for CodeBuddy
	if !strings.Contains(run.Prompt, "tool secret") {
		t.Errorf("prompt should contain tool result context: %s", run.Prompt)
	}
}

func TestCodeBuddyStreamingCompletionForwardsDeltasWithoutDuplicateResult(t *testing.T) {
	fake := &fakeCodeBuddyRunner{status: codebuddy.Status{Available: true, Models: []string{"hy3"}}, events: []codebuddy.Event{
		{Kind: codebuddy.EventText, Text: "hello "},
		{Kind: codebuddy.EventText, Text: "world"},
		{Kind: codebuddy.EventResult, Text: "hello world"},
	}}
	s := &Server{CodeBuddy: fake}
	request := loopbackRequest(http.MethodPost, "/codebuddy/v1/chat/completions", `{"model":"hy3","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	response := httptest.NewRecorder()
	s.handleCodeBuddyInference(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("Content-Type = %q", contentType)
	}
	body := response.Body.String()
	if strings.Count(body, `"content":"hello "`) != 1 || strings.Count(body, `"content":"world"`) != 1 {
		t.Fatalf("unexpected chunks: %s", body)
	}
	if strings.Count(body, "hello world") != 0 {
		t.Fatalf("duplicate final result was forwarded: %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("stream missing finish or DONE: %s", body)
	}
	fake.lastRequest(t)
}

func TestCodeBuddyModelCwdBodyAndErrorSafety(t *testing.T) {
	t.Run("safe unlisted model rejected", func(t *testing.T) {
		fake := &fakeCodeBuddyRunner{status: codebuddy.Status{Available: true, Models: []string{"hy3"}}}
		s := &Server{CodeBuddy: fake}
		request := loopbackRequest(http.MethodPost, "/codebuddy/v1/chat/completions", `{"model":"custom-model:1","messages":[{"role":"user","content":"hello"}]}`)
		response := httptest.NewRecorder()
		s.handleCodeBuddyInference(response, request)
		if response.Code != http.StatusBadRequest || len(fake.requests) != 0 || !strings.Contains(response.Body.String(), "不在可用列表") {
			t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), len(fake.requests))
		}
	})

	t.Run("unsafe model rejected", func(t *testing.T) {
		fake := &fakeCodeBuddyRunner{}
		s := &Server{CodeBuddy: fake}
		request := loopbackRequest(http.MethodPost, "/codebuddy/v1/chat/completions", `{"model":"../../auth.json","messages":[{"role":"user","content":"hello"}]}`)
		response := httptest.NewRecorder()
		s.handleCodeBuddyInference(response, request)
		if response.Code != http.StatusBadRequest || len(fake.requests) != 0 {
			t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), len(fake.requests))
		}
	})

	t.Run("cwd must exist and be directory", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		fake := &fakeCodeBuddyRunner{status: codebuddy.Status{Available: true, Models: []string{"hy3"}}}
		s := &Server{CodeBuddy: fake}
		request := loopbackRequest(http.MethodPost, "/codebuddy/v1/chat/completions", `{"model":"hy3","messages":[{"role":"user","content":"hello"}]}`)
		request.Header.Set("X-Grok-Working-Directory", file)
		response := httptest.NewRecorder()
		s.handleCodeBuddyInference(response, request)
		if response.Code != http.StatusBadRequest || len(fake.requests) != 0 {
			t.Fatalf("status=%d body=%s calls=%d", response.Code, response.Body.String(), len(fake.requests))
		}
	})

	t.Run("runner error does not leak authentication detail", func(t *testing.T) {
		fake := &fakeCodeBuddyRunner{status: codebuddy.Status{Available: true, Models: []string{"hy3"}}, err: errors.New("Authorization Bearer secret-auth-token at ~/.codebuddy/auth.json")}
		s := &Server{CodeBuddy: fake}
		request := loopbackRequest(http.MethodPost, "/codebuddy/v1/chat/completions", `{"model":"hy3","messages":[{"role":"user","content":"hello"}]}`)
		response := httptest.NewRecorder()
		s.handleCodeBuddyInference(response, request)
		if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "secret-auth-token") || strings.Contains(response.Body.String(), "auth.json") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})

	t.Run("body limited to 32MiB", func(t *testing.T) {
		fake := &fakeCodeBuddyRunner{}
		s := &Server{CodeBuddy: fake}
		request := loopbackRequest(http.MethodPost, "/codebuddy/v1/chat/completions", strings.Repeat(" ", codeBuddyMaxBody+1))
		response := httptest.NewRecorder()
		s.handleCodeBuddyInference(response, request)
		if response.Code != http.StatusRequestEntityTooLarge || len(fake.requests) != 0 {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	})
}

func loopbackRequest(method, target, body string) *http.Request {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Content-Type", "application/json")
	return request
}
