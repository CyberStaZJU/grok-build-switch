package netproxy

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func BuildTransport(raw string) (string, *http.Transport, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", nil, fmt.Errorf("代理地址无效")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", nil, fmt.Errorf("代理协议只支持 http、https、socks5 或 socks5h")
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", nil, fmt.Errorf("代理地址不能包含路径、查询参数或片段")
	}
	parsed.Path = ""
	transport := defaultTransportClone()
	transport.Proxy = http.ProxyURL(parsed)
	return parsed.String(), transport, nil
}

func defaultTransportClone() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		return transport.Clone()
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment}
}
