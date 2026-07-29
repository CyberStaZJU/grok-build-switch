package netproxy

import "testing"

func TestBuildTransportNormalizesSupportedProxyURLs(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{"127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"http://127.0.0.1:7890", "http://127.0.0.1:7890"},
		{"socks5://user:pass@127.0.0.1:1080", "socks5://user:pass@127.0.0.1:1080"},
	} {
		normalized, transport, err := BuildTransport(test.input)
		if err != nil || normalized != test.want || transport == nil {
			t.Fatalf("BuildTransport(%q) = %q, %#v, %v", test.input, normalized, transport, err)
		}
	}
}

func TestBuildTransportAllowsEmptyAndRejectsUnsupported(t *testing.T) {
	if normalized, transport, err := BuildTransport(""); err != nil || normalized != "" || transport != nil {
		t.Fatalf("empty proxy = %q, %#v, %v", normalized, transport, err)
	}
	if _, _, err := BuildTransport("ftp://127.0.0.1:21"); err == nil {
		t.Fatal("expected unsupported proxy scheme error")
	}
	if _, _, err := BuildTransport("http://127.0.0.1:7890/path"); err == nil {
		t.Fatal("expected proxy path validation error")
	}
}
