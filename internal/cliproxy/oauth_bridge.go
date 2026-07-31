package cliproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuth callback bridge: CLIProxyAPI in LaunchAgent mode often does not bind
// localhost:1455 itself, so browser OAuth redirects never complete. We listen
// on that port and forward the callback into CLIProxyAPI's management API.

const oauthCallbackPort = "1455"

var (
	bridgeOnce sync.Once
	bridgeErr  error
)

func ensureOAuthCallbackBridge() error {
	bridgeOnce.Do(func() {
		bridgeErr = startOAuthCallbackBridge()
	})
	return bridgeErr
}

func startOAuthCallbackBridge() error {
	ln, err := net.Listen("tcp", "127.0.0.1:"+oauthCallbackPort)
	if err != nil {
		// Another process (possibly CLIProxyAPI itself) already owns the port.
		if isAddrInUse(err) {
			return nil
		}
		return fmt.Errorf("无法启动 OAuth 回调服务: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", handleOAuthBrowserCallback)
	mux.HandleFunc("/", handleOAuthBrowserCallback)
	srv := newOAuthCallbackHTTPServer(mux)
	go func() {
		_ = srv.Serve(ln)
	}()
	return nil
}

func newOAuthCallbackHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

func isAddrInUse(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "address already in use") || strings.Contains(msg, "bind: address already in use")
}

func handleOAuthBrowserCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := firstNonEmpty(q.Get("code"), r.FormValue("code"))
	state := firstNonEmpty(q.Get("state"), r.FormValue("state"))
	errMsg := firstNonEmpty(q.Get("error"), r.FormValue("error"))
	errDesc := firstNonEmpty(q.Get("error_description"), r.FormValue("error_description"))
	provider := firstNonEmpty(q.Get("provider"), "codex")

	if state == "" {
		writeCallbackPage(w, http.StatusBadRequest, "回调缺少 state，请回到 Grok Build Switch 重试登录。", false)
		return
	}

	redirectURL := "http://localhost:" + oauthCallbackPort + r.URL.RequestURI()
	forwardErr := forwardOAuthCallback(r.Context(), provider, state, code, errMsg, errDesc, redirectURL)
	if errMsg != "" {
		writeCallbackPage(w, http.StatusBadRequest, "登录被取消或拒绝："+firstNonEmpty(errDesc, errMsg), false)
		return
	}
	if forwardErr != nil {
		writeCallbackPage(w, http.StatusBadGateway, "回调已收到，但提交给订阅代理失败："+forwardErr.Error(), false)
		return
	}
	writeCallbackPage(w, http.StatusOK, "登录授权已完成，可以关闭此页面，返回 Grok Build Switch。", true)
}

func forwardOAuthCallback(ctx context.Context, provider, state, code, errMsg, errDesc, redirectURL string) error {
	client := &http.Client{Timeout: 12 * time.Second}

	// Documented open endpoint: GET with provider/state/code.
	params := url.Values{}
	params.Set("provider", provider)
	params.Set("state", state)
	if code != "" {
		params.Set("code", code)
	}
	if errMsg != "" {
		params.Set("error", errMsg)
	}
	if errDesc != "" {
		params.Set("error_description", errDesc)
	}
	getURL := "http://127.0.0.1:8317/v0/management/oauth-callback?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, getURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		// Fall through to POST with more detail.
		_ = body
	}

	payload, _ := json.Marshal(map[string]string{
		"provider":     provider,
		"state":        state,
		"code":         code,
		"error":        errMsg,
		"redirect_url": redirectURL,
	})
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:8317/v0/management/oauth-callback", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	postReq.Header.Set("Content-Type", "application/json")
	postResp, err := client.Do(postReq)
	if err != nil {
		return err
	}
	defer postResp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(postResp.Body, 1<<16))
	if postResp.StatusCode >= 200 && postResp.StatusCode < 300 {
		return nil
	}
	if len(body) > 0 {
		return fmt.Errorf("CLIProxyAPI 返回 %s: %s", postResp.Status, string(body))
	}
	return fmt.Errorf("CLIProxyAPI 返回 %s", postResp.Status)
}

func writeCallbackPage(w http.ResponseWriter, status int, message string, ok bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	title := "登录失败"
	if ok {
		title = "登录成功"
	}
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>%s</title>
<style>
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f6f7f9;color:#111;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}
.card{background:#fff;border:1px solid #e5e7eb;border-radius:14px;padding:28px 32px;max-width:420px;box-shadow:0 8px 30px rgba(0,0,0,.06)}
h1{font-size:20px;margin:0 0 10px}p{margin:0;line-height:1.5;color:#444}
</style></head><body><div class="card"><h1>%s</h1><p>%s</p></div></body></html>`, title, title, message)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
