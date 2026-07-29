package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"grok_switch/internal/agentbridge"
	"grok_switch/internal/routing"
)

func TestGenerateSessionTitleHandlesShortErrorResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer upstream.Close()

	s := &Server{}
	title, shouldDelete, reason := s.generateSessionTitle(routing.ModelRoute{
		Model:      "test-model",
		BaseURL:    upstream.URL,
		APIBackend: "chat_completions",
	}, "hello", "test")
	if title != "" || shouldDelete || reason != "" {
		t.Fatalf("unexpected result: title=%q shouldDelete=%v reason=%q", title, shouldDelete, reason)
	}
}

func TestGenerateSessionTitleStopsOnSubscriptionOverload(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"server_is_overloaded"}}`, http.StatusBadGateway)
	}))
	defer upstream.Close()

	s := &Server{}
	_, _, _, err := s.generateSessionTitleContext(context.Background(), routing.ModelRoute{
		Model: "test-model", BaseURL: upstream.URL, APIBackend: "chat_completions",
	}, "hello", "test")
	if err == nil || !strings.Contains(err.Error(), "已停止整理") {
		t.Fatalf("expected overload to stop analysis, got %v", err)
	}
}

func TestGenerateSessionTitleUsesBackendSpecificProtocol(t *testing.T) {
	for _, tc := range []struct {
		name     string
		backend  string
		wantPath string
		response string
	}{
		{name: "responses", backend: "responses", wantPath: "/responses", response: `{"output_text":"{\"title\":\"Reviewed\",\"should_delete\":false,\"reason\":\"keep\"}"}`},
		{name: "messages", backend: "messages", wantPath: "/messages", response: `{"content":[{"type":"text","text":"{\"title\":\"Reviewed\",\"should_delete\":false,\"reason\":\"keep\"}"}]}`},
		{name: "chat", backend: "chat_completions", wantPath: "/chat/completions", response: `{"choices":[{"message":{"content":"{\"title\":\"Reviewed\",\"should_delete\":false,\"reason\":\"keep\"}"}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.wantPath {
					t.Fatalf("path = %q, want %q", r.URL.Path, tc.wantPath)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer upstream.Close()
			s := &Server{}
			title, shouldDelete, reason := s.generateSessionTitle(routing.ModelRoute{Model: "test", BaseURL: upstream.URL, APIBackend: tc.backend}, "hello", "provider")
			if title != "provider: Reviewed" || shouldDelete || reason != "keep" {
				t.Fatalf("result = %q %v %q", title, shouldDelete, reason)
			}
		})
	}
}

func TestWriteSessionLoadError(t *testing.T) {
	recorder := httptest.NewRecorder()
	status := agentbridge.Status{Running: true, State: "ready", SessionID: "fresh-session"}
	writeSessionLoadError(recorder, &agentbridge.SessionLoadError{
		Cause: errors.New("peer disconnected before response"),
	}, status)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusConflict)
	}
	var response struct {
		Error           string             `json:"error"`
		Code            string             `json:"code"`
		ReadonlyHistory bool               `json:"readonly_history"`
		Recoverable     bool               `json:"recoverable"`
		EngineLoaded    bool               `json:"engine_loaded"`
		AgentRestarted  bool               `json:"agent_restarted"`
		Status          agentbridge.Status `json:"status"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != agentbridge.SessionLoadOverflowCode || !response.ReadonlyHistory || !response.Recoverable || response.EngineLoaded || !response.AgentRestarted {
		t.Fatalf("unexpected response: %#v", response)
	}
	if response.Status.SessionID != "fresh-session" || response.Status.State != "ready" {
		t.Fatalf("unexpected recovery status: %#v", response.Status)
	}
	if response.Error == "" {
		t.Fatal("expected a user-facing error message")
	}
}
