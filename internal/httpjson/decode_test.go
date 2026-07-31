package httpjson

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type decodeRequest struct {
	Name string `json:"name"`
}

func TestDecode(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		var request decodeRequest
		err := decodeBody(`{"name":"example"}`, 1<<10, false, &request)
		if err != nil {
			t.Fatal(err)
		}
		if request.Name != "example" {
			t.Fatalf("name = %q, want %q", request.Name, "example")
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		var request decodeRequest
		err := decodeBody(`{"name":"example","extra":true}`, 1<<10, false, &request)
		if err == nil || !strings.Contains(err.Error(), `unknown field "extra"`) {
			t.Fatalf("error = %v, want unknown field error", err)
		}
	})

	t.Run("trailing document", func(t *testing.T) {
		var request decodeRequest
		err := decodeBody(`{"name":"example"} {"name":"second"}`, 1<<10, false, &request)
		if err == nil || !strings.Contains(err.Error(), "multiple JSON documents") {
			t.Fatalf("error = %v, want multiple document error", err)
		}
	})

	t.Run("oversize", func(t *testing.T) {
		var request decodeRequest
		err := decodeBody(`{"name":"`+strings.Repeat("x", 64)+`"}`, 32, false, &request)
		var maxBytesError *http.MaxBytesError
		if !errors.As(err, &maxBytesError) {
			t.Fatalf("error = %v, want http.MaxBytesError", err)
		}
	})

	t.Run("empty rejected by default", func(t *testing.T) {
		var request decodeRequest
		if err := decodeBody(" \n\t", 1<<10, false, &request); err == nil {
			t.Fatal("empty body was accepted without AllowEmpty")
		}
	})

	t.Run("empty explicitly allowed", func(t *testing.T) {
		var request decodeRequest
		if err := decodeBody(" \n\t", 1<<10, true, &request); err != nil {
			t.Fatalf("empty body rejected with AllowEmpty: %v", err)
		}
	})
}

func decodeBody(body string, maxBytes int64, allowEmpty bool, dst any) error {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	response := httptest.NewRecorder()
	return Decode(response, request, dst, Options{MaxBytes: maxBytes, AllowEmpty: allowEmpty})
}
