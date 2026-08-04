package cachestats

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Stats is an aggregated cache hit summary.
type Stats struct {
	Turns              int      `json:"turns"`
	PromptTokens       int64    `json:"prompt_tokens"`
	CachedPromptTokens int64    `json:"cached_prompt_tokens"`
	CompletionTokens   int64    `json:"completion_tokens"`
	ReasoningTokens    int64    `json:"reasoning_tokens"`
	HitRate            *float64 `json:"hit_rate"` // cached / prompt, null if no prompt tokens
}

func (s *Stats) add(prompt, cached, completion, reasoning int64) {
	if prompt <= 0 && cached <= 0 && completion <= 0 && reasoning <= 0 {
		return
	}
	s.Turns++
	s.PromptTokens += prompt
	s.CachedPromptTokens += cached
	s.CompletionTokens += completion
	s.ReasoningTokens += reasoning
}

func (s *Stats) finalize() {
	if s.PromptTokens > 0 {
		rate := float64(s.CachedPromptTokens) / float64(s.PromptTokens)
		if rate > 1 {
			rate = 1
		}
		if rate < 0 {
			rate = 0
		}
		s.HitRate = &rate
	} else {
		s.HitRate = nil
	}
}

// ModelStats is per-model aggregation.
type ModelStats struct {
	Model string `json:"model"`
	Stats
}

// SessionStats is per-session aggregation.
type SessionStats struct {
	SessionID string `json:"session_id"`
	Model     string `json:"model,omitempty"`
	Stats
}

// Turn is a recent inference turn for the detail list.
type Turn struct {
	TS                 time.Time `json:"ts"`
	SessionID          string    `json:"session_id,omitempty"`
	Model              string    `json:"model,omitempty"`
	PromptTokens       int64     `json:"prompt_tokens"`
	CachedPromptTokens int64     `json:"cached_prompt_tokens"`
	CompletionTokens   int64     `json:"completion_tokens"`
	ReasoningTokens    int64     `json:"reasoning_tokens"`
	HitRate            *float64  `json:"hit_rate"`
}

// Report is the API response body.
type Report struct {
	Hours     int            `json:"hours"`
	LogPath   string         `json:"log_path"`
	LogExists bool           `json:"log_exists"`
	Scanned   int            `json:"scanned_events"`
	Overall   Stats          `json:"overall"`
	ByModel   []ModelStats   `json:"by_model"`
	BySession []SessionStats `json:"by_session"`
	Session   *SessionStats  `json:"session,omitempty"`
	Recent    []Turn         `json:"recent"`
}

type logLine struct {
	TS  string `json:"ts"`
	Msg string `json:"msg"`
	SID string `json:"sid"`
	Ctx struct {
		PromptTokens       int64 `json:"prompt_tokens"`
		CachedPromptTokens int64 `json:"cached_prompt_tokens"`
		CompletionTokens   int64 `json:"completion_tokens"`
		ReasoningTokens    int64 `json:"reasoning_tokens"`
	} `json:"ctx"`
}

