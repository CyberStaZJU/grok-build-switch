package ssh

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSSHConnectionCreateStrictJSON(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"name":"example","host":"example.test","user":"deploy","auth_type":"key","unknown":true}`},
		{name: "trailing document", body: `{"name":"example","host":"example.test","user":"deploy","auth_type":"key"} {}`},
		{name: "oversize", body: `{"name":"` + strings.Repeat("x", (1<<20)+1) + `"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(t.TempDir())
			response := serveSSHRequest(handler, http.MethodPost, "/api/ssh/connections", test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
			}
			if configs := handler.manager.ListConfigs(); len(configs) != 0 {
				t.Fatalf("invalid request persisted connections: %#v", configs)
			}
		})
	}
}

func TestSSHJSONEndpointsRejectEmptyBodyExceptImport(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create connection", method: http.MethodPost, path: "/api/ssh/connections"},
		{name: "update connection", method: http.MethodPut, path: "/api/ssh/connections/missing"},
		{name: "connect", method: http.MethodPost, path: "/api/ssh/connect"},
		{name: "delete files", method: http.MethodDelete, path: "/api/ssh/files?conn_id=missing"},
		{name: "save file", method: http.MethodPut, path: "/api/ssh/save"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(t.TempDir())
			response := serveSSHRequest(handler, test.method, test.path, "")
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
			}
		})
	}
}

func TestSSHJSONEndpointsRejectUnknownFields(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create connection", method: http.MethodPost, path: "/api/ssh/connections"},
		{name: "update connection", method: http.MethodPut, path: "/api/ssh/connections/missing"},
		{name: "connect", method: http.MethodPost, path: "/api/ssh/connect"},
		{name: "delete files", method: http.MethodDelete, path: "/api/ssh/files?conn_id=missing"},
		{name: "save file", method: http.MethodPut, path: "/api/ssh/save"},
		{name: "import config", method: http.MethodPost, path: "/api/ssh/import-ssh-config"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(t.TempDir())
			response := serveSSHRequest(handler, test.method, test.path, `{"unknown":true}`)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
			}
		})
	}
}

func TestSSHConnectionCreateAcceptsValidJSON(t *testing.T) {
	handler := NewHandler(t.TempDir())
	response := serveSSHRequest(handler, http.MethodPost, "/api/ssh/connections", `{"name":"example","host":"example.test","user":"deploy","auth_type":"key"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusCreated, response.Body.String())
	}
	configs := handler.manager.ListConfigs()
	if len(configs) != 1 || configs[0].Name != "example" || configs[0].Port != 22 {
		t.Fatalf("persisted configs = %#v", configs)
	}
}

func TestSSHImportConfigAllowsOnlyIntentionalEmptyBody(t *testing.T) {
	testHome := t.TempDir()
	originalHome, hadHome := os.LookupEnv("HOME")
	if err := os.Setenv("HOME", testHome); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadHome {
			_ = os.Setenv("HOME", originalHome)
			return
		}
		_ = os.Unsetenv("HOME")
	})

	t.Run("empty allowed", func(t *testing.T) {
		handler := NewHandler(t.TempDir())
		response := serveSSHRequest(handler, http.MethodPost, "/api/ssh/import-ssh-config", "")
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
		}
	})

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "trailing document", body: `{} {}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHandler(t.TempDir())
			response := serveSSHRequest(handler, http.MethodPost, "/api/ssh/import-ssh-config", test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusBadRequest, response.Body.String())
			}
		})
	}
}

func serveSSHRequest(handler *Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:54321"
	response := httptest.NewRecorder()
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	mux.ServeHTTP(response, request)
	return response
}
