package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubscriptionProviderCreationRejectsUnknownProvider(t *testing.T) {
	s := newRoutingTestServer(t)
	s.SubscriptionProxy = &subscriptionProfileRoutingFake{}
	s.ActualPort = 17878

	request := loopbackRequest(http.MethodPost, "/api/subscription-proxy/providers", `{"provider":"unknown"}`)
	response := httptest.NewRecorder()
	s.handleSubscriptionProxyProviders(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "无效供应商类型") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
