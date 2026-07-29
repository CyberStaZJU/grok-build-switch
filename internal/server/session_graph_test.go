package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"grok_switch/internal/agentbridge"
	"grok_switch/internal/paths"
	"grok_switch/internal/routing"
)

func TestSessionGraphKeepsProviderBranchesUnderOneLogicalSession(t *testing.T) {
	store := newSessionGraphStore(t.TempDir())
	providerA := providerIdentity{ID: "provider-a", Backend: "responses", BaseURL: "https://a.example/v1"}
	providerB := providerIdentity{ID: "provider-b", Backend: "openai", BaseURL: "https://b.example/v1"}
	if _, err := store.Record("logical-1", sessionBranch{Provider: providerA, NativeSessionID: "session-a", Cwd: "/tmp/project", Model: "model-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record("logical-1", sessionBranch{Provider: providerB, NativeSessionID: "session-b", Cwd: "/tmp/project", Model: "model-b"}); err != nil {
		t.Fatal(err)
	}
	branchA, ok, err := store.Branch("logical-1", providerA)
	if err != nil || !ok || branchA.NativeSessionID != "session-a" {
		t.Fatalf("branch A = %#v ok=%v err=%v", branchA, ok, err)
	}
	branchB, ok, err := store.Branch("logical-1", providerB)
	if err != nil || !ok || branchB.NativeSessionID != "session-b" {
		t.Fatalf("branch B = %#v ok=%v err=%v", branchB, ok, err)
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(store.path), "session_graph.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session graph mode = %o, want 600", info.Mode().Perm())
	}
}

