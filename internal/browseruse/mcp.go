// Package browseruse provides an MCP server that exposes web_search and
// web_fetch tools backed by a headless Chrome instance via chromedp.
//
// The server speaks the MCP protocol over stdio so that an ACP client (the
// grok_switch bridge) can inject it into sessions whose target model does
// not support native x_search (i.e. any non-Grok provider).
package browseruse

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

// ToolNames returns the tool names exposed by this MCP server.
func ToolNames() []string { return []string{"web_search", "web_fetch"} }

// IsGrokModel reports whether a model identifier belongs to the Grok series
// and therefore supports native x_search without browser-use fallback.
func IsGrokModel(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	return strings.HasPrefix(model, "grok") || strings.HasPrefix(model, "grok-")
}

// Server is a minimal MCP server implementing JSON-RPC 2.0 over stdio.
type Server struct {
	stdin  io.Reader
	stdout io.Writer
	logger *log.Logger

	mu      sync.Mutex
	started bool

	// chromedp state
	allocCancel context.CancelFunc
	browserCtx  context.Context
}

// New creates a Server bound to os.Stdin/stdout. Call Serve() to block.
func New() *Server {
	return &Server{
		stdin:  os.Stdin,
		stdout: os.Stdout,
		logger: log.New(io.Discard, "", 0),
	}
}

// SetLogger overrides the diagnostics sink (io.Discard by default).
func (s *Server) SetLogger(l *log.Logger) {
	if l != nil {
		s.logger = l
	}
}

// Serve blocks until EOF on stdin or a fatal protocol error.
func (s *Server) Serve(ctx context.Context) error {
	defer s.stopBrowser()

	scanner := bufio.NewScanner(s.stdin)
	scanner.Buffer(make([]byte, 0, 1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		req, err := parseRequest(line)
		if err != nil {
			s.writeError(nil, -32700, "parse error", err.Error())
			continue
		}
		s.handle(ctx, req)
	}
	return scanner.Err()
}

// rpcRequest is a single JSON-RPC 2.0 request.
type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// rpcResponse is a JSON-RPC 2.0 response envelope.
type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result any             `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func parseRequest(line []byte) (*rpcRequest, error) {
	var raw struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}
	return &rpcRequest{ID: raw.ID, Method: raw.Method, Params: raw.Params}, nil
}

func (s *Server) handle(ctx context.Context, req *rpcRequest) {
	switch req.Method {
	case "initialize":
		s.writeResult(req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "grok_switch-browseruse", "version": "1"},
		})
	case "initialized", "notifications/initialized":
		// no-op notification
	case "tools/list":
		s.writeResult(req.ID, map[string]any{"tools": toolsList()})
	case "tools/call":
		s.handleToolCall(ctx, req)
	default:
		s.writeError(req.ID, -32601, "method not found", req.Method)
	}
}

func (s *Server) handleToolCall(ctx context.Context, req *rpcRequest) {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.writeError(req.ID, -32602, "invalid params", err.Error())
		return
	}
	switch params.Name {
	case "web_search":
		s.handleWebSearch(ctx, req.ID, params.Arguments)
	case "web_fetch":
		s.handleWebFetch(ctx, req.ID, params.Arguments)
	default:
		s.writeError(req.ID, -32601, "tool not found", params.Name)
	}
}

func (s *Server) handleWebSearch(ctx context.Context, id json.RawMessage, args map[string]any) {
	query, _ := args["query"].(string)
	if query == "" {
		s.writeError(id, -32602, "invalid params", "query is required")
		return
	}
	maxResults := 5
	if v, ok := args["max_results"].(float64); ok && v > 0 {
		maxResults = int(v)
	}

	results, err := searchWeb(ctx, s, query, maxResults)
	if err != nil {
		s.writeError(id, -32603, "internal error", err.Error())
		return
	}
	s.writeResult(id, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": results},
		},
	})
}

func (s *Server) handleWebFetch(ctx context.Context, id json.RawMessage, args map[string]any) {
	rawURL, _ := args["url"].(string)
	if rawURL == "" {
		s.writeError(id, -32602, "invalid params", "url is required")
		return
	}
	maxChars := 4000
	if v, ok := args["max_chars"].(float64); ok && v > 0 {
		maxChars = int(v)
	}

	text, err := fetchURL(ctx, s, rawURL, maxChars)
	if err != nil {
		s.writeError(id, -32603, "internal error", err.Error())
		return
	}
	s.writeResult(id, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
	})
}

// searchWeb runs a headless browser search and returns plain-text results.
func searchWeb(ctx context.Context, s *Server, query string, maxResults int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if maxResults < 1 {
		maxResults = 1
	}
	if maxResults > 10 {
		maxResults = 10
	}
	endpoint := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/126 Safari/537.36")
	response, err := s.HTTPClient().Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("搜索服务返回 HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return "", err
	}
	results := parseDuckDuckGoResults(string(body), maxResults)
	if len(results) == 0 {
		return "", errors.New("搜索服务未返回可解析的结果")
	}
	var sb strings.Builder
	for i, result := range results {
		fmt.Fprintf(&sb, "%d. %s\n   %s\n", i+1, result.Title, result.URL)
	}
	return sb.String(), nil
}

type searchResult struct {
	Title string
	URL   string
}

func parseDuckDuckGoResults(body string, maxResults int) []searchResult {
	const marker = `class="result__a"`
	results := make([]searchResult, 0, maxResults)
	for len(results) < maxResults {
		index := strings.Index(body, marker)
		if index < 0 {
			break
		}
		body = body[index+len(marker):]
		hrefIndex := strings.Index(body, `href="`)
		if hrefIndex < 0 {
			break
		}
		body = body[hrefIndex+len(`href="`):]
		hrefEnd := strings.IndexByte(body, '"')
		if hrefEnd < 0 {
			break
		}
		rawURL := html.UnescapeString(body[:hrefEnd])
		body = body[hrefEnd+1:]
		textStart := strings.IndexByte(body, '>')
		textEnd := strings.Index(body, "</a>")
		if textStart < 0 || textEnd < 0 || textEnd <= textStart {
			continue
		}
		title := strings.TrimSpace(stripHTML(html.UnescapeString(body[textStart+1 : textEnd])))
		resolvedURL := resolveDuckDuckGoURL(rawURL)
		if title != "" && resolvedURL != "" {
			results = append(results, searchResult{Title: title, URL: resolvedURL})
		}
		body = body[textEnd+len("</a>"):]
	}
	return results
}

func resolveDuckDuckGoURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if value := parsed.Query().Get("uddg"); value != "" {
		return value
	}
	if parsed.IsAbs() {
		return parsed.String()
	}
	return ""
}

func stripHTML(value string) string {
	var out strings.Builder
	insideTag := false
	for _, character := range value {
		switch character {
		case '<':
			insideTag = true
		case '>':
			insideTag = false
		default:
			if !insideTag {
				out.WriteRune(character)
			}
		}
	}
	return out.String()
}

// fetchURL retrieves a public URL through a pinned, redirect-validating HTTP
// transport. It intentionally does not execute page JavaScript: model-controlled
// fetching must not give a browser access to localhost, LAN services or metadata.
func fetchURL(ctx context.Context, s *Server, rawURL string, maxChars int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client, err := publicHTTPClient(ctx)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/126 Safari/537.36")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("目标页面返回 HTTP %d", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return "", err
	}
	body := strings.TrimSpace(stripHTML(string(content)))
	body = strings.Join(strings.Fields(body), " ")
	if len(body) > maxChars {
		body = body[:maxChars] + "\n…(truncated)"
	}
	return body, nil
}

func validatePublicURL(ctx context.Context, rawURL string) error {
	_, _, err := resolvePublicTarget(ctx, rawURL)
	return err
}

func resolvePublicTarget(ctx context.Context, rawURL string) (*url.URL, []netip.Addr, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return nil, nil, fmt.Errorf("invalid url: %s", rawURL)
	}
	if parsed.User != nil {
		return nil, nil, errors.New("URL 不得包含用户凭证")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, nil, errors.New("拒绝访问本机或私有网络地址")
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, nil, fmt.Errorf("解析目标地址失败: %w", err)
	}
	if len(addresses) == 0 {
		return nil, nil, errors.New("目标主机没有可用地址")
	}
	for _, address := range addresses {
		if !isPublicAddress(address) {
			return nil, nil, errors.New("拒绝访问本机、私有、链路本地或保留网络地址")
		}
	}
	return parsed, addresses, nil
}

