package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const csrfHeader = "X-Grok-Switch-CSRF"

func (s *Server) csrfToken() (string, error) {
	s.csrfMu.Lock()
	defer s.csrfMu.Unlock()
	if s.csrfSecret != "" {
		return s.csrfSecret, nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成 CSRF 令牌: %w", err)
	}
	s.csrfSecret = base64.RawURLEncoding.EncodeToString(raw)
	return s.csrfSecret, nil
}

func (s *Server) handleCSRF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	token, err := s.csrfToken()
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"token": token})
}

func (s *Server) csrfAllowed(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return true
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	token, err := s.csrfToken()
	if err != nil || subtle.ConstantTimeCompare([]byte(r.Header.Get(csrfHeader)), []byte(token)) != 1 {
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// Native clients and tests may omit Origin, but must possess the random token.
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Scheme == "http" && parsed.Host == r.Host
}
