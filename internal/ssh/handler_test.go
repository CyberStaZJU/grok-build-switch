package ssh

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeleteConnectionPersistsAndDoesNotDeleteSiblingWithSameEndpoint(t *testing.T) {
	handler := NewHandler(t.TempDir())
	stale := ConnectionConfig{ID: "sshcfg_duplicate", Name: "duplicate", Host: "192.0.2.10", Port: 22022, User: "deploy", AuthType: "key"}
	keep := ConnectionConfig{ID: "sshcfg_primary", Name: "primary", Host: "192.0.2.10", Port: 22022, User: "deploy", AuthType: "key", KeyPath: "~/.ssh/id_ed25519_example"}
	if err := handler.manager.AddConfig(stale); err != nil {
		t.Fatal(err)
	}
	if err := handler.manager.AddConfig(keep); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodDelete, "/api/ssh/connections/"+stale.ID, nil)
	request.RemoteAddr = "127.0.0.1:54321"
	response := httptest.NewRecorder()
	handler.handleConnectionByID(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body.String())
	}

	reloaded := NewHandler(handler.manager.dataDir)
	configs := reloaded.manager.ListConfigs()
	data, _ := json.Marshal(configs)
	if strings.Contains(string(data), stale.ID) {
		t.Fatalf("deleted connection reappeared after reload: %s", data)
	}
	if !strings.Contains(string(data), keep.ID) {
		t.Fatalf("sibling connection was removed: %s", data)
	}
}

func TestDeleteMissingConnectionReturnsNotFound(t *testing.T) {
	handler := NewHandler(t.TempDir())
	request := httptest.NewRequest(http.MethodDelete, "/api/ssh/connections/missing", nil)
	request.RemoteAddr = "127.0.0.1:54321"
	response := httptest.NewRecorder()
	handler.handleConnectionByID(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestIsLoopbackUsesRemoteAddressNotHostHeader(t *testing.T) {
	t.Run("remote client cannot spoof localhost host header", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/api/ssh/connections", nil)
		req.RemoteAddr = "192.168.1.9:54321"
		req.Host = "localhost"
		recorder := httptest.NewRecorder()
		if isLoopback(recorder, req) {
			t.Fatal("remote request was accepted as loopback")
		}
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
		}
	})

	t.Run("loopback remote address is accepted regardless of host", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://example.test/api/ssh/connections", nil)
		req.RemoteAddr = "127.0.0.1:54321"
		req.Host = "example.test"
		recorder := httptest.NewRecorder()
		if !isLoopback(recorder, req) {
			t.Fatalf("loopback request rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})
}
