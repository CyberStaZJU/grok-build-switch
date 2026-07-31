package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateRejectsInvalidPort(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "settings.json"))
	next := Default()
	next.Port = 70000
	if _, err := store.Update(next); err == nil {
		t.Fatal("Update() accepted an invalid port")
	} else if !IsValidationError(err) {
		t.Fatalf("Update() error = %T %v, want ValidationError", err, err)
	}
}

func TestGetRecoversCorruptJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := NewStore(path).Get()
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != Default().Port || got.ActualPort != Default().ActualPort {
		t.Fatalf("recovered settings = %#v", got)
	}
	assertOneCorruptBackup(t, path)
}

func TestSettingsNoLongerPersistOAuthClientID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"port":17878,"actual_port":17878,"oauth_client_id":"legacy-client"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	current, err := store.Get()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(current); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "oauth_client_id") {
		t.Fatalf("legacy OAuth client ID remained in settings: %s", raw)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["oauth_client_id"]; ok {
		t.Fatalf("oauth_client_id persisted: %#v", decoded)
	}
}

func TestGetRepairsInvalidPersistedPort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	data := []byte(`{"port":70000,"actual_port":70000,"theme":"light"}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := NewStore(path).Get()
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != Default().Port || got.ActualPort != Default().Port {
		t.Fatalf("repaired settings = %#v", got)
	}
	assertOneCorruptBackup(t, path)
}

func assertOneCorruptBackup(t *testing.T, path string) {
	t.Helper()
	matches, err := filepath.Glob(path + ".corrupt-*.bak")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("corrupt backups = %#v, want one", matches)
	}
}
