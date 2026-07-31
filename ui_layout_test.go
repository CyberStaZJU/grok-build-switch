package main

import (
	"bytes"
	"regexp"
	"testing"

	"golang.org/x/net/html"
)

func TestChatAndSessionGraphFeaturesAreAbsent(t *testing.T) {
	htmlData, err := assets.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	styleData, err := assets.ReadFile("ui/style.css")
	if err != nil {
		t.Fatal(err)
	}
	combined := append(append(append([]byte{}, htmlData...), appData...), styleData...)
	for _, removed := range []string{
		`id="chatBtn"`, `id="viewChat"`, `id="navSessionGraphBtn"`, `id="viewSessionGraph"`,
		`/api/agent/`, `/api/session-graph`, `nativeChatShell`, `organizePanel`,
		`browserUseStatus`, `Browser-use 注入`,
	} {
		if bytes.Contains(combined, []byte(removed)) {
			t.Fatalf("removed chat/session feature remains in embedded UI: %s", removed)
		}
	}
}

func TestRemovedAccountAndAdvancedFeaturesAreAbsent(t *testing.T) {
	htmlData, err := assets.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	styleData, err := assets.ReadFile("ui/style.css")
	if err != nil {
		t.Fatal(err)
	}
	combined := append(append(append([]byte{}, htmlData...), appData...), styleData...)
	for _, removed := range []string{
		"Grok Auth JSON", "Grok 注册机", "Grok 账号池", "CPA 设备授权", "CodeBuddy", "备份与恢复", "OAuth Client ID",
		`id="grokAuthCard"`, `id="registrarCard"`, `id="grokPoolCard"`, `id="backupFold"`, `id="oauthClientID"`,
		`id="toggleAdvancedBtn"`, "advancedOnly", `data-field="base_url"`, `data-field="api_backend"`,
		`data-field="context_window"`, `data-field="max_completion_tokens"`, `data-field="supports_backend_search"`, `data-field="extra_headers"`,
		"/api/backups", "/api/grok-auth", "/api/grok-pool", "/api/registrar", "/api/cpa-mint", "/api/codebuddy",
	} {
		if bytes.Contains(combined, []byte(removed)) {
			t.Fatalf("removed UI feature remains in embedded UI: %s", removed)
		}
	}
}

func TestStaticDollarIDReferencesExistInHTML(t *testing.T) {
	htmlData, err := assets.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	document, err := html.Parse(bytes.NewReader(htmlData))
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`\$\("([A-Za-z][A-Za-z0-9_-]*)"\)`).FindAllSubmatch(appData, -1)
	seen := map[string]bool{}
	dynamicIDs := map[string]bool{
		"subscriptionOpenLoginBtn":    true,
		"subscriptionCancelLoginBtn":  true,
		"subscriptionDismissLoginBtn": true,
	}
	for _, match := range matches {
		id := string(match[1])
		if seen[id] {
			continue
		}
		seen[id] = true
		if dynamicIDs[id] {
			if !bytes.Contains(appData, []byte(`id="`+id+`"`)) {
				t.Errorf("dynamic element %q is referenced but not created in app.js", id)
			}
			continue
		}
		if htmlElementByID(document, id) == nil {
			t.Errorf("ui/app.js references static element %q, but ui/index.html does not define it", id)
		}
	}
}

func TestOfficialActivationMessageRespectsSwitchedResult(t *testing.T) {
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`if (result.switched)`,
		`toast("已切换到官方账号。新开 grok 会话生效。", "success")`,
		`toast("请完成官方账号登录，登录完成后再次点击切换", "success")`,
	} {
		if !bytes.Contains(appData, []byte(fragment)) {
			t.Fatalf("official activation result handling is missing %q", fragment)
		}
	}
	if bytes.Contains(appData, []byte(`success: "已切换到官方账号。新开 grok 会话生效。"`)) {
		t.Fatal("official activation must not use an unconditional success message")
	}
}

func TestSSHConnectionManagementControlsExist(t *testing.T) {
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`class="btn ghost sshConnEditBtn"`, `class="btn ghost danger sshConnDelBtn"`,
		`method: "DELETE"`, `/api/ssh/connections/${encodeURIComponent(conn.id)}`,
		"只删除本地连接配置",
	} {
		if !bytes.Contains(appData, []byte(fragment)) {
			t.Fatalf("SSH connection management UI is missing %q", fragment)
		}
	}
}

func TestProfileEditorUsesSaveOnly(t *testing.T) {
	htmlData, err := assets.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(htmlData, []byte(`id="saveProfileBtn"`)) {
		t.Fatal("profile editor save button not found")
	}
	for _, stale := range []string{`id="activateCurrentBtn"`, "保存并启用", `$("activateCurrentBtn")`} {
		if bytes.Contains(htmlData, []byte(stale)) || bytes.Contains(appData, []byte(stale)) {
			t.Fatalf("profile editor still contains obsolete save-and-activate behavior: %s", stale)
		}
	}
}

