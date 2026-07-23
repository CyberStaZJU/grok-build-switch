package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type serviceActionProxy struct{ action string }

func (p *serviceActionProxy) Status(context.Context) (SubscriptionProxyStatus, error) {
	return SubscriptionProxyStatus{}, nil
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
func (*serviceActionProxy) Accounts(context.Context) ([]SubscriptionProxyAccount, error) {
	return nil, nil
}
func (*serviceActionProxy) UpdateAccount(context.Context, string, string, bool) (SubscriptionProxyAccount, error) {
	return SubscriptionProxyAccount{}, nil
}
func (*serviceActionProxy) DeleteAccount(context.Context, string) error              { return nil }
func (*serviceActionProxy) Models(context.Context) ([]SubscriptionProxyModel, error) { return nil, nil }
func (*serviceActionProxy) InferenceKey(context.Context) (string, error)             { return "", nil }
func (*serviceActionProxy) Diagnostics(context.Context) ([]SubscriptionProxyCheck, error) {
	return nil, nil
}

func TestSubscriptionProxyServiceAcceptsUIRequest(t *testing.T) {
	proxy := &serviceActionProxy{}
	s := &Server{SubscriptionProxy: proxy}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/subscription-proxy/service", strings.NewReader(`{"action":"start"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()

	s.handleSubscriptionProxyService(rec, req)

	if rec.Code != http.StatusOK || proxy.action != "start" {
		t.Fatalf("status=%d action=%q body=%s", rec.Code, proxy.action, rec.Body.String())
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
