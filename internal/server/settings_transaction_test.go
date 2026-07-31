package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"grok_switch/internal/remoteaccess"
	"grok_switch/internal/settings"
)

func newSettingsTransactionServer(t *testing.T) (*Server, settings.Settings, remoteaccess.Snapshot) {
	t.Helper()
	settingsStore := settings.NewStore(filepath.Join(t.TempDir(), "settings.json"))
	current := settings.Default()
	current.LANAccessEnabled = true
	current.Autostart = false
	current.SilentAutostart = true
	var err error
	current, err = settingsStore.Update(current)
	if err != nil {
		t.Fatal(err)
	}
	remoteStore := remoteaccess.NewStore(filepath.Join(t.TempDir(), "remote.json"))
	snapshot, err := remoteStore.Get()
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Settings: settingsStore, RemoteAccess: remoteStore, ExePath: "/Applications/Grok Build Switch.app/Contents/MacOS/grok_switch"}, current, snapshot
}

func putSettings(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := loopbackRequest(http.MethodPut, "/api/settings", body)
	res := httptest.NewRecorder()
	s.handleSettings(res, req)
	return res
}

func disabledSettingsJSON() string {
	return `{"port":17878,"actual_port":17878,"theme":"light","autostart":true,"silent_autostart":true,"auto_open_browser":true,"lan_access_enabled":false,"provider_order":[],"pinned_provider_ids":[]}`
}

func assertSettingsAndSessionUnchanged(t *testing.T, s *Server, wantSettings settings.Settings, wantSession remoteaccess.Snapshot) {
	t.Helper()
	gotSettings, err := s.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotSettings, wantSettings) {
		t.Fatalf("settings changed: got=%#v want=%#v", gotSettings, wantSettings)
	}
	gotSession, err := s.RemoteAccess.Get()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotSession, wantSession) {
		t.Fatalf("remote session changed: got=%#v want=%#v", gotSession, wantSession)
	}
}

func TestSettingsTransactionRollsBackListenerOnAutostartFailure(t *testing.T) {
	s, current, snapshot := newSettingsTransactionServer(t)
	var listenerCalls []bool
	s.reconfigureLAN = func(enabled bool) error {
		listenerCalls = append(listenerCalls, enabled)
		return nil
	}
	s.autostartSync = func(bool, string, bool) error { return errors.New("autostart failed") }
	res := putSettings(t, s, disabledSettingsJSON())
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if !reflect.DeepEqual(listenerCalls, []bool{false, true}) {
		t.Fatalf("listener calls=%v", listenerCalls)
	}
	assertSettingsAndSessionUnchanged(t, s, current, snapshot)
}

func TestSettingsTransactionRollsBackExternalEffectsOnSessionFailure(t *testing.T) {
	s, current, snapshot := newSettingsTransactionServer(t)
	var listenerCalls []bool
	var autostartCalls []bool
	s.reconfigureLAN = func(enabled bool) error { listenerCalls = append(listenerCalls, enabled); return nil }
	s.autostartSync = func(enabled bool, _ string, _ bool) error {
		autostartCalls = append(autostartCalls, enabled)
		return nil
	}
	s.resetRemoteSessions = func() error { return errors.New("session failed") }
	res := putSettings(t, s, disabledSettingsJSON())
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if !reflect.DeepEqual(listenerCalls, []bool{false, true}) || !reflect.DeepEqual(autostartCalls, []bool{true, false}) {
		t.Fatalf("listener=%v autostart=%v", listenerCalls, autostartCalls)
	}
	assertSettingsAndSessionUnchanged(t, s, current, snapshot)
}

func TestSettingsTransactionRestoresSessionWhenPersistenceFails(t *testing.T) {
	s, current, snapshot := newSettingsTransactionServer(t)
	var listenerCalls []bool
	var autostartCalls []bool
	s.reconfigureLAN = func(enabled bool) error { listenerCalls = append(listenerCalls, enabled); return nil }
	s.autostartSync = func(enabled bool, _ string, _ bool) error {
		autostartCalls = append(autostartCalls, enabled)
		return nil
	}
	s.updateSettings = func(settings.Settings) (settings.Settings, error) {
		return settings.Settings{}, errors.New("persist failed")
	}
	res := putSettings(t, s, disabledSettingsJSON())
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if !reflect.DeepEqual(listenerCalls, []bool{false, true}) || !reflect.DeepEqual(autostartCalls, []bool{true, false}) {
		t.Fatalf("listener=%v autostart=%v", listenerCalls, autostartCalls)
	}
	assertSettingsAndSessionUnchanged(t, s, current, snapshot)
}

func TestSettingsTransactionDoesNotPersistWhenListenerFails(t *testing.T) {
	s, current, snapshot := newSettingsTransactionServer(t)
	s.reconfigureLAN = func(bool) error { return errors.New("bind failed") }
	called := false
	s.autostartSync = func(bool, string, bool) error { called = true; return nil }
	s.updateSettings = func(next settings.Settings) (settings.Settings, error) { called = true; return next, nil }
	res := putSettings(t, s, disabledSettingsJSON())
	if res.Code != http.StatusInternalServerError || called {
		t.Fatalf("status=%d called=%v body=%s", res.Code, called, res.Body.String())
	}
	assertSettingsAndSessionUnchanged(t, s, current, snapshot)
}

func TestSettingsTransactionSurfacesRollbackFailure(t *testing.T) {
	s, _, _ := newSettingsTransactionServer(t)
	calls := 0
	s.reconfigureLAN = func(bool) error {
		calls++
		if calls == 2 {
			return errors.New("restore bind failed")
		}
		return nil
	}
	s.autostartSync = func(bool, string, bool) error { return errors.New("autostart failed") }
	res := putSettings(t, s, disabledSettingsJSON())
	if res.Code != http.StatusInternalServerError || !strings.Contains(res.Body.String(), "回滚失败") || !strings.Contains(res.Body.String(), "restore bind failed") {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestSettingsTransactionSuccessCommitsLastAndRevokesSession(t *testing.T) {
	s, _, snapshot := newSettingsTransactionServer(t)
	var listenerCalls []bool
	s.reconfigureLAN = func(enabled bool) error { listenerCalls = append(listenerCalls, enabled); return nil }
	s.autostartSync = func(bool, string, bool) error { return nil }
	res := putSettings(t, s, disabledSettingsJSON())
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	got, err := s.Settings.Get()
	if err != nil {
		t.Fatal(err)
	}
	if got.LANAccessEnabled || !got.Autostart || !reflect.DeepEqual(listenerCalls, []bool{false}) {
		t.Fatalf("settings=%#v listener=%v", got, listenerCalls)
	}
	authorized, err := s.RemoteAccess.Authorized(snapshot.SessionToken)
	if err != nil {
		t.Fatal(err)
	}
	if authorized {
		t.Fatal("old LAN session remains authorized after successful disable")
	}
}
