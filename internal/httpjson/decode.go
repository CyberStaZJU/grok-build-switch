// Package httpjson provides shared JSON request decoding helpers.
package httpjson

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Options controls strict request decoding.
type Options struct {
	MaxBytes   int64
	AllowEmpty bool
}

// Decode reads exactly one JSON document from r into dst.
// Unknown fields are rejected, and the request body may not exceed MaxBytes.
func Decode(w http.ResponseWriter, r *http.Request, dst any, options Options) error {
	if options.MaxBytes <= 0 {
		return fmt.Errorf("httpjson: MaxBytes must be positive")
	}

	r.Body = http.MaxBytesReader(w, r.Body, options.MaxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) && options.AllowEmpty {
			return nil
		}
		return fmt.Errorf("decode JSON request: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode JSON request: multiple JSON documents")
		}
		return fmt.Errorf("decode JSON request: trailing data: %w", err)
	}
	return nil
}
