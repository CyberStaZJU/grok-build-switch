package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"grok_switch/internal/agentbridge"
)

const sessionGraphVersion = 1

type sessionBranch struct {
	Provider        providerIdentity `json:"provider"`
	NativeSessionID string           `json:"native_session_id"`
	Cwd             string           `json:"cwd,omitempty"`
	Model           string           `json:"model,omitempty"`
	Health          string           `json:"health"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type logicalSession struct {
	ID           string                   `json:"id"`
	ActiveBranch string                   `json:"active_branch,omitempty"`
	Branches     map[string]sessionBranch `json:"branches"`
	CreatedAt    time.Time                `json:"created_at"`
	UpdatedAt    time.Time                `json:"updated_at"`
}

type sessionGraphFile struct {
	Version  int                       `json:"version"`
	Sessions map[string]logicalSession `json:"sessions"`
}

type sessionGraphStore struct {
	path string
	mu   sync.Mutex
}

func newSessionGraphStore(dataDir string) *sessionGraphStore {
	if strings.TrimSpace(dataDir) == "" {
		return nil
	}
	return &sessionGraphStore{path: filepath.Join(dataDir, "session_graph.json")}
}

func providerBranchKey(provider providerIdentity) string {
	return strings.Join([]string{
		strings.TrimSpace(provider.ID),
		strings.TrimSpace(provider.Backend),
		normalizedProviderURL(provider.BaseURL),
	}, "|")
}

func (s *sessionGraphStore) Record(logicalID string, branch sessionBranch) (logicalSession, error) {
	if s == nil {
		return logicalSession{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	graph, err := s.readLocked()
	if err != nil {
		return logicalSession{}, err
	}
	now := time.Now().UTC()
	logicalID = strings.TrimSpace(logicalID)
	if logicalID == "" {
		logicalID = strings.TrimSpace(branch.NativeSessionID)
	}
	if logicalID == "" {
		return logicalSession{}, errors.New("逻辑会话 ID 不能为空")
	}
	item := graph.Sessions[logicalID]
	if item.ID == "" {
		item = logicalSession{ID: logicalID, Branches: map[string]sessionBranch{}, CreatedAt: now}
	}
	if item.Branches == nil {
		item.Branches = map[string]sessionBranch{}
	}
	branch.NativeSessionID = strings.TrimSpace(branch.NativeSessionID)
	branch.Cwd = strings.TrimSpace(branch.Cwd)
	branch.Model = strings.TrimSpace(branch.Model)
	if branch.Health == "" {
		branch.Health = "healthy"
	}
	branch.UpdatedAt = now
	key := providerBranchKey(branch.Provider)
	item.Branches[key] = branch
	item.ActiveBranch = key
	item.UpdatedAt = now
	graph.Sessions[logicalID] = item
	if err := s.writeLocked(graph); err != nil {
		return logicalSession{}, err
	}
	return item, nil
}

func (s *sessionGraphStore) Branch(logicalID string, provider providerIdentity) (sessionBranch, bool, error) {
	if s == nil || strings.TrimSpace(logicalID) == "" {
		return sessionBranch{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	graph, err := s.readLocked()
	if err != nil {
		return sessionBranch{}, false, err
	}
	item, ok := graph.Sessions[strings.TrimSpace(logicalID)]
	if !ok {
		return sessionBranch{}, false, nil
	}
	branch, ok := item.Branches[providerBranchKey(provider)]
	return branch, ok, nil
}

func (s *sessionGraphStore) MarkHealth(logicalID string, provider providerIdentity, health string) error {
	if s == nil || strings.TrimSpace(logicalID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	graph, err := s.readLocked()
	if err != nil {
		return err
	}
	item, ok := graph.Sessions[strings.TrimSpace(logicalID)]
	if !ok {
		return nil
	}
	key := providerBranchKey(provider)
	branch, ok := item.Branches[key]
	if !ok {
		return nil
	}
	branch.Health = strings.TrimSpace(health)
	branch.UpdatedAt = time.Now().UTC()
	item.Branches[key] = branch
	item.UpdatedAt = branch.UpdatedAt
	graph.Sessions[item.ID] = item
	return s.writeLocked(graph)
}

func (s *sessionGraphStore) readLocked() (sessionGraphFile, error) {
	graph := sessionGraphFile{Version: sessionGraphVersion, Sessions: map[string]logicalSession{}}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return graph, nil
	}
	if err != nil {
		return graph, err
	}
	if err := json.Unmarshal(data, &graph); err != nil {
		return sessionGraphFile{}, fmt.Errorf("读取逻辑会话索引失败: %w", err)
	}
	if graph.Sessions == nil {
		graph.Sessions = map[string]logicalSession{}
	}
	graph.Version = sessionGraphVersion
	return graph, nil
}

func (s *sessionGraphStore) writeLocked(graph sessionGraphFile) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(graph, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "session-graph-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

func logicalIDFromHistory(history agentbridge.SessionHistory) string {
	if id := strings.TrimSpace(history.Session.LogicalSessionID); id != "" {
		return id
	}
	return strings.TrimSpace(history.Session.ID)
}
