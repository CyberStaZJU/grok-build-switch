package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type subscriptionProxyServiceFake struct {
	action string
	err    error
}

func (f *subscriptionProxyServiceFake) Status(context.Context) (SubscriptionProxyStatus, error) {
	return SubscriptionProxyStatus{}, nil
}
func (f *subscriptionProxyServiceFake) ServiceAction(_ context.Context, action string) error {
	f.action = action
	return f.err
}
func (f *subscriptionProxyServiceFake) StartLogin(context.Context, string) (SubscriptionProxyLogin, error) {
	return SubscriptionProxyLogin{}, nil
}
func (f *subscriptionProxyServiceFake) Login(context.Context, string) (SubscriptionProxyLogin, error) {
	return SubscriptionProxyLogin{}, nil
}
func (f *subscriptionProxyServiceFake) CancelLogin(context.Context, string) error { return nil }
func (f *subscriptionProxyServiceFake) Accounts(context.Context) ([]SubscriptionProxyAccount, error) {
	return nil, nil
}
func (f *subscriptionProxyServiceFake) UpdateAccount(context.Context, string, string, bool) (SubscriptionProxyAccount, error) {
	return SubscriptionProxyAccount{}, nil
}
func (f *subscriptionProxyServiceFake) DeleteAccount(context.Context, string) error { return nil }
func (f *subscriptionProxyServiceFake) Models(context.Context) ([]SubscriptionProxyModel, error) {
	return nil, nil
}
func (f *subscriptionProxyServiceFake) InferenceKey(context.Context) (string, error) {
	return "key", nil
}
func (f *subscriptionProxyServiceFake) Diagnostics(context.Context) ([]SubscriptionProxyCheck, error) {
	return nil, nil
}

func TestSubscriptionProxyServiceStartDecodesValidJSON(t *testing.T) {
	fake := &subscriptionProxyServiceFake{}
	s := &Server{SubscriptionProxy: fake}
	req := httptest.NewRequest(http.MethodPost, "/api/subscription-proxy/service", strings.NewReader(`{"action":"start"}`))
	req.RemoteAddr = "127.0.0.1:12345"
	response := httptest.NewRecorder()

	s.handleSubscriptionProxyService(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", response.Code, http.StatusOK, response.Body.String())
	}
	if fake.action != "start" {
		t.Fatalf("action = %q, want start", fake.action)
	}
}

func TestSubscriptionProxyServiceSeparatesLocalJSONAndUpstreamErrors(t *testing.T) {
	t.Run("local JSON validation", func(t *testing.T) {
		fake := &subscriptionProxyServiceFake{}
		s := &Server{SubscriptionProxy: fake}
		req := httptest.NewRequest(http.MethodPost, "/api/subscription-proxy/service", strings.NewReader(`{"action":"start","unknown":true}`))
		req.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		s.handleSubscriptionProxyService(response, req)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "请求格式无效") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if fake.action != "" {
			t.Fatalf("runtime called with %q", fake.action)
		}
	})

	t.Run("upstream failure", func(t *testing.T) {
		fake := &subscriptionProxyServiceFake{err: errors.New("raw management token and path")}
		s := &Server{SubscriptionProxy: fake}
		req := httptest.NewRequest(http.MethodPost, "/api/subscription-proxy/service", strings.NewReader(`{"action":"start"}`))
		req.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		s.handleSubscriptionProxyService(response, req)
		if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "订阅代理操作失败") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "management") || strings.Contains(response.Body.String(), "token") {
			t.Fatalf("upstream detail leaked: %s", response.Body.String())
		}
	})
}
