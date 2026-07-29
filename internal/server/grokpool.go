package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"grok_switch/internal/grokpool"
)

func (s *Server) handleGrokPool(w http.ResponseWriter, r *http.Request) {
	if s.GrokPool == nil {
		writeError(w, fmt.Errorf("Grok 号池未初始化"), http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.GrokPool.Status())
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
		var request struct {
			Files []grokpool.ImportFile `json:"files"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, fmt.Errorf("读取号池认证文件: %w", err), http.StatusBadRequest)
			return
		}
		result, err := s.GrokPool.Import(request.Files)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		profile, err := s.upsertGrokAuthProfile()
		if err != nil {
			writeError(w, fmt.Errorf("账号已导入，但更新本地 profile 失败: %w", err), http.StatusInternalServerError)
			return
		}
		s.changed()
		writeJSONStatus(w, map[string]any{
			"result":  result,
			"status":  s.GrokPool.Status(),
			"profile": profile,
		}, http.StatusCreated)
	case http.MethodPut:
		var settings grokpool.Settings
		if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		status, err := s.GrokPool.UpdateSettings(settings)
		if err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		if s.GrokAuth != nil {
			if err := s.GrokAuth.SetProxyURL(status.Settings.ProxyURL); err != nil {
				writeError(w, err, http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, status)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleGrokPoolInspect(w http.ResponseWriter, r *http.Request) {
	if s.GrokPool == nil {
		writeError(w, fmt.Errorf("Grok 号池未初始化"), http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodPost:
		if err := s.GrokPool.StartInspection(); err != nil {
			writeError(w, err, http.StatusConflict)
			return
		}
		writeJSONStatus(w, s.GrokPool.Status(), http.StatusAccepted)
	case http.MethodDelete:
		s.GrokPool.StopInspection()
		writeJSON(w, map[string]bool{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleGrokPoolBulk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.GrokPool == nil {
		writeError(w, fmt.Errorf("Grok 号池未初始化"), http.StatusServiceUnavailable)
		return
	}
	var request struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	result, status, err := s.GrokPool.BulkAction(request.Action)
	if err != nil {
		writeError(w, err, http.StatusBadRequest)
		return
	}
	if result.Action == "delete" {
		if status.Configured || s.singleGrokAuthConfigured() {
			_, err = s.upsertGrokAuthProfile()
		} else {
			err = s.removeGrokAuthProfile()
		}
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
	}
	s.changed()
	writeJSON(w, map[string]any{"result": result, "status": status})
}

func (s *Server) handleGrokPoolAccount(w http.ResponseWriter, r *http.Request) {
	if s.GrokPool == nil {
		writeError(w, fmt.Errorf("Grok 号池未初始化"), http.StatusServiceUnavailable)
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/grok-pool/accounts/"), "/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, fmt.Errorf("无效账号 ID"), http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var request struct {
			Disabled bool `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeError(w, err, http.StatusBadRequest)
			return
		}
		status, err := s.GrokPool.SetDisabled(id, request.Disabled)
		if err != nil {
			writePoolAccountError(w, err)
			return
		}
		writeJSON(w, status)
	case http.MethodDelete:
		status, err := s.GrokPool.Delete(id)
		if err != nil {
			writePoolAccountError(w, err)
			return
		}
		if status.Configured {
			_, err = s.upsertGrokAuthProfile()
		} else if s.singleGrokAuthConfigured() {
			_, err = s.upsertGrokAuthProfile()
		} else {
			err = s.removeGrokAuthProfile()
		}
		if err != nil {
			writeError(w, err, http.StatusInternalServerError)
			return
		}
		s.changed()
		writeJSON(w, status)
	default:
		methodNotAllowed(w)
	}
}

func writePoolAccountError(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, err, http.StatusNotFound)
		return
	}
	writeError(w, err, http.StatusInternalServerError)
}
