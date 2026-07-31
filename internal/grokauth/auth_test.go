package grokauth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestParseCredentialChoosesLatestOfficialEntry(t *testing.T) {
	earlier := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	later := earlier.Add(time.Hour)
	raw := fmt.Sprintf(`{
  "https://auth.x.ai::first": {"key": %q, "expires_at": %q},
  "https://auth.x.ai::second": {"key": %q, "expires_at": %q}
}`, testJWT(map[string]any{"exp": earlier.Unix()}), earlier.Format(time.RFC3339Nano), testJWT(map[string]any{"exp": later.Unix()}), later.Format(time.RFC3339Nano))

	credential, err := ParseCredential([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !credential.ExpiresAt.Equal(later) {
		t.Fatalf("ExpiresAt = %v, want %v", credential.ExpiresAt, later)
	}
}

func TestParseCredentialSupportsFlatOfficialShape(t *testing.T) {
	expiry := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	credential, err := ParseCredential([]byte(fmt.Sprintf(`{"type":"xai","access_token":%q}`, testJWT(map[string]any{"exp": expiry.Unix()}))))
	if err != nil {
		t.Fatal(err)
	}
	if credential.AccessToken == "" || !credential.ExpiresAt.Equal(expiry) {
		t.Fatalf("credential = %#v", credential)
	}
}

func TestParseCredentialRejectsMissingToken(t *testing.T) {
	for _, raw := range []string{"", "{", `{}`, `{"access_token":""}`} {
		if _, err := ParseCredential([]byte(raw)); err == nil {
			t.Fatalf("ParseCredential(%q) succeeded", raw)
		}
	}
}

func TestUpstreamURL(t *testing.T) {
	if got := UpstreamURL(); got != "https://cli-chat-proxy.grok.com/v1" {
		t.Fatalf("UpstreamURL() = %q", got)
	}
}

func testJWT(claims map[string]any) string {
	header, _ := json.Marshal(map[string]any{"alg": "none"})
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
