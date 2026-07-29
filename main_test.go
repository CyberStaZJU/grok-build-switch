package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"grok_switch/internal/settings"
)

func TestWaitForExistingInstanceURL(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), ".grok_switch")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"data_dir": dataDir})
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, rawPort, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}

	store := settings.NewStore(filepath.Join(dataDir, "settings.json"))
	current := settings.Default()
	current.Port = port
	current.ActualPort = port
	if _, err := store.Update(current); err != nil {
		t.Fatal(err)
	}

	got, err := waitForExistingInstanceURL(store, dataDir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got != server.URL {
		t.Fatalf("URL = %q, want %q", got, server.URL)
	}
}
