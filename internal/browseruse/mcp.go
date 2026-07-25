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
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/cdp"
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
	if err := s.startBrowser(ctx); err != nil {
		s.logger.Printf("browser-use: browser start skipped: %v", err)
	}
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
	case "initialized":
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
	ctx = s.browserContext(ctx)
	target := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	var nodes []*cdp.Node
	err := chromedp.Run(ctx,
		chromedp.Navigate(target),
		chromedp.WaitReady("#links", chromedp.ByID),
		chromedp.Nodes(`.result__a`, &nodes, chromedp.ByQueryAll),
	)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for i, n := range nodes {
		if i >= maxResults {
			break
		}
		title := innerText(n)
		href := ""
		for j := 0; j+1 < len(n.Attributes); j += 2 {
			if n.Attributes[j] == "href" {
				href = n.Attributes[j+1]
				break
			}
		}
		fmt.Fprintf(&sb, "%d. %s\n   %s\n", i+1, title, href)
	}
	if sb.Len() == 0 {
		return "(no results found)", nil
	}
	return sb.String(), nil
}

// fetchURL retrieves a URL and returns a plain-text preview.
func fetchURL(ctx context.Context, s *Server, rawURL string, maxChars int) (string, error) {
	ctx = s.browserContext(ctx)
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("invalid url: %s", rawURL)
	}
	var body string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(rawURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.ActionFunc(func(cctx context.Context) error {
			return chromedp.Evaluate(`(() => {
				const t = (document.body && document.body.innerText) || '';
				return t.replace(/[ \t]{2,}/g, ' ').replace(/\n{3,}/g, '\n\n');
			})()`, &body).Do(cctx)
		}),
	); err != nil {
		return "", err
	}
	body = strings.TrimSpace(body)
	if len(body) > maxChars {
		body = body[:maxChars] + "\n…(truncated)"
	}
	return body, nil
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

func (s *Server) startBrowser(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(ctx, "http://127.0.0.1:9222")
	browserCtx, _ := chromedp.NewContext(allocCtx)
	s.allocCancel = allocCancel
	s.browserCtx = browserCtx
	s.started = true
	return nil
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
	s.write(rpcResponse{ID: id, Result: result})
}

func (s *Server) writeError(id json.RawMessage, code int, message, detail string) {
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

func innerText(n *cdp.Node) string {
	if n.NodeValue != "" {
		return n.NodeValue
	}
	var sb strings.Builder
	for _, child := range n.Children {
		sb.WriteString(innerText(child))
	}
	return sb.String()
}

// DefaultRemoteDebugURL is the Chrome CDP endpoint used when launching the
// browser for browser-use tools.
const DefaultRemoteDebugURL = "http://127.0.0.1:9222"

// HTTPClient returns an *http.Client routed through Chrome's CDP when the
// browser is running, or the default transport otherwise.
func (s *Server) HTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
}
