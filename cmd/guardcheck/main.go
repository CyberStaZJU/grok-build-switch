// Command guardcheck verifies the streaming guard end to end.
//
// It starts a real management server whose only provider is pointed at the
// guard while the provider's genuine endpoint is supplied by
// GUARDCHECK_UPSTREAM, then prints the resolved paths. A second terminal can
// then run the real Grok client against it with HOME=GUARDCHECK_DIR, which
// exercises the same request path the installed app uses.
//
// This exists because the guard's contract — where the client sends the
// request and what the upstream must receive — is only fully checkable with a
// real client, and unit tests alone let a path-joining mistake through.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"grok_switch/internal/paths"
	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
	"grok_switch/internal/server"
	"grok_switch/internal/settings"
	"grok_switch/internal/switcher"
)

func main() {
	dir := os.Getenv("GUARDCHECK_DIR")
	if dir == "" {
		fmt.Fprintln(os.Stderr, "GUARDCHECK_DIR is required")
		os.Exit(2)
	}
	upstream := os.Getenv("GUARDCHECK_UPSTREAM")
	if upstream == "" {
		fmt.Fprintln(os.Stderr, "GUARDCHECK_UPSTREAM is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// The CLI resolves its own home as HOME/.grok, so keep the config where a
	// real client run with HOME=GUARDCHECK_DIR will read it.
	grokHome := filepath.Join(dir, ".grok")
	if err := os.MkdirAll(grokHome, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	configPath := filepath.Join(grokHome, "config.toml")
	if err := os.WriteFile(configPath, []byte("[telemetry]\nenabled = false\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	profileStore := profiles.NewStore(filepath.Join(dir, "profiles.json"))
	routingStore := routing.NewStore(filepath.Join(dir, "routing.json"))
	settingsStore := settings.NewStore(filepath.Join(dir, "settings.json"))
	sw := &switcher.Switcher{ConfigPath: configPath, Profiles: profileStore}

	s := &server.Server{
		Paths:      paths.Paths{GrokHome: grokHome, GrokConfig: configPath, DataDir: dir},
		Profiles:   profileStore,
		Routing:    routingStore,
		Settings:   settingsStore,
		Switcher:   sw,
		ExePath:    os.Args[0],
		ActualPort: 0,
	}
	s.SetOnChanged(func() {})

	_, port, err := s.Listen(19099)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	s.ActualPort = port

	apiKey := os.Getenv("GUARDCHECK_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "GUARDCHECK_API_KEY is required")
		os.Exit(2)
	}
	created, err := profileStore.Create(profiles.Profile{
		Name:           "API 池",
		UpstreamFormat: "openai_responses",
		BaseURL:        upstream,
		APIKey:         apiKey,
		DefaultModel:   "gpt-5.6-sol",
		Models: []profiles.ModelDef{
			{Name: "gpt-5.6-sol", Model: "gpt-5.6-sol", APIBackend: "responses",
				ContextWindow: 320000, SupportsReasoningEffort: true,
				ReasoningEfforts: []string{"low", "medium", "high", "xhigh", "max"}},
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := routingStore.Initialize(profileStore); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err := s.SetStreamGuardEnabledOpt(created.ID, true); err != nil {
		fmt.Fprintln(os.Stderr, "enable guard:", err)
		os.Exit(1)
	}

	stored, err := profileStore.Get(created.ID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out := map[string]any{
		"port":              port,
		"guard_base_url":    s.StreamGuardBaseURL(),
		"profile_base_url":  stored.BaseURL,
		"upstream_base_url": stored.UpstreamBaseURL,
		"config_path":       configPath,
		"data_dir":          dir,
	}
	encoded, _ := json.Marshal(out)
	fmt.Println(string(encoded))
	_ = http.StatusOK
	select {}
}
