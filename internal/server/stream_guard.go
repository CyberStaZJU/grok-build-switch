package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"grok_switch/internal/grokauth"
	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
	"grok_switch/internal/streamguard"
)

// StreamGuardPathPrefix is the in-process endpoint the guard listens on.
const StreamGuardPathPrefix = "/stream-guard/v1"

// StreamGuardBaseURL returns the loopback base URL a guarded profile uses as
// its Grok-visible Base URL.
func (s *Server) StreamGuardBaseURL() string {
	if s.ActualPort == 0 {
		return ""
	}
	return fmt.Sprintf("http://127.0.0.1:%d%s", s.ActualPort, StreamGuardPathPrefix)
}

// guardRoute is the upstream a guarded request is forwarded to.
type guardRoute struct {
	BaseURL      string
	APIKey       string
	Models       map[string]string // Grok-visible model id -> upstream model id
	ExtraHeaders map[string]string
}

// activeGuardRoute resolves the profile that owns the currently active
// provider and its real upstream endpoint.
//
// Routing is the source of truth when it is populated. The single-profile
// fallback exists only for servers that predate routing, where no snapshot is
// available and a request would otherwise be forwarded nowhere.
func (s *Server) activeGuardRoute(ctx context.Context) (guardRoute, error) {
	if s.Routing != nil {
		snapshot, err := s.Routing.Snapshot()
		if err != nil {
			return guardRoute{}, err
		}
		if snapshot.ActiveProviderID == routing.OfficialProviderID {
			return s.officialGuardRoute()
		}
		if provider, ok := snapshot.Provider(snapshot.ActiveProviderID); ok && provider.ProfileID != "" {
			if s.Profiles == nil {
				return guardRoute{}, fmt.Errorf("Profile 存储未初始化")
			}
			profile, err := s.Profiles.Get(provider.ProfileID)
			if err != nil {
				return guardRoute{}, err
			}
			return guardRouteFromProfile(profile)
		}
	}
	if s.Switcher != nil {
		profile, ok, err := s.Switcher.ActiveStatus()
		if err != nil {
			return guardRoute{}, err
		}
		if ok {
			return guardRouteFromProfile(profile)
		}
	}
	return guardRoute{}, fmt.Errorf("没有已启用的供应商，无法转发受保护的流式请求")
}

// officialGuardRoute forwards to the xAI endpoint using the credential from
// the official login, so switching to the official account with the guard
// still enabled keeps working.
func (s *Server) officialGuardRoute() (guardRoute, error) {
	credential, ok := s.nativeOfficialCredential()
	if !ok {
		return guardRoute{}, fmt.Errorf("官方账号未登录或凭据已过期")
	}
	return guardRoute{
		BaseURL: strings.TrimRight(grokauth.UpstreamURL(), "/"),
		APIKey:  credential.AccessToken,
		ExtraHeaders: map[string]string{
			"X-XAI-Token-Auth":      "xai-grok-cli",
			"x-grok-client-version": "0.2.93",
			"User-Agent":            "xai-grok-workspace/0.2.93",
		},
	}, nil
}

// guardRouteFromProfile extracts the forwarding target. A profile that has
// already claimed the guard endpoint keeps its original endpoint in
// UpstreamBaseURL; an unclaimed profile is guarded in place.
func guardRouteFromProfile(profile profiles.Profile) (guardRoute, error) {
	upstream := strings.TrimSpace(profile.UpstreamBaseURL)
	if upstream == "" {
		upstream = strings.TrimSpace(profile.BaseURL)
	}
	if upstream == "" {
		return guardRoute{}, fmt.Errorf("供应商 %q 没有可用的 Base URL", profile.Name)
	}
	if isStreamGuardBaseURL(upstream) {
		return guardRoute{}, fmt.Errorf("供应商 %q 的上游仍指向流式保护端点，缺少原始 Base URL", profile.Name)
	}
	models := map[string]string{}
	for _, model := range profile.Models {
		local := strings.TrimSpace(model.Name)
		if local == "" {
			local = strings.TrimSpace(model.Model)
		}
		upstreamID := strings.TrimSpace(model.Model)
		if upstreamID == "" {
			upstreamID = local
		}
		if local != "" && upstreamID != "" {
			models[local] = upstreamID
		}
	}
	return guardRoute{BaseURL: upstream, APIKey: profile.EffectiveAPIKey(), Models: models}, nil
}

// isStreamGuardBaseURL reports whether a base URL already points at the guard.
func isStreamGuardBaseURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	return u.Path == StreamGuardPathPrefix || strings.HasPrefix(u.Path, StreamGuardPathPrefix+"/")
}

