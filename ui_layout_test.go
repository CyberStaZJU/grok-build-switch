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
		`data-field="context_window"`, `data-field="max_completion_tokens"`, `data-field="extra_headers"`,
		"/api/backups", "/api/grok-auth", "/api/grok-pool", "/api/registrar", "/api/cpa-mint", "/api/codebuddy",
	} {
		if bytes.Contains(combined, []byte(removed)) {
			t.Fatalf("removed UI feature remains in embedded UI: %s", removed)
		}
	}
}

func TestRoutingDriftUIUsesUnifiedRouting(t *testing.T) {
	htmlData, err := assets.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"配置与当前模型路由不一致",
		"路由托管字段与当前模型路由不匹配",
		"确认并重新应用路由",
		"保留无关 TOML 设置",
	} {
		if !bytes.Contains(htmlData, []byte(fragment)) {
			t.Fatalf("routing drift copy is missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		`state.status?.config_matches_routing === false`,
		`state.status?.active_routing?.repair_required === true`,
		`customConfirm("将重新生成 Switch 托管的模型定义`,
		`api("/api/routing/reapply", { method: "POST" })`,
	} {
		if !bytes.Contains(appData, []byte(fragment)) {
			t.Fatalf("unified routing drift behavior is missing %q", fragment)
		}
	}
	for _, stale := range []string{
		`state.status?.config_matches_active`,
		`state.status?.active_profile?.id`,
		`/api/profiles/${id}/activate`,
		"用供应商覆盖文件",
	} {
		if bytes.Contains(appData, []byte(stale)) || bytes.Contains(htmlData, []byte(stale)) {
			t.Fatalf("legacy profile drift behavior remains %q", stale)
		}
	}
}

func TestFrontendPromptGlobAndSearchCapabilityContracts(t *testing.T) {
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"dialog.oncancel = (event) =>",
		"ok.onclick = null",
		"cancel.onclick = null",
		"dialog.oncancel = null",
		`.replace(/[.+^${}()|[\]\\/]/g, "\\$&")`,
		"return model.supports_backend_search ?? false",
		`data-field="supports_backend_search"`,
		"仅在上游明确支持时启用",
	} {
		if !bytes.Contains(appData, []byte(fragment)) {
			t.Fatalf("frontend hardening contract is missing %q", fragment)
		}
	}
	if bytes.Contains(appData, []byte("model.supports_backend_search ?? true")) {
		t.Fatal("manual models still default to backend search support")
	}
}

func TestSSHFileDeleteIncludesConnectionID(t *testing.T) {
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		"async function deleteSSHFiles(connID, paths)",
		"`/api/ssh/files?conn_id=${encodeURIComponent(connID)}`",
		"await deleteSSHFiles(ssh.activeConn, paths)",
	} {
		if !bytes.Contains(appData, []byte(fragment)) {
			t.Fatalf("SSH file deletion is missing %q", fragment)
		}
	}
	if bytes.Contains(appData, []byte(`api("/api/ssh/files", { method: "DELETE"`)) {
		t.Fatal("SSH file deletion still omits conn_id")
	}
}