func TestProfileEditorDoesNotDuplicateGlobalRoutingControls(t *testing.T) {
	data, err := assets.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	document, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if control := htmlElementByID(document, "defaultModel"); control == nil || control.Data != "select" {
		t.Fatal("provider-local defaultModel select not found")
	}
	for _, id := range []string{"webSearchModel", "subagentsExploreModel", "subagentsPlanModel"} {
		if control := htmlElementByID(document, id); control != nil {
			t.Fatalf("profile editor must not duplicate global routing control %s", id)
		}
	}
	for _, id := range []string{"routingDefault", "routingWebSearch", "routingExplore", "routingPlan"} {
		if control := htmlElementByID(document, id); control == nil || control.Data != "select" {
			t.Fatalf("global routing select %s not found", id)
		}
	}
	if !bytes.Contains(data, []byte("联网搜索、Explore 和 Plan 请在“模型路由”中统一管理")) {
		t.Fatal("profile editor must direct users to the single routing source of truth")
	}
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"snapshot.official_models", "snapshot.official_logged_in", "opt.dataset.official", "official,"} {
		if !bytes.Contains(appData, []byte(expected)) {
			t.Fatalf("official Grok routing UI behavior missing: %s", expected)
		}
	}
	for _, stale := range []string{`$("webSearchModel")`, `$("subagentsExploreModel")`, `$("subagentsPlanModel")`, `supported.length ? supported : ["low", "medium", "high"]`} {
		if bytes.Contains(appData, []byte(stale)) {
			t.Fatalf("profile editor still reads removed or synthetic routing control %s", stale)
		}
	}
	for _, expected := range []string{`route?.supports_reasoning_effort === true`, `const options = supported.length ? supported : ["none"]`, `effortSel.disabled = supported.length === 0`, `default_reasoning_effort: $("routingReasoningEffort")?.value || "none"`} {
		if !bytes.Contains(appData, []byte(expected)) {
			t.Fatalf("reasoning capability contract missing: %s", expected)
		}
	}
}

func TestFrontendHardeningContracts(t *testing.T) {
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`if (!token) throw new Error("服务器返回了空安全令牌")`,
		`if (csrfTokenPromise === pending) csrfTokenPromise = null`,
		`throw err`,
		`if (res.status !== 403) return false`,
		`for (let attempt = 0; attempt < 2; attempt++)`,
		`attempt > 0 || !csrfRejected(res, data)`,
		`const confirmed = await customConfirm`,
		`if (!confirmed) return false`,
		`user_confirmed_probe: true`,
		`const options = allowed.length ? allowed : ["none"]`,
		`const supported = efforts.length ? efforts : ["none"]`,
	} {
		if !bytes.Contains(appData, []byte(expected)) {
			t.Fatalf("frontend hardening contract missing: %s", expected)
		}
	}
	for _, stale := range []string{
		`anthropic: {`,
		`base_url: "https://api.anthropic.com"`,
		`const options = allowed.length ? allowed : ["low", "medium", "high"]`,
		`const supported = efforts.length ? efforts : ["low", "medium", "high"]`,
		`setReasoningEffortOptions(recommended.length ? recommended : ["low", "medium", "high"]`,
	} {
		if bytes.Contains(appData, []byte(stale)) {
			t.Fatalf("stale frontend behavior remains: %s", stale)
		}
	}
}

