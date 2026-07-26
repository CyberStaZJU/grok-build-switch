package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"grok_switch/internal/registrar"
)

func TestRegistrarConfigAPI(t *testing.T) {
	service, err := registrar.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Registrar: service}

	get := httptest.NewRequest(http.MethodGet, "/api/registrar", nil)
	response := httptest.NewRecorder()
	server.handleRegistrar(response, get)
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"email_provider":"cloudflare"`) {
		t.Fatalf("GET body = %s", response.Body.String())
	}

	body := `{
		"version":1,
		"browser_mode":"auto",
		"email_provider":"cloudmail",
		"hotmail_max_aliases":5,
		"count":2,
		"workers":1,
		"mail_timeout_seconds":180,
		"page_timeout_seconds":300,
		"prefer_protocol_mint":true
	}`
	put := httptest.NewRequest(http.MethodPut, "/api/registrar", strings.NewReader(body))
	response = httptest.NewRecorder()
	server.handleRegistrar(response, put)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", response.Code, response.Body.String())
	}
	if got := service.Get().Config.Count; got != 2 {
		t.Fatalf("saved count = %d", got)
	}
}

func TestRegistrarConfigRedactsSecretsAndPreservesOmittedValues(t *testing.T) {
	service, err := registrar.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := registrar.DefaultConfig()
	config.ClientID = "oauth-client-id-from-settings"
	config.CloudmailPassword = "mail-secret"
	config.CloudflareAPIKey = "api-secret"
	config.HotmailAccountsText = "user@example.com----password----refresh-token----client-id"
	if _, err := service.Update(config); err != nil {
		t.Fatal(err)
	}
	server := &Server{Registrar: service}

	get := httptest.NewRequest(http.MethodGet, "/api/registrar", nil)
	response := httptest.NewRecorder()
	server.handleRegistrar(response, get)
	for _, secret := range []string{"mail-secret", "api-secret", "refresh-token", "user@example.com"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("GET leaked %q: %s", secret, response.Body.String())
		}
	}

	put := httptest.NewRequest(http.MethodPut, "/api/registrar", strings.NewReader(`{"version":1,"email_provider":"cloudflare","count":2,"workers":1,"mail_timeout_seconds":180,"page_timeout_seconds":300}`))
	response = httptest.NewRecorder()
	server.handleRegistrar(response, put)
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", response.Code, response.Body.String())
	}
	saved := service.Get().Config
	if saved.ClientID != "oauth-client-id-from-settings" {
		t.Fatalf("settings-owned client ID was overwritten: %q", saved.ClientID)
	}
	if saved.CloudmailPassword != "mail-secret" || saved.CloudflareAPIKey != "api-secret" || !strings.Contains(saved.HotmailAccountsText, "refresh-token") {
		t.Fatalf("omitted secrets were not preserved")
	}
}

func TestRegistrarProbeRejectsMalformedJSON(t *testing.T) {
	service, err := registrar.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Registrar: service}
	request := httptest.NewRequest(http.MethodPost, "/api/registrar/probe", strings.NewReader(`{"version":`))
	response := httptest.NewRecorder()
	server.handleRegistrarProbe(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
}

func TestRegistrarStartRejectsIncompleteConfig(t *testing.T) {
	service, err := registrar.NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Registrar: service}
	request := httptest.NewRequest(http.MethodPost, "/api/registrar/start", strings.NewReader("{}"))
	response := httptest.NewRecorder()
	server.handleRegistrarStart(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
}