func publicHTTPClient(ctx context.Context) (*http.Client, error) {
	pinned := make(map[string][]netip.Addr)
	transport := &http.Transport{}
	transport.DialContext = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses := pinned[strings.TrimSuffix(strings.ToLower(host), ".")]
		if len(addresses) == 0 {
			return nil, errors.New("目标主机未经安全解析")
		}
		var lastErr error
		for _, ip := range addresses {
			connection, err := (&net.Dialer{}).DialContext(dialCtx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	client := &http.Client{Transport: transport}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("重定向次数过多")
		}
		parsed, addresses, err := resolvePublicTarget(request.Context(), request.URL.String())
		if err != nil {
			return err
		}
		pinned[strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")] = addresses
		return nil
	}
	// The initial request and every redirect are validated and pinned by
	// RoundTrip before the transport resolves or dials the target host.
	client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		parsed, addresses, err := resolvePublicTarget(request.Context(), request.URL.String())
		if err != nil {
			return nil, err
		}
		pinned[strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")] = addresses
		return transport.RoundTrip(request)
	})
	return client, nil
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func isPublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	if address.Is4() {
		value := address.As4()
		// Carrier-grade NAT, documentation, benchmarking and reserved IPv4 ranges.
		if value[0] == 100 && value[1]&0xc0 == 64 ||
			value[0] == 192 && value[1] == 0 && value[2] == 0 ||
			value[0] == 192 && value[1] == 0 && value[2] == 2 ||
			value[0] == 198 && value[1] == 18 || value[0] == 198 && value[1] == 19 ||
			value[0] == 198 && value[1] == 51 && value[2] == 100 ||
			value[0] == 203 && value[1] == 0 && value[2] == 113 ||
			value[0] >= 240 {
			return false
		}
	}
	return !address.Is6() || address.IsGlobalUnicast()
}

// browserContext returns the long-lived browser context, or the input ctx when
// no browser has been started.
func (s *Server) browserContext(fallback context.Context) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.browserCtx != nil {
		return s.browserCtx
	}
	return fallback
}

func (s *Server) ensureBrowser(ctx context.Context) error {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if started {
		return nil
	}
	return s.startBrowser(ctx)
}

func (s *Server) startBrowser(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	browserPath, err := findBrowserExecutable()
	if err != nil {
		return err
	}
	allocOptions := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, allocOptions...)
	browserCtx, _ := chromedp.NewContext(allocCtx)
	if err := chromedp.Run(browserCtx); err != nil {
		allocCancel()
		return fmt.Errorf("启动无头浏览器失败: %w", err)
	}
	s.allocCancel = allocCancel
	s.browserCtx = browserCtx
	s.started = true
	return nil
}

func findBrowserExecutable() (string, error) {
	var candidates []string
	if configured := strings.TrimSpace(os.Getenv("GROK_SWITCH_BROWSER_PATH")); configured != "" {
		candidates = append(candidates, configured)
	}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		)
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge"} {
		if path, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, path)
		}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("未找到 Chrome、Edge 或 Chromium；可通过 GROK_SWITCH_BROWSER_PATH 指定浏览器")
}

func (s *Server) stopBrowser() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allocCancel != nil {
		s.allocCancel()
		s.allocCancel = nil
	}
	s.started = false
}

func (s *Server) writeResult(id json.RawMessage, result any) {
	if len(id) == 0 || string(id) == "null" {
		return
	}
	s.write(rpcResponse{ID: id, Result: result})
}

func (s *Server) writeError(id json.RawMessage, code int, message, detail string) {
	if len(id) == 0 || string(id) == "null" {
		return
	}
	s.write(rpcResponse{ID: id, Error: &rpcError{Code: code, Message: message + ": " + detail}})
}

func (s *Server) write(r rpcResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = s.stdout.Write(data)
}

func toolsList() []map[string]any {
	return []map[string]any{
		{
			"name":        "web_search",
			"description": "Search the web using a headless browser (DuckDuckGo). Returns a numbered list of result titles and URLs. Use when the model does not support native x_search.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":       map[string]any{"type": "string", "description": "Search query"},
					"max_results": map[string]any{"type": "integer", "description": "Maximum number of results to return (default 5)"},
				},
				"required": []string{"query"},
			},
		},
		{
			"name":        "web_fetch",
			"description": "Fetch a URL and return its text content. Use when the model cannot fetch URLs directly.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url":       map[string]any{"type": "string", "description": "HTTP or HTTPS URL to fetch"},
					"max_chars": map[string]any{"type": "integer", "description": "Maximum characters to return (default 4000)"},
				},
				"required": []string{"url"},
			},
		},
	}
}

// HTTPClient returns an *http.Client routed through Chrome's CDP when the
// browser is running, or the default transport otherwise.
func (s *Server) HTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
}
