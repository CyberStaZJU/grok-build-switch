package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type serviceActionProxy struct {
	action        string
	status        SubscriptionProxyStatus
	accountsCalls int
	modelsCalls   int
	deletedID     string
}

func (p *serviceActionProxy) Status(context.Context) (SubscriptionProxyStatus, error) {
	return p.status, nil
}
func (p *serviceActionProxy) ServiceAction(_ context.Context, action string) error {
	p.action = action
	return nil
}
func (*serviceActionProxy) StartLogin(context.Context, string) (SubscriptionProxyLogin, error) {
	return SubscriptionProxyLogin{}, nil
}
func (*serviceActionProxy) Login(context.Context, string) (SubscriptionProxyLogin, error) {
	return SubscriptionProxyLogin{}, nil
}
func (*serviceActionProxy) CancelLogin(context.Context, string) error { return nil }
func (p *serviceActionProxy) Accounts(context.Context) ([]SubscriptionProxyAccount, error) {
	p.accountsCalls++
	return nil, nil
}
func (*serviceActionProxy) UpdateAccount(context.Context, string, string, bool) (SubscriptionProxyAccount, error) {
	return SubscriptionProxyAccount{}, nil
}
func (p *serviceActionProxy) DeleteAccount(_ context.Context, id string) error {
	p.deletedID = id
	return nil
}
func (p *serviceActionProxy) Models(context.Context) ([]SubscriptionProxyModel, error) {
	p.modelsCalls++
	return nil, nil
}
func (*serviceActionProxy) InferenceKey(context.Context) (string, error) { return "", nil }
func (*serviceActionProxy) Diagnostics(context.Context) ([]SubscriptionProxyCheck, error) {
	return nil, nil
}

func TestSubscriptionProxyServiceAcceptsUIRequest(t *testing.T) {
	proxy := &serviceActionProxy{status: SubscriptionProxyStatus{Installed: true, State: "stopped"}}
	s := &Server{SubscriptionProxy: proxy}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/subscription-proxy/service", strings.NewReader(`{"action":"stop"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	s.handleSubscriptionProxyService(rec, req)

	if rec.Code != http.StatusOK || proxy.action != "stop" {
		t.Fatalf("status=%d action=%q body=%s", rec.Code, proxy.action, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"service":{"installed":true,"running":false,"healthy":false,"state":"stopped"`) {
		t.Fatalf("service status missing from action response: %s", rec.Body.String())
	}
}

func TestSubscriptionProxyStatusDoesNotCallManagementAPIWhileStopped(t *testing.T) {
	proxy := &serviceActionProxy{status: SubscriptionProxyStatus{Installed: true, State: "stopped"}}
	s := &Server{SubscriptionProxy: proxy}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/subscription-proxy", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	s.handleSubscriptionProxy(rec, req)

	if rec.Code != http.StatusOK || proxy.accountsCalls != 0 || proxy.modelsCalls != 0 {
		t.Fatalf("status=%d accounts=%d models=%d body=%s", rec.Code, proxy.accountsCalls, proxy.modelsCalls, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"state":"stopped"`) || !strings.Contains(rec.Body.String(), `"accounts":[]`) || !strings.Contains(rec.Body.String(), `"models":[]`) {
		t.Fatalf("stopped status response incomplete: %s", rec.Body.String())
	}
}

func TestSubscriptionProxyAccountDeleteAcceptsEncodedFilename(t *testing.T) {
	proxy := &serviceActionProxy{}
	s := &Server{SubscriptionProxy: proxy}
	id := "codex-user+tag@example.com.json"
	req := httptest.NewRequest(http.MethodDelete, "http://127.0.0.1/api/subscription-proxy/accounts/"+url.PathEscape(id), nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	s.handleSubscriptionProxyAccount(rec, req)

	if rec.Code != http.StatusOK || proxy.deletedID != id {
		t.Fatalf("status=%d deleted=%q body=%s", rec.Code, proxy.deletedID, rec.Body.String())
	}
}

func TestSubscriptionProxyAccountRejectsUnsafeIDs(t *testing.T) {
	for _, id := range []string{"..", ".", `..\\escape.json`, `nested\\escape.json`, "%2Fescape.json", "%00escape.json"} {
		t.Run(id, func(t *testing.T) {
			proxy := &serviceActionProxy{}
			s := &Server{SubscriptionProxy: proxy}
			req := httptest.NewRequest(http.MethodDelete, "http://127.0.0.1/api/subscription-proxy/accounts/"+id, nil)
			req.RemoteAddr = "127.0.0.1:12345"
			rec := httptest.NewRecorder()
			s.handleSubscriptionProxyAccount(rec, req)
			if rec.Code != http.StatusBadRequest || proxy.deletedID != "" {
				t.Fatalf("id=%q status=%d deleted=%q body=%s", id, rec.Code, proxy.deletedID, rec.Body.String())
			}
		})
	}
}

func TestSubscriptionProxyServiceRejectsMalformedJSONBeforeFacade(t *testing.T) {
	proxy := &serviceActionProxy{}
	s := &Server{SubscriptionProxy: proxy}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/subscription-proxy/service", strings.NewReader(`{"action":`))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	s.handleSubscriptionProxyService(rec, req)

	if rec.Code != http.StatusBadRequest || proxy.action != "" || !strings.Contains(rec.Body.String(), "请求格式无效") {
		t.Fatalf("status=%d action=%q body=%s", rec.Code, proxy.action, rec.Body.String())
	}
}
