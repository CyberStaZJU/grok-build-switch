// Command codebuddy-setup installs the managed CodeBuddy profile into local
// Grok Build Switch state and can activate default=hy3.
//
//	CODEBUDDY_API_KEY=ck_... go run ./cmd/codebuddy-setup -activate
//	go run ./cmd/codebuddy-setup -activate -base-url http://127.0.0.1:8788/v1
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"grok_switch/internal/codebuddy"
	"grok_switch/internal/paths"
	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
	"grok_switch/internal/server"
	"grok_switch/internal/settings"
	"grok_switch/internal/switcher"
)

func main() {
	activate := flag.Bool("activate", false, "set active provider to CodeBuddy with default hy3")
	portFlag := flag.Int("port", 0, "Switch listen port used for in-process proxy URL (0 = settings)")
	baseFlag := flag.String("base-url", "", "override profile base_url; empty uses Switch in-process /codebuddy-proxy/v1")
	flag.Parse()

	resolved, err := paths.Resolve()
	if err != nil {
		fatal(err)
	}
	if err := resolved.Ensure(); err != nil {
		fatal(err)
	}

	profileStore := profiles.NewStore(resolved.ProfilesFile)
	routingStore := routing.NewStore(resolved.RoutingFile)
	settingsStore := settings.NewStore(resolved.SettingsFile)
	sw := &switcher.Switcher{ConfigPath: resolved.GrokConfig, Profiles: profileStore}

	key := server.LoadCodeBuddyAPIKey(profileStore)
	if key == "" {
		fatal(fmt.Errorf("CODEBUDDY_API_KEY is empty (env, managed profile, or ~/.grok/codebuddy-bridge/.env)"))
	}

	port := *portFlag
	if port == 0 {
		st, err := settingsStore.Get()
		if err != nil {
			fatal(err)
		}
		port = st.ActualPort
		if port == 0 {
			port = st.Port
		}
		if port == 0 {
			port = 17878
		}
	}

	baseURL := strings.TrimSpace(*baseFlag)
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://127.0.0.1:%d/codebuddy-proxy/v1", port)
	}

	if _, err := routingStore.Initialize(profileStore); err != nil {
		fatal(err)
	}

	s := &server.Server{
		ActualPort: port,
		Paths:      resolved,
		Profiles:   profileStore,
		Routing:    routingStore,
		Switcher:   sw,
	}

	// Upsert profile with the chosen base URL first.
	desired := codebuddy.NewProfile(baseURL, key)
	list, err := profileStore.List()
	if err != nil {
		fatal(err)
	}
	var profile profiles.Profile
	found := false
	for _, existing := range list {
		if existing.Source == codebuddy.SourceTag {
			desired.ID = existing.ID
			desired.CreatedAt = existing.CreatedAt
			updated, err := profileStore.Update(existing.ID, desired)
			if err != nil {
				fatal(err)
			}
			profile = updated
			found = true
			break
		}
	}
	if !found {
		created, err := profileStore.Create(desired)
		if err != nil {
			fatal(err)
		}
		profile = created
	}

	if *activate {
		// Activate via routing transaction so config.toml default becomes hy3.
		if _, err := s.EnsureCodeBuddyProvider(key, true); err != nil {
			fatal(err)
		}
		// EnsureCodeBuddyProvider rewrites base_url to the Switch in-process
		// path. Re-pin a custom standalone bridge base when requested.
		if strings.TrimSpace(*baseFlag) != "" {
			got, err := profileStore.Get(profile.ID)
			if err != nil {
				fatal(err)
			}
			got.BaseURL = baseURL
			got.APIKey = key
			for i := range got.Models {
				got.Models[i].BaseURL = baseURL
				got.Models[i].APIKey = key
			}
			if _, err := profileStore.Update(got.ID, got); err != nil {
				fatal(err)
			}
			if err := s.ApplyCurrentRouting(); err != nil {
				fatal(err)
			}
			profile = got
		} else {
			got, err := profileStore.Get(profile.ID)
			if err != nil {
				fatal(err)
			}
			profile = got
		}
	} else if err := s.ApplyCurrentRouting(); err != nil {
		fatal(err)
	}

	fmt.Printf("ok profile_id=%s name=%q base=%s default=%s activate=%v\n",
		profile.ID, profile.Name, profile.BaseURL, profile.DefaultModel, *activate)
	fmt.Printf("config=%s\n", resolved.GrokConfig)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
