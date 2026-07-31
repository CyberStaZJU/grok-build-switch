package ssh

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"grok_switch/internal/httpjson"
)

// Handler wraps the Manager with HTTP handlers.
type Handler struct {
	manager *Manager
}

// NewHandler creates a new SSH HTTP handler.
func NewHandler(dataDir string) *Handler {
	mgr := NewManager(dataDir)
	for _, cfg := range mgr.ListConfigs() {
		_ = mgr.AddConfig(cfg)
	}
	return &Handler{manager: mgr}
}

// RegisterRoutes registers all SSH API routes.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/ssh/connections", h.handleConnections)
	mux.HandleFunc("/api/ssh/connections/", h.handleConnectionByID)
	mux.HandleFunc("/api/ssh/connect", h.handleConnect)
	mux.HandleFunc("/api/ssh/disconnect/", h.handleDisconnect)
	mux.HandleFunc("/api/ssh/files", h.handleFiles)
	mux.HandleFunc("/api/ssh/preview", h.handlePreview)
	mux.HandleFunc("/api/ssh/save", h.handleSave)
	mux.HandleFunc("/api/ssh/import-ssh-config", h.handleImportSSHConfig)
}

func (h *Handler) handleConnections(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, h.manager.ListConfigs())
	case http.MethodPost:
		var cfg ConnectionConfig
		if !decodeJSON(w, r, &cfg) {
			return
		}
		if cfg.ID == "" {
			cfg.ID = GenerateID()
		}
		if cfg.Port == 0 {
			cfg.Port = 22
		}
		if err := h.manager.AddConfig(cfg); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSONStatus(w, cfg, http.StatusCreated)
	default:
		methodNotAllowed(w)
	}
}

func (h *Handler) handleConnectionByID(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/ssh/connections/")
	if id == "" {
		writeError(w, os.ErrNotExist, http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPut:
		var cfg ConnectionConfig
		if !decodeJSON(w, r, &cfg) {
			return
		}
		cfg.ID = id
		if err := h.manager.UpdateConfig(cfg); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, cfg)
	case http.MethodDelete:
		if err := h.manager.DeleteConfig(id); err != nil {
			status := http.StatusBadRequest
			if os.IsNotExist(err) {
				status = http.StatusNotFound
			}
			writeError(w, err, status)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (h *Handler) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !isLoopback(w, r) {
		return
	}
	var req struct {
		ID       string `json:"id"`
		Password string `json:"password,omitempty"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := h.manager.Connect(req.ID, req.Password); err != nil {
		writeError(w, err, http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (h *Handler) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !isLoopback(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/ssh/disconnect/")
	if id == "" {
		writeError(w, os.ErrNotExist, http.StatusBadRequest)
		return
	}
	if err := h.manager.Disconnect(id); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (h *Handler) handleFiles(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(w, r) {
		return
	}
	connID := r.URL.Query().Get("conn_id")
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}
	switch r.Method {
	case http.MethodGet:
		infos, err := h.manager.ListDirectory(connID, path)
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, infos)
	case http.MethodDelete:
		var req struct {
			Paths []string `json:"paths"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		if len(req.Paths) == 0 {
			writeError(w, os.ErrNotExist, http.StatusBadRequest)
			return
		}
		if err := h.manager.DeleteFiles(connID, req.Paths); err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (h *Handler) handlePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !isLoopback(w, r) {
		return
	}
	connID := r.URL.Query().Get("conn_id")
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, os.ErrNotExist, http.StatusBadRequest)
		return
	}
	data, err := h.manager.GetFileContent(connID, path)
	if err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"content": string(data),
		"path":    path,
		"name":    filepath.Base(path),
	})
}

func (h *Handler) handleSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut || !isLoopback(w, r) {
		return
	}
	var req struct {
		ConnID  string `json:"conn_id"`
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Path == "" {
		writeError(w, os.ErrNotExist, http.StatusBadRequest)
		return
	}
	if err := h.manager.SaveFileContent(req.ConnID, req.Path, []byte(req.Content)); err != nil {
		writeError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (h *Handler) handleImportSSHConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !isLoopback(w, r) {
		return
	}
	var req struct {
		IDs []string `json:"ids"` // which hosts to import (empty = all)
	}
	if !decodeJSONOptions(w, r, &req, true) {
		return
	}

	configs := ParseSSHConfig()
	if len(req.IDs) > 0 {
		filtered := []ConnectionConfig{}
		for _, c := range configs {
			for _, id := range req.IDs {
				if c.ID == id {
					filtered = append(filtered, c)
					break
				}
			}
		}
		configs = filtered
	}

	// Save each config.
	imported := []ConnectionConfig{}
	for _, cfg := range configs {
		if err := h.manager.AddConfig(cfg); err == nil {
			imported = append(imported, cfg)
		}
	}
	writeJSON(w, map[string]any{"imported": imported, "available": ParseSSHConfig()})
}

// ——— Helpers ———

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONStatus(w http.ResponseWriter, v any, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, os.ErrInvalid, http.StatusMethodNotAllowed)
}

func isLoopback(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil || !ip.IsLoopback() {
		writeError(w, os.ErrPermission, http.StatusForbidden)
		return false
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	return decodeJSONOptions(w, r, out, false)
}

func decodeJSONOptions(w http.ResponseWriter, r *http.Request, out any, allowEmpty bool) bool {
	if err := httpjson.Decode(w, r, out, httpjson.Options{MaxBytes: 1 << 20, AllowEmpty: allowEmpty}); err != nil {
		writeError(w, os.ErrInvalid, http.StatusBadRequest)
		return false
	}
	return true
}