func TestDefaultReasoningEffortControl(t *testing.T) {
	htmlData, err := assets.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	document, err := html.Parse(bytes.NewReader(htmlData))
	if err != nil {
		t.Fatal(err)
	}
	control := htmlElementByID(document, "defaultReasoningEffort")
	if control == nil || control.Data != "select" {
		t.Fatal("defaultReasoningEffort select not found")
	}
	connectBlock := htmlElementByID(document, "connectBlock")
	if connectBlock == nil || !htmlElementContains(connectBlock, control) {
		t.Fatal("defaultReasoningEffort must be inside connectBlock")
	}
	var values, labels []string
	for child := control.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode || child.Data != "option" {
			continue
		}
		for _, attribute := range child.Attr {
			if attribute.Key == "value" {
				values = append(values, attribute.Val)
			}
		}
		if child.FirstChild != nil {
			labels = append(labels, child.FirstChild.Data)
		}
	}
	want := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	wantLabels := []string{"禁用推理 (none)", "最小 (minimal)", "低 (low)", "中 (medium)", "高 (high)", "超高 (xhigh)", "最大 (max，仅部分模型)"}
	if len(values) != len(want) {
		t.Fatalf("defaultReasoningEffort options = %v, want %v", values, want)
	}
	for i := range want {
		if values[i] != want[i] || labels[i] != wantLabels[i] {
			t.Fatalf("defaultReasoningEffort options = %v (%v), want %v (%v)", values, labels, want, wantLabels)
		}
	}
	if !bytes.Contains(appData, []byte(`"max"`)) || !bytes.Contains(htmlData, []byte(`value="max"`)) {
		t.Fatal("reasoning effort options must include max")
	}
	if !bytes.Contains(appData, []byte(`$("defaultReasoningEffort")`)) {
		t.Fatal("defaultReasoningEffort client binding not found")
	}
	for _, id := range []string{"detectReasoningEffortsBtn", "reasoningEffortStatus"} {
		if !bytes.Contains(htmlData, []byte(`id="`+id+`"`)) {
			t.Fatalf("%s control not found", id)
		}
		if !bytes.Contains(appData, []byte(`$("`+id+`")`)) {
			t.Fatalf("%s client binding not found", id)
		}
	}
	if !bytes.Contains(appData, []byte("/api/models/reasoning-efforts")) {
		t.Fatal("reasoning effort discovery endpoint not found")
	}
	for _, fragment := range []string{"fallbackReasoningEffort", "updateRoutingReasoningEfforts", "route.reasoning_efforts", "已按模型能力更新可选档位"} {
		if !bytes.Contains(appData, []byte(fragment)) {
			t.Fatalf("model-aware reasoning effort behavior missing: %s", fragment)
		}
	}
	if !bytes.Contains(appData, []byte("上游接受请求，可能静默忽略")) {
		t.Fatal("accepted reasoning effort disclaimer not found")
	}
}

func TestSubscriptionProxyPageContract(t *testing.T) {
	htmlData, err := assets.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	styleData, err := assets.ReadFile("ui/style.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"navSubscriptionProxyBtn", "viewSubscriptionProxy", "subscriptionStartBtn", "subscriptionStopBtn", "subscriptionRestartBtn", "subscriptionAccounts", "subscriptionModels", "saveSubscriptionModelsBtn", "subscriptionProviderHint", "runSubscriptionDiagnosticsBtn", "backFromSubscriptionProxyBtn"} {
		if !bytes.Contains(htmlData, []byte(`id="`+id+`"`)) {
			t.Fatalf("%s control not found", id)
		}
		if !bytes.Contains(appData, []byte(`$("`+id+`")`)) {
			t.Fatalf("%s client handler not found", id)
		}
	}
	for _, requestPart := range []string{
		`api("/api/subscription-proxy/service", { method: "POST", body: JSON.stringify({ action }) })`,
		`for (const action of ["start", "stop", "restart"])`,
		`const headers = { "Content-Type": "application/json", ...(options.headers || {}) }`,
		`headers["X-Grok-Switch-CSRF"] = await csrfToken({ refresh: attempt === 1 })`,
		`async function csrfToken({ refresh = false } = {})`,
	} {
		if !bytes.Contains(appData, []byte(requestPart)) {
			t.Fatalf("subscription service JSON request contract not found: %s", requestPart)
		}
	}
	for _, endpoint := range []string{"/api/subscription-proxy", "/api/subscription-proxy/service", "/api/subscription-proxy/login", "/api/subscription-proxy/login/open", "/api/subscription-proxy/accounts/", "/api/subscription-proxy/models", "/api/subscription-proxy/providers", "/api/subscription-proxy/diagnostics"} {
		if !bytes.Contains(appData, []byte(endpoint)) {
			t.Fatalf("client endpoint %s not found", endpoint)
		}
	}
	for _, expected := range []string{"第 1 步：添加订阅账号", "第 2 步：选择模型", "第 3 步：创建 / 更新供应商", "不会自动改变当前路由"} {
		if !bytes.Contains(htmlData, []byte(expected)) {
			t.Fatalf("subscription workflow guidance missing: %s", expected)
		}
	}
	for _, expected := range []string{"await customConfirm", `JSON.stringify({ provider })`, "已同步；请到“模型路由”选择要使用的模型"} {
		if !bytes.Contains(appData, []byte(expected)) {
			t.Fatalf("subscription account/provider behavior missing: %s", expected)
		}
	}
	for _, marker := range []string{"subscriptionLoginGrid", "subscriptionProviderGrid", "@media (max-width: 375px)"} {
		if !bytes.Contains(styleData, []byte(marker)) {
			t.Fatalf("responsive class %s not found", marker)
		}
	}
}

func htmlElementByID(node *html.Node, id string) *html.Node {
	if node.Type == html.ElementNode {
		for _, attribute := range node.Attr {
			if attribute.Key == "id" && attribute.Val == id {
				return node
			}
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := htmlElementByID(child, id); found != nil {
			return found
		}
	}
	return nil
}

func htmlElementContains(root, target *html.Node) bool {
	if root == nil || target == nil {
		return false
	}
	if root == target {
		return true
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if htmlElementContains(child, target) {
			return true
		}
	}
	return false
}
