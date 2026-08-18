// Command codebuddy-proxy runs a standalone OpenAI-compatible bridge for
// CodeBuddy when Grok Build Switch is not yet rebuilt with the in-process
// /codebuddy-proxy route.
//
//	CODEBUDDY_API_KEY=ck_... go run ./cmd/codebuddy-proxy -addr 127.0.0.1:8788
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"

	"grok_switch/internal/codebuddy"
	"grok_switch/internal/paths"
	"grok_switch/internal/profiles"
	"grok_switch/internal/server"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8788", "listen address")
	flag.Parse()

	var key string
	if resolved, err := paths.Resolve(); err == nil {
		key = server.LoadCodeBuddyAPIKey(profiles.NewStore(resolved.ProfilesFile))
	}
	if key == "" {
		key = strings.TrimSpace(os.Getenv("CODEBUDDY_API_KEY"))
	}
	if key == "" {
		fmt.Fprintln(os.Stderr, "CODEBUDDY_API_KEY is empty")
		os.Exit(1)
	}

	mux := http.NewServeMux()
	h := &codebuddy.Handler{FallbackKey: key}
	// Accept both /v1/... and bare /chat/completions for convenience.
	mux.Handle("/v1/", h)
	mux.Handle("/v1", h)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"codebuddy-proxy"}`))
	})

	fmt.Fprintf(os.Stderr, "codebuddy-proxy listening on http://%s\n", *addr)
	fmt.Fprintf(os.Stderr, "  GET  /v1/models\n")
	fmt.Fprintf(os.Stderr, "  POST /v1/chat/completions\n")
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
