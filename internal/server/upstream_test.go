package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestModelEndpointsAreNormalizedAndHaveUsefulFallback(t *testing.T) {
	for _, tc := range []struct {
		base string
		want []string
	}{
		{"https://gateway.example/v1", []string{"https://gateway.example/v1/models"}},
		{"https://gateway.example/", []string{"https://gateway.example/models", "https://gateway.example/v1/models"}},
		{"https://gateway.example/custom", []string{"https://gateway.example/custom/models", "https://gateway.example/custom/v1/models"}},
	} {
		if got := modelEndpoints(tc.base); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("modelEndpoints(%q)=%v want=%v", tc.base, got, tc.want)
		}
	}
}

func TestFetchModelListFallsBackAndUsesOnlyCallableIDs(t *testing.T) {
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"id": "provider/model-id", "name": "Friendly Model Name"},
			map[string]any{"name": "Claude Sonnet 4"},
		}})
	}))
	defer upstream.Close()
	models, err := fetchModelList(context.Background(), upstream.URL, "secret", "openai_chat")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"/models", "/v1/models"}) {
		t.Fatalf("paths=%v", paths)
	}
	if !reflect.DeepEqual(models, []string{"provider/model-id"}) {
		t.Fatalf("models=%v", models)
	}
	joined := strings.Join(models, ",")
	if strings.Contains(joined, "Friendly Model Name") || strings.Contains(joined, "Claude Sonnet 4") {
		t.Fatalf("display name leaked into callable IDs: %v", models)
	}
}

func TestExtractModelsIgnoresUnrelatedNestedObjects(t *testing.T) {
	payload := map[string]any{
		"metadata": map[string]any{"id": "not-a-model"},
		"models":   []any{map[string]any{"id": "model-a", "name": "Display A"}, "model-b"},
	}
	if got := extractModels(payload); !reflect.DeepEqual(got, []string{"model-a", "model-b"}) {
		t.Fatalf("models=%v", got)
	}
}

func TestProbeModelSupportsThirdPartyMessagesGateway(t *testing.T) {
	const apiKey = "third-party-secret"
	var called bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		for header, want := range map[string]string{
			"Content-Type":      "application/json",
			"Accept":            "application/json",
			"X-Api-Key":         apiKey,
			"Authorization":     "Bearer " + apiKey,
			"Anthropic-Version": "2023-06-01",
		} {
			if got := r.Header.Get(header); got != want {
				t.Errorf("%s = %q, want %q", header, got, want)
			}
		}
		var body struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "compatible-model" || body.MaxTokens != 1 || len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != "ping" {
			t.Errorf("payload = %#v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer gateway.Close()

	if err := probeModel(t.Context(), gateway.URL+"/v1", apiKey, "anthropic", "messages", "compatible-model"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("third-party Messages gateway was not called")
	}
}