func TestMaxCollaborationUIContract(t *testing.T) {
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
	for _, id := range []string{
		"collaborationCard", "collaborationBadge", "collaborationRelationship", "collaborationIssues", "collaborationMode", "collaborationFederationDisclosure", "collaborationFederationMap", "collaborationFederationConsent", "collaborationFederationWarning",
		"collaborationMainCoordinatorProvider", "collaborationMainCoordinatorDataScope", "collaborationMainCoordinatorModel", "collaborationMainCoordinatorSpeed", "collaborationMainCoordinatorEffort", "collaborationMainCoordinatorCapability",
		"collaborationTaskDecompositionProvider", "collaborationTaskDecompositionDataScope", "collaborationTaskDecompositionModel", "collaborationTaskDecompositionSpeed", "collaborationTaskDecompositionEffort", "collaborationTaskDecompositionCapability",
		"collaborationMainImplementationProvider", "collaborationMainImplementationDataScope", "collaborationMainImplementationModel", "collaborationMainImplementationSpeed", "collaborationMainImplementationEffort", "collaborationMainImplementationCapability",
		"collaborationDifficultReviewProvider", "collaborationDifficultReviewDataScope", "collaborationDifficultReviewModel", "collaborationDifficultReviewSpeed", "collaborationDifficultReviewEffort", "collaborationDifficultReviewCapability",
		"collaborationTier", "collaborationTierHint", "collaborationCreditWarning", "collaborationLaunchTitle", "collaborationLaunchBudget", "collaborationLaunchObjective", "collaborationLaunchInstruction", "copyCollaborationLaunchBtn", "previewCollaborationBtn", "applyCollaborationBtn",
		"disableCollaborationBtn", "collaborationPreview", "collaborationConfigBefore", "collaborationConfigAfter",
		"collaborationArtifacts", "collaborationFingerprint",
	} {
		if !bytes.Contains(htmlData, []byte(`id="`+id+`"`)) {
			t.Fatalf("collaboration control %q is missing", id)
		}
	}
	for _, fragment := range []string{
		"与路由策略的关系", "路由策略管理普通主会话的 default、web_search、explore 和 plan",
		"只将 default 和默认推理强度对齐到主协调", "不覆盖 web_search、explore 或 plan",
		"主协调", "任务拆解", "主实现", "困难实现 / 复核", "角色名称不绑定 Terra、Luna 或 Sol",
		"Standard/Fast 速度档", "Fast 请求 priority", "更多订阅 credits", "缺失时不会回退",
		"Switch 只预览并生成用户级 role/workflow", "budget 1", "budget 2", "budget 3", "budget 4",
		"停用（保留文件）", "Critical Reviewed Build · 4 agents（显式选择）", "all_workflow_tiers_v1", "跨供应商数据流确认", "适用于全部五条可执行路径", "Adaptive 仅是默认提示，不缩小本次授权", "我已核对并同意以上跨供应商数据流", "Prompt 约束不是硬 DLP 边界", "在 Grok Build 中启动", "直接 slash 启动固定使用默认 budget 128", "复制精确代理指令", "请调用 workflow 工具运行 named workflow gbs-max-collab", "agent_budget=1", "请不要启动并说明原因", "数据范围（workflow 固定）",
	} {
		if !bytes.Contains(htmlData, []byte(fragment)) {
			t.Fatalf("collaboration guidance is missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"function trustedCollaborationEfforts", "function collaborationRouteSupportsEffort", "function collaborationRouteSupportsMax",
		"function collaborationStandardRoutes", "function resolveCollaborationRoute", "function populateCollaborationSpeedOptions", "function collaborationLaunchParameters", "function updateCollaborationLaunchGuide",
		`source !== "declared" && source !== "probe"`, "speed_tier", "const roles =", "main_coordinator", "task_decomposition",
		"main_implementation", "difficult_implementation_review", `api("/api/collaboration/preview"`,
		`api("/api/collaboration"`, "confirmed: true", "fingerprint: pending.preview.fingerprint",
		"async function disableCollaboration()", "路由 default 会对齐主协调解析后的具体 Standard/Fast 路由", "web_search、explore 和 plan 保持不变",
		"更多订阅 credits", "不会回退到 Standard", "Switch 本身不会启动 agent", "不会删除已生成的 role/workflow", "JSON.stringify(args)", "workflow 工具运行 named workflow gbs-max-collab", "不要使用 /gbs-max-collab 或 /workflow slash 启动",
	} {
		if !bytes.Contains(appData, []byte(fragment)) {
			t.Fatalf("collaboration client contract is missing %q", fragment)
		}
	}
	for _, selector := range []string{".collaborationRelationship", ".collaborationGrid", ".collaborationRole", ".collaborationRoleHead", ".collaborationLaunchCard", ".collaborationLaunchCopyRow", ".collaborationFederationCard", ".collaborationFederationHead", ".collaborationFederationMap", ".collaborationFederationBoundary", ".collaborationConsent", ".collaborationCreditWarning", ".collaborationTierField", ".collaborationBudgetGrid", ".collaborationDiffGrid", ".collaborationArtifact"} {
		if !bytes.Contains(styleData, []byte(selector)) {
			t.Fatalf("collaboration style %q is missing", selector)
		}
	}
	for _, forbidden := range []string{`/api/agent/`, `id="viewChat"`, `id="viewSessionGraph"`} {
		if bytes.Contains(append(append(append([]byte{}, htmlData...), appData...), styleData...), []byte(forbidden)) {
			t.Fatalf("collaboration UI restored forbidden runtime surface %q", forbidden)
		}
	}
}

func TestCacheStatisticsUIContract(t *testing.T) {
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
	for _, id := range []string{
		"cacheStatsHours", "refreshCacheStatsBtn", "cacheHitRate", "cacheTurns",
		"cachePromptTokens", "cacheCachedTokens", "cacheCompletionTokens", "cacheReasoningTokens",
		"cacheStatsHint", "cacheByModel", "cacheRecent",
	} {
		if !bytes.Contains(htmlData, []byte(`id="`+id+`"`)) {
			t.Fatalf("cache statistics control %q is missing", id)
		}
	}
	for _, fragment := range []string{
		"async function loadCacheStats()", "/api/cache-stats?hours=", "const overall = data.overall || {}",
		`$("cacheHitRate").textContent`, `$("cacheCompletionTokens").textContent`, `$("cacheReasoningTokens").textContent`,
		`$("cacheByModel").innerHTML`, `$("cacheRecent").innerHTML`, `"Completion", "Reasoning"`,
	} {
		if !bytes.Contains(appData, []byte(fragment)) {
			t.Fatalf("cache statistics renderer is missing %q", fragment)
		}
	}
	for _, selector := range []string{".cacheStatsSummary", ".cacheStatTile", ".cacheStatsTables", ".cacheDataTable", ".smSelect"} {
		if !bytes.Contains(styleData, []byte(selector)) {
			t.Fatalf("cache statistics style %q is missing", selector)
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
		`official && !profile.logged_in ? "登录"`,
		`toast("已切换到官方账号。新开 grok 会话生效。", "success")`,
		`toast("已打开官方登录。完成登录后不会自动启用，请回到此处再次点击“启用”。", "success")`,
	} {
		if !bytes.Contains(appData, []byte(fragment)) {
			t.Fatalf("official activation result handling is missing %q", fragment)
		}
	}
	if bytes.Contains(appData, []byte(`success: "已切换到官方账号。新开 grok 会话生效。"`)) {
		t.Fatal("official activation must not use an unconditional success message")
	}
}

func TestProviderActivationAndRoutingDropdownContracts(t *testing.T) {
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`profile.is_active ? "disabled" : ""`,
		`customProviderSwitchWarning(routing.active_provider_id, profile)`,
		`officialProviderSwitchWarning(state.status?.active_id)`,
		`route.api_backend === "responses" && route.supports_backend_search === true`,
		`id === "routingWebSearch" ? webSearchRoutes : routes`,
		`const activeProviderID = state.routing?.active_provider_id || ""`,
	} {
		if !bytes.Contains(appData, []byte(fragment)) {
			t.Fatalf("provider activation/routing contract is missing %q", fragment)
		}
	}
	if bytes.Contains(appData, []byte("登录并启用")) {
		t.Fatal("logged-out official card still claims login immediately enables official routing")
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

func TestProfilePresetFeatureIsAbsent(t *testing.T) {
	htmlData, err := assets.ReadFile("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	appData, err := assets.ReadFile("ui/app.js")
	if err != nil {
		t.Fatal(err)
	}
	combined := append(append([]byte{}, htmlData...), appData...)
	for _, stale := range []string{
		`id="templateSelect"`, "类型模板", "TEMPLATES", "TEMPLATE_KEYS", "applyTemplate", "templateValue",
	} {
		if bytes.Contains(combined, []byte(stale)) {
			t.Fatalf("profile preset feature remains in embedded UI: %s", stale)
		}
	}
	if !bytes.Contains(htmlData, []byte(`id="upstreamFormat"`)) {
		t.Fatal("profile protocol-format selector must remain available")
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
	for _, expected := range []string{"snapshot.official_models", "snapshot.official_logged_in", "snapshot.active_provider_id", "routingProvider"} {
		if !bytes.Contains(appData, []byte(expected)) {
			t.Fatalf("official Grok routing UI behavior missing: %s", expected)
		}
	}
	for _, stale := range []string{`$("webSearchModel")`, `$("subagentsExploreModel")`, `$("subagentsPlanModel")`, `supported.length ? supported : ["low", "medium", "high"]`} {
		if bytes.Contains(appData, []byte(stale)) {
			t.Fatalf("profile editor still reads removed or synthetic routing control %s", stale)
		}
	}
	for _, expected := range []string{`route?.supports_reasoning_effort`, `const options = supported.length ? supported : ["none"]`, `select.disabled = supported.length === 0`, `default_reasoning_effort: $("routingReasoningEffort").value || "none"`} {
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
