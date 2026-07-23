package server

import (
	"net/http"
	"strconv"
	"strings"

	"grok_switch/internal/cachestats"
)

func (s *Server) handleCacheStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	hours := 24
	if raw := strings.TrimSpace(r.URL.Query().Get("hours")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			hours = n
		}
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	report, err := cachestats.Collect(s.Paths.GrokHome, hours, sessionID)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, report)
}
