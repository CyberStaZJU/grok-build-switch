package server

import (
	"fmt"
	"net/http"
	"strings"

	"grok_switch/internal/profiles"
)

// SetStreamGuardEnabledOpt marks a profile as guarded and repoints its Base URL
// at the in-process guard while remembering the real upstream.
//
// The guard exists because some OpenAI-compatible gateways emit non-standard
// streaming frames (for example `{"type":"keepalive"}`) that abort the Grok
// turn with a serialization error. Enabling or disabling it changes only where
// the client connects, never the provider credential or model catalog.
//
// The caller must not already hold routingMu: this takes it, then re-projects
// routing through the locked variant, because ApplyCurrentRouting would take
// the same mutex again and deadlock.
func (s *Server) SetStreamGuardEnabledOpt(profileID string, enabled bool) (profiles.Profile, error) {
	if s.Profiles == nil || s.ActualPort == 0 {
		return profiles.Profile{}, fmt.Errorf("服务未就绪")
	}
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	return s.setStreamGuardEnabledLocked(profileID, enabled)
}

func (s *Server) setStreamGuardEnabledLocked(profileID string, enabled bool) (profiles.Profile, error) {
	profile, err := s.Profiles.Get(strings.TrimSpace(profileID))
	if err != nil {
		return profiles.Profile{}, err
	}
	switch {
	case enabled:
		if isStreamGuardBaseURL(profile.BaseURL) {
			return profile, nil
		}
		if strings.TrimSpace(profile.BaseURL) == "" {
			return profiles.Profile{}, fmt.Errorf("供应商 %q 没有 Base URL，无法启用流式保护", profile.Name)
		}
		if err := s.validateStreamGuardProjection(); err != nil {
			return profiles.Profile{}, err
		}
		profile.UpstreamBaseURL = profile.BaseURL
		target := s.StreamGuardBaseURL()
		profile.BaseURL = target
		for i := range profile.Models {
			if strings.TrimSpace(profile.Models[i].BaseURL) != "" {
				profile.Models[i].BaseURL = target
			}
		}
	default:
		restored := strings.TrimSpace(profile.UpstreamBaseURL)
		if restored == "" {
			return profile, nil
		}
		profile.BaseURL = restored
		for i := range profile.Models {
			if strings.TrimSpace(profile.Models[i].BaseURL) != "" {
				profile.Models[i].BaseURL = restored
			}
		}
		profile.UpstreamBaseURL = ""
	}
	updated, err := s.Profiles.Update(profile.ID, profile)
	if err != nil {
		return profiles.Profile{}, err
	}
	if s.Routing != nil {
		if err := s.applyCurrentRoutingLocked(); err != nil {
			return profiles.Profile{}, err
		}
	}
	s.changed()
	return updated, nil
}

// validateStreamGuardProjection rejects enabling the guard while the official
// account is active, because the config would then point the official route at
// a local endpoint that never receives official traffic. Switching back to a
// custom provider first keeps the projection unambiguous.
func (s *Server) validateStreamGuardProjection() error {
	if s.Routing == nil {
		return nil
	}
	snapshot, err := s.Routing.Snapshot()
	if err != nil {
		return nil
	}
	if snapshot.IsOfficial() {
		return fmt.Errorf("当前启用的是官方账号；请先切换到该供应商，再启用流式保护")
	}
	return nil
}

// handleStreamGuardSettings exposes the per-profile guard toggle.
func (s *Server) handleStreamGuardSettings(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeError(w, fmt.Errorf("仅允许本机修改流式保护设置"), http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.streamGuardStatus())
	case http.MethodPut:
		var request struct {
			ProfileID *string `json:"profile_id"`
			Enabled   *bool   `json:"enabled"`
		}
		if err := decodeManagementJSON(w, r, &request); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		if request.ProfileID == nil || strings.TrimSpace(*request.ProfileID) == "" {
			writeError(w, fmt.Errorf("缺少 profile_id"), http.StatusBadRequest)
			return
		}
		if request.Enabled == nil {
			writeError(w, fmt.Errorf("缺少 enabled"), http.StatusBadRequest)
			return
		}
		s.routingMu.Lock()
		updated, err := s.setStreamGuardEnabledLocked(*request.ProfileID, *request.Enabled)
		s.routingMu.Unlock()
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, s.streamGuardStatusFor(updated))
	default:
		methodNotAllowed(w)
	}
}

type streamGuardStatusDTO struct {
	Endpoint string                     `json:"endpoint"`
	Profiles []streamGuardProfileStatus `json:"profiles"`
}

type streamGuardProfileStatus struct {
	ProfileID       string `json:"profile_id"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	BaseURL         string `json:"base_url"`
	UpstreamBaseURL string `json:"upstream_base_url,omitempty"`
}

func (s *Server) streamGuardStatus() streamGuardStatusDTO {
	out := streamGuardStatusDTO{Endpoint: s.StreamGuardBaseURL()}
	if s.Profiles == nil {
		return out
	}
	list, err := s.Profiles.List()
	if err != nil {
		return out
	}
	for _, profile := range list {
		out.Profiles = append(out.Profiles, s.streamGuardStatusFor(profile))
	}
	return out
}

func (s *Server) streamGuardStatusFor(profile profiles.Profile) streamGuardProfileStatus {
	status := streamGuardProfileStatus{
		ProfileID: profile.ID,
		Name:      profile.Name,
		Enabled:   isStreamGuardBaseURL(profile.BaseURL),
		BaseURL:   profile.BaseURL,
	}
	if status.Enabled {
		status.UpstreamBaseURL = profile.UpstreamBaseURL
	}
	return status
}
