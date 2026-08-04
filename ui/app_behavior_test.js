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
  customPrompt,
  customProviderSwitchWarning,
  officialProviderSwitchWarning,
  capableWebSearchRoutes,
  trustedCollaborationEfforts,
  collaborationRouteSupportsEffort,
  collaborationRouteSupportsMax,
  collaborationCapabilityLabel,
  collaborationRoutes,
  collaborationStandardRoutes,
  resolveCollaborationRoute,
  collaborationFastRouteState,
  preferredCollaborationRoute,
  preferredCollaborationEffort,
  collaborationModelOptionsHTML,
  validateCollaborationSpec,
  setCollaborationSpec,
  loadCollaborationSpec,
  collaborationFederationConsent,
  collaborationRequestFromValues,
  collaborationSelectionValid,
  renderCollaborationFederationDisclosure,
  updateCollaborationControls,
  collaborationLaunchParameters,
  updateCollaborationLaunchGuide,
  populateCollaborationSpeedOptions,
  collaborationRequestKey,
  populateCollaborationEffortOptions,
  renderCollaboration,
  renderCollaborationStatus,
  renderCollaborationPreview,
  previewCollaboration,
  applyCollaborationPreview,
  disableCollaboration,
  renderDrift,
  reapplyRouting,
  deleteSSHFiles,
  modelSupportsBackendSearch,
  minimatch,
  setStatus(value) { state.status = value; },
  setRouting(value) { state.routing = value; },
  getCollaborationSpec() { return state.collaborationSpec; },
  getCollaborationSpecIssue() { return state.collaborationSpecIssue; },
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
    document: { getElementById(id) { return elements[id] || null; } },
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

