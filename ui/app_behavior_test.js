#!/usr/bin/env node

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const appPath = path.join(__dirname, "app.js");
const appSource = fs.readFileSync(appPath, "utf8");
const testableSource = appSource.split("// Custom prompt dialog")[0] + `
this.appTest = {
  TEMPLATES,
  api,
  csrfToken,
  newProfileDraft,
  normalizeReasoningEffort,
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

function loadApp(fetchImpl) {
  const context = {
    console,
    fetch: fetchImpl,
    localStorage: { getItem() { return null; } },
    setTimeout,
    clearTimeout,
  };
  vm.createContext(context);
  vm.runInContext(testableSource, context, { filename: appPath });
  return context.appTest;
}

test("Anthropic template uses /v1 and unknown reasoning defaults to none", () => {
  const app = loadApp(async () => response(500));
  assert.equal(app.TEMPLATES.anthropic.base_url, "https://api.anthropic.com/v1");
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
