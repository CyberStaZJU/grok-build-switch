// Package grokauth provides read-only parsing for the official Grok CLI auth file.
package grokauth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const upstreamURL = "https://cli-chat-proxy.grok.com/v1"

type Credential struct {
	AccessToken string
	ExpiresAt   time.Time
}

// ParseCredential extracts the newest usable credential from the official
// Grok CLI auth.json shape. It never writes, refreshes, or stores credentials.
func ParseCredential(raw []byte) (Credential, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return Credential{}, fmt.Errorf("认证文件不是有效 JSON: %w", err)
	}

	candidates := make([]Credential, 0, len(root)+1)
	if credential, ok := credentialFromMap(root); ok {
		candidates = append(candidates, credential)
	}
	keys := make([]string, 0, len(root))
	for key := range root {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entry, ok := root[key].(map[string]any)
		if !ok {
			continue
		}
		if credential, found := credentialFromMap(entry); found {
			candidates = append(candidates, credential)
		}
	}
	if len(candidates) == 0 {
		return Credential{}, fmt.Errorf("未找到 Grok CLI access token")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].ExpiresAt.After(candidates[j].ExpiresAt)
	})
	return candidates[0], nil
}

func credentialFromMap(entry map[string]any) (Credential, bool) {
	accessToken := firstNonEmpty(stringValue(entry["access_token"]), stringValue(entry["key"]))
	if accessToken == "" {
		return Credential{}, false
	}
	expiresAt := parseTime(firstNonEmpty(stringValue(entry["expired"]), stringValue(entry["expires_at"])))
	if expiresAt.IsZero() {
		expiresAt = jwtExpiry(accessToken)
	}
	return Credential{AccessToken: accessToken, ExpiresAt: expiresAt}, true
}

func parseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
		if unix > 1e12 {
			unix /= 1000
		}
		return time.Unix(unix, 0).UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	var claims map[string]any
	if err := decoder.Decode(&claims); err != nil {
		return time.Time{}
	}
	switch value := claims["exp"].(type) {
	case json.Number:
		if unix, err := value.Int64(); err == nil {
			return time.Unix(unix, 0).UTC()
		}
	case float64:
		return time.Unix(int64(value), 0).UTC()
	}
	return time.Time{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func UpstreamURL() string { return upstreamURL }