test("provider switch warnings distinguish custom and official activation", () => {
  const app = loadApp(async () => response(500));
  assert.equal(app.customProviderSwitchWarning("provider-one", { id: "provider-one", name: "One" }), "");
  assert.match(app.customProviderSwitchWarning("provider-one", { id: "provider-two", name: "Two" }), /另一个自定义供应商/);
  assert.match(app.customProviderSwitchWarning("provider-one", { id: "provider-two", name: "Two" }), /Two/);
  assert.equal(app.officialProviderSwitchWarning("official"), "");
  assert.match(app.officialProviderSwitchWarning("provider-one"), /移除 config\.toml 中全部自定义模型定义、自定义端点和认证/);
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

test("collaboration roles require explicit trusted support for each selected effort", () => {
  const app = loadApp(async () => response(500));
  const declared = { id: "terra", supports_reasoning_effort: true, reasoning_efforts: ["none", "high", "max"], reasoning_efforts_source: "declared" };
  const probed = { id: "luna", supports_reasoning_effort: true, reasoning_efforts: ["medium", "xhigh"], reasoning_efforts_source: "probe" };
  assert.deepEqual(Array.from(app.trustedCollaborationEfforts(declared)), ["high", "max"]);
  assert.deepEqual(Array.from(app.trustedCollaborationEfforts(probed)), ["medium", "xhigh"]);
  assert.equal(app.collaborationRouteSupportsEffort(declared, "high"), true);
  assert.equal(app.collaborationRouteSupportsMax(declared), true);
  assert.equal(app.collaborationRouteSupportsMax(probed), false);
  assert.equal(app.collaborationRouteSupportsEffort({ ...declared, reasoning_efforts_source: "unknown" }, "high"), false);
  assert.equal(app.collaborationRouteSupportsEffort({ ...declared, supports_reasoning_effort: false }, "high"), false);
  assert.equal(app.collaborationCapabilityLabel(declared), "high / max · declared");
  assert.equal(app.collaborationCapabilityLabel({ ...declared, reasoning_efforts_source: "unknown" }), "无可信推理档位 · unknown");
  assert.equal(app.preferredCollaborationEffort(declared), "max");

  const snapshot = { active_provider_id: "provider-1", model_routes: [
    { ...declared, id: "terra", provider_id: "provider-1", name: "subscription/codex/gpt-5.6-terra", speed_tier: "standard", standard_anchor: "terra" },
    { ...probed, id: "terra-fast", provider_id: "provider-1", name: "subscription/codex/gpt-5.6-terra-fast", speed_tier: "fast", standard_anchor: "terra" },
    { ...probed, id: "luna", provider_id: "provider-1", name: "subscription/codex/gpt-5.6-luna", speed_tier: "standard", standard_anchor: "luna" },
    { ...declared, id: "sol", provider_id: "provider-2", name: "subscription/codex/gpt-5.6-sol", speed_tier: "standard", standard_anchor: "sol" },
  ] };
  const routes = app.collaborationRoutes(snapshot);
  assert.deepEqual(Array.from(routes).map((route) => route.id), ["terra", "terra-fast", "luna"]);
  assert.deepEqual(Array.from(app.collaborationStandardRoutes(routes)).map((route) => route.id), ["terra", "luna"]);
  assert.equal(app.resolveCollaborationRoute(routes, "terra", "standard").id, "terra");
  assert.equal(app.resolveCollaborationRoute(routes, "terra", "fast").id, "terra-fast");
  assert.equal(app.resolveCollaborationRoute(routes, "luna", "fast"), null);
  assert.equal(app.preferredCollaborationRoute(routes, "terra-fast"), "terra");
  assert.match(app.collaborationModelOptionsHTML(routes, "provider-1:removed"), /已保存 Standard 路由当前不可用/);
  assert.doesNotMatch(app.collaborationModelOptionsHTML(routes), /value="terra-fast"/);
});

test("collaboration effort selector retains a saved effort that the current route no longer supports", () => {
  const effortSelect = { innerHTML: "", disabled: false, value: "" };
  const app = loadApp(async () => response(500), {
    collaborationMainCoordinatorEffort: effortSelect,
  });
  const route = {
    supports_reasoning_effort: true,
    reasoning_efforts: ["low", "medium"],
    reasoning_efforts_source: "declared",
  };

  assert.equal(app.populateCollaborationEffortOptions({ key: "MainCoordinator" }, route, "max"), "max");
  assert.match(effortSelect.innerHTML, /value="max" disabled/);
  assert.match(effortSelect.innerHTML, /当前模型不再支持/);
  assert.equal(effortSelect.disabled, false);
  assert.equal(effortSelect.value, "max");
});

test("collaboration effort selector keeps an untrusted saved effort visible for explicit replacement", () => {
  const effortSelect = { innerHTML: "", disabled: false, value: "" };
  const app = loadApp(async () => response(500), {
    collaborationMainCoordinatorEffort: effortSelect,
  });
  const route = {
    supports_reasoning_effort: true,
    reasoning_efforts: ["max"],
    reasoning_efforts_source: "unknown",
  };

  assert.equal(app.populateCollaborationEffortOptions({ key: "MainCoordinator" }, route, "max"), "max");
  assert.match(effortSelect.innerHTML, /value="max" disabled/);
  assert.equal(effortSelect.disabled, false);
  assert.equal(effortSelect.value, "max");
});

test("disabled collaboration status keeps saved four-role selections in the form", () => {
  const roleElements = {};
  for (const key of ["MainCoordinator", "TaskDecomposition", "MainImplementation", "DifficultReview"]) {
    roleElements[`collaboration${key}Model`] = { innerHTML: "", value: "", onchange: null };
    roleElements[`collaboration${key}Speed`] = { innerHTML: "", value: "", disabled: false, onchange: null };
    roleElements[`collaboration${key}Effort`] = { innerHTML: "", value: "", disabled: false, onchange: null };
    roleElements[`collaboration${key}Capability`] = { textContent: "", className: "" };
  }
  Object.assign(roleElements, {
    collaborationTier: { value: "adaptive", onchange: null },
    collaborationTierHint: { textContent: "" },
    previewCollaborationBtn: { disabled: false },
    applyCollaborationBtn: { disabled: false },
    disableCollaborationBtn: { disabled: false },
    saveRoutingPolicyBtn: { disabled: false },
    routingProvider: { value: "provider-1" },
    collaborationBadge: { textContent: "", dataset: {} },
    collaborationIssues: { hidden: true, innerHTML: "" },
    collaborationCreditWarning: { hidden: true, textContent: "" },
    collaborationPreview: { hidden: true, open: false },
  });
  const app = loadApp(async () => response(500), roleElements);
  const routes = [
    { id: "provider-1:terra", provider_id: "provider-1", name: "terra", speed_tier: "standard", standard_anchor: "provider-1:terra", supports_reasoning_effort: true, reasoning_efforts: ["high", "xhigh"], reasoning_efforts_source: "declared" },
    { id: "provider-1:terra-fast", provider_id: "provider-1", name: "terra-fast", speed_tier: "fast", standard_anchor: "provider-1:terra", supports_reasoning_effort: true, reasoning_efforts: ["high", "xhigh"], reasoning_efforts_source: "declared" },
    { id: "provider-1:luna", provider_id: "provider-1", name: "luna", speed_tier: "standard", standard_anchor: "provider-1:luna", supports_reasoning_effort: true, reasoning_efforts: ["medium"], reasoning_efforts_source: "declared" },
    { id: "provider-1:sol", provider_id: "provider-1", name: "sol", speed_tier: "standard", standard_anchor: "provider-1:sol", supports_reasoning_effort: true, reasoning_efforts: ["max"], reasoning_efforts_source: "declared" },
    { id: "provider-1:sol-fast", provider_id: "provider-1", name: "sol-fast", speed_tier: "fast", standard_anchor: "provider-1:sol", supports_reasoning_effort: true, reasoning_efforts: ["max"], reasoning_efforts_source: "declared" },
  ];
  app.renderCollaboration({ active_provider_id: "provider-1", model_routes: routes, policy: {} }, {
    configured: true,
    valid: true,
    policy: {
      enabled: false,
      provider_id: "provider-1",
      default_tier: "critical",
      roles: {
        main_coordinator: { model: "provider-1:terra", speed_tier: "fast", reasoning_effort: "high" },
        task_decomposition: { model: "provider-1:luna", speed_tier: "standard", reasoning_effort: "medium" },
        main_implementation: { model: "provider-1:terra", speed_tier: "fast", reasoning_effort: "xhigh" },
        difficult_implementation_review: { model: "provider-1:sol", speed_tier: "fast", reasoning_effort: "max" },
      },
    },
    issues: [],
  });

  assert.equal(roleElements.collaborationMainCoordinatorModel.value, "provider-1:terra");
  assert.equal(roleElements.collaborationTaskDecompositionModel.value, "provider-1:luna");
  assert.equal(roleElements.collaborationMainCoordinatorSpeed.value, "fast");
  assert.equal(roleElements.collaborationTaskDecompositionSpeed.value, "standard");
  assert.equal(roleElements.collaborationMainImplementationEffort.value, "xhigh");
  assert.equal(roleElements.collaborationDifficultReviewEffort.value, "max");
  assert.equal(roleElements.collaborationTier.value, "critical");
  assert.equal(roleElements.collaborationCreditWarning.hidden, false);
  assert.match(roleElements.collaborationCreditWarning.textContent, /3 个角色选择 Fast/);
});

test("saved Fast stays visible when its concrete partner disappears and never falls back", () => {
  const speedSelect = { innerHTML: "", disabled: false, value: "" };
  const app = loadApp(async () => response(500), {
    collaborationMainCoordinatorSpeed: speedSelect,
  });
  const routes = [{
    id: "provider-1:terra", provider_id: "provider-1", name: "terra",
    speed_tier: "standard", standard_anchor: "provider-1:terra",
    supports_reasoning_effort: true, reasoning_efforts: ["high"], reasoning_efforts_source: "declared",
  }];

  assert.equal(app.populateCollaborationSpeedOptions({ key: "MainCoordinator" }, routes, "provider-1:terra", "fast"), "fast");
  assert.match(speedSelect.innerHTML, /value="fast" disabled/);
  assert.match(speedSelect.innerHTML, /已保存/);
  assert.equal(app.resolveCollaborationRoute(routes, "provider-1:terra", "fast"), null);
});

test("collaboration request keeps a missing speed tier empty instead of silently choosing Standard", () => {
  const app = loadApp(async () => response(500));
  const request = app.collaborationRequestFromValues({ active_provider_id: "provider-1" }, {
    mainCoordinatorModel: "provider-1:terra",
    mainCoordinatorEffort: "high",
  });
  assert.equal(request.roles.main_coordinator.speed_tier, "");
});

test("collaboration request stores four independent provider-local role assignments", () => {
  const app = loadApp(async () => response(500));
  const request = app.collaborationRequestFromValues({ active_provider_id: "provider-1" }, {
    mainCoordinatorModel: "provider-1:terra",
    mainCoordinatorSpeed: "standard",
    mainCoordinatorEffort: "high",
    taskDecompositionModel: "provider-1:luna",
    taskDecompositionSpeed: "fast",
    taskDecompositionEffort: "medium",
    mainImplementationModel: "provider-1:terra",
    mainImplementationSpeed: "fast",
    mainImplementationEffort: "xhigh",
    difficultReviewModel: "provider-1:sol",
    difficultReviewSpeed: "standard",
    difficultReviewEffort: "max",
    defaultTier: "assurance",
  });
  assert.deepEqual(JSON.parse(JSON.stringify(request)), {
    version: 4, enabled: true, mode: "single_provider", provider_id: "provider-1",
    roles: {
      main_coordinator: { provider_id: "provider-1", model: "provider-1:terra", speed_tier: "standard", reasoning_effort: "high", data_scope: "repository_plus_minimized_prior_work_products" },
      task_decomposition: { provider_id: "provider-1", model: "provider-1:luna", speed_tier: "fast", reasoning_effort: "medium", data_scope: "repository_only" },
      main_implementation: { provider_id: "provider-1", model: "provider-1:terra", speed_tier: "fast", reasoning_effort: "xhigh", data_scope: "repository_plus_minimized_prior_work_products" },
      difficult_implementation_review: { provider_id: "provider-1", model: "provider-1:sol", speed_tier: "standard", reasoning_effort: "max", data_scope: "repository_plus_minimized_prior_work_products" },
    }, default_tier: "assurance",
  });
  assert.equal(app.collaborationRequestKey(request), app.collaborationRequestKey(JSON.parse(JSON.stringify(request))));
});

test("collaboration request ignores tampered disabled data-scope controls and emits canonical scopes", () => {
  const app = loadApp(async () => response(500));
  const request = app.collaborationRequestFromValues({ active_provider_id: "p1" }, {
    mainCoordinatorModel: "p1:m", mainCoordinatorSpeed: "standard", mainCoordinatorEffort: "high", mainCoordinatorDataScope: "repository_only",
    taskDecompositionModel: "p1:d", taskDecompositionSpeed: "standard", taskDecompositionEffort: "medium", taskDecompositionDataScope: "repository_plus_minimized_prior_work_products",
    mainImplementationModel: "p1:i", mainImplementationSpeed: "standard", mainImplementationEffort: "high", mainImplementationDataScope: "repository_only",
    difficultReviewModel: "p1:r", difficultReviewSpeed: "standard", difficultReviewEffort: "max", difficultReviewDataScope: "repository_only",
  });
  assert.equal(request.roles.task_decomposition.data_scope, "repository_only");
  assert.equal(request.roles.main_coordinator.data_scope, "repository_plus_minimized_prior_work_products");
  assert.equal(request.roles.main_implementation.data_scope, "repository_plus_minimized_prior_work_products");
  assert.equal(request.roles.difficult_implementation_review.data_scope, "repository_plus_minimized_prior_work_products");
});

const collaborationWorkflowSpec = { schema_version: 1, collaboration_policy_version: 4, workflow_paths: [
  { tier: "economy", budget: 1, roles: ["main_coordinator"], data_flows: [] },
  { tier: "focused-evidence", budget: 2, roles: ["task_decomposition", "main_coordinator"], data_flows: [{ from: "task_decomposition", to: "main_coordinator" }] },
  { tier: "focused-build", budget: 2, roles: ["main_implementation", "main_coordinator"], data_flows: [{ from: "main_implementation", to: "main_coordinator" }] },
  { tier: "assurance", budget: 3, roles: ["task_decomposition", "main_implementation", "main_coordinator"], data_flows: [{ from: "task_decomposition", to: "main_implementation" }, { from: "task_decomposition", to: "main_coordinator" }, { from: "main_implementation", to: "main_coordinator" }] },
  { tier: "critical", budget: 4, roles: ["task_decomposition", "main_implementation", "difficult_implementation_review", "main_coordinator"], data_flows: [{ from: "task_decomposition", to: "main_implementation" }, { from: "task_decomposition", to: "difficult_implementation_review" }, { from: "main_implementation", to: "difficult_implementation_review" }, { from: "task_decomposition", to: "main_coordinator" }, { from: "main_implementation", to: "main_coordinator" }, { from: "difficult_implementation_review", to: "main_coordinator" }] },
] };

function cloneCollaborationWorkflowSpec() {
  return JSON.parse(JSON.stringify(collaborationWorkflowSpec));
}

test("collaboration spec validator accepts only the exact versioned canonical contract", () => {
  const app = loadApp(async () => response(500));
  const mutations = [
    (spec) => { delete spec.workflow_paths[1].data_flows; },
    (spec) => { spec.workflow_paths[1].tier = "economy"; },
    (spec) => { spec.workflow_paths[1].roles[0] = "unknown_role"; },
    (spec) => { spec.workflow_paths[1].data_flows.push({ ...spec.workflow_paths[1].data_flows[0] }); },
    (spec) => { spec.workflow_paths[2].budget = 3; },
    (spec) => { spec.workflow_paths.splice(2, 1); },
    (spec) => { spec.workflow_paths.push({ ...spec.workflow_paths[4] }); },
    (spec) => { spec.schema_version = 2; },
    (spec) => { spec.collaboration_policy_version = 5; },
    (spec) => { spec.extra = true; },
    (spec) => { spec.workflow_paths[0].extra = true; },
    (spec) => { spec.workflow_paths[1].data_flows[0].extra = true; },
  ];
  assert.doesNotThrow(() => app.validateCollaborationSpec(cloneCollaborationWorkflowSpec()));
  for (const mutate of mutations) {
    const spec = cloneCollaborationWorkflowSpec();
    mutate(spec);
    assert.throws(() => app.validateCollaborationSpec(spec), /unavailable/i);
  }
});

test("validated collaboration spec is detached and frozen against raw response mutation", () => {
  const app = loadApp(async () => response(500));
  const raw = cloneCollaborationWorkflowSpec();
  const validated = app.setCollaborationSpec(raw);
  raw.workflow_paths[1].data_flows[0].from = "main_coordinator";
  raw.workflow_paths.push({ tier: "extra" });
  assert.equal(validated.workflow_paths.length, 5);
  assert.equal(validated.workflow_paths[1].data_flows[0].from, "task_decomposition");
  assert.equal(Object.isFrozen(validated), true);
  assert.equal(Object.isFrozen(validated.workflow_paths), true);
  assert.equal(Object.isFrozen(validated.workflow_paths[1].data_flows[0]), true);
});

test("collaboration spec fetch failure clears trusted state and records a sanitized local issue", async () => {
  const app = loadApp(async () => { throw new Error("network\nsecret detail"); });
  app.setCollaborationSpec(cloneCollaborationWorkflowSpec());
  assert.equal(await app.loadCollaborationSpec(), null);
  assert.equal(app.getCollaborationSpec(), null);
  assert.match(app.getCollaborationSpecIssue(), /unavailable/i);
  assert.doesNotMatch(app.getCollaborationSpecIssue(), /secret detail/);
});

test("newer malformed collaboration spec supersedes an older valid response without stale UI recovery", async () => {
  const oldRequest = deferred();
  const latestRequest = deferred();
  const pendingRequests = [oldRequest, latestRequest];
  const elements = {
    collaborationMode: { value: "federated" },
    collaborationFederationConsent: { checked: true, disabled: false },
    collaborationFederationDisclosure: { hidden: true },
    collaborationFederationMap: { textContent: "", innerHTML: "unchanged" },
    previewCollaborationBtn: { disabled: false },
    applyCollaborationBtn: { disabled: false },
  };
  const app = loadApp(() => pendingRequests.shift().promise, elements);
  const oldLoad = app.loadCollaborationSpec();
  const latestLoad = app.loadCollaborationSpec();

  latestRequest.resolve(response(200, { schema_version: 1, collaboration_policy_version: 4, workflow_paths: [] }));
  assert.equal(await latestLoad, null);
  const latestIssue = app.getCollaborationSpecIssue();
  app.renderCollaborationFederationDisclosure();
  app.updateCollaborationControls();
  assert.match(latestIssue, /unavailable/i);
  assert.equal(elements.collaborationFederationDisclosure.hidden, false);
  assert.match(elements.collaborationFederationMap.textContent, /unavailable/i);
  assert.equal(elements.collaborationFederationConsent.disabled, true);
  assert.equal(elements.previewCollaborationBtn.disabled, true);
  assert.equal(elements.applyCollaborationBtn.disabled, true);

  oldRequest.resolve(response(200, cloneCollaborationWorkflowSpec()));
  assert.equal(await oldLoad, null);
  assert.equal(app.getCollaborationSpec(), null);
  assert.equal(app.getCollaborationSpecIssue(), latestIssue);
  assert.equal(elements.collaborationFederationDisclosure.hidden, false);
  assert.match(elements.collaborationFederationMap.textContent, /unavailable/i);
  assert.equal(elements.collaborationFederationConsent.disabled, true);
  assert.equal(elements.previewCollaborationBtn.disabled, true);
  assert.equal(elements.applyCollaborationBtn.disabled, true);
});

test("newer valid collaboration spec supersedes older malformed and rejected responses", async () => {
  for (const settleOld of [
    (request) => request.resolve(response(200, { schema_version: 1, collaboration_policy_version: 4, workflow_paths: [] })),
    (request) => request.reject(new Error("old network\nsecret detail")),
  ]) {
    const oldRequest = deferred();
    const latestRequest = deferred();
    const pendingRequests = [oldRequest, latestRequest];
    const app = loadApp(() => pendingRequests.shift().promise);
    const oldLoad = app.loadCollaborationSpec();
    const latestLoad = app.loadCollaborationSpec();

    latestRequest.resolve(response(200, cloneCollaborationWorkflowSpec()));
    const latestSpec = await latestLoad;
    assert.equal(latestSpec.workflow_paths.length, 5);
    assert.equal(app.getCollaborationSpec(), latestSpec);
    assert.equal(app.getCollaborationSpecIssue(), "");

    settleOld(oldRequest);
    assert.equal(await oldLoad, null);
    assert.equal(app.getCollaborationSpec(), latestSpec);
    assert.equal(app.getCollaborationSpecIssue(), "");
  }
});

test("only the latest of three collaboration spec loads may commit or clear an issue", async () => {
  const firstRequest = deferred();
  const secondRequest = deferred();
  const latestRequest = deferred();
  const pendingRequests = [firstRequest, secondRequest, latestRequest];
  const app = loadApp(() => pendingRequests.shift().promise);
  const firstLoad = app.loadCollaborationSpec();
  const secondLoad = app.loadCollaborationSpec();
  const latestLoad = app.loadCollaborationSpec();

  latestRequest.reject(new Error("latest failure\nprivate detail"));
  assert.equal(await latestLoad, null);
  const latestIssue = app.getCollaborationSpecIssue();
  assert.match(latestIssue, /unavailable/i);
  assert.doesNotMatch(latestIssue, /private detail/);

  secondRequest.resolve(response(200, cloneCollaborationWorkflowSpec()));
  firstRequest.resolve(response(200, { schema_version: 1, collaboration_policy_version: 4, workflow_paths: [] }));
  assert.deepEqual(await Promise.all([firstLoad, secondLoad]), [null, null]);
  assert.equal(app.getCollaborationSpec(), null);
  assert.equal(app.getCollaborationSpecIssue(), latestIssue);

  const directSpec = app.setCollaborationSpec(cloneCollaborationWorkflowSpec());
  assert.equal(directSpec.workflow_paths.length, 5);
  assert.equal(app.getCollaborationSpec(), directSpec);
  assert.equal(app.getCollaborationSpecIssue(), "");
});

test("direct collaboration spec helper deterministically supersedes an in-flight production load", async () => {
  const pendingRequest = deferred();
  const app = loadApp(() => pendingRequest.promise);
  const pendingLoad = app.loadCollaborationSpec();
  const directSpec = app.setCollaborationSpec(cloneCollaborationWorkflowSpec());

  pendingRequest.resolve(response(200, { schema_version: 1, collaboration_policy_version: 4, workflow_paths: [] }));
  assert.equal(await pendingLoad, null);
  assert.equal(app.getCollaborationSpec(), directSpec);
  assert.equal(app.getCollaborationSpecIssue(), "");
});

test("collaboration launch guide maps UI tiers to exact fail-closed workflow tool instructions", () => {
  const elements = {
    collaborationTier: { value: "adaptive" },
    collaborationLaunchObjective: { value: "执行最小只读 smoke" },
    collaborationLaunchBudget: { textContent: "" },
    collaborationLaunchInstruction: { textContent: "" },
    collaborationTierHint: { textContent: "" },
  };
  const app = loadApp(async () => response(500), elements);

  const economy = app.collaborationLaunchParameters();
  assert.equal(economy.tier, "economy");
  assert.equal(economy.label, "Economy");
  assert.equal(economy.budget, 1);
  assert.equal(economy.objective, "执行最小只读 smoke");
  assert.match(economy.instruction, /^请调用 workflow 工具运行 named workflow gbs-max-collab/);
  assert.match(economy.instruction, /name="gbs-max-collab"/);
  assert.match(economy.instruction, /args=\{"objective":"执行最小只读 smoke","tier":"economy"\}/);
  assert.match(economy.instruction, /精确设置 agent_budget=1/);
  assert.match(economy.instruction, /不要使用 \/gbs-max-collab 或 \/workflow slash 启动/);
  assert.match(economy.instruction, /如果不能精确设置 agent_budget=1，请不要启动并说明原因/);

  app.updateCollaborationLaunchGuide();
  assert.equal(elements.collaborationLaunchBudget.textContent, "budget 1");
  assert.equal(elements.collaborationLaunchInstruction.textContent, economy.instruction);

  for (const [tier, label, budget] of [
    ["focused-evidence", "Focused Evidence", 2],
    ["focused-build", "Focused Build", 2],
    ["assurance", "Assurance", 3],
    ["critical", "Critical", 4],
  ]) {
    elements.collaborationTier.value = tier;
    const launch = app.collaborationLaunchParameters();
    assert.equal(launch.label, label);
    assert.equal(launch.budget, budget);
    assert.match(launch.instruction, new RegExp(`"tier":"${tier}"`));
    assert.match(launch.instruction, new RegExp(`agent_budget=${budget}`));
    assert.match(launch.instruction, /workflow 工具/);
    assert.match(launch.instruction, /不要使用 \/gbs-max-collab 或 \/workflow slash 启动/);
    assert.match(launch.instruction, /请不要启动并说明原因/);
  }

  elements.collaborationTier.value = "focused-evidence";
  elements.collaborationLaunchObjective.value = "检查 \\\"quoted\\\"\npath\\to\\file";
  const special = app.collaborationLaunchParameters();
  const encodedArgs = JSON.stringify({ objective: '检查 \\"quoted\\"\npath\\to\\file', tier: "focused-evidence" });
  assert.equal(special.objective, '检查 \\"quoted\\"\npath\\to\\file');
  assert.ok(special.instruction.includes(`args=${encodedArgs}`));
  assert.match(special.instruction, /agent_budget=2/);

  elements.collaborationTier.value = "critical";
  elements.collaborationLaunchObjective.value = "   ";
  const empty = app.collaborationLaunchParameters();
  assert.equal(empty.objective, "");
  assert.match(empty.instruction, /"objective":"<填写任务目标>"/);
  assert.match(empty.instruction, /"tier":"critical"/);
  assert.match(empty.instruction, /agent_budget=4/);
});

test("federated collaboration requires explicit consent and emits canonical provider set and edges", () => {
  const app = loadApp(async () => response(500));
  app.setCollaborationSpec(collaborationWorkflowSpec);
  const request = app.collaborationRequestFromValues({ active_provider_id: "p1" }, {
    mode: "federated", federationConsent: true,
    mainCoordinatorProvider: "p1", mainCoordinatorModel: "p1:m", mainCoordinatorSpeed: "standard", mainCoordinatorEffort: "high",
    taskDecompositionProvider: "p2", taskDecompositionModel: "p2:d", taskDecompositionSpeed: "standard", taskDecompositionEffort: "medium",
    mainImplementationProvider: "p2", mainImplementationModel: "p2:i", mainImplementationSpeed: "standard", mainImplementationEffort: "high",
    difficultReviewProvider: "p1", difficultReviewModel: "p1:r", difficultReviewSpeed: "standard", difficultReviewEffort: "max",
  });
  assert.equal(request.federation_consent.basis, "all_workflow_tiers_v1");
  assert.deepEqual(Array.from(request.federation_consent.provider_ids), ["p1", "p2"]);
  assert.deepEqual(Array.from(request.federation_consent.never_transfer), ["credentials", "secrets", "full_transcripts"]);
  assert.deepEqual(JSON.parse(JSON.stringify(request.federation_consent.tier_handoff_edges)), [
    { tier: "economy", edges: [] },
    { tier: "focused-evidence", edges: [{ from: "task_decomposition", to: "main_coordinator" }] },
    { tier: "focused-build", edges: [{ from: "main_implementation", to: "main_coordinator" }] },
    { tier: "assurance", edges: [{ from: "task_decomposition", to: "main_coordinator" }, { from: "main_implementation", to: "main_coordinator" }] },
    { tier: "critical", edges: [{ from: "task_decomposition", to: "difficult_implementation_review" }, { from: "main_implementation", to: "difficult_implementation_review" }, { from: "task_decomposition", to: "main_coordinator" }, { from: "main_implementation", to: "main_coordinator" }] },
  ]);
});

test("federation disclosure escapes through textContent and shows every tier before consent", () => {
  const elements = {
    collaborationMode: { value: "federated" },
    collaborationFederationConsent: { checked: false },
    collaborationFederationDisclosure: { hidden: true },
    collaborationFederationMap: { textContent: "", innerHTML: "unchanged" },
  };
  for (const [key, provider, model, speed, effort] of [
    ["MainCoordinator", "<p1>", "p1:m", "standard", "high"],
    ["TaskDecomposition", "p2", "p2:d", "standard", "medium"],
    ["MainImplementation", "p2", "p2:i", "standard", "high"],
    ["DifficultReview", "<p1>", "p1:r", "standard", "max"],
  ]) {
    elements[`collaboration${key}Provider`] = { value: provider };
    elements[`collaboration${key}Model`] = { value: model };
    elements[`collaboration${key}Speed`] = { value: speed };
    elements[`collaboration${key}Effort`] = { value: effort };
  }
  const app = loadApp(async () => response(500), elements);
  app.setCollaborationSpec(collaborationWorkflowSpec);
  app.renderCollaborationFederationDisclosure();
  assert.equal(elements.collaborationFederationDisclosure.hidden, false);
  assert.equal(elements.collaborationFederationMap.innerHTML, "unchanged");
  assert.match(elements.collaborationFederationMap.textContent, /Providers: <p1>, p2/);
  for (const tier of ["economy", "focused-evidence", "focused-build", "assurance", "critical"]) assert.match(elements.collaborationFederationMap.textContent, new RegExp(tier));
});

test("invalid collaboration spec renders controlled disclosure and disables only federated controls", () => {
  const elements = {
    collaborationMode: { value: "federated" },
    collaborationFederationConsent: { checked: true, disabled: false },
    collaborationFederationDisclosure: { hidden: true },
    collaborationFederationMap: { textContent: "", innerHTML: "unchanged" },
    previewCollaborationBtn: { disabled: false },
    applyCollaborationBtn: { disabled: false },
    saveRoutingPolicyBtn: { disabled: false },
    routingProvider: { value: "p1" },
  };
  const app = loadApp(async () => response(500), elements);
  app.setCollaborationSpec({ schema_version: 1, collaboration_policy_version: 4, workflow_paths: [] });
  assert.doesNotThrow(() => app.renderCollaborationFederationDisclosure());
  assert.equal(elements.collaborationFederationDisclosure.hidden, false);
  assert.match(elements.collaborationFederationMap.textContent, /unavailable/i);
  assert.doesNotMatch(elements.collaborationFederationMap.textContent, /Providers:/);
  assert.equal(app.collaborationSelectionValid(), false);
  app.updateCollaborationControls();
  assert.equal(elements.collaborationFederationConsent.disabled, true);
  assert.equal(elements.previewCollaborationBtn.disabled, true);
  assert.equal(elements.applyCollaborationBtn.disabled, true);

  elements.collaborationMode.value = "single_provider";
  app.setRouting({ active_provider_id: "p1", model_routes: [
    { id: "p1:m", provider_id: "p1", name: "m", speed_tier: "standard", standard_anchor: "p1:m", supports_reasoning_effort: true, reasoning_efforts: ["high"], reasoning_efforts_source: "declared" },
  ] });
  for (const [key, provider, model, speed, effort] of [
    ["MainCoordinator", "p1", "p1:m", "standard", "high"],
    ["TaskDecomposition", "p1", "p1:m", "standard", "high"],
    ["MainImplementation", "p1", "p1:m", "standard", "high"],
    ["DifficultReview", "p1", "p1:m", "standard", "high"],
  ]) {
    elements[`collaboration${key}Provider`] = { value: provider };
    elements[`collaboration${key}Model`] = { value: model };
    elements[`collaboration${key}Speed`] = { value: speed };
    elements[`collaboration${key}Effort`] = { value: effort };
  }
  app.updateCollaborationControls();
  assert.equal(elements.collaborationFederationConsent.disabled, false);
  assert.equal(elements.previewCollaborationBtn.disabled, false);
  assert.equal(elements.collaborationFederationDisclosure.hidden, true);
  assert.doesNotThrow(() => app.renderCollaborationFederationDisclosure());
  assert.equal(elements.collaborationFederationDisclosure.hidden, true);
});

test("federation consent fails deterministically before emitting without a valid spec", () => {
  const app = loadApp(async () => response(500));
  assert.throws(() => app.collaborationFederationConsent({}), /unavailable/i);
  assert.equal(app.collaborationRequestFromValues({ active_provider_id: "p1" }, { mode: "single_provider" }).mode, "single_provider");
});

test("routing drift banner distinguishes config mismatch and routing repair", () => {
  const banner = { hidden: true, style: { display: "none" } };
  const title = { textContent: "" };
  const detail = { textContent: "" };
  const app = loadApp(async () => response(500), { driftBanner: banner, driftTitle: title, driftDetail: detail });

  app.setStatus({ config_matches_routing: false, active_routing: { repair_required: false } });
  app.renderDrift();
  assert.equal(banner.hidden, false);
  assert.match(detail.textContent, /路由托管字段/);
  assert.match(detail.textContent, /保留无关 TOML 设置/);

  app.setStatus({ config_matches_routing: true, active_routing: { repair_required: true } });
  app.renderDrift();
  assert.match(title.textContent, /保存的模型路由需要修复/);
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
  assert.equal(app.normalizeReasoningEffort("unknown"), "none");
  assert.equal(draft.default_reasoning_effort, "none");
  assert.equal(Object.hasOwn(draft, "template"), false);
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
