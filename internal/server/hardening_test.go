package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"grok_switch/internal/settings"
)

func TestApplicationHTTPServerTimeoutProfile(t *testing.T) {
	srv := newApplicationHTTPServer(http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.IdleTimeout <= 0 || srv.MaxHeaderBytes <= 0 {
		t.Fatalf("incomplete server limits: %#v", srv)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("streaming application server WriteTimeout=%s, want 0", srv.WriteTimeout)
	}
}

func TestSubscriptionStateConcurrentInitializationIsShared(t *testing.T) {
	s := &Server{}
	const goroutines = 64
	states := make(chan *subscriptionProxySelection, goroutines)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			state := s.subscriptionState()
			state.mu.Lock()
			state.sessions[string(rune('A'+i%26))+time.Now().Format("150405.000000000")] = "value"
			state.selected[string(rune('a'+i%26))] = true
			state.mu.Unlock()
			states <- state
		}(i)
	}
	close(start)
	wg.Wait()
	close(states)
	var first *subscriptionProxySelection
	for state := range states {
		if first == nil {
			first = state
		} else if first != state {
			t.Fatal("concurrent initialization returned different state pointers")
		}
	}
	if first == nil || first.sessions == nil || first.selected == nil || len(first.sessions) == 0 || len(first.selected) == 0 {
		t.Fatalf("state lost initialization or writes: %#v", first)
	}
}

func TestCoreManagementEndpointsRejectUnknownTrailingAndOversizeJSON(t *testing.T) {
	const oversized = 2<<20 + 1
	newInvoke := func(name string) http.HandlerFunc {
		s := &Server{}
		if name == "settings" {
			s.Settings = settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
		}
		switch name {
		case "profiles":
			return s.handleProfiles
		case "settings":
			return s.handleSettings
		case "models-fetch":
			return s.handleFetchModels
		case "reasoning":
			return s.handleReasoningEfforts
		case "connection":
			return s.handleConnectionTest
		case "config":
			return s.handleConfig
		default:
			return s.handleConfigPreview
		}
	}
	tests := []string{"profiles", "settings", "models-fetch", "reasoning", "connection", "config", "config-preview"}
	for _, name := range tests {
		tc := struct {
			name   string
			invoke http.HandlerFunc
		}{name: name, invoke: newInvoke(name)}
		t.Run(tc.name+" unknown", func(t *testing.T) {
			method := http.MethodPost
			if tc.name == "settings" || tc.name == "config" {
				method = http.MethodPut
			}
			req := loopbackRequest(method, "/api/test", `{"unknown_field":true}`)
			res := httptest.NewRecorder()
			tc.invoke(res, req)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
			}
		})
		t.Run(tc.name+" trailing", func(t *testing.T) {
			method := http.MethodPost
			if tc.name == "settings" || tc.name == "config" {
				method = http.MethodPut
			}
			req := loopbackRequest(method, "/api/test", `{}`+`{}`)
			res := httptest.NewRecorder()
			tc.invoke(res, req)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
			}
		})
		t.Run(tc.name+" oversize", func(t *testing.T) {
			method := http.MethodPost
			if tc.name == "settings" || tc.name == "config" {
				method = http.MethodPut
			}
			req := loopbackRequest(method, "/api/test", `{"padding":"`+strings.Repeat("x", oversized)+`"}`)
			res := httptest.NewRecorder()
			tc.invoke(res, req)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
			}
		})
	}
}
