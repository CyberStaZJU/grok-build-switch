const state = {
  profiles: [],
  settings: null,
  status: null,
  lanAccess: null,
  availableModels: [],
  view: "home",
  layout: localStorage.getItem("gs_layout") || "card",
  search: "",
  draggedProviderKey: "",
  subscriptionProxy: null,
  routing: null,
  collaboration: null,
  collaborationSpec: null,
  collaborationSpecIssue: "Collaboration workflow data-flow contract is unavailable; federated mode is disabled",
  collaborationPreview: null,
};

const OFFICIAL_PROVIDER_KEY = "official";

const $ = (id) => document.getElementById(id);

function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[ch]));
}

let toastTimer = null;
let refreshTimer = null;
let subscriptionLoginPollTimer = null;
let subscriptionLoginBusy = false;
const REASONING_EFFORTS = ["none", "minimal", "low", "medium", "high", "xhigh", "max"];
const REASONING_EFFORT_LABELS = {
  none: "禁用推理 (none)", minimal: "最小 (minimal)", low: "低 (low)", medium: "中 (medium)", high: "高 (high)", xhigh: "超高 (xhigh)", max: "最大 (max，仅部分模型)",
};

function normalizeReasoningEffort(effort) {
  return REASONING_EFFORTS.includes(effort) ? effort : "none";
}

function newProfileDraft() {
  return {
    upstream_format: "openai_responses",
    default_reasoning_effort: "none",
    models: [],
    available_models: [],
  };
}

let csrfTokenPromise = null;

async function csrfToken({ refresh = false } = {}) {
  if (refresh) csrfTokenPromise = null;
  if (!csrfTokenPromise) {
    const pending = fetch("/api/csrf").then(async (res) => {
      if (!res.ok) throw new Error("无法获取安全令牌");
      const token = String((await res.json()).token || "").trim();
      if (!token) throw new Error("服务器返回了空安全令牌");
      return token;
    });
    csrfTokenPromise = pending;
    try {
      await pending;
    } catch (err) {
      if (csrfTokenPromise === pending) csrfTokenPromise = null;
      throw err;
    }
  }
  return csrfTokenPromise;
}

function csrfRejected(res, data) {
  if (res.status !== 403) return false;
  const code = String(data?.code || "").toLowerCase();
  if (code === "csrf" || code.startsWith("csrf_") || code.endsWith("_csrf") || code.includes("csrf_token")) return true;
  return String(data?.error || "").toLowerCase().includes("csrf");
}

async function api(path, options = {}) {
  const method = String(options.method || "GET").toUpperCase();
  const needsCSRF = !["GET", "HEAD", "OPTIONS"].includes(method) && path.startsWith("/api/");
  for (let attempt = 0; attempt < 2; attempt++) {
    const headers = { "Content-Type": "application/json", ...(options.headers || {}) };
    if (needsCSRF) {
      headers["X-Grok-Switch-CSRF"] = await csrfToken({ refresh: attempt === 1 });
    }
    const res = await fetch(path, {
      ...options,
      headers,
    });
    const data = await res.json().catch(() => ({}));
    if (res.ok) return data;
    if (!needsCSRF || attempt > 0 || !csrfRejected(res, data)) {
      const error = new Error(data.error || res.statusText || "请求失败");
      error.code = data.code || "";
      error.status = res.status;
      error.data = data;
      throw error;
    }
  }
}

