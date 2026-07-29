package agentbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoredSessionTransferTextRejectsHistoryWithoutConversationMessages(t *testing.T) {
	grokHome := t.TempDir()
	sessionDir := filepath.Join(grokHome, "sessions", "encoded-cwd", "session-empty")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var summary storedSummary
	summary.Info.ID = "session-empty"
	summary.Info.Cwd = "/tmp/project"
	summary.GeneratedTitle = "Only a title"
	summaryData, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.json"), summaryData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "chat_history.jsonl"), []byte(`{"type":"reasoning","content":"private"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bridge := New(grokHome, filepath.Join(t.TempDir(), "agent.log"))
	text, err := bridge.StoredSessionTransferText("session-empty", 48000)
	if err != nil {
		t.Fatal(err)
	}
	if text != "" {
		t.Fatalf("empty conversation transfer = %q, want empty", text)
	}
}

func TestStoredSessionProviderAndTransferText(t *testing.T) {
	grokHome := t.TempDir()
	projectDir := t.TempDir()
	sessionDir := filepath.Join(grokHome, "sessions", "encoded-cwd", "session-provider")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var summary storedSummary
	summary.Info.ID = "session-provider"
	summary.Info.Cwd = projectDir
	summary.GeneratedTitle = "Provider handoff"
	summary.CreatedAt = time.Now().UTC()
	summary.UpdatedAt = summary.CreatedAt
	summary.CurrentModelID = "old-model"
	summaryData, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.json"), summaryData, 0o644); err != nil {
		t.Fatal(err)
	}
	history := strings.Join([]string{
		`{"type":"user","content":"<user_query>inspect the project</user_query>"}`,
		`{"type":"reasoning","summary":[{"type":"summary_text","text":"private chain of thought"}]}`,
		`{"type":"assistant","content":"I will inspect it.","model_id":"old-model","tool_calls":[{"id":"call-1","name":"read_file","arguments":"{\"path\":\"secret.txt\"}"}]}`,
		`{"type":"tool_result","tool_call_id":"call-1","content":"SECRET_TOOL_OUTPUT"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(sessionDir, "chat_history.jsonl"), []byte(history), 0o644); err != nil {
		t.Fatal(err)
	}

	bridge := New(grokHome, filepath.Join(t.TempDir(), "agent.log"))
	if err := bridge.SetStoredSessionProvider("session-provider", SessionProvider{
		ID: "provider-a", Name: "Provider A", Backend: "responses", Model: "old-model",
		LogicalSessionID: "logical-1", ParentSessionID: "parent-session",
		MigrationMode: "text_migration", Health: "healthy",
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := bridge.StoredSessionHistory("session-provider")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Session.ProviderID != "provider-a" || loaded.Session.ProviderBackend != "responses" ||
		loaded.Session.LogicalSessionID != "logical-1" || loaded.Session.ParentSessionID != "parent-session" ||
		loaded.Session.MigrationMode != "text_migration" || loaded.Session.BranchHealth != "healthy" {
		t.Fatalf("provider metadata not loaded: %#v", loaded.Session)
	}
	info, err := os.Stat(filepath.Join(sessionDir, "grok_switch_provider.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("provider sidecar mode = %o, want 600", info.Mode().Perm())
	}

	transfer, err := bridge.StoredSessionTransferText("session-provider", 48000)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"用户：inspect the project", "助手：I will inspect it.", "重新检查文件和环境"} {
		if !strings.Contains(transfer, expected) {
			t.Fatalf("transfer text missing %q: %s", expected, transfer)
		}
	}
	for _, forbidden := range []string{"private chain of thought", "read_file", "call-1", "SECRET_TOOL_OUTPUT", "secret.txt"} {
		if strings.Contains(transfer, forbidden) {
			t.Fatalf("transfer text leaked provider-specific data %q: %s", forbidden, transfer)
		}
	}
}
