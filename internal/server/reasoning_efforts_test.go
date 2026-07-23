package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"grok_switch/internal/profiles"
)

func TestReasoningEffortsDeclaredDoesNotProbe(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	store := profiles.NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	profile, err := store.Create(profiles.Profile{Name: "test", BaseURL: upstream.URL, APIKey: "secret-key", Models: []profiles.ModelDef{{Name: "reasoner", Model: "model-1", SupportsReasoningEffort: true, ReasoningEfforts: []string{"max", "xhigh", "minimal", "bogus", "none", "high"}, ReasoningEffortsSource: "declared"}}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Profiles: store}
	response := invokeReasoningEfforts(t, s, `{"profile_id":"`+profile.ID+`","model":"model-1"}`)
	if calls.Load() != 0 || response.Source != "declared" || strings.Join(response.Efforts, ",") != "none,minimal,high,xhigh,max" {
		t.Fatalf("response=%+v calls=%d", response, calls.Load())
	}
	assertNoKeyLeak(t, response, "secret-key")
}

func TestReasoningEffortsDefaultMetadataStillProbes(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	store := profiles.NewStore(filepath.Join(t.TempDir(), "profiles.json"))
	profile, err := store.Create(profiles.Profile{Name: "legacy", BaseURL: upstream.URL, Models: []profiles.ModelDef{{Name: "m", Model: "m"}}})
	if err != nil {
		t.Fatal(err)
	}
	response := invokeReasoningEfforts(t, &Server{Profiles: store}, `{"profile_id":"`+profile.ID+`","model":"m"}`)
	if response.Source != "probe" || calls.Load() != 7 {
		t.Fatalf("response=%+v calls=%d", response, calls.Load())
	}
}

func TestReasoningEffortsProbeClassificationAndChatPayload(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["reasoning_effort"] == nil || body["max_tokens"] != float64(1) {
			t.Errorf("unexpected payload: %#v", body)
		}
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusOK)
		case 2:
			http.Error(w, `invalid reasoning_effort`, http.StatusBadRequest)
		case 3:
			http.Error(w, `secret-key`, http.StatusUnauthorized)
		case 4:
			http.Error(w, `rate limit secret-key`, http.StatusTooManyRequests)
		default:
			http.Error(w, `upstream secret-key`, http.StatusInternalServerError)
		}
	}))
	defer upstream.Close()
	response := invokeReasoningEfforts(t, &Server{}, `{"base_url":"`+upstream.URL+`","api_key":"secret-key","model":"m","api_backend":"chat_completions"}`)
	want := []string{"accepted", "unsupported", "unknown", "unknown", "unknown", "unknown", "unknown"}
	for i, status := range want {
		if response.Results[i].Status != status {
			t.Fatalf("result[%d]=%+v want %s", i, response.Results[i], status)
		}
	}
	if response.Source != "probe" || strings.Join(response.Efforts, ",") != "none" {
		t.Fatalf("response=%+v", response)
	}
	assertNoKeyLeak(t, response, "secret-key")
}

func TestReasoningEffortsResponsesPayload(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["reasoning_effort"] == nil || body["max_output_tokens"] != float64(1) {
			t.Errorf("unexpected payload: %#v", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	response := invokeReasoningEfforts(t, &Server{}, `{"base_url":"`+upstream.URL+`","model":"m","api_backend":"responses"}`)
	if len(response.Efforts) != 7 {
		t.Fatalf("response=%+v", response)
	}
}

func TestReasoningEffortsMessagesUnknownWithoutProbe(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer upstream.Close()
	response := invokeReasoningEfforts(t, &Server{}, `{"base_url":"`+upstream.URL+`","model":"m","api_backend":"messages"}`)
	if response.Source != "unknown" || calls.Load() != 0 || !strings.Contains(response.Note, "messages") {
		t.Fatalf("response=%+v calls=%d", response, calls.Load())
	}
}

func invokeReasoningEfforts(t *testing.T, s *Server, body string) reasoningEffortsResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/models/reasoning-efforts", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleReasoningEfforts(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response reasoningEffortsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func assertNoKeyLeak(t *testing.T, response reasoningEffortsResponse, key string) {
	t.Helper()
	raw, _ := json.Marshal(response)
	if strings.Contains(string(raw), key) {
		t.Fatalf("API key leaked: %s", raw)
	}
}