// Collect reads ~/.grok/logs/unified.jsonl and aggregates cache hit metrics.
// hours <= 0 defaults to 24. sessionID filters the dedicated session block.
func Collect(grokHome string, hours int, sessionID string) (Report, error) {
	if hours <= 0 {
		hours = 24
	}
	if hours > 24*30 {
		hours = 24 * 30
	}
	logPath := filepath.Join(grokHome, "logs", "unified.jsonl")
	report := Report{
		Hours:     hours,
		LogPath:   logPath,
		Recent:    []Turn{},
		ByModel:   []ModelStats{},
		BySession: []SessionStats{},
	}
	info, err := os.Stat(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			report.LogExists = false
			report.Overall.finalize()
			return report, nil
		}
		return report, err
	}
	report.LogExists = true

	cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
	modelBySID := loadSessionModels(grokHome)

	// Cap scan size: for large logs only read the trailing portion.
	const maxScan = 32 << 20 // 32 MiB
	f, err := os.Open(logPath)
	if err != nil {
		return report, err
	}
	defer f.Close()
	if info.Size() > maxScan {
		if _, err := f.Seek(info.Size()-maxScan, 0); err != nil {
			return report, err
		}
	}

	overall := Stats{}
	byModel := map[string]*Stats{}
	bySession := map[string]*Stats{}
	var recent []Turn

	scanner := bufio.NewScanner(f)
	// inference lines can be long
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	// If we sought into the middle of a line, drop the partial first line.
	if info.Size() > maxScan {
		_ = scanner.Scan()
	}

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		// Cheap filter before full JSON parse.
		if !bytesContains(line, []byte(`"shell.turn.inference_done"`)) {
			continue
		}
		var entry logLine
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Msg != "shell.turn.inference_done" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, entry.TS)
		if err != nil {
			ts, err = time.Parse(time.RFC3339, entry.TS)
			if err != nil {
				continue
			}
		}
		if ts.Before(cutoff) {
			continue
		}
		report.Scanned++
		prompt := entry.Ctx.PromptTokens
		cached := entry.Ctx.CachedPromptTokens
		completion := entry.Ctx.CompletionTokens
		reasoning := entry.Ctx.ReasoningTokens
		overall.add(prompt, cached, completion, reasoning)

		model := modelBySID[entry.SID]
		if model == "" {
			model = "未知模型"
		}
		if byModel[model] == nil {
			byModel[model] = &Stats{}
		}
		byModel[model].add(prompt, cached, completion, reasoning)

		sid := entry.SID
		if sid == "" {
			sid = "（无会话）"
		}
		if bySession[sid] == nil {
			bySession[sid] = &Stats{}
		}
		bySession[sid].add(prompt, cached, completion, reasoning)

		turn := Turn{
			TS:                 ts,
			SessionID:          entry.SID,
			Model:              model,
			PromptTokens:       prompt,
			CachedPromptTokens: cached,
			CompletionTokens:   completion,
			ReasoningTokens:    reasoning,
		}
		if prompt > 0 {
			rate := float64(cached) / float64(prompt)
			if rate > 1 {
				rate = 1
			}
			turn.HitRate = &rate
		}
		recent = append(recent, turn)
	}
	if err := scanner.Err(); err != nil {
		return report, err
	}

	overall.finalize()
	report.Overall = overall

	for model, st := range byModel {
		st.finalize()
		report.ByModel = append(report.ByModel, ModelStats{Model: model, Stats: *st})
	}
	sort.Slice(report.ByModel, func(i, j int) bool {
		return report.ByModel[i].PromptTokens > report.ByModel[j].PromptTokens
	})

	for sid, st := range bySession {
		st.finalize()
		report.BySession = append(report.BySession, SessionStats{
			SessionID: sid,
			Model:     modelBySID[sid],
			Stats:     *st,
		})
	}
	sort.Slice(report.BySession, func(i, j int) bool {
		return report.BySession[i].PromptTokens > report.BySession[j].PromptTokens
	})
	if len(report.BySession) > 20 {
		report.BySession = report.BySession[:20]
	}

	// Keep last 20 turns (most recent).
	if len(recent) > 20 {
		recent = recent[len(recent)-20:]
	}
	// Reverse to newest first.
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}
	report.Recent = recent

	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		st := bySession[sessionID]
		if st == nil {
			empty := Stats{}
			empty.finalize()
			report.Session = &SessionStats{SessionID: sessionID, Model: modelBySID[sessionID], Stats: empty}
		} else {
			cp := *st
			cp.finalize()
			report.Session = &SessionStats{SessionID: sessionID, Model: modelBySID[sessionID], Stats: cp}
		}
	}

	return report, nil
}

func loadSessionModels(grokHome string) map[string]string {
	out := map[string]string{}
	root := filepath.Join(grokHome, "sessions")
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() != "summary.json" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var summary struct {
			Info struct {
				ID string `json:"id"`
			} `json:"info"`
			CurrentModelID string `json:"current_model_id"`
		}
		if json.Unmarshal(raw, &summary) != nil {
			return nil
		}
		if summary.Info.ID != "" && summary.CurrentModelID != "" {
			out[summary.Info.ID] = summary.CurrentModelID
		}
		return nil
	})
	return out
}

func bytesContains(b, sub []byte) bool {
	return bytes.Contains(b, sub)
}