func TestSessionGraphAPIReturnsPersistedBranches(t *testing.T) {
	dir := t.TempDir()
	store := newSessionGraphStore(dir)
	provider := providerIdentity{ID: "provider-a", Name: "Provider A", Backend: "responses", BaseURL: "https://a.example/v1"}
	if _, err := store.Record("logical-1", sessionBranch{Provider: provider, NativeSessionID: "session-a", Model: "model-a"}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Paths: paths.Paths{DataDir: dir}, sessionGraph: store}
	req := httptest.NewRequest(http.MethodGet, "/api/session-graph", nil)
	recorder := httptest.NewRecorder()
	s.handleSessionGraph(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"logical-1"`) || !strings.Contains(recorder.Body.String(), `"session-a"`) || !strings.Contains(recorder.Body.String(), `"active_branch"`) {
		t.Fatalf("unexpected graph response: %s", recorder.Body.String())
	}
}

func TestSessionGraphRemoveBranchPromotesRemainingBranchAndDropsEmptyLogicalSession(t *testing.T) {
	store := newSessionGraphStore(t.TempDir())
	providerA := providerIdentity{ID: "provider-a", Backend: "responses", BaseURL: "https://a.example/v1"}
	providerB := providerIdentity{ID: "provider-b", Backend: "openai", BaseURL: "https://b.example/v1"}
	if _, err := store.Record("logical-1", sessionBranch{Provider: providerA, NativeSessionID: "session-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record("logical-1", sessionBranch{Provider: providerB, NativeSessionID: "session-b"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveBranch("logical-1", "session-b"); err != nil {
		t.Fatal(err)
	}
	graph, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	logical := graph.Sessions["logical-1"]
	if len(logical.Branches) != 1 || logical.ActiveBranch != providerBranchKey(providerA) {
		t.Fatalf("logical after remove = %#v", logical)
	}
	if err := store.RemoveBranch("logical-1", "session-a"); err != nil {
		t.Fatal(err)
	}
	graph, err = store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := graph.Sessions["logical-1"]; ok {
		t.Fatalf("empty logical session was not removed: %#v", graph.Sessions["logical-1"])
	}
}

func TestSessionGraphDoesNotPersistProviderCredentials(t *testing.T) {
	store := newSessionGraphStore(t.TempDir())
	provider := providerIdentity{ID: "provider-a", Backend: "responses", BaseURL: "https://private.example/v1"}
	if _, err := store.Record("logical-1", sessionBranch{Provider: provider, NativeSessionID: "session-a"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"api_key", "authorization", "extra_headers"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("session graph leaked %q: %s", forbidden, data)
		}
	}
}

type sessionSwitchAgentFake struct {
	status     agentbridge.Status
	history    agentbridge.SessionHistory
	transfer   string
	stopErr    error
	stopCalls  int
	promptText string
	promptErr  error
	deletedIDs []string
	deleteErr  error
	historyErr error
}

func (f *sessionSwitchAgentFake) Status() agentbridge.Status                          { return f.status }
func (*sessionSwitchAgentFake) Start(context.Context, agentbridge.StartOptions) error { return nil }
func (*sessionSwitchAgentFake) NewSession(context.Context, string) error              { return nil }
func (f *sessionSwitchAgentFake) Prompt(text string, _ []agentbridge.Attachment) error {
	f.promptText = text
	return f.promptErr
}
func (*sessionSwitchAgentFake) CancelPrompt() error { return nil }
func (*sessionSwitchAgentFake) Subscribe() (string, <-chan agentbridge.Event) {
	return "", make(chan agentbridge.Event)
}
func (*sessionSwitchAgentFake) Unsubscribe(string)                           {}
func (*sessionSwitchAgentFake) RespondPermission(string, bool) error         { return nil }
func (*sessionSwitchAgentFake) RespondPermissionEx(string, bool, bool) error { return nil }
func (*sessionSwitchAgentFake) SetSessionAutoApprove(bool)                   {}
func (*sessionSwitchAgentFake) SetMcpServers([]acp.McpServer)                {}
func (*sessionSwitchAgentFake) McpServers() []acp.McpServer                  { return nil }
func (*sessionSwitchAgentFake) ListStoredSessions(string, int) ([]agentbridge.SessionSummary, error) {
	return nil, nil
}
func (f *sessionSwitchAgentFake) StoredSessionHistory(string) (agentbridge.SessionHistory, error) {
	return f.history, f.historyErr
}
func (*sessionSwitchAgentFake) RenameStoredSession(string, string) error { return nil }
func (f *sessionSwitchAgentFake) DeleteStoredSession(id string) error {
	f.deletedIDs = append(f.deletedIDs, id)
	return f.deleteErr
}
func (*sessionSwitchAgentFake) SetStoredSessionProvider(string, agentbridge.SessionProvider) error {
	return nil
}
func (f *sessionSwitchAgentFake) StoredSessionTransferText(string, int) (string, error) {
	return f.transfer, nil
}
func (f *sessionSwitchAgentFake) Stop() error { f.stopCalls++; return f.stopErr }

func TestSessionGraphMergeRequiresCurrentTargetAndPromptsSafeText(t *testing.T) {
	dir := t.TempDir()
	store := newSessionGraphStore(dir)
	providerA := providerIdentity{ID: "provider-a", Name: "Provider A", Backend: "responses", BaseURL: "https://a.example/v1"}
	providerB := providerIdentity{ID: "provider-b", Name: "Provider B", Backend: "openai", BaseURL: "https://b.example/v1"}
	if _, err := store.Record("logical-1", sessionBranch{Provider: providerA, NativeSessionID: "session-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record("logical-1", sessionBranch{Provider: providerB, NativeSessionID: "session-b"}); err != nil {
		t.Fatal(err)
	}
	agent := &sessionSwitchAgentFake{status: agentbridge.Status{Running: true, SessionID: "session-b"}, transfer: "用户：旧问题\n\n助手：旧回答"}
	s := &Server{Paths: paths.Paths{DataDir: dir}, sessionGraph: store, Agent: agent}
	body := strings.NewReader(`{"logical_session_id":"logical-1","source_session_id":"session-a","target_session_id":"session-b"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/session-graph/merge", body)
	recorder := httptest.NewRecorder()
	s.handleSessionGraphMerge(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(agent.promptText, "不可信的历史数据") || !strings.Contains(agent.promptText, "<untrusted_branch_history>") || !strings.Contains(agent.promptText, "旧问题") {
		t.Fatalf("unsafe or missing merge prompt: %q", agent.promptText)
	}

	agent.promptText = ""
	body = strings.NewReader(`{"logical_session_id":"logical-1","source_session_id":"session-a","target_session_id":"session-x"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/session-graph/merge", body)
	recorder = httptest.NewRecorder()
	s.handleSessionGraphMerge(recorder, req)
	if recorder.Code != http.StatusConflict || agent.promptText != "" {
		t.Fatalf("non-current target status=%d prompt=%q body=%s", recorder.Code, agent.promptText, recorder.Body.String())
	}
}

func TestSessionGraphDeleteRejectsCurrentBranchAndRemovesInactiveBranch(t *testing.T) {
	dir := t.TempDir()
	store := newSessionGraphStore(dir)
	providerA := providerIdentity{ID: "provider-a", Backend: "responses"}
	providerB := providerIdentity{ID: "provider-b", Backend: "openai"}
	if _, err := store.Record("logical-1", sessionBranch{Provider: providerA, NativeSessionID: "session-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record("logical-1", sessionBranch{Provider: providerB, NativeSessionID: "session-b"}); err != nil {
		t.Fatal(err)
	}
	agent := &sessionSwitchAgentFake{status: agentbridge.Status{Running: true, SessionID: "session-b"}}
	s := &Server{Paths: paths.Paths{DataDir: dir}, sessionGraph: store, Agent: agent}

	body := strings.NewReader(`{"logical_session_id":"logical-1","session_id":"session-b"}`)
	req := httptest.NewRequest(http.MethodDelete, "/api/session-graph/branch", body)
	recorder := httptest.NewRecorder()
	s.handleSessionGraphBranch(recorder, req)
	if recorder.Code != http.StatusConflict || len(agent.deletedIDs) != 0 {
		t.Fatalf("current delete status=%d deleted=%v body=%s", recorder.Code, agent.deletedIDs, recorder.Body.String())
	}

	body = strings.NewReader(`{"logical_session_id":"logical-1","session_id":"session-a"}`)
	req = httptest.NewRequest(http.MethodDelete, "/api/session-graph/branch", body)
	recorder = httptest.NewRecorder()
	s.handleSessionGraphBranch(recorder, req)
	if recorder.Code != http.StatusOK || len(agent.deletedIDs) != 1 || agent.deletedIDs[0] != "session-a" {
		t.Fatalf("inactive delete status=%d deleted=%v body=%s", recorder.Code, agent.deletedIDs, recorder.Body.String())
	}
	graph, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Sessions["logical-1"].Branches) != 1 {
		t.Fatalf("graph branch not removed: %#v", graph.Sessions["logical-1"])
	}
}

func TestSessionGraphMarksMissingFilesAndAllowsStaleBranchCleanup(t *testing.T) {
	dir := t.TempDir()
	store := newSessionGraphStore(dir)
	provider := providerIdentity{ID: "provider-a", Backend: "responses"}
	if _, err := store.Record("logical-missing", sessionBranch{Provider: provider, NativeSessionID: "missing-session"}); err != nil {
		t.Fatal(err)
	}
	agent := &sessionSwitchAgentFake{historyErr: os.ErrNotExist, deleteErr: os.ErrNotExist}
	s := &Server{Paths: paths.Paths{DataDir: dir}, sessionGraph: store, Agent: agent}

	req := httptest.NewRequest(http.MethodGet, "/api/session-graph", nil)
	recorder := httptest.NewRecorder()
	s.handleSessionGraph(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"health":"missing"`) {
		t.Fatalf("missing branch was not surfaced safely: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	body := strings.NewReader(`{"logical_session_id":"logical-missing","session_id":"missing-session"}`)
	req = httptest.NewRequest(http.MethodDelete, "/api/session-graph/branch", body)
	recorder = httptest.NewRecorder()
	s.handleSessionGraphBranch(recorder, req)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"session_file_already_missing":true`) {
		t.Fatalf("stale branch cleanup status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	graph, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := graph.Sessions["logical-missing"]; ok {
		t.Fatalf("stale logical session remains: %#v", graph.Sessions["logical-missing"])
	}
}

func TestSessionLoadMissingFileCleansGraphAndReturnsActionableMessage(t *testing.T) {
	dir := t.TempDir()
	store := newSessionGraphStore(dir)
	provider := providerIdentity{ID: "provider-a", Backend: "responses"}
	if _, err := store.Record("logical-missing", sessionBranch{Provider: provider, NativeSessionID: "missing-session"}); err != nil {
		t.Fatal(err)
	}
	agent := &sessionSwitchAgentFake{historyErr: os.ErrNotExist}
	s := &Server{Paths: paths.Paths{DataDir: dir}, sessionGraph: store, Agent: agent}
	body := strings.NewReader(`{"session_id":"missing-session"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/session/load", body)
	recorder := httptest.NewRecorder()
	s.handleAgentSessionLoad(recorder, req)
	if recorder.Code != http.StatusGone || !strings.Contains(recorder.Body.String(), "失效记录已从会话图谱清理") {
		t.Fatalf("missing load status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	graph, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := graph.Sessions["logical-missing"]; ok {
		t.Fatalf("missing session reference remains: %#v", graph.Sessions["logical-missing"])
	}
}

func TestPrepareProviderSwitchRejectsBusyTurn(t *testing.T) {
	s := newRoutingTestServer(t)
	agent := &sessionSwitchAgentFake{status: agentbridge.Status{Running: true, Busy: true, SessionID: "session-a"}}
	s.Agent = agent
	if _, _, err := s.prepareAgentForProviderSwitch(providerIdentity{ID: "provider-b", Backend: "openai"}); err == nil {
		t.Fatal("expected busy provider switch to be rejected")
	}
	if agent.stopCalls != 0 {
		t.Fatal("busy switch must not stop the running agent")
	}
}

func TestRollbackProviderHandoffRestoresNonOfficialActiveProfile(t *testing.T) {
	s := newRoutingTestServer(t)
	before := s.activeProviderIdentity()
	stored, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	active, _, err := s.Switcher.ActiveStatus()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Switcher.ActivateOfficial(); err != nil {
		t.Fatal(err)
	}
	handoff := &providerHandoff{Source: before, SourceActiveProfile: active.ID, SourceRoutingPolicy: &stored.Policy}
	if err := s.rollbackProviderHandoff(handoff); err != nil {
		t.Fatal(err)
	}
	after := s.activeProviderIdentity()
	if !sameProvider(before, after) || before.Model != after.Model {
		t.Fatalf("provider not restored: before=%#v after=%#v", before, after)
	}
	restored, _, err := s.Switcher.ActiveStatus()
	if err != nil || restored.ID != active.ID {
		t.Fatalf("active profile = %#v err=%v, want %q", restored, err, active.ID)
	}
}

func TestRollbackProviderHandoffRestoresOfficialSource(t *testing.T) {
	s := newRoutingTestServer(t)
	// Set routing policy to official mode.
	profileList, err := s.Profiles.List()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.applyRoutingPolicyTransaction(profileList, routing.RoutingPolicy{Official: true}); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Routing.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	source := s.activeProviderIdentity()
	if _, err := s.activateProfileRouting(profileList[0].ID); err != nil {
		t.Fatal(err)
	}
	handoff := &providerHandoff{Source: source, SourceRoutingPolicy: &stored.Policy}
	if err := s.rollbackProviderHandoff(handoff); err != nil {
		t.Fatal(err)
	}
	if after := s.activeProviderIdentity(); !after.Official {
		t.Fatalf("official source not restored: %#v", after)
	}
	if err := s.ApplyCurrentRouting(); err != nil {
		t.Fatal(err)
	}
	if after := s.activeProviderIdentity(); !after.Official {
		t.Fatalf("official source was overwritten by routing reapply: %#v", after)
	}
	storedAfter, err := s.Routing.Snapshot()
	if err != nil || !storedAfter.Policy.Official {
		t.Fatalf("official routing policy not persisted: %#v err=%v", storedAfter.Policy, err)
	}
	active, _, err := s.Switcher.ActiveStatus()
	if err != nil || active.ID != "" {
		t.Fatalf("active profile = %#v err=%v, want cleared", active, err)
	}
}

func TestPrepareProviderSwitchResumesExistingTargetBranch(t *testing.T) {
	dir := t.TempDir()
	s := newRoutingTestServer(t)
	source := s.activeProviderIdentity()
	target := providerIdentity{ID: "provider-b", Backend: "openai", BaseURL: "https://b.example/v1"}
	agent := &sessionSwitchAgentFake{
		status:   agentbridge.Status{Running: true, State: "ready", SessionID: "session-a", Cwd: dir, Model: "model-a"},
		history:  agentbridge.SessionHistory{Session: agentbridge.SessionSummary{ID: "session-a", Cwd: dir, LogicalSessionID: "logical-1"}},
		transfer: "safe text",
	}
	s.Agent = agent
	s.Paths.DataDir = dir
	store := s.sessionGraphStore()
	if _, err := store.Record("logical-1", sessionBranch{Provider: target, NativeSessionID: "session-b", Cwd: dir, Model: "model-b"}); err != nil {
		t.Fatal(err)
	}
	handoff, same, err := s.prepareAgentForProviderSwitch(target)
	if err != nil {
		t.Fatal(err)
	}
	if same || handoff == nil || handoff.Mode != "branch_resume" || handoff.TargetSessionID != "session-b" || handoff.TransferText != "" {
		t.Fatalf("unexpected handoff: %#v same=%v", handoff, same)
	}
	if !sameProvider(handoff.Source, source) {
		t.Fatalf("source = %#v, want official", handoff.Source)
	}
}
