#!/usr/bin/env node

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const appPath = path.join(__dirname, "app.js");
const appSource = fs.readFileSync(appPath, "utf8");
const htmlSource = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");
const testableSource = appSource.split("// Custom confirm dialog")[0] + `
this.appTest = {
  api,
  csrfToken,
  newProfileDraft,
  normalizeReasoningEffort,
  preservedDefaultReasoningEffort,
  reasoningEffortOptions,
  preservedReasoningEfforts,
  customPrompt,
  customProviderSwitchWarning,
  officialProviderSwitchWarning,
  capableWebSearchRoutes,
  renderDrift,
  reapplyRouting,
  deleteSSHFiles,
  modelSupportsBackendSearch,
  suggestContextWindow,
  minimatch,
  setStatus(value) { state.status = value; },
  setRouting(value) { state.routing = value; },
  formatTokenCount,
  formatHitRate,
  cacheTableHTML,
  loadCacheStats,
  normalizedCodeBuddySelection,
  buildCodeBuddySavePayload,
  resetCSRF() { csrfTokenPromise = null; },
};
`;

function response(status, data = {}, statusText = "") {
  return {
    ok: status >= 200 && status < 300,
    status,
    statusText,
    async json() { return data; },
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function loadApp(fetchImpl, elements = {}, confirmImpl = () => true) {
  const context = {
    console,
    fetch: fetchImpl,
    document: {
      getElementById(id) { return elements[id] || null; },
      createElement() { return { value: "", textContent: "", disabled: false }; },
    },
    localStorage: { getItem() { return null; } },
    window: { confirm: confirmImpl },
    customConfirm: async (...args) => confirmImpl(...args),
    setTimeout,
    clearTimeout,
  };
  vm.createContext(context);
  vm.runInContext(testableSource, context, { filename: appPath });
  return context.appTest;
}

test("CodeBuddy selection keeps only catalog models and constrains the default", () => {
  const app = loadApp(async () => response(500));
  const selection = app.normalizedCodeBuddySelection(
    ["hy3", "hy4-preview", "glm-5.3"],
    ["hy4-preview", "missing", "hy4-preview"],
    "hy3",
  );
  assert.deepEqual([...selection.models], ["hy3", "hy4-preview", "glm-5.3"]);
  assert.deepEqual([...selection.enabledModels], ["hy4-preview"]);
  assert.equal(selection.defaultModel, "hy4-preview");

  const payload = app.buildCodeBuddySavePayload(
    selection.models,
    selection.enabledModels,
    selection.defaultModel,
    { activate: true },
  );
  assert.deepEqual([...payload.enabled_models], ["hy4-preview"]);
  assert.equal(payload.default_model, "hy4-preview");
  assert.equal(payload.activate, true);
});

test("CodeBuddy save rejects an empty subset or a default outside it", () => {
  const app = loadApp(async () => response(500));
  assert.throws(
    () => app.buildCodeBuddySavePayload(["hy4-preview"], [], "", { activate: false }),
    /至少选择一个/,
  );
  assert.throws(
    () => app.buildCodeBuddySavePayload(["hy3", "hy4-preview"], ["hy4-preview"], "hy3", { activate: false }),
    /必须属于/,
  );
});

test("custom prompt resolves null on Escape and clears every handler", async () => {
  const dialog = {
    open: false,
    oncancel: null,
    showModal() { this.open = true; },
    close() { this.open = false; },
  };
  const input = {
    value: "",
    onkeydown: null,
    focus() {},
    select() {},
  };
  const label = { textContent: "" };
  const ok = { onclick: null };
  const cancel = { onclick: null };
  const app = loadApp(async () => response(500), {
    promptDialog: dialog,
    promptInput: input,
    promptLabel: label,
    promptOk: ok,
    promptCancel: cancel,
  });

  const pending = app.customPrompt("SSH password", "secret");
  assert.equal(typeof dialog.oncancel, "function");
  let prevented = false;
  dialog.oncancel({ preventDefault() { prevented = true; } });
  assert.equal(await pending, null);
  assert.equal(prevented, true);
  assert.equal(dialog.open, false);
  assert.equal(input.onkeydown, null);
  assert.equal(ok.onclick, null);
  assert.equal(cancel.onclick, null);
  assert.equal(dialog.oncancel, null);
});

test("suggestContextWindow resolves known leaves and leaves unknown models unset", () => {
  const app = loadApp(async () => response(500));
  assert.equal(app.suggestContextWindow("k3-256k"), 262144);
  assert.equal(app.suggestContextWindow("K3-256K"), 262144);
  assert.equal(app.suggestContextWindow("subscription/codex/gpt-5.6-sol"), 320000);
  assert.equal(app.suggestContextWindow("subscription/codex/gpt-5.6-sol-fast"), 320000);
  assert.equal(app.suggestContextWindow("subscription/gemini/gemini-3.7-flash-high"), 1048576);
  assert.equal(app.suggestContextWindow("subscription/grok/grok-4.5"), 500000);
  assert.equal(app.suggestContextWindow("subscription/grok/grok-4.6"), 500000);
  assert.equal(app.suggestContextWindow("hy4-preview"), 1000000);
  assert.equal(app.suggestContextWindow("glm-5.3"), 1000000);
  assert.equal(app.suggestContextWindow("glm-5.3-flash"), 1000000);
  assert.equal(app.suggestContextWindow("kimi-k3-2"), 1000000);
  assert.equal(app.suggestContextWindow("hy3"), 128000);
  assert.equal(app.suggestContextWindow("deepseek-v4-flash"), 1000000);
  assert.equal(app.suggestContextWindow("deepseek-v4-pro"), 1000000);
  assert.equal(app.suggestContextWindow("unknown-model"), 0);
  assert.equal(app.suggestContextWindow("grok-4.5-mini"), 0);
  assert.equal(app.suggestContextWindow(""), 0);
});

test("SSH filename glob treats question mark as one arbitrary character", () => {
  const app = loadApp(async () => response(500));
  assert.equal(app.minimatch("ab.txt", "a?.txt"), true);
  assert.equal(app.minimatch("a..txt", "a?.txt"), true);
  assert.equal(app.minimatch("abc.txt", "a?.txt"), false);
  assert.equal(app.minimatch("aX.txt", "a?.txt"), true);
  assert.equal(app.minimatch("a[.txt", "a[.txt"), true);
  assert.equal(app.minimatch("a/b.txt", "a/b.txt"), true);
});

test("provider switch warnings distinguish custom and official activation", () => {
  const app = loadApp(async () => response(500));
  assert.equal(app.customProviderSwitchWarning("provider-one", { id: "provider-one", name: "One" }), "");
  assert.match(app.customProviderSwitchWarning("provider-one", { id: "provider-two", name: "Two" }), /default 改为「Two」/);
  assert.match(app.customProviderSwitchWarning("provider-one", { id: "provider-two", name: "Two" }), /混合显示各供应商已启用的模型/);
  assert.equal(app.officialProviderSwitchWarning("official"), "");
  assert.match(app.officialProviderSwitchWarning("provider-one"), /默认改为官方 Grok 模型/);
  assert.match(app.officialProviderSwitchWarning("provider-one"), /只显示官方目录/);
});

test("web search dropdown routes require responses backend and backend search support", () => {
  const app = loadApp(async () => response(500));
  const routes = [
    { id: "capable", api_backend: "responses", supports_backend_search: true },
    { id: "no-flag", api_backend: "responses", supports_backend_search: false },
    { id: "wrong-backend", api_backend: "chat_completions", supports_backend_search: true },
  ];
  assert.deepEqual(Array.from(app.capableWebSearchRoutes(routes)).map((route) => route.id), ["capable"]);
  assert.deepEqual(Array.from(app.capableWebSearchRoutes(routes, true)).map((route) => route.id), ["capable", "no-flag", "wrong-backend"]);
});

test("manual models do not claim backend search unless explicitly enabled", () => {
  const app = loadApp(async () => response(500));
  assert.equal(app.modelSupportsBackendSearch({}), false);
  assert.equal(app.modelSupportsBackendSearch({ supports_backend_search: false }), false);
  assert.equal(app.modelSupportsBackendSearch({ supports_backend_search: true }), true);
});

test("routing drift banner distinguishes config mismatch and routing repair", () => {
  const banner = { hidden: true, style: { display: "none" } };
  const title = { textContent: "" };
  const detail = { textContent: "" };
  const app = loadApp(async () => response(500), { driftBanner: banner, driftTitle: title, driftDetail: detail });

  app.setStatus({ config_matches_routing: false, active_routing: { repair_required: false } });
  app.renderDrift();
  assert.equal(banner.hidden, false);
  assert.match(detail.textContent, /default \/ web_search \/ explore \/ plan/);
  assert.match(detail.textContent, /保留无关 TOML 设置/);

  app.setStatus({ config_matches_routing: true, active_routing: { repair_required: true } });
  app.renderDrift();
  assert.match(title.textContent, /保存的默认模型设置需要修复/);
  assert.match(detail.textContent, /模型引用已过期/);

  app.setStatus({ config_matches_routing: false, active_routing: { repair_required: true } });
  app.renderDrift();
  assert.match(title.textContent, /都需要修复/);

  app.setStatus({ config_matches_routing: true, active_routing: { repair_required: false } });
  app.renderDrift();
  assert.equal(banner.hidden, true);
  assert.equal(banner.style.display, "none");
});

test("routing reapply requires confirmation and uses the unified endpoint with CSRF", async () => {
  const calls = [];
  const replies = [
    response(200, { token: "routing-token" }),
    response(200, { message: "路由策略已重新应用" }),
  ];
  const app = loadApp(async (url, options = {}) => {
    calls.push({ url, options });
    return replies.shift();
  });

  // No dialog is present in this harness, so customConfirm uses the affirmative window.confirm fallback.
  await app.reapplyRouting();
  assert.equal(calls.length, 2);
  assert.equal(calls[0].url, "/api/csrf");
  assert.equal(calls[1].url, "/api/routing/reapply");
  assert.equal(calls[1].options.method, "POST");
  assert.equal(calls[1].options.headers["X-Grok-Switch-CSRF"], "routing-token");

  const cancelledCalls = [];
  const cancelledApp = loadApp(async (...args) => {
    cancelledCalls.push(args);
    return response(500);
  }, {}, () => false);
  assert.equal(await cancelledApp.reapplyRouting(), false);
  assert.equal(cancelledCalls.length, 0);
});

test("SSH file deletion includes the encoded active connection ID", async () => {
  const calls = [];
  const replies = [
    response(200, { token: "ssh-delete-token" }),
    response(200, { ok: true }),
  ];
  const app = loadApp(async (url, options = {}) => {
    calls.push({ url, options });
    return replies.shift();
  });

  await app.deleteSSHFiles("connection id/with?reserved", ["/tmp/a.txt", "/tmp/b.txt"]);
  assert.equal(calls.length, 2);
  assert.equal(calls[0].url, "/api/csrf");
  assert.equal(calls[1].url, "/api/ssh/files?conn_id=connection%20id%2Fwith%3Freserved");
  assert.equal(calls[1].options.method, "DELETE");
  assert.equal(calls[1].options.headers["X-Grok-Switch-CSRF"], "ssh-delete-token");
  assert.deepEqual(JSON.parse(calls[1].options.body), { paths: ["/tmp/a.txt", "/tmp/b.txt"] });
});

test("cache statistics disclose session-level model attribution", () => {
  assert.match(appSource, /模型按会话当前模型近似归属/);
  assert.match(htmlSource, /会话中途切换模型时，历史 turn 可能被归到新模型/);
});

test("cache statistics render the nested report and escape labels", async () => {
  const elements = Object.fromEntries([
    "cacheStatsHours", "cacheHitRate", "cacheTurns", "cachePromptTokens", "cacheCachedTokens",
    "cacheCompletionTokens", "cacheReasoningTokens", "cacheStatsHint", "cacheByModel", "cacheRecent",
  ].map((id) => [id, { value: id === "cacheStatsHours" ? "24" : "", textContent: "", innerHTML: "" }]));
  let requestedURL = "";
  const app = loadApp(async (url) => {
    requestedURL = url;
    return response(200, {
      log_exists: true,
      scanned_events: 12,
      overall: {
        turns: 12,
        prompt_tokens: 1_000_000,
        cached_prompt_tokens: 750_000,
        completion_tokens: 120_000,
        reasoning_tokens: 80_000,
        hit_rate: 0.75,
      },
      by_model: [{
        model: `<img src=x onerror="fail()">`,
        turns: 12,
        prompt_tokens: 1_000_000,
        cached_prompt_tokens: 750_000,
        completion_tokens: 120_000,
        reasoning_tokens: 80_000,
        hit_rate: 0.75,
      }],
      recent: [{
        ts: "2026-07-31T12:00:00Z",
        session_id: `<bad-session>`,
        model: `<script>fail()</script>`,
        prompt_tokens: 1000,
        completion_tokens: 300,
        reasoning_tokens: 200,
        hit_rate: 0.5,
      }],
    });
  }, elements);

  await app.loadCacheStats();
  assert.equal(requestedURL, "/api/cache-stats?hours=24");
  assert.equal(elements.cacheHitRate.textContent, "75.0%");
  assert.equal(elements.cacheTurns.textContent, "12");
  assert.equal(elements.cachePromptTokens.textContent, "1.00M");
  assert.equal(elements.cacheCachedTokens.textContent, "750.0k");
  assert.equal(elements.cacheCompletionTokens.textContent, "120.0k");
  assert.equal(elements.cacheReasoningTokens.textContent, "80.0k");
  assert.match(elements.cacheStatsHint.textContent, /事件 12/);
  assert.match(elements.cacheByModel.innerHTML, /&lt;img src=x onerror=&quot;fail\(\)&quot;&gt;/);
  assert.match(elements.cacheByModel.innerHTML, /Reasoning/);
  assert.doesNotMatch(elements.cacheByModel.innerHTML, /<img/);
  assert.match(elements.cacheRecent.innerHTML, /&lt;script&gt;fail\(\)&lt;\/script&gt;/);
  assert.doesNotMatch(elements.cacheRecent.innerHTML, /<script>/);
});

test("cache statistics render empty and missing-log states", async () => {
  const elements = Object.fromEntries([
    "cacheStatsHours", "cacheHitRate", "cacheTurns", "cachePromptTokens", "cacheCachedTokens",
    "cacheCompletionTokens", "cacheReasoningTokens", "cacheStatsHint", "cacheByModel", "cacheRecent",
  ].map((id) => [id, { value: id === "cacheStatsHours" ? "6" : "", textContent: "", innerHTML: "" }]));
  const app = loadApp(async () => response(200, {
    log_exists: false,
    overall: {},
    by_model: [],
    recent: [],
  }), elements);

  await app.loadCacheStats();
  assert.equal(elements.cacheHitRate.textContent, "—");
  assert.equal(elements.cacheTurns.textContent, "0");
  assert.equal(elements.cachePromptTokens.textContent, "0");
  assert.equal(elements.cacheCompletionTokens.textContent, "0");
  assert.equal(elements.cacheReasoningTokens.textContent, "0");
  assert.match(elements.cacheStatsHint.textContent, /未找到 Grok 日志/);
  assert.match(elements.cacheByModel.innerHTML, /暂无数据/);
  assert.match(elements.cacheRecent.innerHTML, /暂无数据/);
});

test("new profiles default to disabled reasoning without preset metadata", () => {
  const app = loadApp(async () => response(500));
  const draft = app.newProfileDraft();
  assert.equal(app.normalizeReasoningEffort("max"), "max");
  assert.equal(app.normalizeReasoningEffort("unknown"), "none");
  assert.equal(app.preservedDefaultReasoningEffort("low"), "low");
  assert.equal(app.preservedDefaultReasoningEffort(" "), "none");
  assert.deepEqual([...app.preservedReasoningEfforts(["low", "medium", "ultra", "low", " ", ""])], ["low", "medium"]);
  assert.equal(draft.default_reasoning_effort, "none");
  assert.equal(Object.hasOwn(draft, "template"), false);
});

test("reasoning selector preserves a saved model-declared low effort", () => {
  const app = loadApp(async () => response(500));
  const result = app.reasoningEffortOptions("low");
  assert.equal(result.current, "low");
  assert.equal(result.options[0].value, "low");
  assert.match(result.options[0].label, /已保存/);
  assert.equal(result.options.some((option) => option.value === "ultra"), false);
  assert.equal(result.options.filter((option) => option.value === "medium").length, 1);
});

test("failed and empty CSRF token acquisitions are not cached", async () => {
  const replies = [
    () => Promise.reject(new Error("temporary failure")),
    () => response(200, { token: "  " }),
    () => response(200, { token: "fresh-token" }),
  ];
  let calls = 0;
  const app = loadApp(async () => replies[calls++]());

  await assert.rejects(app.csrfToken(), /temporary failure/);
  await assert.rejects(app.csrfToken(), /空安全令牌/);
  assert.equal(await app.csrfToken(), "fresh-token");
  assert.equal(calls, 3);
});

test("concurrent CSRF callers share a rejected acquisition and can retry", async () => {
  let calls = 0;
  let rejectFetch;
  const failedFetch = new Promise((_, reject) => { rejectFetch = reject; });
  const app = loadApp(async () => {
    calls++;
    if (calls === 1) return failedFetch;
    return response(200, { token: "retry-token" });
  });

  const first = app.csrfToken();
  const second = app.csrfToken();
  rejectFetch(new Error("shared failure"));
  const results = await Promise.allSettled([first, second]);
  assert.deepEqual(results.map((item) => item.status), ["rejected", "rejected"]);
  assert.equal(calls, 1);
  assert.equal(await app.csrfToken(), "retry-token");
  assert.equal(calls, 2);
});

test("server-indicated CSRF 403 refreshes the token and retries exactly once", async () => {
  const calls = [];
  const replies = [
    response(200, { token: "old-token" }),
    response(403, { error: "CSRF 校验失败" }, "Forbidden"),
    response(200, { token: "new-token" }),
    response(200, { ok: true }),
  ];
  const app = loadApp(async (url, options = {}) => {
    calls.push({ url, options });
    return replies.shift();
  });

  assert.deepEqual(await app.api("/api/settings", { method: "PUT", body: "{}" }), { ok: true });
  assert.equal(calls.length, 4);
  assert.equal(calls[1].options.headers["X-Grok-Switch-CSRF"], "old-token");
  assert.equal(calls[3].options.headers["X-Grok-Switch-CSRF"], "new-token");
});

test("a repeated CSRF 403 stops after the single retry", async () => {
  let calls = 0;
  const replies = [
    response(200, { token: "old-token" }),
    response(403, { code: "csrf_invalid", error: "expired" }, "Forbidden"),
    response(200, { token: "new-token" }),
    response(403, { code: "csrf_invalid", error: "still expired" }, "Forbidden"),
  ];
  const app = loadApp(async () => {
    calls++;
    return replies.shift();
  });

  await assert.rejects(
    app.api("/api/settings", { method: "PUT", body: "{}" }),
    (error) => error.status === 403 && error.code === "csrf_invalid",
  );
  assert.equal(calls, 4);
});

test("ordinary 403 responses are not retried", async () => {
  let calls = 0;
  const replies = [
    response(200, { token: "valid-token" }),
    response(403, { error: "仅允许本机操作" }, "Forbidden"),
  ];
  const app = loadApp(async () => {
    calls++;
    return replies.shift();
  });

  await assert.rejects(
    app.api("/api/settings", { method: "PUT", body: "{}" }),
    (error) => error.status === 403 && error.message === "仅允许本机操作",
  );
  assert.equal(calls, 2);
});
