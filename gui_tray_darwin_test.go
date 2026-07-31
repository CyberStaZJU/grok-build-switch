//go:build wailsgui && darwin

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDarwinTrayProviderClientAddsCSRFToken(t *testing.T) {
	csrfRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/csrf":
			csrfRequests++
			_, _ = w.Write([]byte(`{"token":"darwin-token"}`))
		case "PUT /api/routing/policy":
			if r.Header.Get("X-Grok-Switch-CSRF") != "darwin-token" {
				http.Error(w, "missing csrf", http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := newDarwinTrayProviderClient(server.URL)
	if err := client.updatePolicy(context.Background(), map[string]any{"default": "grok-4.5"}); err != nil {
		t.Fatal(err)
	}
	if err := client.updatePolicy(context.Background(), map[string]any{"web_search": "grok-4.5"}); err != nil {
		t.Fatal(err)
	}
	if csrfRequests != 1 {
		t.Fatalf("CSRF requests = %d, want 1", csrfRequests)
	}
}

func TestDarwinCloseHidesWindowToMenuBar(t *testing.T) {
	controller := newGUITrayController("http://127.0.0.1:17878", nil)
	var hidden, quit int
	controller.hideAction = func(context.Context) { hidden++ }
	controller.quitAction = func(context.Context) { quit++ }

	ctx := context.Background()
	controller.startup(ctx)

	// Before ready, close should NOT be intercepted.
	if prevent := controller.beforeClose(ctx); prevent {
		t.Fatal("close was intercepted before tray became ready")
	}

	// After ready, close should hide the window (menu-bar resident).
	controller.ready.Store(true)
	if prevent := controller.beforeClose(ctx); !prevent || hidden != 1 {
		t.Fatalf("ready close: prevent=%v hidden=%d, want true/1", prevent, hidden)
	}

	// Explicit quit should call quit action exactly once.
	controller.requestQuit()
	controller.requestQuit()
	if quit != 1 {
		t.Fatalf("quit = %d, want exactly 1", quit)
	}

	// After quit requested, close should NOT be intercepted.
	if prevent := controller.beforeClose(ctx); prevent {
		t.Fatal("close was intercepted after explicit quit")
	}
}

func TestDarwinQuitRequestedBeforeWailsStartup(t *testing.T) {
	controller := newGUITrayController("http://127.0.0.1:17878", nil)
	quit := 0
	controller.quitAction = func(context.Context) { quit++ }

	controller.requestQuit()
	if quit != 0 {
		t.Fatalf("quit before startup = %d, want 0", quit)
	}

	controller.startup(context.Background())
	if quit != 1 {
		t.Fatalf("quit after startup = %d, want 1", quit)
	}
}

func TestDarwinRoutingSnapshotFingerprint(t *testing.T) {
	s1 := routingSnapshot{
		DefaultModel:    "gpt-5.6-sol",
		WebSearchModel:  "gpt-5.6-sol",
		AvailableModels: []routingModel{{ID: "m1", Name: "gpt-5.6-sol"}},
	}
	s2 := routingSnapshot{
		DefaultModel:    "gpt-5.6-sol",
		WebSearchModel:  "gpt-5.6-sol",
		AvailableModels: []routingModel{{ID: "m1", Name: "gpt-5.6-sol"}},
	}
	s3 := routingSnapshot{
		DefaultModel:    "claude-4",
		WebSearchModel:  "gpt-5.6-sol",
		AvailableModels: []routingModel{{ID: "m1", Name: "gpt-5.6-sol"}},
	}
	if s1.fingerprint() != s2.fingerprint() {
		t.Fatal("identical snapshots produced different fingerprints")
	}
	if s1.fingerprint() == s3.fingerprint() {
		t.Fatal("different snapshots produced same fingerprint")
	}
}

func TestDarwinCacheStatsFingerprint(t *testing.T) {
	rate1 := 0.75
	rate2 := 0.75
	rate3 := 0.50
	s1 := cacheStatsSnapshot{Turns: 10, PromptTokens: 1000, CachedPromptTokens: 750, HitRate: &rate1}
	s2 := cacheStatsSnapshot{Turns: 10, PromptTokens: 1000, CachedPromptTokens: 750, HitRate: &rate2}
	s3 := cacheStatsSnapshot{Turns: 10, PromptTokens: 1000, CachedPromptTokens: 500, HitRate: &rate3}
	s4 := cacheStatsSnapshot{Turns: 10, PromptTokens: 1000, CachedPromptTokens: 0}

	if s1.fingerprint() != s2.fingerprint() {
		t.Fatal("identical stats produced different fingerprints")
	}
	if s1.fingerprint() == s3.fingerprint() {
		t.Fatal("different stats produced same fingerprint")
	}
	if s4.fingerprint() == "" {
		t.Fatal("empty stats should still produce a fingerprint")
	}
}
