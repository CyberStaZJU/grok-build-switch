#!/usr/bin/env node

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const appPath = path.join(__dirname, "app.js");
const appSource = fs.readFileSync(appPath, "utf8");
const testableSource = appSource.split("// Custom confirm dialog")[0] + `
this.appTest = {
  TEMPLATES,
  api,
  csrfToken,
  newProfileDraft,
  normalizeReasoningEffort,
  customPrompt,
  renderDrift,
  reapplyRouting,
  deleteSSHFiles,
  modelSupportsBackendSearch,
  minimatch,
  setStatus(value) { state.status = value; },
  formatTokenCount,
  formatHitRate,
  cacheTableHTML,
  loadCacheStats,
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

function loadApp(fetchImpl, elements = {}) {
  const context = {
    console,
    fetch: fetchImpl,
    document: { getElementById(id) { return elements[id] || null; } },
    localStorage: { getItem() { return null; } },
    setTimeout,
    clearTimeout,
  };
  vm.createContext(context);
  vm.runInContext(testableSource, context, { filename: appPath });
  return context.appTest;
}

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

test("SSH filename glob treats question mark as one arbitrary character", () => {
  const app = loadApp(async () => response(500));
  assert.equal(app.minimatch("ab.txt", "a?.txt"), true);
  assert.equal(app.minimatch("a..txt", "a?.txt"), true);
  assert.equal(app.minimatch("abc.txt", "a?.txt"), false);
  assert.equal(app.minimatch("aX.txt", "a?.txt"), true);
  assert.equal(app.minimatch("a[.txt", "a[.txt"), true);
  assert.equal(app.minimatch("a/b.txt", "a/b.txt"), true);
});

test("manual models do not claim backend search unless explicitly enabled", () => {
  const app = loadApp(async () => response(500));
  assert.equal(app.modelSupportsBackendSearch({}), false);
  assert.equal(app.modelSupportsBackendSearch({ supports_backend_search: false }), false);
  assert.equal(app.modelSupportsBackendSearch({ supports_backend_search: true }), true);
});

test("routing drift banner follows config_matches_routing without an active profile", () => {
  const banner = { hidden: true, style: { display: "none" } };
  const app = loadApp(async () => response(500), { driftBanner: banner });

  app.setStatus({ config_matches_routing: false, active_profile: null, config_matches_active: true });
  app.renderDrift();
  assert.equal(banner.hidden, false);
  assert.equal(banner.style.display, "");

  app.setStatus({ config_matches_routing: true, active_profile: { id: "legacy-profile" }, config_matches_active: false });
  app.renderDrift();
  assert.equal(banner.hidden, true);
  assert.equal(banner.style.display, "none");

  app.setStatus({});
  app.renderDrift();
  assert.equal(banner.hidden, true);
  assert.equal(banner.style.display, "none");
});

test("routing reapply uses the unified routing endpoint with CSRF", async () => {
  const calls = [];
  const replies = [
    response(200, { token: "routing-token" }),
    response(200, { message: "路由策略已重新应用" }),
  ];
  const app = loadApp(async (url, options = {}) => {
    calls.push({ url, options });
    return replies.shift();
  });

  await app.reapplyRouting();
  assert.equal(calls.length, 2);
  assert.equal(calls[0].url, "/api/csrf");
  assert.equal(calls[1].url, "/api/routing/reapply");
  assert.equal(calls[1].options.method, "POST");
  assert.equal(calls[1].options.headers["X-Grok-Switch-CSRF"], "routing-token");
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

test("cache statistics render the nested report and escape labels", async () => {
  const elements = Object.fromEntries([
    "cacheStatsHours", "cacheHitRate", "cacheTurns", "cachePromptTokens", "cacheCachedTokens",
    "cacheStatsHint", "cacheByModel", "cacheRecent",
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
        hit_rate: 0.75,
      },
      by_model: [{
        model: `<img src=x onerror="fail()">`,
        turns: 12,
        prompt_tokens: 1_000_000,
        cached_prompt_tokens: 750_000,
        hit_rate: 0.75,
      }],
      recent: [{
        ts: "2026-07-31T12:00:00Z",
        session_id: `<bad-session>`,
        model: `<script>fail()</script>`,
        prompt_tokens: 1000,
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
  assert.match(elements.cacheStatsHint.textContent, /事件 12/);
  assert.match(elements.cacheByModel.innerHTML, /&lt;img src=x onerror=&quot;fail\(\)&quot;&gt;/);
  assert.doesNotMatch(elements.cacheByModel.innerHTML, /<img/);
  assert.match(elements.cacheRecent.innerHTML, /&lt;script&gt;fail\(\)&lt;\/script&gt;/);
  assert.doesNotMatch(elements.cacheRecent.innerHTML, /<script>/);
});

test("cache statistics render empty and missing-log states", async () => {
  const elements = Object.fromEntries([
    "cacheStatsHours", "cacheHitRate", "cacheTurns", "cachePromptTokens", "cacheCachedTokens",
    "cacheStatsHint", "cacheByModel", "cacheRecent",
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
  assert.match(elements.cacheStatsHint.textContent, /未找到 Grok 日志/);
  assert.match(elements.cacheByModel.innerHTML, /暂无数据/);
  assert.match(elements.cacheRecent.innerHTML, /暂无数据/);
});

test("official Anthropic template is absent and unknown reasoning defaults to none", () => {
  const app = loadApp(async () => response(500));
  assert.equal(app.TEMPLATES.anthropic, undefined);
  assert.equal(app.normalizeReasoningEffort("unknown"), "none");
  assert.equal(app.newProfileDraft().default_reasoning_effort, "none");
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