function formatTokenCount(n) {
  const v = Number(n) || 0;
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(2)}M`;
  if (v >= 10_000) return `${(v / 1000).toFixed(1)}k`;
  if (v >= 1000) return `${(v / 1000).toFixed(2)}k`;
  return String(v);
}

function formatHitRate(rate) {
  if (rate == null || Number.isNaN(Number(rate))) return "—";
  return `${(Number(rate) * 100).toFixed(1)}%`;
}

function cacheTableHTML(headers, rows) {
  if (!rows.length) return `<p class="muted tiny">暂无数据</p>`;
  const head = headers.map((heading) => `<th>${escapeHtml(heading)}</th>`).join("");
  const body = rows.map((columns) => `<tr>${columns.map((cell) => `<td>${cell}</td>`).join("")}</tr>`).join("");
  return `<table class="cacheDataTable"><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table>`;
}

function renderDrift() {
  const banner = $("driftBanner");
  if (!banner) return;
  const configMismatch = state.status?.config_matches_routing === false;
  const repairRequired = state.status?.active_routing?.repair_required === true;
  const drifted = configMismatch || repairRequired;
  banner.hidden = !drifted;
  banner.style.display = drifted ? "" : "none";
  if (!drifted) return;

  const title = $("driftTitle");
  const detail = $("driftDetail");
  if (configMismatch && repairRequired) {
    if (title) title.textContent = "配置与保存的模型路由都需要修复";
    if (detail) detail.textContent = "config.toml 的路由托管字段不匹配，且保存的模型引用已过期；重新应用会修复引用并保留无关 TOML 设置。";
  } else if (repairRequired) {
    if (title) title.textContent = "保存的模型路由需要修复";
    if (detail) detail.textContent = "部分模型引用已过期；重新应用会按当前供应商目录修复路由记录，并保留无关 TOML 设置。";
  } else {
    if (title) title.textContent = "配置与当前模型路由不一致";
    if (detail) detail.textContent = "config.toml 的路由托管字段与当前模型路由不匹配；重新应用会保留无关 TOML 设置。";
  }
}

async function reapplyRouting() {
  const confirmed = await customConfirm("将重新生成 Switch 托管的模型定义和 default、web_search、explore、plan 路由，并持久化必要的模型引用修复；其他 TOML 设置将保留。确认继续？", { okLabel: "确认重新应用" });
  if (!confirmed) return false;
  await api("/api/routing/reapply", { method: "POST" });
  return true;
}

async function deleteSSHFiles(connID, paths) {
  await api(`/api/ssh/files?conn_id=${encodeURIComponent(connID)}`, {
    method: "DELETE",
    body: JSON.stringify({ paths }),
  });
}

function modelSupportsBackendSearch(model = {}) {
  return model.supports_backend_search ?? false;
}

// Simple glob matching for SSH filename filters.
function minimatch(name, pattern) {
  const escaped = pattern
    .replace(/[.+^${}()|[\]\\/]/g, "\\$&")
    .replace(/\*/g, ".*")
    .replace(/\?/g, ".");
  return new RegExp(`^${escaped}$`, "i").test(name);
}

async function loadCacheStats() {
  const hours = Number($("cacheStatsHours")?.value || 24);
  const data = await api(`/api/cache-stats?hours=${encodeURIComponent(hours)}`);
  const overall = data.overall || {};
  if ($("cacheHitRate")) $("cacheHitRate").textContent = formatHitRate(overall.hit_rate);
  if ($("cacheTurns")) $("cacheTurns").textContent = String(overall.turns || 0);
  if ($("cachePromptTokens")) $("cachePromptTokens").textContent = formatTokenCount(overall.prompt_tokens);
  if ($("cacheCachedTokens")) $("cacheCachedTokens").textContent = formatTokenCount(overall.cached_prompt_tokens);
  if ($("cacheCompletionTokens")) $("cacheCompletionTokens").textContent = formatTokenCount(overall.completion_tokens);
  if ($("cacheReasoningTokens")) $("cacheReasoningTokens").textContent = formatTokenCount(overall.reasoning_tokens);
  if ($("cacheStatsHint")) {
    if (!data.log_exists) {
      $("cacheStatsHint").textContent = "未找到 Grok 日志 unified.jsonl。运行 Grok CLI 后会自动生成。";
    } else if (!(overall.turns > 0)) {
      $("cacheStatsHint").textContent = `已扫描日志，近 ${hours} 小时暂无推理事件。`;
    } else {
      $("cacheStatsHint").textContent = `统计窗口 ${hours}h · 事件 ${data.scanned_events || overall.turns} · 命中率 = cached_prompt_tokens / prompt_tokens · 模型按会话当前模型近似归属`;
    }
  }
  if ($("cacheByModel")) {
    const rows = (data.by_model || []).map((row) => [
      escapeHtml(row.model || "—"),
      formatHitRate(row.hit_rate),
      String(row.turns || 0),
      formatTokenCount(row.prompt_tokens),
      formatTokenCount(row.cached_prompt_tokens),
      formatTokenCount(row.completion_tokens),
      formatTokenCount(row.reasoning_tokens),
    ]);
    $("cacheByModel").innerHTML = cacheTableHTML(["模型", "命中率", "次数", "Prompt", "Cached", "Completion", "Reasoning"], rows);
  }
  if ($("cacheRecent")) {
    const rows = (data.recent || []).map((row) => {
      const ts = row.ts ? new Date(row.ts).toLocaleString() : "—";
      const sid = row.session_id ? String(row.session_id).slice(0, 8) : "—";
      return [
        escapeHtml(ts),
        escapeHtml(row.model || "—"),
        escapeHtml(sid),
        formatHitRate(row.hit_rate),
        formatTokenCount(row.prompt_tokens),
        formatTokenCount(row.completion_tokens),
        formatTokenCount(row.reasoning_tokens),
      ];
    });
    $("cacheRecent").innerHTML = cacheTableHTML(["时间", "模型", "会话", "命中率", "Prompt", "Completion", "Reasoning"], rows);
  }
  return data;
}

// Custom prompt dialog (window.prompt is unreliable in Wails WebView2)
function customPrompt(message, defaultValue) {
  return new Promise((resolve) => {
    const dialog = $("promptDialog");
    const input = $("promptInput");
    const ok = $("promptOk");
    const cancel = $("promptCancel");
    $("promptLabel").textContent = message;
    input.value = defaultValue || "";
    let settled = false;
    const cleanup = () => {
      if (dialog.open) dialog.close();
      input.onkeydown = null;
      ok.onclick = null;
      cancel.onclick = null;
      dialog.oncancel = null;
    };
    const finish = (value) => {
      if (settled) return;
      settled = true;
      cleanup();
      resolve(value);
    };
    input.onkeydown = (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        finish(input.value);
      }
    };
    ok.onclick = (event) => { event.preventDefault(); finish(input.value); };
    cancel.onclick = (event) => { event.preventDefault(); finish(null); };
    dialog.oncancel = (event) => {
      event.preventDefault();
      finish(null);
    };
    dialog.showModal();
    input.focus();
    input.select();
  });
}

function customProviderSwitchWarning(currentProviderID, targetProvider) {
  if (!currentProviderID || currentProviderID === targetProvider.id || currentProviderID === OFFICIAL_PROVIDER_KEY) return "";
  return `当前启用的是另一个自定义供应商。切换到「${targetProvider.name}」会立即改写 default、web_search、Explore 和 Plan；旧会话固定的自定义模型别名仍会保留。是否继续？`;
}

function officialProviderSwitchWarning(currentProviderID) {
  if (!currentProviderID || currentProviderID === OFFICIAL_PROVIDER_KEY) return "";
  return "切换到官方账号会立即移除 config.toml 中全部自定义模型定义、自定义端点和认证。切回自定义供应商时会从 Profile 重建目录，但当前自定义路由会被官方路由替换。是否继续？";
}

function capableWebSearchRoutes(routes, official = false) {
  return official ? routes : routes.filter((route) => route.api_backend === "responses" && route.supports_backend_search === true);
}

const COLLABORATION_EFFORTS = REASONING_EFFORTS.filter((effort) => effort !== "none");
const COLLABORATION_SPEED_STANDARD = "standard";
const COLLABORATION_SPEED_FAST = "fast";
const COLLABORATION_ROLE_DEFS = [
  { key: "MainCoordinator", requestKey: "main_coordinator", preferredRoutingKey: "default", dataScope: "repository_plus_minimized_prior_work_products" },
  { key: "TaskDecomposition", requestKey: "task_decomposition", preferredRoutingKey: "plan", dataScope: "repository_only" },
  { key: "MainImplementation", requestKey: "main_implementation", preferredRoutingKey: "default", dataScope: "repository_plus_minimized_prior_work_products" },
  { key: "DifficultReview", requestKey: "difficult_implementation_review", preferredRoutingKey: "", dataScope: "repository_plus_minimized_prior_work_products" },
];
const COLLABORATION_SPEC_SCHEMA_VERSION = 1;
const COLLABORATION_POLICY_VERSION = 4;
const COLLABORATION_SPEC_UNAVAILABLE = "Collaboration workflow data-flow contract is unavailable; federated mode is disabled";
const COLLABORATION_WORKFLOW_PATHS_V1 = [
  { tier: "economy", budget: 1, roles: ["main_coordinator"], data_flows: [] },
  { tier: "focused-evidence", budget: 2, roles: ["task_decomposition", "main_coordinator"], data_flows: [{ from: "task_decomposition", to: "main_coordinator" }] },
  { tier: "focused-build", budget: 2, roles: ["main_implementation", "main_coordinator"], data_flows: [{ from: "main_implementation", to: "main_coordinator" }] },
  { tier: "assurance", budget: 3, roles: ["task_decomposition", "main_implementation", "main_coordinator"], data_flows: [{ from: "task_decomposition", to: "main_implementation" }, { from: "task_decomposition", to: "main_coordinator" }, { from: "main_implementation", to: "main_coordinator" }] },
  { tier: "critical", budget: 4, roles: ["task_decomposition", "main_implementation", "difficult_implementation_review", "main_coordinator"], data_flows: [{ from: "task_decomposition", to: "main_implementation" }, { from: "task_decomposition", to: "difficult_implementation_review" }, { from: "main_implementation", to: "difficult_implementation_review" }, { from: "task_decomposition", to: "main_coordinator" }, { from: "main_implementation", to: "main_coordinator" }, { from: "difficult_implementation_review", to: "main_coordinator" }] },
];

function plainJSONObject(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === null || Object.getPrototypeOf(prototype) === null;
}

function exactObjectFields(value, fields) {
  if (!plainJSONObject(value)) return false;
  const keys = Object.keys(value).sort();
  const expected = [...fields].sort();
  return keys.length === expected.length && keys.every((key, index) => key === expected[index]);
}

function validateCollaborationSpec(raw) {
  const fail = (detail) => { throw new Error(`${COLLABORATION_SPEC_UNAVAILABLE}: ${detail}`); };
  if (!exactObjectFields(raw, ["schema_version", "collaboration_policy_version", "workflow_paths"])) fail("response must contain only the version fields and workflow_paths");
  if (!Number.isInteger(raw.schema_version) || raw.schema_version !== COLLABORATION_SPEC_SCHEMA_VERSION) fail("incompatible schema_version");
  if (!Number.isInteger(raw.collaboration_policy_version) || raw.collaboration_policy_version !== COLLABORATION_POLICY_VERSION) fail("incompatible collaboration_policy_version");
  if (!Array.isArray(raw.workflow_paths) || raw.workflow_paths.length !== COLLABORATION_WORKFLOW_PATHS_V1.length) fail("workflow_paths must contain exactly five paths");
  const knownRoles = new Set(COLLABORATION_ROLE_DEFS.map((role) => role.requestKey));
  const seenTiers = new Set();
  const paths = raw.workflow_paths.map((path, pathIndex) => {
    const expected = COLLABORATION_WORKFLOW_PATHS_V1[pathIndex];
    if (!exactObjectFields(path, ["tier", "budget", "roles", "data_flows"])) fail(`invalid fields for workflow path ${pathIndex}`);
    if (typeof path.tier !== "string" || path.tier !== expected.tier || seenTiers.has(path.tier)) fail(`invalid or duplicate tier at workflow path ${pathIndex}`);
    seenTiers.add(path.tier);
    if (!Number.isInteger(path.budget) || path.budget !== expected.budget) fail(`wrong budget for ${expected.tier}`);
    if (!Array.isArray(path.roles) || path.roles.length !== expected.roles.length) fail(`wrong role sequence for ${expected.tier}`);
    const seenRoles = new Set();
    const roles = path.roles.map((role, roleIndex) => {
      if (typeof role !== "string" || !knownRoles.has(role) || role !== expected.roles[roleIndex] || seenRoles.has(role)) fail(`invalid role sequence for ${expected.tier}`);
      seenRoles.add(role);
      return role;
    });
    if (!Array.isArray(path.data_flows) || path.data_flows.length !== expected.data_flows.length) fail(`incomplete data_flows for ${expected.tier}`);
    const rolePositions = new Map(roles.map((role, index) => [role, index]));
    const seenEdges = new Set();
    const dataFlows = path.data_flows.map((edge, edgeIndex) => {
      const expectedEdge = expected.data_flows[edgeIndex];
      if (!exactObjectFields(edge, ["from", "to"]) || typeof edge.from !== "string" || typeof edge.to !== "string") fail(`invalid edge for ${expected.tier}`);
      if (!knownRoles.has(edge.from) || !knownRoles.has(edge.to) || !rolePositions.has(edge.from) || !rolePositions.has(edge.to) || rolePositions.get(edge.from) >= rolePositions.get(edge.to)) fail(`invalid edge endpoints/order for ${expected.tier}`);
      const edgeKey = `${edge.from}\u0000${edge.to}`;
      if (seenEdges.has(edgeKey)) fail(`duplicate edge for ${expected.tier}`);
      seenEdges.add(edgeKey);
      if (edge.from !== expectedEdge.from || edge.to !== expectedEdge.to) fail(`extra, missing, or reordered edge for ${expected.tier}`);
      return Object.freeze({ from: edge.from, to: edge.to });
    });
    return Object.freeze({ tier: path.tier, budget: path.budget, roles: Object.freeze(roles), data_flows: Object.freeze(dataFlows) });
  });
  return Object.freeze({ schema_version: raw.schema_version, collaboration_policy_version: raw.collaboration_policy_version, workflow_paths: Object.freeze(paths) });
}

let collaborationSpecRequestToken = null;

function commitCollaborationSpec(raw, fetchError = null) {
  state.collaborationSpec = null;
  state.collaborationSpecIssue = COLLABORATION_SPEC_UNAVAILABLE;
  if (fetchError) return null;
  try {
    const validated = validateCollaborationSpec(raw);
    state.collaborationSpec = validated;
    state.collaborationSpecIssue = "";
    return validated;
  } catch (error) {
    state.collaborationSpecIssue = String(error?.message || COLLABORATION_SPEC_UNAVAILABLE).replace(/[\r\n\t]+/g, " ").slice(0, 240);
    return null;
  }
}

function setCollaborationSpec(raw, fetchError = null) {
  collaborationSpecRequestToken = {};
  return commitCollaborationSpec(raw, fetchError);
}

async function loadCollaborationSpec() {
  const requestToken = {};
  collaborationSpecRequestToken = requestToken;
  try {
    const raw = await api("/api/collaboration/spec");
    if (collaborationSpecRequestToken !== requestToken) return null;
    return commitCollaborationSpec(raw);
  } catch (error) {
    if (collaborationSpecRequestToken !== requestToken) return null;
    return commitCollaborationSpec(null, error);
  }
}

function trustedCollaborationEfforts(route = {}) {
  const source = String(route.reasoning_efforts_source || "").trim().toLowerCase();
  if (route.supports_reasoning_effort !== true || (source !== "declared" && source !== "probe")) return [];
  const supported = new Set((Array.isArray(route.reasoning_efforts) ? route.reasoning_efforts : [])
    .map((effort) => String(effort || "").trim().toLowerCase()));
  return COLLABORATION_EFFORTS.filter((effort) => supported.has(effort));
}

function collaborationRouteSupportsEffort(route = {}, effort = "") {
  return trustedCollaborationEfforts(route).includes(String(effort || "").trim().toLowerCase());
}

function collaborationRouteSupportsMax(route = {}) {
  return collaborationRouteSupportsEffort(route, "max");
}

function collaborationCapabilityLabel(route = {}) {
  const source = String(route.reasoning_efforts_source || "unknown").trim().toLowerCase() || "unknown";
  const efforts = trustedCollaborationEfforts(route);
  if (efforts.length) return `${efforts.join(" / ")} · ${source}`;
  if (route.supports_reasoning_effort !== true) return "未声明推理能力";
  return `无可信推理档位 · ${source}`;
}

function collaborationRoutes(snapshot = {}, providerID = snapshot.active_provider_id || "") {
  if (!providerID || providerID === OFFICIAL_PROVIDER_KEY) return [];
  return (snapshot.model_routes || []).filter((route) => route.provider_id === providerID);
}

function collaborationStandardRoutes(routes = []) {
  return routes.filter((route) => route.speed_tier === COLLABORATION_SPEED_STANDARD && route.standard_anchor === route.id);
}

function resolveCollaborationRoute(routes = [], standardAnchorID = "", speedTier = COLLABORATION_SPEED_STANDARD) {
  const anchorID = String(standardAnchorID || "").trim();
  const tier = String(speedTier || "").trim().toLowerCase();
  const standard = routes.find((route) => route.id === anchorID);
  if (!standard || standard.speed_tier !== COLLABORATION_SPEED_STANDARD || standard.standard_anchor !== standard.id) return null;
  if (tier === COLLABORATION_SPEED_STANDARD) return standard;
  if (tier !== COLLABORATION_SPEED_FAST) return null;
  const matches = routes.filter((route) => route.provider_id === standard.provider_id
    && route.speed_tier === COLLABORATION_SPEED_FAST
    && route.standard_anchor === standard.id);
  return matches.length === 1 ? matches[0] : null;
}

function collaborationFastRouteState(routes = [], standardAnchorID = "") {
  const standard = resolveCollaborationRoute(routes, standardAnchorID, COLLABORATION_SPEED_STANDARD);
  if (!standard) return { available: false, ambiguous: false, route: null };
  const matches = routes.filter((route) => route.provider_id === standard.provider_id
    && route.speed_tier === COLLABORATION_SPEED_FAST
    && route.standard_anchor === standard.id);
  return { available: matches.length === 1, ambiguous: matches.length > 1, route: matches.length === 1 ? matches[0] : null };
}

function collaborationAnchorCapable(routes = [], standard = {}) {
  const standardRoute = resolveCollaborationRoute(routes, standard.id, COLLABORATION_SPEED_STANDARD);
  const fastRoute = resolveCollaborationRoute(routes, standard.id, COLLABORATION_SPEED_FAST);
  return trustedCollaborationEfforts(standardRoute || {}).length > 0 || trustedCollaborationEfforts(fastRoute || {}).length > 0;
}

function preferredCollaborationRoute(routes, preferredID = "") {
  const standards = collaborationStandardRoutes(routes);
  const preferred = routes.find((route) => route.id === preferredID);
  const preferredAnchor = preferred?.speed_tier === COLLABORATION_SPEED_FAST ? preferred.standard_anchor : preferred?.id;
  const eligible = standards.filter((route) => collaborationAnchorCapable(routes, route));
  return eligible.find((route) => route.id === preferredAnchor)?.id || eligible[0]?.id || "";
}

function preferredCollaborationEffort(route = {}) {
  const efforts = trustedCollaborationEfforts(route);
  for (const effort of ["max", "xhigh", "high", "medium", "low", "minimal"]) {
    if (efforts.includes(effort)) return effort;
  }
  return "";
}

function collaborationFederationConsent(roles = {}) {
  if (!state.collaborationSpec) throw new Error(state.collaborationSpecIssue || COLLABORATION_SPEC_UNAVAILABLE);
  const providerIDs = [...new Set(Object.values(roles).map((role) => role.provider_id).filter(Boolean))].sort();
  const tierHandoffEdges = state.collaborationSpec.workflow_paths.map((path) => ({
    tier: path.tier,
    edges: path.data_flows.filter((edge) => roles[edge.from]?.provider_id && roles[edge.to]?.provider_id && roles[edge.from].provider_id !== roles[edge.to].provider_id),
  }));
  return { basis: "all_workflow_tiers_v1", provider_ids: providerIDs, handoff_policy: "bounded_work_products", tier_handoff_edges: tierHandoffEdges, never_transfer: ["credentials", "secrets", "full_transcripts"] };
}

function collaborationRequestFromValues(snapshot = {}, values = {}) {
  const mode = values.mode || "single_provider";
  const coordinatorProvider = values.mainCoordinatorProvider || snapshot.active_provider_id || "";
  const role = (prefix, dataScope) => ({
    provider_id: values[`${prefix}Provider`] || coordinatorProvider,
    model: values[`${prefix}Model`] || "",
    speed_tier: values[`${prefix}Speed`] || "",
    reasoning_effort: values[`${prefix}Effort`] || "",
    data_scope: dataScope,
  });
  const roles = Object.fromEntries(COLLABORATION_ROLE_DEFS.map((definition) => {
    const prefix = definition.key[0].toLowerCase() + definition.key.slice(1);
    return [definition.requestKey, role(prefix, definition.dataScope)];
  }));
  const request = { version: 4, enabled: true, mode, provider_id: coordinatorProvider, roles, default_tier: values.defaultTier || "adaptive" };
  if (mode === "federated" && values.federationConsent === true) request.federation_consent = collaborationFederationConsent(roles);
  return request;
}

function collaborationRequestKey(request) {
  return JSON.stringify(request || {});
}

function collaborationFormValues() {
  const values = { defaultTier: $("collaborationTier")?.value || "adaptive", mode: $("collaborationMode")?.value || "single_provider", federationConsent: $("collaborationFederationConsent")?.checked === true };
  for (const role of COLLABORATION_ROLE_DEFS) {
    const prefix = role.key[0].toLowerCase() + role.key.slice(1);
    values[`${prefix}Provider`] = $(`collaboration${role.key}Provider`)?.value || "";
    values[`${prefix}Model`] = $(`collaboration${role.key}Model`)?.value || "";
    values[`${prefix}Speed`] = $(`collaboration${role.key}Speed`)?.value || "";
    values[`${prefix}Effort`] = $(`collaboration${role.key}Effort`)?.value || "";
  }
  return values;
}

function currentCollaborationRequest() {
  try {
    return collaborationRequestFromValues(state.routing || {}, collaborationFormValues());
  } catch (error) {
    return null;
  }
}

function collaborationSelectionValid() {
  const request = currentCollaborationRequest();
  if (!request || !request.provider_id || request.provider_id === OFFICIAL_PROVIDER_KEY) return false;
  if (request.mode === "federated" && !request.federation_consent) return false;
  return Object.values(request.roles || {}).every((assignment) => {
    const routes = collaborationRoutes(state.routing || {}, assignment.provider_id);
    if (!assignment?.model || !assignment?.speed_tier || !assignment?.reasoning_effort) return false;
    const route = resolveCollaborationRoute(routes, assignment.model, assignment.speed_tier);
    return !!route && collaborationRouteSupportsEffort(route, assignment.reasoning_effort);
  });
}

function invalidateCollaborationPreview() {
  state.collaborationPreview = null;
  const details = $("collaborationPreview");
  if (details) {
    details.hidden = true;
    details.open = false;
  }
  if ($("applyCollaborationBtn")) $("applyCollaborationBtn").disabled = true;
}

function collaborationOptionHTML(route, routes = []) {
  const capable = collaborationAnchorCapable(routes, route);
  const model = route.model || route.profile_model || "";
  const fast = collaborationFastRouteState(routes, route.id);
  const speedLabel = fast.available ? "Standard / Fast" : (fast.ambiguous ? "Fast 配对歧义" : "仅 Standard");
  return `<option value="${escapeHtml(route.id)}" ${capable ? "" : "disabled"}>${escapeHtml(route.name)} — ${escapeHtml(model)} · ${escapeHtml(speedLabel)}</option>`;
}

function collaborationModelOptionsHTML(routes, requestedModel = "") {
  const requested = String(requestedModel || "").trim();
  const standards = collaborationStandardRoutes(routes);
  const missing = requested && !standards.some((route) => route.id === requested)
    ? `<option value="${escapeHtml(requested)}" disabled>${escapeHtml(requested)}（已保存 Standard 路由当前不可用）</option>`
    : "";
  return `${missing}<option value="">（请选择可信 Standard 模型）</option>${standards.map((route) => collaborationOptionHTML(route, routes)).join("")}`;
}

function renderCollaborationStatus(status = {}, localIssues = []) {
  const badge = $("collaborationBadge");
  const policy = status.policy || {};
  let label = "未配置";
  let badgeState = "stopped";
  if (status.unavailable) {
    label = "仅限本机";
    badgeState = "unhealthy";
  } else if (status.configured && !policy.enabled) {
    label = "已停用";
  } else if (status.configured && policy.enabled && status.valid) {
    label = "已启用";
    badgeState = "running";
  } else if (status.configured && status.drifted) {
    label = "文件漂移";
    badgeState = "error";
  } else if (status.configured) {
    label = "配置失效";
    badgeState = "unhealthy";
  }
  if (badge) {
    badge.textContent = label;
    badge.dataset.state = badgeState;
  }
  const issues = [...(status.issues || []), ...localIssues].filter(Boolean);
  const issueBox = $("collaborationIssues");
  if (issueBox) {
    issueBox.hidden = issues.length === 0;
    issueBox.innerHTML = issues.length
      ? `<div><strong>需要处理</strong><span>${issues.map(escapeHtml).join("<br>")}</span></div>`
      : "";
  }
  if ($("disableCollaborationBtn")) $("disableCollaborationBtn").disabled = !(status.configured && policy.enabled);
}

function populateCollaborationSpeedOptions(role, routes, standardAnchorID, requestedSpeed = "") {
  const select = $(`collaboration${role.key}Speed`);
  if (!select) return "";
  const selected = String(requestedSpeed || COLLABORATION_SPEED_STANDARD).trim().toLowerCase();
  const standard = resolveCollaborationRoute(routes, standardAnchorID, COLLABORATION_SPEED_STANDARD);
  const fast = collaborationFastRouteState(routes, standardAnchorID);
  const options = [];
  if (standard) options.push('<option value="standard">Standard · 标准额度</option>');
  if (fast.available) options.push('<option value="fast">Fast · priority（更高 credits）</option>');
  if (selected === COLLABORATION_SPEED_FAST && !fast.available) {
    const reason = fast.ambiguous ? "配对歧义" : "当前不可用";
    options.push(`<option value="fast" disabled>Fast（已保存，但${reason}）</option>`);
  }
  select.innerHTML = options.length ? options.join("") : '<option value="">无可信速度档</option>';
  select.disabled = !standard && !(selected === COLLABORATION_SPEED_FAST && !fast.available);
  select.value = selected;
  return select.value;
}

function populateCollaborationEffortOptions(role, route, requestedEffort = "") {
  const select = $(`collaboration${role.key}Effort`);
  if (!select) return "";
  const efforts = trustedCollaborationEfforts(route);
  const selected = String(requestedEffort || "").trim().toLowerCase();
  const options = efforts.map((effort) => `<option value="${escapeHtml(effort)}">${escapeHtml(REASONING_EFFORT_LABELS[effort] || effort)}</option>`);
  if (selected && !efforts.includes(selected)) {
    options.unshift(`<option value="${escapeHtml(selected)}" disabled>${escapeHtml(REASONING_EFFORT_LABELS[selected] || selected)}（当前模型不再支持）</option>`);
  }
  select.innerHTML = options.length ? options.join("") : '<option value="">无可信推理档位</option>';
  // A saved-but-stale effort must remain visible and selected so the UI cannot
  // silently manufacture a different valid request. Keep the selector enabled
  // when that disabled sentinel exists, allowing the user to choose a trusted
  // replacement explicitly.
  select.disabled = efforts.length === 0 && !selected;
  const next = selected || preferredCollaborationEffort(route);
  select.value = next;
  return select.value;
}

function updateCollaborationRoleControls(role, routes, requestedEffort = "") {
  const modelSelect = $(`collaboration${role.key}Model`);
  const speedSelect = $(`collaboration${role.key}Speed`);
  const effortSelect = $(`collaboration${role.key}Effort`);
  const output = $(`collaboration${role.key}Capability`);
  if (!modelSelect || !speedSelect || !effortSelect || !output) return;
  const route = resolveCollaborationRoute(routes, modelSelect.value, speedSelect.value);
  populateCollaborationEffortOptions(role, route || {}, requestedEffort || effortSelect.value);
  const valid = !!route && collaborationRouteSupportsEffort(route, effortSelect.value);
  output.textContent = route
    ? `${speedSelect.value === COLLABORATION_SPEED_FAST ? "Fast" : "Standard"} → ${route.name} · ${collaborationCapabilityLabel(route)}${valid ? ` · 已选 ${effortSelect.value}` : " · 请选择受支持档位"}`
    : (speedSelect.value === COLLABORATION_SPEED_FAST ? "已保存 Fast 当前不可解析；不会回退到 Standard" : "请选择可信 Standard 模型");
  output.className = `muted tiny ${valid ? "ok" : "warn"}`;
}

const COLLABORATION_LAUNCH_TIERS = {
  adaptive: { tier: "economy", label: "Economy", budget: 1 },
  economy: { tier: "economy", label: "Economy", budget: 1 },
  "focused-evidence": { tier: "focused-evidence", label: "Focused Evidence", budget: 2 },
  "focused-build": { tier: "focused-build", label: "Focused Build", budget: 2 },
  assurance: { tier: "assurance", label: "Assurance", budget: 3 },
  critical: { tier: "critical", label: "Critical", budget: 4 },
};

function collaborationLaunchParameters(selectedTier = $("collaborationTier")?.value || "adaptive", objective = $("collaborationLaunchObjective")?.value || "") {
  const selected = COLLABORATION_LAUNCH_TIERS[selectedTier] || COLLABORATION_LAUNCH_TIERS.adaptive;
  const normalizedObjective = String(objective || "").trim();
  return {
    ...selected,
    objective: normalizedObjective,
    instruction: `使用 ${selected.label} 运行 gbs-max-collab，目标是：${normalizedObjective || "<填写任务目标>"}`,
  };
}

function updateCollaborationLaunchGuide() {
  const launch = collaborationLaunchParameters();
  if ($("collaborationLaunchBudget")) $("collaborationLaunchBudget").textContent = `budget ${launch.budget}`;
  if ($("collaborationLaunchInstruction")) $("collaborationLaunchInstruction").textContent = launch.instruction;
}

function updateCollaborationTierHint() {
  const tier = $("collaborationTier")?.value || "adaptive";
  const hint = $("collaborationTierHint");
  if (hint) {
    hint.textContent = tier === "critical"
      ? "Critical 仍不会由 Switch 自动运行；请使用下方复制式启动指令，由 Grok 以 agent_budget=4 调用 workflow。"
      : "Adaptive 映射为 Economy-first 启动建议；请使用下方复制式指令，让 Grok 传入精确 agent_budget。";
  }
  updateCollaborationLaunchGuide();
}

function updateCollaborationCreditWarning() {
  const request = currentCollaborationRequest();
  const fastRoles = COLLABORATION_ROLE_DEFS.filter((role) => request?.roles?.[role.requestKey]?.speed_tier === COLLABORATION_SPEED_FAST);
  const warning = $("collaborationCreditWarning");
  if (!warning) return;
  warning.hidden = fastRoles.length === 0;
  warning.textContent = fastRoles.length
    ? `已为 ${fastRoles.length} 个角色选择 Fast。Fast 会请求 priority service tier，通常更快但会消耗更多订阅 credits；不存在静默回退。`
    : "";
}

function renderCollaborationFederationDisclosure() {
  const disclosure = $("collaborationFederationDisclosure");
  const map = $("collaborationFederationMap");
  const mode = $("collaborationMode")?.value || "single_provider";
  if (disclosure) disclosure.hidden = mode !== "federated";
  if (!map || mode !== "federated") return;
  if (!state.collaborationSpec) {
    map.textContent = state.collaborationSpecIssue || COLLABORATION_SPEC_UNAVAILABLE;
    return;
  }
  let consent;
  try {
    consent = collaborationFederationConsent(collaborationRequestFromValues(state.routing || {}, collaborationFormValues()).roles);
  } catch (error) {
    map.textContent = state.collaborationSpecIssue || COLLABORATION_SPEC_UNAVAILABLE;
    return;
  }
  const lines = [`Providers: ${consent.provider_ids.join(", ") || "（未完整选择）"}`];
  for (const path of consent.tier_handoff_edges) {
    const edges = path.edges.length ? path.edges.map((edge) => `${edge.from} → ${edge.to}`).join("; ") : "无跨供应商边";
    lines.push(`${path.tier}: ${edges}`);
  }
  map.textContent = lines.join("\n");
}

function updateCollaborationControls() {
  const snapshot = state.routing || {};
  for (const role of COLLABORATION_ROLE_DEFS) {
    const providerSelect = $(`collaboration${role.key}Provider`);
    const dataScopeSelect = $(`collaboration${role.key}DataScope`);
    const modelSelect = $(`collaboration${role.key}Model`);
    const speedSelect = $(`collaboration${role.key}Speed`);
    const effortSelect = $(`collaboration${role.key}Effort`);
    const output = $(`collaboration${role.key}Capability`);
    if (!modelSelect || !speedSelect || !effortSelect || !output) continue;
    const roleRoutes = collaborationRoutes(snapshot, providerSelect?.value || snapshot.active_provider_id || "");
    const route = resolveCollaborationRoute(roleRoutes, modelSelect.value, speedSelect.value);
    const valid = !!route && collaborationRouteSupportsEffort(route, effortSelect.value);
    output.textContent = route
      ? `${speedSelect.value === COLLABORATION_SPEED_FAST ? "Fast" : "Standard"} → ${route.name} · ${collaborationCapabilityLabel(route)}${valid ? ` · 已选 ${effortSelect.value}` : " · 请选择受支持档位"}`
      : (speedSelect.value === COLLABORATION_SPEED_FAST ? "已保存 Fast 当前不可解析；不会回退到 Standard" : "请选择可信 Standard 模型");
    output.className = `muted tiny ${valid ? "ok" : "warn"}`;
  }
  updateCollaborationCreditWarning();
  updateCollaborationTierHint();
  const mode = $("collaborationMode")?.value || "single_provider";
  renderCollaborationFederationDisclosure();
  if ($("collaborationConsentField")) $("collaborationConsentField").hidden = mode !== "federated";
  if ($("collaborationFederationWarning")) $("collaborationFederationWarning").hidden = mode !== "federated";
  const federatedSpecAvailable = mode !== "federated" || !!state.collaborationSpec;
  const consentCheckbox = $("collaborationFederationConsent");
  if (consentCheckbox) consentCheckbox.disabled = mode === "federated" && !state.collaborationSpec;
  const previewButton = $("previewCollaborationBtn");
  if (previewButton) previewButton.disabled = !federatedSpecAvailable || !collaborationSelectionValid();
  const current = currentCollaborationRequest();
  const pending = state.collaborationPreview;
  if ($("applyCollaborationBtn")) {
    $("applyCollaborationBtn").disabled = !(federatedSpecAvailable
      && current
      && pending?.mode === "enable"
      && pending.preview?.fingerprint
      && collaborationRequestKey(pending.request) === collaborationRequestKey(current));
  }
  if ($("saveRoutingPolicyBtn")) $("saveRoutingPolicyBtn").disabled = !$("routingProvider")?.value;
  const policy = state.collaboration?.policy || {};
  if ($("disableCollaborationBtn")) {
    $("disableCollaborationBtn").disabled = !(state.collaboration?.configured && policy.enabled);
  }
}

function renderCollaboration(snapshot, status = {}) {
  state.collaboration = status;
  invalidateCollaborationPreview();
  const routes = snapshot.model_routes || [];
  const policy = status.policy || {};
  const activeProviderID = snapshot.active_provider_id || "";
  // Keep persisted assignments visible even when the active provider or model
  // catalog changed. Missing routes and stale efforts remain disabled invalid
  // choices until the user explicitly selects replacements and previews again.
  const savedRoles = status.configured ? (policy.roles || {}) : {};
  const activePolicy = snapshot.policy || {};
  const preferredModels = {
    MainCoordinator: activePolicy.default || "",
    TaskDecomposition: activePolicy.subagents?.plan || activePolicy.default || "",
    MainImplementation: activePolicy.default || "",
    DifficultReview: activePolicy.default || "",
  };

  for (const role of COLLABORATION_ROLE_DEFS) {
    const saved = savedRoles[role.requestKey] || {};
    const providerSelect = $(`collaboration${role.key}Provider`);
    const dataScopeSelect = $(`collaboration${role.key}DataScope`);
    const modelSelect = $(`collaboration${role.key}Model`);
    const speedSelect = $(`collaboration${role.key}Speed`);
    const effortSelect = $(`collaboration${role.key}Effort`);
    if (!modelSelect || !speedSelect || !effortSelect) continue;
    const savedProvider = saved.provider_id || policy.provider_id || activeProviderID;
    if (providerSelect) {
      const providers = snapshot.providers || [];
      const missing = savedProvider && !providers.some((p) => p.id === savedProvider) ? `<option value="${escapeHtml(savedProvider)}" disabled>${escapeHtml(savedProvider)}（已保存供应商当前不可用）</option>` : "";
      providerSelect.innerHTML = missing + providers.map((p) => `<option value="${escapeHtml(p.id)}">${escapeHtml(p.name || p.id)}</option>`).join("");
      providerSelect.value = savedProvider;
    }
    if (dataScopeSelect) {
      dataScopeSelect.value = role.dataScope;
      dataScopeSelect.disabled = true;
    }
    const roleRoutes = collaborationRoutes(snapshot, savedProvider);
    modelSelect.innerHTML = collaborationModelOptionsHTML(roleRoutes, saved.model || "");
    modelSelect.value = saved.model || preferredCollaborationRoute(roleRoutes, preferredModels[role.key]);
    populateCollaborationSpeedOptions(role, roleRoutes, modelSelect.value, saved.speed_tier || COLLABORATION_SPEED_STANDARD);
    const route = resolveCollaborationRoute(roleRoutes, modelSelect.value, speedSelect.value) || {};
    populateCollaborationEffortOptions(role, route, saved.reasoning_effort || "");
    if (providerSelect) providerSelect.onchange = () => {
      invalidateCollaborationPreview();
      const nextRoutes = collaborationRoutes(snapshot, providerSelect.value);
      modelSelect.innerHTML = collaborationModelOptionsHTML(nextRoutes, "");
      modelSelect.value = preferredCollaborationRoute(nextRoutes, "");
      populateCollaborationSpeedOptions(role, nextRoutes, modelSelect.value, COLLABORATION_SPEED_STANDARD);
      populateCollaborationEffortOptions(role, resolveCollaborationRoute(nextRoutes, modelSelect.value, speedSelect.value) || {}, "");
      updateCollaborationControls();
    };
    modelSelect.onchange = () => {
      invalidateCollaborationPreview();
      const nextRoutes = collaborationRoutes(snapshot, providerSelect?.value || savedProvider);
      populateCollaborationSpeedOptions(role, nextRoutes, modelSelect.value, COLLABORATION_SPEED_STANDARD);
      const selectedRoute = resolveCollaborationRoute(nextRoutes, modelSelect.value, speedSelect.value) || {};
      populateCollaborationEffortOptions(role, selectedRoute, "");
      updateCollaborationControls();
    };
    speedSelect.onchange = () => {
      invalidateCollaborationPreview();
      const nextRoutes = collaborationRoutes(snapshot, providerSelect?.value || savedProvider);
      const selectedRoute = resolveCollaborationRoute(nextRoutes, modelSelect.value, speedSelect.value) || {};
      populateCollaborationEffortOptions(role, selectedRoute, "");
      updateCollaborationControls();
    };
    effortSelect.onchange = () => {
      invalidateCollaborationPreview();
      updateCollaborationControls();
    };
  }
  const allowedTiers = ["adaptive", "economy", "focused-evidence", "focused-build", "assurance", "critical"];
  const tier = status.configured && allowedTiers.includes(policy.default_tier) ? policy.default_tier : "adaptive";
  if ($("collaborationTier")) {
    $("collaborationTier").value = tier;
    $("collaborationTier").onchange = () => {
      invalidateCollaborationPreview();
      updateCollaborationControls();
    };
  }
  if ($("collaborationLaunchObjective")) {
    $("collaborationLaunchObjective").oninput = updateCollaborationLaunchGuide;
  }
  if ($("collaborationMode")) { $("collaborationMode").value = policy.mode || "single_provider"; $("collaborationMode").onchange = () => { invalidateCollaborationPreview(); updateCollaborationControls(); }; }
  if ($("collaborationFederationConsent")) { $("collaborationFederationConsent").checked = false; $("collaborationFederationConsent").disabled = false; $("collaborationFederationConsent").onchange = () => { invalidateCollaborationPreview(); updateCollaborationControls(); }; }
  const mode = $("collaborationMode")?.value || "single_provider";
  if ($("collaborationConsentField")) $("collaborationConsentField").hidden = mode !== "federated";
  if ($("collaborationFederationWarning")) { $("collaborationFederationWarning").hidden = mode !== "federated"; $("collaborationFederationWarning").textContent = "Federated 会在预览中列出精确跨供应商边；当前 active-provider/config 架构若不能安全同时引用多供应商路由，服务端会明确阻止应用。"; }
  const localIssues = [];
  if (state.collaborationSpecIssue) localIssues.push(state.collaborationSpecIssue);
  if (!activeProviderID || activeProviderID === OFFICIAL_PROVIDER_KEY) {
    localIssues.push("Collaboration 只支持当前启用的自定义供应商；请先在路由策略中启用一个供应商。");
  } else if (policy.enabled && policy.provider_id && policy.provider_id !== activeProviderID) {
    localIssues.push(`已保存 Collaboration 属于供应商 ${policy.provider_id}，当前供应商已切换；请为当前供应商重新选择四个角色并预览。`);
  } else if (!collaborationStandardRoutes(routes).some((route) => collaborationAnchorCapable(routes, route))) {
    localIssues.push("当前供应商没有同时具备显式 Standard 锚点与可信推理档位的模型；未分类或未知能力会 fail closed。");
  }
  renderCollaborationStatus(status, localIssues);
  updateCollaborationControls();
}

function collaborationArtifactHTML(artifact = {}) {
  const path = String(artifact.path || "");
  const filename = path.split(/[\\/]/).pop() || path || "artifact";
  const actionLabels = { create: "新建", update: "更新", unchanged: "不变" };
  const action = actionLabels[artifact.action] || artifact.action || "预览";
  const previous = artifact.previously_exists ? (artifact.previous_content || "") : "（新文件）";
  return `<details class="collaborationArtifact"><summary><span><strong>${escapeHtml(filename)}</strong><code>${escapeHtml(path)}</code></span><span class="badge">${escapeHtml(action)}</span></summary><div class="collaborationArtifactBody"><p class="muted tiny mono">SHA-256 ${escapeHtml(artifact.sha256 || "—")}</p><div class="collaborationDiffGrid"><label class="field">当前<textarea class="configTextarea mono" readonly rows="10">${escapeHtml(previous)}</textarea></label><label class="field">应用后<textarea class="configTextarea mono" readonly rows="10">${escapeHtml(artifact.content || "")}</textarea></label></div></div></details>`;
}

function renderCollaborationPreview(preview, mode = "enable", request = null) {
  state.collaborationPreview = { preview, mode, request: request || (mode === "enable" ? currentCollaborationRequest() : { version: 4, enabled: false }) };
  const details = $("collaborationPreview");
  if (details) {
    details.hidden = false;
    details.open = true;
  }
  if ($("collaborationWarnings")) {
    $("collaborationWarnings").innerHTML = (preview.warnings || []).map((warning) => `<p>${escapeHtml(warning)}</p>`).join("");
  }
  if ($("collaborationConfigBefore")) $("collaborationConfigBefore").value = preview.config_before || "";
  if ($("collaborationConfigAfter")) $("collaborationConfigAfter").value = preview.config_after || "";
  if ($("collaborationConfigChange")) {
    $("collaborationConfigChange").textContent = preview.config_changed ? "将更新" : "不变";
    $("collaborationConfigChange").dataset.state = preview.config_changed ? "unhealthy" : "running";
  }
  const artifacts = preview.artifacts || [];
  if ($("collaborationArtifactCount")) $("collaborationArtifactCount").textContent = `${artifacts.length} 个`;
  if ($("collaborationArtifacts")) {
    $("collaborationArtifacts").innerHTML = artifacts.length
      ? artifacts.map(collaborationArtifactHTML).join("")
      : '<p class="muted tiny">停用不会删除或改写已生成的 role/workflow。</p>';
  }
  if ($("collaborationFingerprint")) $("collaborationFingerprint").textContent = `preview fingerprint: ${preview.fingerprint || "—"}`;
  updateCollaborationControls();
  return preview;
}

async function previewCollaboration(request = currentCollaborationRequest(), mode = "enable") {
  if (!request) {
    toast(state.collaborationSpecIssue || COLLABORATION_SPEC_UNAVAILABLE, "error");
    return false;
  }
  const preview = await api("/api/collaboration/preview", { method: "POST", body: JSON.stringify(request) });
  return renderCollaborationPreview(preview, mode, request);
}

async function applyCollaborationPreview() {
  const pending = state.collaborationPreview;
  const current = currentCollaborationRequest();
  if (!current || !pending || pending.mode !== "enable" || !pending.preview?.fingerprint || collaborationRequestKey(pending.request) !== collaborationRequestKey(current)) {
    toast(!current ? (state.collaborationSpecIssue || COLLABORATION_SPEC_UNAVAILABLE) : "请先预览当前选择，再应用未过期的变更", "error");
    return false;
  }
  const critical = current.default_tier === "critical";
  const fastCount = Object.values(current.roles || {}).filter((assignment) => assignment.speed_tier === COLLABORATION_SPEED_FAST).length;
  const fastNotice = fastCount ? ` 当前有 ${fastCount} 个角色使用 Fast priority，通常会消耗更多订阅 credits，且缺失时不会回退到 Standard。` : "";
  const message = critical
    ? `将把默认层级设为 Critical，并按预览写入 config.toml、routing policy、4 个角色文件和 1 个串行 workflow。路由 default 会对齐主协调解析后的具体 Standard/Fast 路由；web_search、explore 和 plan 保持不变。${fastNotice} Switch 本身不会启动 agent；以后运行该 workflow 仍必须显式选择 Critical 并传入 agent_budget=4。确认应用当前预览？`
    : `将按当前预览更新 config.toml、routing policy，并写入 4 个角色文件和 1 个串行 workflow。路由 default 会对齐主协调解析后的具体 Standard/Fast 路由；web_search、explore 和 plan 保持不变。${fastNotice} Switch 不会启动 agent，也不会保存消息或 transcript。确认应用？`;
  if (!(await customConfirm(message, { okLabel: "确认应用" }))) return false;
  const result = await api("/api/collaboration", {
    method: "PUT",
    body: JSON.stringify({ ...pending.request, confirmed: true, fingerprint: pending.preview.fingerprint }),
  });
  await refreshAll();
  await loadRoutingView();
  return result;
}

async function disableCollaboration() {
  const request = { version: 4, enabled: false };
  const preview = await previewCollaboration(request, "disable");
  const message = "停用只会停止使用 Max Collaboration policy；不会删除已生成的 role/workflow，也不会改写当前 config.toml 或 routing。确认按预览停用？";
  if (!(await customConfirm(message, { okLabel: "确认停用", danger: true }))) return false;
  const result = await api("/api/collaboration", {
    method: "PUT",
    body: JSON.stringify({ ...request, confirmed: true, fingerprint: preview.fingerprint }),
  });
  await refreshAll();
  await loadRoutingView();
  return result;
}

// Custom confirm dialog (window.confirm is unreliable in Wails WebView)
function customConfirm(message, { okLabel = "确定", cancelLabel = "取消", danger = false } = {}) {
  return new Promise((resolve) => {
    const dialog = $("confirmDialog");
    if (!dialog) {
      resolve(window.confirm(message));
      return;
    }
    const msg = $("confirmMessage");
    const ok = $("confirmOk");
    const cancel = $("confirmCancel");
    if (msg) msg.textContent = message;
    if (ok) {
      ok.textContent = okLabel;
      ok.classList.toggle("danger", !!danger);
      ok.classList.toggle("primary", !danger);
    }
    if (cancel) cancel.textContent = cancelLabel;
    const finish = (value) => {
      dialog.close();
      ok.onclick = null;
      cancel.onclick = null;
      dialog.oncancel = null;
      resolve(value);
    };
    ok.onclick = (e) => { e.preventDefault(); finish(true); };
    cancel.onclick = (e) => { e.preventDefault(); finish(false); };
    dialog.oncancel = () => finish(false);
    dialog.showModal();
    ok?.focus();
  });
}

function toast(message, type = "info") {
  const el = $("toast");
  el.textContent = message;
  el.classList.remove("error", "success", "show");
  if (type === "error") el.classList.add("error");
  if (type === "success") el.classList.add("success");
  requestAnimationFrame(() => el.classList.add("show"));
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove("show"), type === "error" ? 4200 : 2800);
}

function setBusy(button, busy, labelWhenBusy) {
  if (!button) return;
  if (busy) {
    if (!button.dataset.label) button.dataset.label = button.textContent;
    button.disabled = true;
    button.classList.add("busy");
    if (labelWhenBusy) button.textContent = labelWhenBusy;
  } else {
    button.disabled = false;
    button.classList.remove("busy");
    if (button.dataset.label) {
      button.textContent = button.dataset.label;
      delete button.dataset.label;
    }
  }
}

async function run(fn, { button, busyLabel, success } = {}) {
  try {
    setBusy(button, true, busyLabel);
    const result = await fn();
    if (result === false) return;
    if (success) toast(success, "success");
  } catch (err) {
    toast(err.message || String(err), "error");
  } finally {
    setBusy(button, false);
  }
}

async function refreshAll() {
  const [status, profiles, settings, lanAccess] = await Promise.all([
    api("/api/status"),
    api("/api/profiles"),
    api("/api/settings"),
    api("/api/lan-access"),
  ]);
  state.status = status;
  state.profiles = profiles;
  state.settings = settings;
  state.lanAccess = lanAccess;
  renderDrift();
  renderEmptyState();
  renderProfiles();
  renderSettings(settings);
  renderLANAccess(lanAccess);
}

function activeProfile() {
  return state.profiles.find((p) => p.is_active) || state.status?.active_profile || null;
}

async function loadConfigEditor() {
  const data = await api("/api/config");
  if ($("configPathLabel")) {
    $("configPathLabel").textContent = data.path || "";
  }
  if ($("configEditor")) {
    $("configEditor").value = data.content ?? "";
  }
  if ($("configEditorStatus")) {
    $("configEditorStatus").textContent = data.exists === false ? "文件尚不存在，保存后将创建。" : "已加载";
  }
}

async function saveConfigEditor(button) {
  await run(async () => {
    const content = $("configEditor")?.value ?? "";
    await api("/api/config", {
      method: "PUT",
      body: JSON.stringify({ content }),
    });
    await refreshAll();
    await loadConfigEditor();
  }, { button, busyLabel: "保存中…", success: "config.toml 已保存" });
}

let previewTimer = null;
async function refreshProviderConfigPreview() {
  const status = $("providerConfigPreviewStatus");
  const area = $("providerConfigPreview");
  if (!area) return;
  try {
    const profile = readForm();
    if (!profile.base_url && !profile.name) {
      area.value = "";
      if (status) status.textContent = "先填写名称与服务地址";
      return;
    }
    if (status) status.textContent = "生成预览…";
    const data = await api("/api/config/preview", {
      method: "POST",
      body: JSON.stringify(profile),
    });
    const full = $("previewFullConfig")?.checked;
    area.value = full ? (data.full || "") : (data.snippet || "");
    if (status) {
      status.textContent = full
        ? `合并到 ${data.path || "config.toml"} 后的完整文件预览（未保存）`
        : "仅显示此供应商会覆盖的段落";
    }
  } catch (err) {
    if (status) status.textContent = err.message || String(err);
  }
}

function scheduleProviderPreview() {
  clearTimeout(previewTimer);
  previewTimer = setTimeout(() => {
    if (state.view === "edit" && $("configPreviewBlock")?.open) {
      refreshProviderConfigPreview();
    }
  }, 400);
}

function showView(name) {
  state.view = name;
  const home = $("viewHome");
  const edit = $("viewEdit");
  const settings = $("viewSettings");
  const routing = $("viewRouting");
  const subscriptionProxy = $("viewSubscriptionProxy");
  const ssh = $("viewSSH");
  if (home) {
    home.hidden = name !== "home";
    home.style.display = name === "home" ? "" : "none";
  }
  if (edit) {
    edit.hidden = name !== "edit";
    edit.style.display = name === "edit" ? "" : "none";
  }
  if (settings) {
    settings.hidden = name !== "settings";
    settings.style.display = name === "settings" ? "" : "none";
  }
  if (routing) {
    routing.hidden = name !== "routing";
    routing.style.display = name === "routing" ? "" : "none";
  }
  if (subscriptionProxy) {
    subscriptionProxy.hidden = name !== "subscriptionProxy";
    subscriptionProxy.style.display = name === "subscriptionProxy" ? "" : "none";
  }
  if (ssh) {
    ssh.hidden = name !== "ssh";
    ssh.style.display = name === "ssh" ? "" : "none";
  }
  if ($("navHomeBtn")) $("navHomeBtn").hidden = name === "home";
  document.querySelectorAll("[data-home-only]").forEach((el) => {
    el.hidden = name !== "home";
  });
  // Keep header add/import only on home list.
  if ($("headerSubtitle")) {
    $("headerSubtitle").textContent =
      name === "settings" ? "设置" : name === "routing" ? "模型路由" : name === "subscriptionProxy" ? "订阅代理" : name === "ssh" ? "SSH 远程文件" : name === "edit" ? ( $("profileId")?.value ? "编辑供应商" : "添加供应商") : "供应商";
  }
  if (name === "settings") {
    loadConfigEditor().catch((err) => toast(err.message, "error"));
    loadCacheStats().catch((err) => toast(err.message, "error"));
  }
  if (name === "ssh") {
    loadSSHConnections().catch((err) => toast(err.message, "error"));
  }
  if (name === "routing") {
    loadRoutingView().catch(() => {});
  }
  if (name === "subscriptionProxy") {
    loadSubscriptionProxy().catch((err) => toast(err.message, "error"));
  } else {
    clearSubscriptionLoginPoll();
  }
}

function renderEmptyState() {
	const empty = false;
	$("emptyState").hidden = true;
	if ($("listControls")) $("listControls").hidden = false;
	$("profiles").hidden = false;
	if ($("searchEmpty")) $("searchEmpty").hidden = true;
	$("profileCount").textContent = `${state.profiles.length + 1} 个`;
}

function providerCards() {
	const settings = state.settings || {};
	const order = Array.isArray(settings.provider_order) ? settings.provider_order : [];
	const pinned = new Set(Array.isArray(settings.pinned_provider_ids) ? settings.pinned_provider_ids : []);
	const position = new Map(order.map((key, index) => [key, index]));
	const cards = [
		{
			key: OFFICIAL_PROVIDER_KEY,
			kind: "official",
			name: "官方账号",
			is_active: !!state.status?.official_active,
			logged_in: !!state.status?.official_logged_in,
		},
		...state.profiles.map((profile) => ({
			...profile,
			key: `profile:${profile.id}`,
			kind: "profile",
		})),
	];
	cards.forEach((card, index) => {
		card.pinned = pinned.has(card.key);
		card.position = position.has(card.key) ? position.get(card.key) : order.length + index;
	});
	cards.sort((a, b) => Number(b.pinned) - Number(a.pinned) || a.position - b.position);
	return cards;
}

function filteredProfiles() {
	const q = (state.search || "").trim().toLowerCase();
	const cards = providerCards();
	if (!q) return cards;
	return cards.filter((p) => (p.name || "").toLowerCase().includes(q));
}

function applyLayoutUI() {
  const layout = state.layout === "list" ? "list" : "card";
  state.layout = layout;
  localStorage.setItem("gs_layout", layout);
  if ($("profiles")) $("profiles").dataset.layout = layout;
  if ($("layoutCardBtn")) $("layoutCardBtn").classList.toggle("active", layout === "card");
  if ($("layoutListBtn")) $("layoutListBtn").classList.toggle("active", layout === "list");
}

function formatUpstream(value) {
  if (value === "openai_responses") return "Responses";
  if (value === "anthropic") return "Messages 兼容网关";
  return "OpenAI";
}

function hostOf(url) {
  try {
    return new URL(url).host || url;
  } catch {
    return url || "—";
  }
}

function renderProfiles() {
  applyLayoutUI();
  $("profiles").innerHTML = "";
	const list = filteredProfiles();
	const emptyAll = false;
  if ($("searchEmpty")) {
    $("searchEmpty").hidden = emptyAll || list.length > 0;
  }
  if (emptyAll) return;

	list.forEach((profile) => {
		const el = document.createElement("article");
		el.className = `provider${profile.is_active ? " active" : ""}${profile.pinned ? " pinned" : ""}`;
		el.dataset.providerKey = profile.key;
		el.dataset.pinned = profile.pinned ? "1" : "0";
		const official = profile.kind === "official";
		const meta = official
			? `${profile.logged_in ? "已登录 grok.com" : "尚未登录"} · OAuth 官方模型`
			: `${escapeHtml(profile.default_model || "未设默认模型")} · ${formatUpstream(profile.upstream_format)} · ${profile.models?.length || 0} 模型`;
		el.innerHTML = `
			<div class="providerTop">
				<button type="button" class="dragHandle" draggable="true" data-action="drag" title="拖动排序" aria-label="拖动 ${escapeHtml(profile.name)} 排序">↕</button>
				<div class="providerInfo">
					<h3 class="providerName">${escapeHtml(profile.name)}</h3>
					<p class="providerUrl">${official ? "grok.com / auth.json" : escapeHtml(profile.base_url || hostOf(profile.base_url))}</p>
					<p class="providerMeta">${meta}</p>
				</div>
				<div class="providerFlags">
					${profile.pinned ? '<span class="pinBadge">已置顶</span>' : ""}
					${profile.is_active ? '<span class="badge">当前启用</span>' : ""}
				</div>
			</div>
			<div class="providerActions">
				<button type="button" class="btn sm ghost" data-action="pin">${profile.pinned ? "取消置顶" : "置顶"}</button>
        <button type="button" class="btn sm primary" data-action="activate" ${profile.is_active ? "disabled" : ""}>${official && !profile.logged_in ? "登录" : profile.is_active ? "已启用" : "启用"}</button>
				${official ? "" : '<button type="button" class="btn sm" data-action="edit">编辑</button><button type="button" class="btn sm ghost" data-action="copy">复制</button><button type="button" class="btn sm ghost" data-action="export">导出</button><button type="button" class="btn sm danger" data-action="delete">删除</button>'}
			</div>
		`;

		el.querySelector('[data-action="pin"]').onclick = () => toggleProviderPin(profile.key);
    el.querySelector('[data-action="activate"]').onclick = () => {
      const activateButton = el.querySelector('[data-action="activate"]');
      if (official) return activateOfficial(activateButton);
      return run(async () => {
        const routing = await api("/api/routing");
        const warning = customProviderSwitchWarning(routing.active_provider_id, profile);
        if (warning && !(await customConfirm(warning, { okLabel: "确认切换" }))) return false;
        const policy = routing.provider_policies?.[profile.id] || {};
        await api("/api/routing/policy", { method: "PUT", body: JSON.stringify({ active_provider_id: profile.id, ...policy }) });
        await refreshAll();
      }, { button: activateButton, busyLabel: "启用中…", success: `已启用「${profile.name}」` });
    };
		bindProviderDrag(el, profile.key);

		if (!official) {
			el.querySelector('[data-action="edit"]').onclick = () => openEdit(profile);
			el.querySelector('[data-action="copy"]').onclick = () => {
				copyProfile(profile);
				showView("edit");
				$("name").focus();
			};
			el.querySelector('[data-action="export"]').onclick = () => exportProfile(profile);
			const deleteBtn = el.querySelector('[data-action="delete"]');
			let deleteConfirmTimer = 0;
			deleteBtn.onclick = () => {
				if (deleteBtn.dataset.confirmDelete !== "1") {
					deleteBtn.dataset.confirmDelete = "1";
					deleteBtn.dataset.originalLabel = deleteBtn.textContent;
					deleteBtn.textContent = "再次点击确认删除";
					toast(`再次点击以删除「${profile.name}」`, "error");
					clearTimeout(deleteConfirmTimer);
					deleteConfirmTimer = setTimeout(() => {
						deleteBtn.dataset.confirmDelete = "0";
						deleteBtn.textContent = deleteBtn.dataset.originalLabel || "删除";
					}, 5000);
					return;
				}
				clearTimeout(deleteConfirmTimer);
				run(async () => {
					await api(`/api/profiles/${profile.id}`, { method: "DELETE" });
					await refreshAll();
					showView("home");
				}, { button: deleteBtn, busyLabel: "删除中…", success: "已删除" });
			};
		}

    $("profiles").appendChild(el);
  });
}

async function saveProviderLayout(order, pinned) {
	const next = {
		...(state.settings || {}),
		provider_order: order,
		pinned_provider_ids: pinned,
	};
	state.settings = await api("/api/settings", { method: "PUT", body: JSON.stringify(next) });
}

async function toggleProviderPin(key) {
	await run(async () => {
		const cards = providerCards();
		const pinned = new Set(state.settings?.pinned_provider_ids || []);
		if (pinned.has(key)) pinned.delete(key); else pinned.add(key);
		await saveProviderLayout(cards.map((card) => card.key), [...pinned]);
		renderProfiles();
	}, { success: "卡片顺序已保存" });
}

function bindProviderDrag(card, key) {
	const handle = card.querySelector('[data-action="drag"]');
	handle.addEventListener("dragstart", (event) => {
		state.draggedProviderKey = key;
		card.classList.add("dragging");
		event.dataTransfer.effectAllowed = "move";
		event.dataTransfer.setData("text/plain", key);
	});
	handle.addEventListener("dragend", () => {
		state.draggedProviderKey = "";
		card.classList.remove("dragging");
		document.querySelectorAll(".provider.dragOver").forEach((item) => item.classList.remove("dragOver"));
	});
	card.addEventListener("dragover", (event) => {
		const source = document.querySelector(`[data-provider-key="${CSS.escape(state.draggedProviderKey)}"]`);
		if (!source || source === card || source.dataset.pinned !== card.dataset.pinned) return;
		event.preventDefault();
		card.classList.add("dragOver");
	});
	card.addEventListener("dragleave", () => card.classList.remove("dragOver"));
	card.addEventListener("drop", (event) => {
		event.preventDefault();
		card.classList.remove("dragOver");
		reorderProviderCards(state.draggedProviderKey, key);
	});
}

async function reorderProviderCards(sourceKey, targetKey) {
	if (!sourceKey || sourceKey === targetKey) return;
	await run(async () => {
		const cards = providerCards();
		const order = cards.map((card) => card.key);
		const sourceIndex = order.indexOf(sourceKey);
		const targetIndex = order.indexOf(targetKey);
		if (sourceIndex < 0 || targetIndex < 0) return false;
		order.splice(sourceIndex, 1);
		order.splice(targetIndex, 0, sourceKey);
		await saveProviderLayout(order, state.settings?.pinned_provider_ids || []);
		renderProfiles();
	}, { success: "卡片顺序已保存" });
}

async function activateOfficial(button) {
  const warning = state.status?.official_logged_in ? officialProviderSwitchWarning(state.status?.active_id) : "";
  if (warning) {
    const confirmed = await customConfirm(warning, { okLabel: "切换到官方", danger: true });
    if (!confirmed) return;
  }
	await run(async () => {
		const result = await api("/api/official/activate", { method: "POST" });
		await refreshAll();
		showView("home");
		if (result.switched) {
			toast("已切换到官方账号。新开 grok 会话生效。", "success");
		} else {
			toast("已打开官方登录。完成登录后不会自动启用，请回到此处再次点击“启用”。", "success");
		}
		return false;
	}, {
		button,
		busyLabel: state.status?.official_logged_in ? "切换中…" : "登录中…",
	});
}

function renderSettings(settings) {
  $("autostart").checked = !!settings.autostart;
  $("silentAutostart").checked = !!settings.silent_autostart;
  $("autoOpenBrowser").checked = !!settings.auto_open_browser;
  $("lanAccessEnabled").checked = !!settings.lan_access_enabled;
  $("port").value = settings.port;
  const actual = state.status?.port;
  const hint = $("portHint");
  if (actual && settings.port && actual !== settings.port) {
    hint.hidden = false;
    hint.textContent = `实际端口 ${actual}（配置 ${settings.port} 可能被占用）`;
  } else {
    hint.hidden = true;
  }
}

async function copyText(value, successMessage) {
  if (!value) throw new Error("没有可复制的内容");
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value);
  } else {
    const area = document.createElement("textarea");
    area.value = value;
    area.style.position = "fixed";
    area.style.opacity = "0";
    document.body.appendChild(area);
    area.select();
    const copied = document.execCommand("copy");
    area.remove();
    if (!copied) throw new Error("复制失败");
  }
  toast(successMessage, "success");
}

function openEdit(profile) {
  fillForm(profile || newProfileDraft());
  // Keep advanced sections collapsed by default for a simple add flow.
  if ($("connectBlock")) $("connectBlock").open = false;
  if ($("configPreviewBlock")) $("configPreviewBlock").open = false;
  showView("edit");
  $("name").focus();
}

function fillForm(profile) {
  $("formTitle").textContent = profile.id ? "编辑供应商" : "添加供应商";
  $("formHint").textContent = profile.id ? "修改供应商信息后保存；实际使用的模型由“模型路由”统一管理" : "名称、服务地址与 API Key 即可开始；保存后到“模型路由”选择要使用的模型";
  $("profileId").value = profile.id || "";
  $("name").value = profile.name || "";
  $("baseUrl").value = profile.base_url || "";
  $("profileApiKey").value = profile.api_key || firstModelKey(profile) || "";
  $("upstreamFormat").value = upstreamFormatValue(profile.upstream_format);
  $("defaultReasoningEffort").value = normalizeReasoningEffort(profile.default_reasoning_effort);
  state.availableModels = unique([
    ...(profile.available_models || []),
    ...(profile.models || []).map((model) => model.name || model.model),
  ]);
  $("modelsBody").innerHTML = "";
  (profile.models || []).forEach((model) => addModelCard(model));
  renderModelSelect();
  // Rebuild the provider-local default selector from enabled models. Global
  // web_search and subagent choices are owned exclusively by Model Routing.
  syncEnabledModelList(profile.default_model || "");
  hideConnectionStatus();
  if ($("connectBlock")) $("connectBlock").open = false;
}

function copyProfile(profile) {
  const source = profile.id ? profile : profile;
  const clone = {
    ...source,
    id: "",
    name: `${source.name || "供应商"} 副本`,
    is_active: false,
    models: (source.models || []).map((m) => ({ ...m, extra_headers: { ...(m.extra_headers || {}) } })),
  };
  fillForm(clone);
  toast("已载入副本，保存后生效", "info");
}

function stripSecrets(profile, includeKey) {
  const out = {
    name: profile.name,
    upstream_format: profile.upstream_format,
    base_url: profile.base_url,
    default_model: profile.default_model,
    default_reasoning_effort: normalizeReasoningEffort(profile.default_reasoning_effort),
    available_models: profile.available_models || [],
    models: (profile.models || []).map((m) => {
      const item = {
        name: m.name,
        model: m.model,
        base_url: m.base_url || "",
        api_backend: m.api_backend,
        extra_headers: m.extra_headers || {},
        supports_backend_search: !!m.supports_backend_search,
        supports_reasoning_effort: !!m.supports_reasoning_effort || !!m.reasoning_efforts?.length,
        reasoning_efforts: m.reasoning_efforts?.filter((effort) => REASONING_EFFORTS.includes(effort)) || [],
        reasoning_efforts_source: m.reasoning_efforts_source || "default",
        context_window: m.context_window || 0,
        max_completion_tokens: m.max_completion_tokens || 0,
      };
      if (includeKey) item.api_key = m.api_key || profile.api_key || "";
      return item;
    }),
  };
  if (includeKey) out.api_key = profile.api_key || "";
  return out;
}

function exportProfile(profile) {
  const includeKey = confirm("导出是否包含 API Key？\n\n取消 = 仅结构（适合分享）\n确定 = 含密钥（仅私用）");
  const payload = {
    format: "grok_switch_profile",
    version: 1,
    exported_at: new Date().toISOString(),
    profile: stripSecrets(profile, includeKey),
  };
  const blob = new Blob([JSON.stringify(payload, null, 2)], { type: "application/json" });
  const a = document.createElement("a");
  const safe = (profile.name || "profile").replace(/[\\/:*?"<>|]+/g, "_");
  a.href = URL.createObjectURL(blob);
  a.download = `${safe}.json`;
  a.click();
  URL.revokeObjectURL(a.href);
  toast(includeKey ? "已导出（含密钥）" : "已导出（不含密钥）", "success");
}

function importProfileJSON(text) {
  let data;
  try {
    data = JSON.parse(text);
  } catch {
    throw new Error("JSON 解析失败");
  }
  const profile = data.profile || data;
  if (!profile || typeof profile !== "object") throw new Error("无效的供应商 JSON");
  fillForm({
    id: "",
    name: profile.name ? `${profile.name} 导入` : "Imported",
    upstream_format: profile.upstream_format || "openai_chat",
    base_url: profile.base_url || "",
    api_key: profile.api_key || "",
    default_model: profile.default_model || "",
    default_reasoning_effort: normalizeReasoningEffort(profile.default_reasoning_effort),
    available_models: profile.available_models || [],
    models: profile.models || [],
  });
  showView("edit");
  toast("已载入 JSON，确认后点保存", "success");
}

function upstreamFormatValue(value) {
  if (value === "openai_responses" || value === "anthropic") return value;
  return "openai_chat";
}

function apiBackendFor(upstream) {
  if (upstream === "openai_responses") return "responses";
  if (upstream === "anthropic") return "messages";
  return "chat_completions";
}

function firstModelKey(profile) {
  return (profile.models || []).find((model) => model.api_key)?.api_key || "";
}

function removeModelByName(modelName) {
  [...$("modelsBody").querySelectorAll(".modelCard")].forEach((card) => {
    const name = card.querySelector('[data-field="name"]')?.value.trim();
    const model = card.querySelector('[data-field="model"]')?.value.trim();
    if (name === modelName || model === modelName) card.remove();
  });
  syncEnabledModelList();
}

function renderModelSelect() {
  const query = $("modelSearchInput")?.value.trim().toLowerCase() || "";
  const enabled = new Set(readEnabledModelNames());
  const models = state.availableModels
    .filter((model) => !query || model.toLowerCase().includes(query))
    .slice(0, 24);
  $("modelSuggestions").innerHTML = "";
  if (!state.availableModels.length) {
    $("modelPoolStatus").textContent = "尚未拉取模型。点 chip 仅启用，不会自动设置默认模型。";
    $("modelSuggestions").innerHTML = `<button type="button" class="chip mutedChip">先拉取模型</button>`;
    return;
  }
  $("modelPoolStatus").textContent = `已缓存 ${state.availableModels.length} 个模型。点 chip 启用/取消；默认模型请手动填写。`;
  models.forEach((model) => {
    const chip = document.createElement("button");
    chip.type = "button";
    const isOn = enabled.has(model);
    chip.className = isOn ? "chip selected" : "chip";
    chip.textContent = isOn ? `${model} ✓` : model;
    chip.onclick = () => {
      if (isOn) removeModelByName(model);
      else {
        addModelCard({
          name: model,
          model,
          api_backend: apiBackendFor($("upstreamFormat").value),
          context_window: 0,
          max_completion_tokens: 0,
        });
      }
      renderModelSelect();
      syncEnabledModelList();
    };
    $("modelSuggestions").appendChild(chip);
  });
  if (!models.length) {
    $("modelSuggestions").innerHTML = `<button type="button" class="chip mutedChip">没有匹配</button>`;
  }
}

function defaultModelCard() {
  const selected = $("defaultModel")?.value || "";
  return [...$("modelsBody")?.querySelectorAll(".modelCard") || []].find((card) => {
    const name = card.querySelector('[data-field="name"]')?.value.trim();
    const model = card.querySelector('[data-field="model"]')?.value.trim();
    return selected && (selected === name || selected === model);
  }) || null;
}

function fallbackReasoningEffort(efforts) {
  for (const effort of ["medium", "high", "low", ...efforts]) {
    if (efforts.includes(effort)) return effort;
  }
  return efforts[0] || "";
}

function setReasoningEffortOptions(supported = REASONING_EFFORTS, statuses = {}) {
  const select = $("defaultReasoningEffort");
  if (!select) return;
  const current = REASONING_EFFORTS.includes(select.value) ? select.value : "";
  const allowed = unique(supported.filter((effort) => REASONING_EFFORTS.includes(effort)));
  const options = allowed.length ? allowed : ["none"];
  select.replaceChildren(...options.map((effort) => {
    const option = document.createElement("option");
    option.value = effort;
    const suffix = statuses[effort] === "accepted" ? " — 已检测" : "";
    option.textContent = `${REASONING_EFFORT_LABELS[effort]}${suffix}`;
    return option;
  }));
  select.value = options.includes(current) ? current : fallbackReasoningEffort(options);
}

function updateReasoningEffortMetadata() {
  const status = $("reasoningEffortStatus");
  if (!status) return;
  status.classList.remove("ok", "warn", "fail");
  const selected = $("defaultModel")?.value || "";
  const card = defaultModelCard();
  const efforts = card ? JSON.parse(card.dataset.reasoningEfforts || "[]") : [];
  const supported = efforts.length ? efforts : ["none"];
  setReasoningEffortOptions(supported);
  if (!selected) {
    status.textContent = "选择默认模型后，将按该模型能力显示推理档位。";
  } else if (efforts.length) {
    status.textContent = `当前模型可用档位：${supported.join("、")}。`;
    status.classList.add("ok");
  } else {
    status.textContent = "模型未声明推理能力，默认禁用；如需探测，请点击检测并确认会向上游发送最多 6 个最小请求。";
  }
}

async function detectReasoningEfforts() {
  const current = readForm();
  if (!current.default_model) throw new Error("请先选择默认模型");
  const card = defaultModelCard();
  const model = card?.querySelector('[data-field="model"]')?.value.trim() || current.default_model;
  const baseURL = card?.modelDraft?.base_url || current.base_url;
  const apiBackend = card?.modelDraft?.api_backend || apiBackendFor(current.upstream_format);
  const confirmed = await customConfirm(`将向 ${baseURL || "上游服务"} 为模型 ${model} 发送最多 6 个最小请求，逐项探测 reasoning_effort。是否继续？`, {
    okLabel: "发送最多 6 个探测请求",
  });
  if (!confirmed) return false;
  const requestContext = `${current.id}\n${current.default_model}\n${model}\n${baseURL}\n${apiBackend}`;
  const status = $("reasoningEffortStatus");
  if (status) {
    status.classList.remove("ok", "warn", "fail");
    status.textContent = "正在发送最多 6 个最小请求检测支持档位…";
  }
  try {
    const data = await api("/api/models/reasoning-efforts", {
      method: "POST",
      body: JSON.stringify({
        profile_id: current.id, base_url: baseURL, api_key: current.api_key,
        upstream_format: current.upstream_format, model, api_backend: apiBackend,
        user_confirmed_probe: true,
      }),
    });
    const latest = readForm();
    const latestCard = defaultModelCard();
    const latestModel = latestCard?.querySelector('[data-field="model"]')?.value.trim() || latest.default_model;
    const latestBaseURL = latestCard?.modelDraft?.base_url || latest.base_url;
    const latestBackend = latestCard?.modelDraft?.api_backend || apiBackendFor(latest.upstream_format);
    if (`${latest.id}\n${latest.default_model}\n${latestModel}\n${latestBaseURL}\n${latestBackend}` !== requestContext) return false;
    const statuses = Object.fromEntries((data.results || []).map((item) => [item.effort, item.status]));
    const recommended = data.source === "declared"
      ? (data.efforts || []).filter((effort) => REASONING_EFFORTS.includes(effort))
      : (data.results || []).filter((item) => item.status === "accepted").map((item) => item.effort);
    if (recommended.length && latestCard) {
      latestCard.dataset.reasoningEfforts = JSON.stringify(recommended);
      latestCard.dataset.reasoningEffortsSource = data.source === "declared" ? "declared" : "probe";
    }
    setReasoningEffortOptions(recommended.length ? recommended : ["none"], statuses);
    const details = data.source === "declared"
      ? [`模型明确声明支持：${recommended.join("、") || "未提供档位"}`]
      : (data.results || []).map((item) => {
      if (item.status === "accepted") return `${item.effort}：上游接受请求，可能静默忽略`;
      if (item.status === "unsupported") return `${item.effort}：不支持`;
      return `${item.effort}：未知`;
    });
    if (status) {
      status.textContent = [details.join("；"), data.note].filter(Boolean).join("。") || "检测完成，已按模型能力更新可选档位。";
      status.classList.add(recommended.length ? "ok" : "warn");
    }
  } catch (err) {
    if (status) {
      status.textContent = `检测失败：${err.message || String(err)}；继续使用模型已保存的能力档位。`;
      status.classList.add("fail");
    }
    throw err;
  }
}

function syncEnabledModelList(preferredDefault) {
  const names = unique(readEnabledModelNames());
  const sel = $("defaultModel");
  if (!sel) return;
  const current = preferredDefault ?? sel.value ?? "";
  sel.innerHTML = "";
  const empty = document.createElement("option");
  empty.value = "";
  empty.textContent = names.length ? "（未选择）" : "（请先启用模型）";
  sel.appendChild(empty);
  names.forEach((name) => {
    const opt = document.createElement("option");
    opt.value = name;
    opt.textContent = name;
    sel.appendChild(opt);
  });
  // Keep the saved provider default visible while the model list is mid-edit.
  if (current && !names.includes(current)) {
    const orphan = document.createElement("option");
    orphan.value = current;
    orphan.textContent = `${current}（未启用）`;
    sel.appendChild(orphan);
    sel.value = current;
  } else if (current && names.includes(current)) {
    sel.value = current;
  } else {
    sel.value = "";
  }
  updateReasoningEffortMetadata();
}

function renderLANAccess(access) {
  const remote = !!access?.remote;
  if ($("lanAccessCard")) $("lanAccessCard").hidden = remote;
  if ($("lanAccessEnabled")) $("lanAccessEnabled").disabled = remote;
  if (remote) return;
  const enabled = !!state.settings?.lan_access_enabled && !!access?.enabled;
  const badge = $("lanAccessBadge");
  const empty = $("lanAccessDisabled");
  const details = $("lanAccessDetails");
  if (badge) {
    badge.textContent = enabled ? "已开启" : "未开启";
    badge.classList.toggle("active", enabled);
  }
  if (empty) empty.hidden = enabled;
  if (details) details.hidden = !enabled;
  if (!enabled) return;

  const addresses = access?.addresses || [];
  const select = $("lanAccessAddress");
  if (!select) return;
  const current = select.value;
  select.innerHTML = addresses.length
    ? addresses.map((item, index) => `<option value="${index}">${escapeHtml(item.address)}</option>`).join("")
    : `<option value="">未找到局域网地址</option>`;
  if (addresses.length) {
    const selected = Number.isInteger(Number(current)) && Number(current) < addresses.length ? Number(current) : 0;
    select.value = String(selected);
    renderLANAddress(addresses[selected], access);
  } else {
    renderLANAddress(null, access);
  }
}

function renderLANAddress(address, access) {
  const qr = $("lanAccessQr");
  const url = $("lanAccessUrl");
  const code = $("lanAccessCode");
  const expiry = $("lanAccessExpiry");
  if (qr) {
    qr.hidden = !address?.qr_code;
    if (address?.qr_code) qr.src = address.qr_code;
    else qr.removeAttribute("src");
  }
  if (url) url.value = address?.pair_url || "";
  if (code) code.textContent = access?.pairing_code || "—";
  if (expiry) {
    expiry.textContent = access?.pairing_expiry
      ? `有效至 ${new Date(access.pairing_expiry).toLocaleTimeString()}`
      : "";
  }
}

function syncModelBaseURLs() {
  const baseURL = $("baseUrl")?.value.trim() || "";
  $("modelsBody")?.querySelectorAll(".modelCard").forEach((card) => {
    card.modelDraft = { ...(card.modelDraft || {}), base_url: baseURL };
  });
}

function addModelCard(model = {}) {
  const card = document.createElement("div");
  card.className = "modelCard";
  card.modelDraft = {
    ...model,
    base_url: model.base_url || $("baseUrl")?.value.trim() || "",
    api_backend: model.api_backend || apiBackendFor($("upstreamFormat").value),
    extra_headers: model.extra_headers || {},
    supports_backend_search: modelSupportsBackendSearch(model),
    context_window: Number(model.context_window || 0),
    max_completion_tokens: Number(model.max_completion_tokens || 0),
  };
  card.dataset.reasoningEfforts = JSON.stringify((model.reasoning_efforts || []).filter((effort) => REASONING_EFFORTS.includes(effort)));
  card.dataset.reasoningEffortsSource = model.reasoning_efforts_source || "default";
  card.innerHTML = `
    <div class="modelCardTop">
      <strong>${escapeHtml(model.name || model.model || "新模型")}</strong>
      <div class="inlineActions">
        <button type="button" class="btn sm" data-action="test-model">测试连通</button>
        <button type="button" class="btn sm danger" data-action="remove-model">删除</button>
      </div>
    </div>
    <p class="muted tiny modelProbeStatus" data-field="probe_status" hidden></p>
    <div class="modelCardGrid">
      <label class="field">名称
        <input data-field="name" class="mono" value="${escapeAttr(model.name || "")}" placeholder="配置中的模型名">
      </label>
      <label class="field">Model
        <input data-field="model" class="mono" value="${escapeAttr(model.model || "")}" placeholder="上游模型 ID">
      </label>
      <details class="modelAdvanced full">
        <summary>模型高级设置</summary>
        <label class="check">
          <input type="checkbox" data-field="supports_backend_search" ${card.modelDraft.supports_backend_search ? "checked" : ""}>
          支持原生后端搜索（仅在上游明确支持时启用）
        </label>
      </details>
    </div>
  `;
  const nameInput = card.querySelector('[data-field="name"]');
  const modelInput = card.querySelector('[data-field="model"]');
  const backendSearchInput = card.querySelector('[data-field="supports_backend_search"]');
  const onFieldChange = () => {
    card.querySelector("strong").textContent = nameInput.value.trim() || modelInput.value.trim() || "新模型";
    renderModelSelect();
    syncEnabledModelList();
  };
  nameInput.addEventListener("input", onFieldChange);
  modelInput.addEventListener("input", onFieldChange);
  backendSearchInput.addEventListener("change", () => {
    card.modelDraft = { ...(card.modelDraft || {}), supports_backend_search: backendSearchInput.checked };
    scheduleProviderPreview();
  });
  card.querySelector('[data-action="remove-model"]').onclick = () => {
    card.remove();
    renderModelSelect();
    syncEnabledModelList();
    scheduleProviderPreview();
  };
  card.querySelector('[data-action="test-model"]').onclick = () => testSingleModel(card);
  $("modelsBody").appendChild(card);
  scheduleProviderPreview();
}

async function testSingleModel(card) {
  const btn = card.querySelector('[data-action="test-model"]');
  const statusEl = card.querySelector('[data-field="probe_status"]');
  const modelName = card.querySelector('[data-field="model"]')?.value.trim()
    || card.querySelector('[data-field="name"]')?.value.trim();
  const modelBase = card.modelDraft?.base_url || "";
  const backend = card.modelDraft?.api_backend || "";
  await run(async () => {
    const current = readForm();
    if (!current.base_url && !modelBase) throw new Error("先填写服务地址");
    if (!current.api_key) throw new Error("先填写 API Key");
    if (!modelName) throw new Error("模型名为空");
    if (statusEl) {
      statusEl.hidden = false;
      statusEl.textContent = "测试中…";
      statusEl.classList.remove("ok", "fail");
    }
    const result = await api("/api/connection/test", {
      method: "POST",
      body: JSON.stringify({
        profile_id: current.id,
        base_url: modelBase || current.base_url,
        api_key: current.api_key,
        upstream_format: current.upstream_format,
        api_backend: backend || apiBackendFor(current.upstream_format),
        model: modelName,
      }),
    });
    if (!result.ok) {
      if (statusEl) {
        statusEl.textContent = `失败 ${result.latency_ms}ms：${result.error || "未知错误"}`;
        statusEl.classList.add("fail");
      }
      throw new Error(result.error || `${modelName} 不通`);
    }
    if (statusEl) {
      statusEl.textContent = `连通 ${result.latency_ms}ms`;
      statusEl.classList.add("ok");
    }
    toast(`${modelName} 连通（${result.latency_ms}ms）`, "success");
  }, { button: btn, busyLabel: "测试中…" });
}

function syncModelBackends() {
  const backend = apiBackendFor($("upstreamFormat").value);
  $("modelsBody").querySelectorAll(".modelCard").forEach((card) => {
    card.modelDraft = { ...(card.modelDraft || {}), api_backend: backend };
  });
}

function readEnabledModelNames() {
  return [...$("modelsBody").querySelectorAll(".modelCard")].map((row) => {
    const name = row.querySelector('[data-field="name"]')?.value.trim();
    const model = row.querySelector('[data-field="model"]')?.value.trim();
    return name || model;
  }).filter(Boolean);
}

function readForm() {
  const rows = [...$("modelsBody").querySelectorAll(".modelCard")];
  const apiKey = $("profileApiKey").value.trim();
  return {
    id: $("profileId").value,
    name: $("name").value.trim(),
    upstream_format: $("upstreamFormat").value,
    base_url: $("baseUrl").value.trim(),
    api_key: apiKey,
    available_models: state.availableModels,
    default_model: $("defaultModel")?.value?.trim() || "",
    default_reasoning_effort: $("defaultReasoningEffort")?.value || "none",
    models: rows.map((row) => {
      const name = row.querySelector('[data-field="name"]')?.value.trim() || "";
      const model = row.querySelector('[data-field="model"]')?.value.trim() || "";
      const reasoningEfforts = JSON.parse(row.dataset.reasoningEfforts || "[]");
      return {
        ...(row.modelDraft || {}),
        name,
        model,
        api_key: apiKey,
        supports_reasoning_effort: reasoningEfforts.length > 0,
        reasoning_efforts: reasoningEfforts,
        reasoning_efforts_source: row.dataset.reasoningEffortsSource || "default",
      };
    }).filter((m) => m.name || m.model),
  };
}

function escapeAttr(value) {
  return escapeHtml(value);
}

function unique(values) {
  return [...new Set(values.filter(Boolean))];
}

function hideConnectionStatus() {
  const el = $("connectionStatus");
  el.hidden = true;
  el.textContent = "";
  el.classList.remove("ok", "fail");
}

function showConnectionStatus(ok, text) {
  const el = $("connectionStatus");
  el.hidden = false;
  el.textContent = text;
  el.classList.toggle("ok", ok);
  el.classList.toggle("fail", !ok);
}

async function saveCurrentProfile() {
  const profile = readForm();
  if (!profile.name) throw new Error("请填写名称");
  if (!profile.base_url) throw new Error("请填写服务地址");
  if (profile.id) {
    return await api(`/api/profiles/${profile.id}`, { method: "PUT", body: JSON.stringify(profile) });
  }
  return await api("/api/profiles", { method: "POST", body: JSON.stringify(profile) });
}

function clearSubscriptionLoginPoll() {
  clearTimeout(subscriptionLoginPollTimer);
  subscriptionLoginPollTimer = null;
}

function subscriptionStatusLabel(value) {
  return ({
    running: "运行中",
    stopped: "已停止",
    starting: "启动中",
    stopping: "停止中",
    pending: "等待浏览器授权",
    wait: "等待浏览器授权",
    waiting: "等待浏览器授权",
    opening: "等待浏览器授权",
    ok: "登录成功",
    completed: "登录成功",
    success: "登录成功",
    failed: "失败",
    error: "失败",
    cancelled: "已取消",
    ready: "可用",
  })[value] || value || "未知";
}

function renderSubscriptionService(service = {}) {
  state.subscriptionProxy = { ...(state.subscriptionProxy || {}), service };
  $("subscriptionServiceBadge").textContent = subscriptionStatusLabel(service.state);
  $("subscriptionServiceBadge").dataset.state = service.state || "unknown";
  $("subscriptionServiceDetail").textContent = [service.version, service.pid ? `PID ${service.pid}` : "", service.config_path, service.last_error].filter(Boolean).join(" · ") || "服务未启动";
  $("subscriptionBaseUrl").textContent = service.base_url || "—";
  $("subscriptionApiKey").textContent = service.api_key_masked || "—";
  const transitioning = ["starting", "stopping"].includes(service.state);
  $("subscriptionStartBtn").disabled = transitioning || service.state === "running";
  $("subscriptionStopBtn").disabled = transitioning || service.state !== "running";
  $("subscriptionRestartBtn").disabled = transitioning || service.state !== "running";
}

function renderSubscriptionProxy(data) {
  state.subscriptionProxy = data || {};
  renderSubscriptionService(data?.service || {});
  const accounts = Array.isArray(data?.accounts) ? data.accounts : [];
  $("subscriptionAccountCount").textContent = `${accounts.length} 个`;
  $("subscriptionAccounts").innerHTML = accounts.length ? accounts.map((account) => `<article class="subscriptionAccount"><div><strong>${escapeHtml(account.name || account.label || account.email || account.id)}</strong><p>${escapeHtml([account.provider, account.email, account.status_message].filter(Boolean).join(" · "))}</p></div><span class="badge">${escapeHtml(subscriptionStatusLabel(account.status))}</span><div class="inlineActions"><button type="button" class="btn sm subscriptionAccountToggle" data-id="${escapeAttr(account.id)}" data-disabled="${account.disabled ? "1" : "0"}">${account.disabled ? "启用" : "禁用"}</button><button type="button" class="btn sm danger subscriptionAccountDelete" data-id="${escapeAttr(account.id)}">删除</button></div></article>`).join("") : '<p class="muted tiny">尚未登录订阅账号。</p>';
  const models = Array.isArray(data?.models) ? data.models : [];
  const groups = Object.groupBy ? Object.groupBy(models, (model) => model.provider || "其他") : models.reduce((result, model) => { (result[model.provider || "其他"] ||= []).push(model); return result; }, {});
  $("subscriptionModels").innerHTML = Object.entries(groups).map(([provider, items]) => `<fieldset><legend>${escapeHtml(provider)}</legend>${items.map((model) => `<label class="check"><input type="checkbox" value="${escapeAttr(model.id)}" data-provider="${escapeAttr(model.provider)}" ${model.selected ? "checked" : ""}> <span>${escapeHtml(model.label || model.id)}</span></label>`).join("")}</fieldset>`).join("") || '<p class="muted tiny">暂无可用模型。</p>';
  const serviceReady = data?.service?.state === "running";
  document.querySelectorAll(".subscriptionLoginBtn").forEach((button) => {
    button.disabled = !serviceReady;
    button.title = serviceReady ? "在浏览器中登录此订阅账号" : "请先启动订阅代理服务";
  });
  const accountProviders = new Set(accounts.filter((account) => !account.disabled && !account.unavailable).map((account) => account.provider));
  const selectedProviders = new Set(models.filter((model) => model.selected).map((model) => model.provider));
  document.querySelectorAll(".subscriptionProviderBtn").forEach((button) => {
    const provider = button.dataset.provider;
    const label = provider === "gemini" ? "Gemini" : provider === "grok" ? "Grok" : "Codex";
    const exists = !!data?.providers?.[provider];
    const ready = accountProviders.has(provider) && selectedProviders.has(provider) && data?.service?.state === "running";
    button.textContent = `${exists ? "更新" : "创建"} ${label} 供应商`;
    button.disabled = !ready;
    button.title = ready ? `同步 ${label} 账号和已选模型到供应商列表` : `请先启动代理、添加 ${label} 账号并保存该类型的模型`;
  });
  const readyProviders = [...accountProviders].filter((provider) => selectedProviders.has(provider));
  $("subscriptionProviderHint").textContent = readyProviders.length
    ? `可同步：${readyProviders.map((provider) => provider === "gemini" ? "Gemini" : provider === "grok" ? "Grok" : "Codex").join("、")}。创建后请到“模型路由”选择模型。`
    : "请先完成账号登录，并为对应类型勾选和保存至少一个模型。";
  bindSubscriptionDynamicHandlers();
  if (data?.login_session) renderSubscriptionLoginSession(data.login_session);
}

async function loadSubscriptionProxy() {
  renderSubscriptionProxy(await api("/api/subscription-proxy"));
}

function isSubscriptionLoginPending(status) {
  return ["pending", "wait", "waiting", "opening"].includes(String(status || "").toLowerCase());
}

function isSubscriptionLoginCompleted(status) {
  return ["completed", "success", "done", "authenticated", "authorized"].includes(String(status || "").toLowerCase());
}

function isSubscriptionLoginFailed(status) {
  return ["failed", "error", "timeout", "timed_out", "expired", "cancelled", "canceled"].includes(String(status || "").toLowerCase());
}

function renderSubscriptionLoginSession(session) {
  const box = $("subscriptionLoginSession");
  if (!session?.id) { box.hidden = true; return; }
  box.hidden = false;
  const pending = isSubscriptionLoginPending(session.status);
  const completed = isSubscriptionLoginCompleted(session.status);
  const failed = isSubscriptionLoginFailed(session.status);
  const hint = session.status_message
    || (pending ? "已打开浏览器。请用要添加的 ChatGPT 账号登录并授权，完成后会自动回到这里。" : "")
    || (completed ? "账号已写入订阅代理。" : "");
  box.innerHTML = `<div class="rowBetween"><div><strong>${escapeHtml(subscriptionStatusLabel(session.status))}</strong><p class="muted tiny">${escapeHtml(session.provider || "codex")}${hint ? ` · ${escapeHtml(hint)}` : ""}</p></div><div class="inlineActions">${pending ? `<button type="button" id="subscriptionOpenLoginBtn" class="btn primary">重新打开浏览器</button><button type="button" id="subscriptionCancelLoginBtn" class="btn">取消</button>` : `<button type="button" id="subscriptionDismissLoginBtn" class="btn">关闭</button>`}</div></div>`;
  if ($("subscriptionOpenLoginBtn")) {
    $("subscriptionOpenLoginBtn").onclick = () => run(() => api("/api/subscription-proxy/login/open", { method: "POST", body: JSON.stringify({ id: session.id }) }), { button: $("subscriptionOpenLoginBtn"), busyLabel: "打开中…" });
  }
  if ($("subscriptionCancelLoginBtn")) {
    $("subscriptionCancelLoginBtn").onclick = () => run(async () => {
      await api(`/api/subscription-proxy/login?id=${encodeURIComponent(session.id)}`, { method: "DELETE" });
      clearSubscriptionLoginPoll();
      await loadSubscriptionProxy();
    }, { button: $("subscriptionCancelLoginBtn"), busyLabel: "取消中…" });
  }
  if ($("subscriptionDismissLoginBtn")) {
    $("subscriptionDismissLoginBtn").onclick = () => { clearSubscriptionLoginPoll(); box.hidden = true; box.innerHTML = ""; };
  }
  clearSubscriptionLoginPoll();
  if (pending) subscriptionLoginPollTimer = setTimeout(() => pollSubscriptionLogin(session.id), 1500);
  if (completed) {
    // Keep a short success state, then refresh account list.
  }
  if (failed && session.status_message) {
    // Message already shown in the card.
  }
}

async function pollSubscriptionLogin(id) {
  if (state.view !== "subscriptionProxy") return;
  try {
    const result = await api(`/api/subscription-proxy/login?id=${encodeURIComponent(id)}`);
    const session = result.session || result;
    const wasPending = isSubscriptionLoginPending(session.status);
    clearSubscriptionLoginPoll();
    renderSubscriptionLoginSession(session);
    if (isSubscriptionLoginCompleted(session.status)) {
      toast("订阅账号登录成功", "success");
      await loadSubscriptionProxy();
      return;
    }
    if (isSubscriptionLoginFailed(session.status)) {
      toast(session.status_message || "登录失败，请重试", "error");
      return;
    }
    // renderSubscriptionLoginSession already re-arms the timer for pending states.
    if (!wasPending && isSubscriptionLoginPending(session.status)) {
      subscriptionLoginPollTimer = setTimeout(() => pollSubscriptionLogin(id), 1500);
    }
  } catch (err) {
    toast(err.message, "error");
    clearSubscriptionLoginPoll();
  }
}

function bindSubscriptionDynamicHandlers() {
  document.querySelectorAll(".subscriptionAccountToggle").forEach((button) => button.onclick = () => run(async () => { await api(`/api/subscription-proxy/accounts/${encodeURIComponent(button.dataset.id)}`, { method: "PATCH", body: JSON.stringify({ disabled: button.dataset.disabled !== "1" }) }); await loadSubscriptionProxy(); }, { button, busyLabel: "保存中…" }));
  document.querySelectorAll(".subscriptionAccountDelete").forEach((button) => button.onclick = () => run(async () => {
    const account = (state.subscriptionProxy?.accounts || []).find((item) => item.id === button.dataset.id);
    const label = account?.email || account?.label || account?.name || button.dataset.id;
    const confirmed = await customConfirm(`删除订阅账号「${label}」？\n\n这会移除本地保存的登录凭据；已创建的供应商仍会保留，但可能因没有可用账号而无法调用。`, { okLabel: "删除账号", danger: true });
    if (!confirmed) return false;
    await api(`/api/subscription-proxy/accounts/${encodeURIComponent(button.dataset.id)}`, { method: "DELETE" });
    await loadSubscriptionProxy();
  }, { button, busyLabel: "删除中…", success: "账号已删除" }));
}

// ——— SSH Remote File Manager ———

const ssh = {
  connections: [],
  activeConn: null,
  currentPath: "/",
  selected: new Set(),
  filter: null,
};

async function loadSSHConnections() {
  ssh.connections = await api("/api/ssh/connections");
  renderSSHConnections();
}

function renderSSHConnections() {
  const box = $("sshConnections");
  if (!ssh.connections.length) {
    box.innerHTML = '<p class="muted tiny" style="padding:14px">还没有连接</p>';
    return;
  }
  box.innerHTML = ssh.connections.map((c) => {
    const status = c.connected ? "connected" : "";
    return `<div class="sshConnection${ssh.activeConn === c.id ? " active" : ""}" data-id="${escapeAttr(c.id)}">
      <div class="sshConnectionMain" data-action="connect">
        <div class="sshConnectionName"><span class="sshConnectionStatus ${status}"></span>${escapeHtml(c.name)}</div>
        <div class="sshConnectionMeta">${escapeHtml(c.user)}@${escapeHtml(c.host)}:${c.port || 22}</div>
      </div>
      <div class="sshConnectionActions">
        <button type="button" class="btn ghost sshConnEditBtn" data-action="edit" title="编辑连接">编辑</button>
        <button type="button" class="btn ghost danger sshConnDelBtn" data-action="delete" title="删除连接">删除</button>
      </div>
    </div>`;
  }).join("");
  box.querySelectorAll(".sshConnection").forEach((el) => {
    const conn = ssh.connections.find((item) => item.id === el.dataset.id);
    el.querySelector('[data-action="connect"]').onclick = () => connectSSH(el.dataset.id);
    el.querySelector('[data-action="edit"]').onclick = (event) => {
      event.stopPropagation();
      ssh.editingConn = conn.id;
      showSSHConnDialog(conn);
    };
    el.querySelector('[data-action="delete"]').onclick = (event) => {
      event.stopPropagation();
      deleteSSHConnection(conn, event.currentTarget);
    };
  });
}

async function deleteSSHConnection(conn, button) {
  if (!conn?.id) return;
  const ok = await customConfirm(
    `删除 SSH 连接「${conn.name}」？\n\n${conn.user}@${conn.host}:${conn.port || 22}\n\n只删除本地连接配置，不会删除 ~/.ssh/config、私钥或远端文件。`,
    { okLabel: "删除连接", cancelLabel: "取消", danger: true },
  );
  if (!ok) return;
  await run(async () => {
    await api(`/api/ssh/connections/${encodeURIComponent(conn.id)}`, { method: "DELETE" });
    if (ssh.activeConn === conn.id) {
      ssh.activeConn = null;
      ssh.currentPath = "/";
      ssh.selected.clear();
      renderSSHFiles([]);
    }
    await loadSSHConnections();
  }, { button, busyLabel: "删除中…", success: "SSH 连接已删除" });
}

async function connectSSH(id) {
  const conn = ssh.connections.find((c) => c.id === id);
  if (!conn) return;
  try {
    let password = null;
    if (conn.auth_type === "password") {
      password = await customPrompt("输入 SSH 密码", "");
      if (password === null) return;
    }
    await api("/api/ssh/connect", { method: "POST", body: JSON.stringify({ id, password }) });
    ssh.activeConn = id;
    ssh.currentPath = "/";
    ssh.selected.clear();
    toast(`已连接到 ${conn.name}`, "success");
    await loadSSHConnections();
    await loadSSHFiles("/");
  } catch (err) {
    toast(err.message, "error");
  }
}

async function disconnectSSH(id) {
  await api(`/api/ssh/disconnect/${id}`, { method: "POST" });
  if (ssh.activeConn === id) { ssh.activeConn = null; renderSSHFiles([]); }
  await loadSSHConnections();
}

async function loadSSHFiles(path) {
  if (!ssh.activeConn) { renderSSHFiles([]); return; }
  ssh.currentPath = path;
  ssh.selected.clear();
  updateSSHToolbar();
  const infos = await api(`/api/ssh/files?conn_id=${encodeURIComponent(ssh.activeConn)}&path=${encodeURIComponent(path)}`);
  renderSSHFiles(infos || []);
  renderBreadcrumb(path);
}

function renderBreadcrumb(path) {
  const box = $("sshBreadcrumb");
  const parts = path.split("/").filter(Boolean);
  let accum = "";
  const links = [`<a data-path="/">/</a>`];
  for (const part of parts) {
    accum += "/" + part;
    links.push(`<span class="sep">/</span><a data-path="${escapeAttr(accum)}">${escapeHtml(part)}</a>`);
  }
  box.innerHTML = links.join("");
  box.querySelectorAll("a").forEach((a) => { a.onclick = () => loadSSHFiles(a.dataset.path); });
}

function renderSSHFiles(infos) {
  const box = $("sshFiles");
  const empty = $("sshEmpty");
  if (!ssh.activeConn) { box.innerHTML = ""; empty.textContent = "选择一个连接开始浏览"; empty.hidden = false; return; }
  if (!infos.length) { box.innerHTML = ""; empty.textContent = "目录为空"; empty.hidden = false; return; }
  empty.hidden = true;
  const filtered = ssh.filter ? infos.filter((f) => minimatch(f.name, ssh.filter)) : infos;
  const dirs = filtered.filter((f) => f.is_dir).sort((a, b) => a.name.localeCompare(b.name));
  const files = filtered.filter((f) => !f.is_dir).sort((a, b) => a.name.localeCompare(b.name));
  box.innerHTML = [...dirs, ...files].map((f) => {
    const icon = f.is_dir ? "📁" : getFileIcon(f.name);
    const size = f.is_dir ? "" : formatSize(f.size);
    const selected = ssh.selected.has(f.path) ? " selected" : "";
    return `<div class="sshFileRow${selected}" data-path="${escapeAttr(f.path)}" data-dir="${f.is_dir}">
      <input type="checkbox" class="sshFileCheck" ${ssh.selected.has(f.path) ? "checked" : ""}>
      <span class="sshFileIcon">${icon}</span>
      <span class="sshFileName">${escapeHtml(f.name)}</span>
      <span class="sshFileSize">${size}</span>
      <div class="sshFileActions">
        <button type="button" class="btn sm danger sshFileActionBtn" data-action="delete">删除</button>
      </div>
    </div>`;
  }).join("");
  box.querySelectorAll(".sshFileRow").forEach((row) => {
    const path = row.dataset.path;
    const isDir = row.dataset.dir === "true";
    // Single click: select/deselect.
    row.onclick = (e) => {
      if (e.target.closest(".sshFileActionBtn") || e.target.classList.contains("sshFileCheck")) return;
      toggleSSHSelection(path, row);
    };
    // Double-click: enter folder or preview file.
    row.ondblclick = (e) => {
      if (e.target.closest(".sshFileActionBtn") || e.target.classList.contains("sshFileCheck")) return;
      if (isDir) { loadSSHFiles(path); }
      else { previewSSHFile(path); }
    };
    const check = row.querySelector(".sshFileCheck");
    if (check) check.onchange = () => { toggleSSHSelection(path, row); };
    row.querySelectorAll(".sshFileActionBtn").forEach((btn) => {
      btn.onclick = (e) => { e.stopPropagation(); handleSSFAction(btn.dataset.action, path, isDir); };
    });
  });
}

function toggleSSHSelection(path, row) {
  if (ssh.selected.has(path)) { ssh.selected.delete(path); row.classList.remove("selected"); }
  else { ssh.selected.add(path); row.classList.add("selected"); }
  row.querySelector(".sshFileCheck").checked = ssh.selected.has(path);
  updateSSHToolbar();
}

function updateSSHToolbar() {
  const toolbar = $("sshToolbar");
  const delBtn = $("sshDeleteSelectedBtn");
  toolbar.hidden = !ssh.activeConn;
  delBtn.hidden = ssh.selected.size === 0;
  delBtn.textContent = `删除 (${ssh.selected.size})`;
}

async function handleSSFAction(action, path, isDir) {
  if (action === "preview") { await previewSSHFile(path); }
  else if (action === "delete") { await deleteSSFFiles([path]); }
}

// ——— SSH Floating Windows ———

const sshWindows = new Map(); // path -> { el, textarea, status, savedContent }
let sshWindowZIndex = 1000;

async function previewSSHFile(path) {
  // Don't reopen if already open.
  if (sshWindows.has(path)) {
    focusSSHWindow(path);
    return;
  }
  try {
    const data = await api(`/api/ssh/preview?conn_id=${encodeURIComponent(ssh.activeConn)}&path=${encodeURIComponent(path)}`);
    createSSHWindow(data.name, path, data.content);
  } catch (err) { toast(err.message, "error"); }
}

function createSSHWindow(name, path, content) {
  const win = document.createElement("div");
  win.className = "sshWindow";
  win.style.left = (80 + sshWindows.size * 30) + "px";
  win.style.top = (60 + sshWindows.size * 30) + "px";
  win.style.zIndex = ++sshWindowZIndex;
  win.dataset.path = path;

  win.innerHTML = `
    <div class="sshWindowHeader">
      <span class="sshWindowName">${escapeHtml(name)}</span>
      <div class="sshWindowActions">
        <button type="button" class="btn sm sshWindowBtn" data-action="save">保存</button>
        <button type="button" class="sshWindowClose" data-action="close">×</button>
      </div>
    </div>
    <div class="sshWindowBody">
      <textarea spellcheck="false">${escapeHtml(content)}</textarea>
    </div>
    <div class="sshWindowFooter">
      <span class="sshWindowStatus">已加载</span>
      <span class="sshWindowStatus">${escapeHtml(path)}</span>
    </div>
  `;

  $("sshWindows").append(win);

  const textarea = win.querySelector("textarea");
  const status = win.querySelector(".sshWindowStatus");
  const savedContent = content;

  sshWindows.set(path, { el: win, textarea, status, savedContent, name });

  // Make draggable.
  const header = win.querySelector(".sshWindowHeader");
  makeDraggable(win, header);

  // Focus on click.
  win.onmousedown = () => { win.style.zIndex = ++sshWindowZIndex; };

  // Close button.
  win.querySelector(".sshWindowClose").onclick = () => closeSSHWindow(path);

  // Save button.
  win.querySelector('[data-action="save"]').onclick = () => saveSSHFile(path);

  // Track dirty state.
  textarea.addEventListener("input", () => {
    const w = sshWindows.get(path);
    if (w) {
      status.textContent = textarea.value !== w.savedContent ? "未保存" : "已保存";
      status.className = "sshWindowStatus " + (textarea.value !== w.savedContent ? "dirty" : "saved");
    }
  });

  // Keyboard shortcut: Ctrl+S to save.
  textarea.addEventListener("keydown", (e) => {
    if ((e.ctrlKey || e.metaKey) && e.key === "s") {
      e.preventDefault();
      saveSSHFile(path);
    }
  });

  status.textContent = "已加载";
  status.className = "sshWindowStatus saved";
}

function makeDraggable(win, handle) {
  let startX, startY, startLeft, startTop;
  handle.addEventListener("mousedown", (e) => {
    if (e.target.closest(".sshWindowActions")) return;
    e.preventDefault();
    startX = e.clientX;
    startY = e.clientY;
    const rect = win.getBoundingClientRect();
    startLeft = rect.left;
    startTop = rect.top;
    win.classList.add("dragging");
    const onMove = (ev) => {
      win.style.left = (startLeft + ev.clientX - startX) + "px";
      win.style.top = (startTop + ev.clientY - startY) + "px";
    };
    const onUp = () => {
      win.classList.remove("dragging");
      document.removeEventListener("mousemove", onMove);
      document.removeEventListener("mouseup", onUp);
    };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  });
}

function focusSSHWindow(path) {
  const w = sshWindows.get(path);
  if (w) {
    w.el.style.zIndex = ++sshWindowZIndex;
    w.textarea.focus();
  }
}

function closeSSHWindow(path) {
  const w = sshWindows.get(path);
  if (!w) return;
  // Warn if unsaved.
  if (w.textarea.value !== w.savedContent) {
    if (!confirm("有未保存的修改，确定关闭？")) return;
  }
  w.el.remove();
  sshWindows.delete(path);
}

async function saveSSHFile(path) {
  const w = sshWindows.get(path);
  if (!w) return;
  try {
    await api("/api/ssh/save", {
      method: "PUT",
      body: JSON.stringify({ conn_id: ssh.activeConn, path, content: w.textarea.value }),
    });
    w.savedContent = w.textarea.value;
    w.status.textContent = "已保存";
    w.status.className = "sshWindowStatus saved";
    toast("已保存", "success");
  } catch (err) {
    toast(err.message, "error");
    w.status.textContent = "保存失败";
    w.status.className = "sshWindowStatus dirty";
  }
}

async function deleteSSFFiles(paths) {
  const msg = paths.length === 1 ? `确定删除 "${paths[0].split("/").pop()}"？` : `确定删除选中的 ${paths.length} 个文件？`;
  if (!confirm(msg)) return;
  try {
    await deleteSSHFiles(ssh.activeConn, paths);
    ssh.selected.clear();
    await loadSSHFiles(ssh.currentPath);
    toast("已删除", "success");
  } catch (err) { toast(err.message, "error"); }
}

function getFileIcon(name) {
  const ext = name.split(".").pop().toLowerCase();
  const icons = { py: "🐍", js: "📜", ts: "📘", json: "📋", md: "📝", sh: "⚙️", yaml: "📄", yml: "📄", txt: "📄", log: "📃", html: "🌐", css: "🎨", go: "🔵", rs: "🦀", java: "☕", c: "©️", cpp: "➕", rb: "💎", php: "🐘" };
  return icons[ext] || "📄";
}

function formatSize(bytes) {
  if (bytes < 1024) return bytes + " B";
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KB";
  if (bytes < 1024 * 1024 * 1024) return (bytes / 1048576).toFixed(1) + " MB";
  return (bytes / 1073741824).toFixed(1) + " GB";
}

// ——— SSH Dialog Handlers ———

function showSSHConnDialog(existing) {
  $("sshConnDialogTitle").textContent = existing ? "编辑连接" : "新建连接";
  $("sshConnName").value = existing?.name || "";
  $("sshConnHost").value = existing?.host || "";
  $("sshConnPort").value = existing?.port || 22;
  $("sshConnUser").value = existing?.user || "";
  $("sshConnAuthType").value = existing?.auth_type || "key";
  $("sshConnKeyPath").value = existing?.key_path || "~/.ssh/id_rsa";
  updateSSHAuthUI();
  $("sshConnDialog").showModal();
}

async function importSSHConfig() {
  try {
    const data = await api("/api/ssh/import-ssh-config", { method: "POST", body: JSON.stringify({}) });
    const available = data.available || [];
    if (!available.length) { toast("~/.ssh/config 中没有找到连接", "info"); return; }
    // Let user pick which to import.
    const picked = await customPrompt(
      `找到 ${available.length} 个连接。输入要导入的序号（逗号分隔，或 all 全部）：`,
      "all"
    );
    if (picked === null) return;
    let ids = [];
    if (picked.trim().toLowerCase() === "all") {
      ids = available.map((c) => c.id);
    } else {
      const idxs = picked.split(",").map((s) => parseInt(s.trim()) - 1).filter((i) => i >= 0 && i < available.length);
      ids = idxs.map((i) => available[i].id);
    }
    const result = await api("/api/ssh/import-ssh-config", { method: "POST", body: JSON.stringify({ ids }) });
    toast(`已导入 ${result.imported?.length || 0} 个连接`, "success");
    await loadSSHConnections();
  } catch (err) { toast(err.message, "error"); }
}

function updateSSHAuthUI() {
  const isKey = $("sshConnAuthType").value === "key";
  $("sshConnKeyPathWrap").hidden = !isKey;
  $("sshConnPasswordWrap").hidden = isKey;
}

async function saveSSHConnection() {
  const cfg = {
    id: ssh.editingConn || "ssh_" + Date.now(),
    name: $("sshConnName").value.trim(),
    host: $("sshConnHost").value.trim(),
    port: parseInt($("sshConnPort").value) || 22,
    user: $("sshConnUser").value.trim(),
    auth_type: $("sshConnAuthType").value,
    key_path: $("sshConnKeyPath").value.trim(),
  };
  if (!cfg.name || !cfg.host || !cfg.user) { toast("请填写完整", "error"); return; }
  const url = ssh.editingConn ? `/api/ssh/connections/${cfg.id}` : "/api/ssh/connections";
  const method = ssh.editingConn ? "PUT" : "POST";
  await api(url, { method, body: JSON.stringify(cfg) });
  $("sshConnDialog").close();
  ssh.editingConn = null;
  await loadSSHConnections();
  toast("已保存", "success");
}

// Navigation
$("navHomeBtn").onclick = () => showView("home");
$("navSettingsBtn").onclick = () => showView("settings");
$("navRoutingBtn").onclick = () => showView("routing");
$("navSubscriptionProxyBtn").onclick = () => showView("subscriptionProxy");
$("navSSHBtn").onclick = () => showView("ssh");
if ($("refreshCacheStatsBtn")) {
  $("refreshCacheStatsBtn").onclick = () => run(loadCacheStats, {
    button: $("refreshCacheStatsBtn"),
    busyLabel: "刷新中…",
  });
}
if ($("cacheStatsHours")) {
  $("cacheStatsHours").onchange = () => {
    loadCacheStats().catch((err) => toast(err.message, "error"));
  };
}
$("backFromSubscriptionProxyBtn").onclick = () => showView("home");
$("subscriptionRefreshBtn").onclick = () => run(loadSubscriptionProxy, { button: $("subscriptionRefreshBtn"), busyLabel: "刷新中…" });
for (const action of ["start", "stop", "restart"]) $("subscription" + action[0].toUpperCase() + action.slice(1) + "Btn").onclick = (event) => run(async () => {
  const result = await api("/api/subscription-proxy/service", { method: "POST", body: JSON.stringify({ action }) });
  if (result?.service) renderSubscriptionService(result.service);
  // Account/model endpoints are unavailable while stopped and can briefly lag
  // during restart. Refresh them only after a successful start/restart.
  if (action !== "stop") await loadSubscriptionProxy();
}, { button: event.currentTarget, busyLabel: "处理中…", success: action === "stop" ? "订阅代理已停止" : action === "restart" ? "订阅代理已重启" : "订阅代理已启动" });
document.querySelectorAll(".subscriptionLoginBtn").forEach((button) => button.onclick = () => {
  if (subscriptionLoginBusy) return;
  subscriptionLoginBusy = true;
  run(async () => {
    const result = await api("/api/subscription-proxy/login", {
      method: "POST",
      body: JSON.stringify({ provider: button.dataset.provider }),
    });
    const session = result.session || result;
    renderSubscriptionLoginSession(session);
    toast("已打开浏览器，请用要添加的账号完成授权", "info");
  }, { button, busyLabel: "打开浏览器…" }).finally(() => {
    subscriptionLoginBusy = false;
  });
});
$("saveSubscriptionModelsBtn").onclick = () => run(async () => { const selected = [...document.querySelectorAll("#subscriptionModels input:checked")].map((input) => ({ id: input.value, provider: input.dataset.provider })); await api("/api/subscription-proxy/models", { method: "PUT", body: JSON.stringify({ models: selected }) }); await loadSubscriptionProxy(); }, { button: $("saveSubscriptionModelsBtn"), busyLabel: "保存中…", success: "模型已保存" });
document.querySelectorAll(".subscriptionProviderBtn").forEach((button) => button.onclick = () => run(async () => {
  const provider = button.dataset.provider;
  const result = await api("/api/subscription-proxy/providers", { method: "POST", body: JSON.stringify({ provider }) });
  await refreshAll();
  await loadSubscriptionProxy();
  const profile = result?.providers?.[0];
  toast(`${profile?.name || "订阅代理供应商"}已同步；请到“模型路由”选择要使用的模型`, "success");
}, { button, busyLabel: "同步中…" }));
$("runSubscriptionDiagnosticsBtn").onclick = () => run(async () => { const result = await api("/api/subscription-proxy/diagnostics", { method: "POST" }); $("subscriptionDiagnostics").textContent = JSON.stringify(result, null, 2); }, { button: $("runSubscriptionDiagnosticsBtn"), busyLabel: "诊断中…" });
$("backFromEditBtn").onclick = () => showView("home");
$("backFromSettingsBtn").onclick = () => showView("home");
$("addBtn").onclick = () => openEdit(newProfileDraft());
$("emptyNewBtn").onclick = () => openEdit(newProfileDraft());

// SSH event handlers
$("sshNewConnBtn").onclick = () => { ssh.editingConn = null; showSSHConnDialog(null); };
$("sshImportBtn").onclick = () => importSSHConfig();
$("sshRefreshBtn").onclick = () => loadSSHConnections();
$("sshRefreshFilesBtn").onclick = () => loadSSHFiles(ssh.currentPath);
$("sshSelectAll").onchange = (e) => {
  const rows = document.querySelectorAll(".sshFileRow");
  rows.forEach((row) => {
    const path = row.dataset.path;
    if (e.target.checked) { ssh.selected.add(path); row.classList.add("selected"); }
    else { ssh.selected.delete(path); row.classList.remove("selected"); }
    row.querySelector(".sshFileCheck").checked = e.target.checked;
  });
  updateSSHToolbar();
};
$("sshDeleteSelectedBtn").onclick = () => deleteSSFFiles([...ssh.selected]);
document.querySelectorAll(".sshFilter").forEach((btn) => {
  btn.onclick = () => {
    const active = ssh.filter === btn.dataset.filter;
    ssh.filter = active ? null : btn.dataset.filter;
    document.querySelectorAll(".sshFilter").forEach((b) => b.classList.toggle("active", b.dataset.filter === ssh.filter && !active));
    loadSSHFiles(ssh.currentPath);
  };
});
$("sshConnCancel").onclick = () => $("sshConnDialog").close();
$("sshConnForm").onsubmit = (e) => { e.preventDefault(); saveSSHConnection(); };
$("sshConnAuthType").onchange = updateSSHAuthUI;
$("reapplyBtn").onclick = () => run(async () => {
  if (!(await reapplyRouting())) return false;
  await refreshAll();
}, {
  button: $("reapplyBtn"),
  busyLabel: "重新应用中…",
  success: "当前模型路由已重新应用",
});
$("openConfigFromDriftBtn").onclick = () => showView("settings");

$("reloadConfigBtn").onclick = () => run(async () => {
  await loadConfigEditor();
}, { button: $("reloadConfigBtn"), busyLabel: "加载中…", success: "已重新加载" });
$("saveConfigBtn").onclick = () => saveConfigEditor($("saveConfigBtn"));
if ($("refreshPreviewBtn")) {
  $("refreshPreviewBtn").onclick = () => run(async () => {
    await refreshProviderConfigPreview();
  }, { button: $("refreshPreviewBtn"), busyLabel: "生成中…" });
}
if ($("previewFullConfig")) {
  $("previewFullConfig").onchange = () => refreshProviderConfigPreview();
}
if ($("configPreviewBlock")) {
  $("configPreviewBlock").addEventListener("toggle", () => {
    if ($("configPreviewBlock").open) refreshProviderConfigPreview();
  });
}
["name", "baseUrl", "profileApiKey", "defaultModel", "defaultReasoningEffort", "upstreamFormat"].forEach((id) => {
  const el = $(id);
  if (!el) return;
  el.addEventListener("input", scheduleProviderPreview);
  el.addEventListener("change", scheduleProviderPreview);
  if (id === "baseUrl") {
    el.addEventListener("input", syncModelBaseURLs);
    el.addEventListener("change", syncModelBaseURLs);
  }
  if (id === "defaultModel") el.addEventListener("change", updateReasoningEffortMetadata);
});

if ($("providerSearch")) {
  $("providerSearch").value = state.search || "";
  $("providerSearch").oninput = () => {
    state.search = $("providerSearch").value;
    renderProfiles();
  };
}
if ($("layoutCardBtn")) {
  $("layoutCardBtn").onclick = () => {
    state.layout = "card";
    applyLayoutUI();
    renderProfiles();
  };
}
if ($("layoutListBtn")) {
  $("layoutListBtn").onclick = () => {
    state.layout = "list";
    applyLayoutUI();
    renderProfiles();
  };
}

// Edit form
$("cancelBtn").onclick = () => fillForm(newProfileDraft());
$("upstreamFormat").onchange = syncModelBackends;
$("detectReasoningEffortsBtn").onclick = () => run(detectReasoningEfforts, {
  button: $("detectReasoningEffortsBtn"), busyLabel: "检测中…",
});
$("copyProfileBtn").onclick = () => {
  const current = readForm();
  if (!current.name && !current.base_url) {
    toast("请先填写供应商信息", "error");
    return;
  }
  copyProfile(current);
};
$("exportProfileBtn").onclick = () => {
  const current = readForm();
  if (!current.name && !current.base_url) {
    toast("请先填写供应商信息", "error");
    return;
  }
  exportProfile(current);
};
$("importProfileJsonBtn").onclick = () => $("importProfileFile").click();
$("importProfileFile").onchange = async (event) => {
  const file = event.target.files?.[0];
  event.target.value = "";
  if (!file) return;
  try {
    importProfileJSON(await file.text());
  } catch (err) {
    toast(err.message || String(err), "error");
  }
};

$("privacyProtectBtn").onclick = () => run(async () => {
	await api("/api/config/privacy", { method: "POST" });
	if ($("configPreviewBlock")?.open) await refreshProviderConfigPreview();
}, {
	button: $("privacyProtectBtn"),
	busyLabel: "应用中…",
	success: "隐私保护配置已写入 config.toml",
});
$("modelSearchInput").oninput = renderModelSelect;
$("toggleProfileKey").onclick = () => {
  const input = $("profileApiKey");
  input.type = input.type === "password" ? "text" : "password";
  $("toggleProfileKey").textContent = input.type === "password" ? "显示" : "隐藏";
};
$("addModelBtn").onclick = () => {
  addModelCard();
  syncEnabledModelList();
};
$("testConnectionBtn").onclick = () => run(async () => {
  const current = readForm();
  if (!current.base_url) throw new Error("先填写服务地址");
  if (!current.api_key) throw new Error("先填写 API Key");
  const result = await api("/api/connection/test", {
    method: "POST",
    body: JSON.stringify({
      profile_id: current.id,
      base_url: current.base_url,
      api_key: current.api_key,
      upstream_format: current.upstream_format,
    }),
  });
  if (!result.ok) {
    showConnectionStatus(false, `失败 ${result.latency_ms}ms：${result.error}`);
    throw new Error(result.error || "连接失败");
  }
  if (result.sample_models?.length) {
    state.availableModels = unique([...(state.availableModels || []), ...result.sample_models]);
    renderModelSelect();
  }
  showConnectionStatus(true, `成功 · ${result.latency_ms}ms · ${result.model_count} 模型`);
  toast(`连接成功（${result.latency_ms}ms）`, "success");
}, { button: $("testConnectionBtn"), busyLabel: "测试中…" });
$("fetchModelsBtn").onclick = () => run(async () => {
  const current = readForm();
  if (!current.base_url) throw new Error("先填写服务地址");
  if (!current.api_key) throw new Error("先填写 API Key");
  const result = await api("/api/models/fetch", {
    method: "POST",
    body: JSON.stringify({
      profile_id: current.id,
      base_url: current.base_url,
      api_key: current.api_key,
      upstream_format: current.upstream_format,
    }),
  });
  state.availableModels = unique(result.models);
  renderModelSelect();
  if ($("connectBlock")) $("connectBlock").open = true;
  showConnectionStatus(true, `已获取 ${result.models.length} 个模型`);
  toast(`获取到 ${result.models.length} 个模型`, "success");
}, { button: $("fetchModelsBtn"), busyLabel: "拉取中…" });

$("profileForm").onsubmit = (event) => {
  event.preventDefault();
  run(async () => {
    const saved = await saveCurrentProfile();
    await refreshAll();
    if (saved?.id) {
      const latest = state.profiles.find((p) => p.id === saved.id) || saved;
      fillForm(latest);
    }
  }, { button: $("saveProfileBtn"), busyLabel: "保存中…", success: "已保存" });
};

$("settingsForm").onsubmit = (event) => {
  event.preventDefault();
  run(async () => {
    const settings = {
      ...state.settings,
      autostart: $("autostart").checked,
      silent_autostart: $("silentAutostart").checked,
      auto_open_browser: $("autoOpenBrowser").checked,
      lan_access_enabled: $("lanAccessEnabled").checked,
      theme: "light",
      port: Number($("port").value || 17878),
    };
    await api("/api/settings", { method: "PUT", body: JSON.stringify(settings) });
    await refreshAll();
  }, { button: $("saveSettingsBtn"), busyLabel: "保存中…", success: "设置已保存" });
};

$("lanAccessAddress").onchange = () => {
  const index = Number($("lanAccessAddress").value);
  renderLANAddress(state.lanAccess?.addresses?.[index], state.lanAccess);
};

$("copyLanAccessUrlBtn").onclick = () => run(async () => {
  await copyText($("lanAccessUrl").value, "手机配对地址已复制");
}, { button: $("copyLanAccessUrlBtn"), busyLabel: "复制中…" });

$("refreshLanPairingBtn").onclick = () => run(async () => {
  state.lanAccess = await api("/api/lan-access", { method: "POST" });
  renderLANAccess(state.lanAccess);
}, { button: $("refreshLanPairingBtn"), busyLabel: "生成中…", success: "新的配对二维码已生成" });

function scheduleRefresh() {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => {
    refreshAll().catch((err) => toast(err.message, "error"));
  }, 400);
}

document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "visible") scheduleRefresh();
});
window.addEventListener("focus", () => scheduleRefresh());
document.addEventListener("keydown", (event) => {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") {
    if (state.view === "edit") {
      event.preventDefault();
      $("saveProfileBtn").click();
    }
  }
  if (event.key === "Escape" && state.view !== "home") {
    showView("home");
  }
});

// ——— Routing View ———
async function loadRoutingView() {
  const routingStatus = $("routingStatus");
  const catalog = $("routingCatalog");
  if (routingStatus) routingStatus.textContent = "加载中…";
  if (catalog) catalog.innerHTML = '<p class="muted tiny">加载中…</p>';
  if ($("collaborationBadge")) {
    $("collaborationBadge").textContent = "加载中";
    $("collaborationBadge").dataset.state = "stopped";
  }
  const [routingSnapshot, collaborationResult, collaborationSpecResult] = await Promise.all([
    api("/api/routing"),
    api("/api/collaboration").then((status) => ({ status, error: null })).catch((error) => ({ status: null, error })),
    loadCollaborationSpec().then((spec) => ({ spec, error: spec ? null : new Error(state.collaborationSpecIssue) })),
  ]);
  renderRouting(routingSnapshot);
  const collaborationStatus = collaborationResult.status || {
    configured: false,
    valid: false,
    unavailable: true,
    issues: [collaborationResult.error?.message || "Max Collaboration 状态仅允许在本机查看"],
  };
  renderCollaboration(routingSnapshot, collaborationStatus);
}

function renderRouting(snapshot) {
  if (!snapshot) return;
  // Provider selection replaces the old opt.dataset.official mixed catalog.
  // updateRoutingReasoningEfforts is now scoped inside renderProviderPolicy.
  state.routing = snapshot;
  const providers = snapshot.providers || [];
  const activeProviderID = snapshot.active_provider_id || "";
  const providerSelect = $("routingProvider");
  providerSelect.replaceChildren();
  if (snapshot.official_logged_in) {
    const option = document.createElement("option");
    option.value = OFFICIAL_PROVIDER_KEY;
    option.textContent = "官方账号（grok.com）";
    providerSelect.append(option);
  }
  for (const provider of providers) {
    const option = document.createElement("option");
    option.value = provider.id;
    option.textContent = provider.name;
    providerSelect.append(option);
  }
  providerSelect.value = activeProviderID;

  const renderProviderPolicy = (providerID) => {
    const official = providerID === OFFICIAL_PROVIDER_KEY;
    const policy = snapshot.provider_policies?.[providerID] || (providerID === activeProviderID ? snapshot.policy : {}) || {};
    const routes = official ? (snapshot.official_models || []) : (snapshot.model_routes || []).filter((route) => route.provider_id === providerID);
    const webSearchRoutes = capableWebSearchRoutes(routes, official);
    const warning = $("routingCompatibilityWarning");
    warning.hidden = false;
    warning.textContent = official
      ? "切换到官方账号会移除 config.toml 中的自定义模型定义和自定义认证；切回自定义供应商时会从 Profile 目录重建完整自定义模型目录。"
      : "只允许从当前供应商选择 default、web_search、explore 和 plan。切换自定义供应商时，config.toml 仍保留所有自定义模型定义，以兼容旧会话固定的旧别名。";
    const values = { routingDefault: policy.default || "", routingWebSearch: policy.web_search || "", routingExplore: policy.subagents?.explore || "", routingPlan: policy.subagents?.plan || "" };
    for (const [id, value] of Object.entries(values)) {
      const select = $(id);
      select.innerHTML = id === "routingDefault" ? "" : '<option value="">（未设置）</option>';
      const selectRoutes = id === "routingWebSearch" ? webSearchRoutes : routes;
      for (const route of selectRoutes) {
        const option = document.createElement("option");
        option.value = route.id;
        option.dataset.routeName = route.name;
        option.textContent = `${route.name} — ${route.model || route.profile_model}`;
        select.append(option);
      }
      select.value = value;
    }
    const updateEfforts = () => {
      const route = routes.find((item) => item.id === $("routingDefault").value);
      const supported = route?.supports_reasoning_effort ? (route.reasoning_efforts || []).filter((effort) => REASONING_EFFORTS.includes(effort) && effort !== "none") : [];
      const options = supported.length ? supported : ["none"];
      const select = $("routingReasoningEffort");
      select.disabled = supported.length === 0;
      select.replaceChildren(...options.map((effort) => { const option = document.createElement("option"); option.value = effort; option.textContent = REASONING_EFFORT_LABELS[effort]; return option; }));
      select.value = options.includes(policy.default_reasoning_effort) ? policy.default_reasoning_effort : fallbackReasoningEffort(options);
    };
    $("routingDefault").onchange = updateEfforts;
    updateEfforts();
  };
  providerSelect.onchange = () => {
    renderProviderPolicy(providerSelect.value);
    if (providerSelect.value !== activeProviderID) {
      renderCollaboration({ ...snapshot, active_provider_id: "" }, state.collaboration || {});
    } else {
      renderCollaboration(snapshot, state.collaboration || {});
    }
  };
  renderProviderPolicy(activeProviderID);

  const modelRoutes = snapshot.model_routes || [];
  $("routingStatus").textContent = `更新于 ${snapshot.updated_at ? new Date(snapshot.updated_at).toLocaleTimeString("zh-CN") : "—"} · ${providers.length} 个自定义供应商 · ${modelRoutes.length} 个模型`;
  $("routingModelCount").textContent = `${modelRoutes.length} 个`;
  const byProvider = {};
  for (const route of modelRoutes) (byProvider[route.provider_id] ||= []).push(route);
  $("routingCatalog").innerHTML = modelRoutes.length ? Object.entries(byProvider).map(([providerID, routes]) => {
    const provider = providers.find((item) => item.id === providerID);
    return `<section class="routingCatalogGroup"><div class="routingCatalogHead"><strong>${escapeHtml(provider?.name || providerID)}</strong><span class="muted tiny">${routes.length} 个模型</span></div><div class="routingCatalogModels">${routes.map((route) => `<div class="routingCatalogModel"><div class="routingCatalogModelInfo"><strong>${escapeHtml(route.name)}</strong><code>${escapeHtml(route.model)}</code><span class="muted tiny">backend: ${escapeHtml(route.api_backend || "")}</span></div><div class="routingCatalogModelMeta">${route.supports_backend_search ? '<span class="badge active">搜索</span>' : '<span class="badge">无搜索</span>'}${route.supports_reasoning_effort ? `<span class="badge ${collaborationRouteSupportsMax(route) ? "active" : ""}">${escapeHtml(collaborationCapabilityLabel(route))}</span>` : ""}</div></div>`).join("")}</div></section>`;
  }).join("") : '<div class="routingUnavailable"><strong>暂无可用模型</strong><p>请先添加至少一个包含模型的供应商。</p></div>';
}

function saveRoutingPolicy() {
  const providerID = $("routingProvider").value;
  const payload = {
    active_provider_id: providerID,
    default: $("routingDefault").value,
    default_reasoning_effort: $("routingReasoningEffort").value || "none",
    web_search: $("routingWebSearch").value,
    subagents: { explore: $("routingExplore").value, plan: $("routingPlan").value },
  };
  run(async () => {
    const activeProviderID = state.routing?.active_provider_id || "";
    if (providerID !== activeProviderID) {
      const provider = state.routing?.providers?.find((item) => item.id === providerID);
      const warning = providerID === OFFICIAL_PROVIDER_KEY
        ? officialProviderSwitchWarning(activeProviderID)
        : customProviderSwitchWarning(activeProviderID, provider || { id: providerID, name: providerID });
      if (warning && !(await customConfirm(warning, { okLabel: providerID === OFFICIAL_PROVIDER_KEY ? "切换到官方" : "确认切换", danger: providerID === OFFICIAL_PROVIDER_KEY }))) return false;
    }
    await api("/api/routing/policy", { method: "PUT", body: JSON.stringify(payload) });
    await refreshAll();
    await loadRoutingView();
  }, { button: $("saveRoutingPolicyBtn"), busyLabel: "保存中…", success: "已启用供应商并保存其路由策略" });
}

// ——— Routing Event Handlers ———
$("refreshRoutingBtn").onclick = () => run(loadRoutingView, { button: $("refreshRoutingBtn"), busyLabel: "刷新中…" });
$("backFromRoutingBtn").onclick = () => showView("home");
$("saveRoutingPolicyBtn").onclick = () => saveRoutingPolicy();
$("previewCollaborationBtn").onclick = () => run(() => previewCollaboration(), { button: $("previewCollaborationBtn"), busyLabel: "预览中…", success: "预览已生成，尚未写入任何文件" });
$("applyCollaborationBtn").onclick = () => run(applyCollaborationPreview, { button: $("applyCollaborationBtn"), busyLabel: "应用中…", success: "Max Collaboration 已应用；Switch 未启动任何 agent" });
$("disableCollaborationBtn").onclick = () => run(disableCollaboration, { button: $("disableCollaborationBtn"), busyLabel: "停用中…", success: "Max Collaboration 已停用，受管文件已保留" });
$("copyCollaborationLaunchBtn").onclick = () => run(async () => {
  const launch = collaborationLaunchParameters();
  if (!launch.objective) throw new Error("请先填写任务目标");
  await copyText(launch.instruction, `${launch.label} 启动指令已复制（agent_budget=${launch.budget}）`);
}, { button: $("copyCollaborationLaunchBtn"), busyLabel: "复制中…" });

showView("home");
refreshAll().catch((err) => toast(err.message, "error"));
