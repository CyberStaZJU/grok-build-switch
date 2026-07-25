package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	"grok_switch/internal/agentbridge"
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
	status    agentbridge.Status
	history   agentbridge.SessionHistory
	transfer  string
	stopErr   error
	stopCalls int
}

func (f *sessionSwitchAgentFake) Status() agentbridge.Status                          { return f.status }
func (*sessionSwitchAgentFake) Start(context.Context, agentbridge.StartOptions) error { return nil }
func (*sessionSwitchAgentFake) NewSession(context.Context, string) error              { return nil }
func (*sessionSwitchAgentFake) Prompt(string, []agentbridge.Attachment) error         { return nil }
func (*sessionSwitchAgentFake) CancelPrompt() error                                   { return nil }
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
	return f.history, nil
}
func (*sessionSwitchAgentFake) RenameStoredSession(string, string) error { return nil }
func (*sessionSwitchAgentFake) DeleteStoredSession(string) error         { return nil }
func (*sessionSwitchAgentFake) SetStoredSessionProvider(string, agentbridge.SessionProvider) error {
	return nil
}
func (f *sessionSwitchAgentFake) StoredSessionTransferText(string, int) (string, error) {
	return f.transfer, nil
}
func (f *sessionSwitchAgentFake) Stop() error { f.stopCalls++; return f.stopErr }

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