// handleStreamGuard forwards inference to the active provider's real endpoint
// while stripping streaming frames the Grok client cannot deserialize.
//
// Only loopback callers are served: the endpoint carries the upstream
// credential and is never exposed beyond this machine.
func (s *Server) handleStreamGuard(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		http.Error(w, "仅允许本机访问", http.StatusForbidden)
		return
	}
	// The client appends its API path to the profile's Base URL, so the path
	// after the guard prefix is the upstream-relative API path. The prefix
	// already carries the /v1 segment the provider expects.
	suffix := strings.TrimPrefix(r.URL.Path, StreamGuardPathPrefix)
	if suffix == "" || !strings.HasPrefix(suffix, "/") {
		http.NotFound(w, r)
		return
	}

	route, err := s.activeGuardRoute(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// /v1/models is served locally so Grok never needs to reach the provider
	// directly, keeping the advertised ids aligned with config.toml.
	if r.Method == http.MethodGet && suffix == "/models" {
		writeGuardModels(w, route)
		return
	}

	target := strings.TrimRight(route.BaseURL, "/") + suffix

	var body io.Reader
	if r.Body != nil {
		raw, readErr := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if readErr != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		raw = rewriteGuardModel(raw, route)
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, body)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	copyGuardHeaders(req.Header, r.Header)
	for name, value := range route.ExtraHeaders {
		req.Header.Set(name, value)
	}
	if key := guardAPIKey(r, route); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := s.guardClient().Do(req)
	if err != nil {
		http.Error(w, "上游不可用: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	isSSE := strings.Contains(strings.ToLower(contentType), "text/event-stream")

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(errBody)
		return
	}

	if !isSSE {
		// Non-streaming responses cannot carry stray SSE frames.
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}

	mode := streamguard.ModeResponses
	if strings.HasSuffix(suffix, "/chat/completions") {
		mode = streamguard.ModeChatCompletions
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	writer := &flushWriter{w: w, flusher: flusher}
	if err := streamguard.SanitizeStream(writer, resp.Body, mode); err != nil {
		// The client is gone or the upstream failed mid-stream; nothing left
		// to repair, and the caller already has whatever arrived.
		return
	}
	writer.flush()
}

func (s *Server) guardClient() *http.Client {
	if s.streamGuardClient != nil {
		return s.streamGuardClient
	}
	return &http.Client{} // streaming: no overall timeout
}

// flushWriter flushes after every write so streamed frames reach the client
// as they arrive instead of at end of response.
type flushWriter struct {
	w       io.Writer
	flusher http.Flusher
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if f.flusher != nil {
		f.flusher.Flush()
	}
	return n, err
}

func (f *flushWriter) flush() {
	if f.flusher != nil {
		f.flusher.Flush()
	}
}

// rewriteGuardModel maps the Grok-visible model id onto the provider's own id
// so a provider that exposes a different upstream name still resolves.
func rewriteGuardModel(raw []byte, route guardRoute) []byte {
	if len(raw) == 0 || len(route.Models) == 0 {
		return raw
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw
	}
	current, _ := payload["model"].(string)
	current = strings.TrimSpace(current)
	if current == "" {
		return raw
	}
	upstream, ok := route.Models[current]
	if !ok || upstream == "" || upstream == current {
		return raw
	}
	payload["model"] = upstream
	encoded, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return encoded
}

// guardAPIKey resolves the credential for the upstream request.
//
// The profile's own key wins: it is the configured authority for this
// endpoint, and a signed-in client may send its OIDC session token instead of
// the provider key, which the provider rejects. The caller header is used only
// when the profile stores no key.
func guardAPIKey(r *http.Request, route guardRoute) string {
	if key := strings.TrimSpace(route.APIKey); key != "" {
		return key
	}
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		if _, token, ok := strings.Cut(auth, " "); ok {
			if token = strings.TrimSpace(token); token != "" {
				return token
			}
		}
	}
	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}

// copyGuardHeaders forwards request headers minus hop-by-hop and credentials,
// which are re-set explicitly.
func copyGuardHeaders(dst, src http.Header) {
	for name, values := range src {
		switch strings.ToLower(name) {
		case "host", "content-length", "connection", "transfer-encoding",
			"authorization", "x-api-key", "accept-encoding", "accept":
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func writeGuardModels(w http.ResponseWriter, route guardRoute) {
	ids := make([]string, 0, len(route.Models))
	for id := range route.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	out := make([]model, 0, len(ids))
	for _, id := range ids {
		out = append(out, model{ID: id, Object: "model", Created: 1700000000, OwnedBy: "stream-guard"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": out})
}
