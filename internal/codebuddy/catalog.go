package codebuddy

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"grok_switch/internal/profiles"
)

const maxCatalogBytes = 32 << 20

// Catalog is the CodeBuddy model list available to the WorkBuddy CLI agent.
// The local WorkBuddy product cache is read-only and contains no API key.
type Catalog struct {
	Models    []CatalogModel `json:"models"`
	Source    string         `json:"source"`
	UpdatedAt time.Time      `json:"updated_at,omitempty"`
}

// CatalogModel contains only model capability fields needed by the Switch.
type CatalogModel struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name,omitempty"`
	MaxInputTokens         int64    `json:"max_input_tokens,omitempty"`
	MaxOutputTokens        int64    `json:"max_output_tokens,omitempty"`
	SupportsReasoning      bool     `json:"supports_reasoning"`
	SupportsToolCall       bool     `json:"supports_tool_call"`
	CanDisableThinking     bool     `json:"can_disable_thinking,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	ReasoningEfforts       []string `json:"reasoning_efforts,omitempty"`
}

// LoadCatalog reads the newest valid WorkBuddy product catalog. It falls back
// to the Switch-owned catalog when WorkBuddy is absent or its cache is invalid.
func LoadCatalog(home string) Catalog {
	fallback := BuiltInCatalog()
	dir := filepath.Join(strings.TrimSpace(home), ".workbuddy", "local_storage")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fallback
	}
	type candidate struct {
		path    string
		modTime time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !strings.HasSuffix(name, ".info") ||
			(!strings.HasPrefix(name, "entry_") && !strings.HasPrefix(name, "wb_entry_")) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxCatalogBytes {
			continue
		}
		candidates = append(candidates, candidate{path: filepath.Join(dir, name), modTime: info.ModTime()})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].modTime.After(candidates[j].modTime) })
	var newest Catalog
	var newestModTime time.Time
	for _, item := range candidates {
		catalog, ok := readCatalogFile(item.path, item.modTime)
		if !ok {
			continue
		}
		if newest.Models == nil || catalog.UpdatedAt.After(newest.UpdatedAt) ||
			(catalog.UpdatedAt.Equal(newest.UpdatedAt) && item.modTime.After(newestModTime)) {
			newest = catalog
			newestModTime = item.modTime
		}
	}
	if len(newest.Models) > 0 {
		return newest
	}
	return fallback
}

func readCatalogFile(path string, modTime time.Time) (Catalog, bool) {
	file, err := os.Open(path)
	if err != nil {
		return Catalog{}, false
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxCatalogBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maxCatalogBytes {
		return Catalog{}, false
	}
	var rows []struct {
		TS   int64 `json:"ts"`
		Data struct {
			Agents []struct {
				Name   string   `json:"name"`
				Models []string `json:"models"`
			} `json:"agents"`
			Models []struct {
				ID                string `json:"id"`
				Name              string `json:"name"`
				MaxAllowedSize    int64  `json:"maxAllowedSize"`
				MaxInputTokens    int64  `json:"maxInputTokens"`
				MaxOutputTokens   int64  `json:"maxOutputTokens"`
				SupportsReasoning bool   `json:"supportsReasoning"`
				SupportsToolCall  bool   `json:"supportsToolCall"`
				Reasoning         struct {
					CanDisableThinking bool     `json:"canDisableThinking"`
					DefaultEffort      string   `json:"defaultEffort"`
					SupportedEfforts   []string `json:"supportedEfforts"`
				} `json:"reasoning"`
			} `json:"models"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) == 0 {
		return Catalog{}, false
	}
	row := rows[0]
	for _, candidate := range rows[1:] {
		if candidate.TS > row.TS {
			row = candidate
		}
	}
	var allowed []string
	for _, agent := range row.Data.Agents {
		if agent.Name == "cli" {
			allowed = append(allowed, agent.Models...)
			break
		}
	}
	if len(allowed) == 0 {
		return Catalog{}, false
	}
	byID := make(map[string]CatalogModel, len(row.Data.Models))
	for _, model := range row.Data.Models {
		id := strings.TrimSpace(model.ID)
		if !IsValidModelID(id) || !model.SupportsToolCall {
			continue
		}
		input := model.MaxInputTokens
		if input == 0 {
			input = model.MaxAllowedSize
		}
		byID[id] = CatalogModel{
			ID:                     id,
			Name:                   strings.TrimSpace(model.Name),
			MaxInputTokens:         input,
			MaxOutputTokens:        model.MaxOutputTokens,
			SupportsReasoning:      model.SupportsReasoning,
			SupportsToolCall:       model.SupportsToolCall,
			CanDisableThinking:     model.Reasoning.CanDisableThinking,
			DefaultReasoningEffort: strings.TrimSpace(model.Reasoning.DefaultEffort),
			ReasoningEfforts:       uniqueModelIDs(model.Reasoning.SupportedEfforts),
		}
	}
	models := make([]CatalogModel, 0, len(allowed))
	seen := map[string]bool{}
	for _, id := range allowed {
		id = strings.TrimSpace(id)
		model, ok := byID[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, model)
	}
	if len(models) == 0 {
		return Catalog{}, false
	}
	updatedAt := modTime
	if row.TS > 0 {
		updatedAt = time.UnixMilli(row.TS)
	}
	return Catalog{Models: models, Source: "workbuddy_local_catalog", UpdatedAt: updatedAt}, true
}

// BuiltInCatalog is the offline fallback and includes models directly verified
// against CodeBuddy's /v2/chat/completions endpoint.
func BuiltInCatalog() Catalog {
	models := make([]CatalogModel, 0, len(KnownModels))
	for _, id := range KnownModels {
		model := CatalogModel{ID: id, Name: id, SupportsReasoning: true, SupportsToolCall: true}
		switch id {
		case "kimi-k3-2":
			model.Name, model.MaxInputTokens, model.MaxOutputTokens = "Kimi-K3", 1000000, 32000
			model.CanDisableThinking, model.DefaultReasoningEffort = true, "high"
			model.ReasoningEfforts = []string{"low", "high", "xhigh"}
		case "hy4-preview":
			model.Name, model.MaxInputTokens, model.MaxOutputTokens = "Hy4 preview", 1000000, 64000
			model.DefaultReasoningEffort = "high"
			model.ReasoningEfforts = []string{"high"}
		case "glm-5.3":
			model.Name, model.MaxInputTokens, model.MaxOutputTokens = "GLM-5.3", 1000000, 48000
			model.CanDisableThinking, model.DefaultReasoningEffort = true, "high"
			model.ReasoningEfforts = []string{"low", "high", "xhigh"}
		case "glm-5.3-flash":
			model.Name, model.MaxInputTokens, model.MaxOutputTokens = "GLM-5.3-Flash", 1000000, 32000
			model.CanDisableThinking, model.DefaultReasoningEffort = true, "high"
			model.ReasoningEfforts = []string{"low", "high", "max"}
		}
		models = append(models, model)
	}
	return Catalog{Models: models, Source: "switch_builtin"}
}

func (c Catalog) IDs() []string {
	ids := make([]string, 0, len(c.Models))
	for _, model := range c.Models {
		if IsValidModelID(model.ID) {
			ids = append(ids, model.ID)
		}
	}
	return uniqueModelIDs(ids)
}

func (c Catalog) Find(id string) (CatalogModel, bool) {
	id = strings.TrimSpace(id)
	for _, model := range c.Models {
		if model.ID == id {
			return model, true
		}
	}
	return CatalogModel{}, false
}

// ModelDefinition maps catalog metadata into a managed Grok model definition.
func ModelDefinition(model CatalogModel, baseURL, apiKey string) profiles.ModelDef {
	contextWindow := model.MaxInputTokens
	if contextWindow == 0 {
		contextWindow = profiles.KnownContextWindow(model.ID)
	}
	if contextWindow == 0 {
		contextWindow = 128000
	}
	maxOutput := model.MaxOutputTokens
	if maxOutput == 0 {
		maxOutput = 8192
	}
	efforts := uniqueModelIDs(model.ReasoningEfforts)
	def := profiles.ModelDef{
		Name:                    model.ID,
		Model:                   model.ID,
		BaseURL:                 baseURL,
		APIKey:                  apiKey,
		APIBackend:              "chat_completions",
		SupportsBackendSearch:   false,
		SupportsReasoningEffort: model.SupportsReasoning,
		ReasoningEfforts:        efforts,
		ContextWindow:           contextWindow,
		MaxCompletionTokens:     maxOutput,
		StreamToolCalls:         profiles.BoolPtr(false),
	}
	if model.SupportsReasoning && len(efforts) > 0 {
		def.ReasoningEffortsSource = "declared"
	}
	return def
}

// IsValidModelID rejects aliases, whitespace, control characters, and other
// values that should never be forwarded as a CodeBuddy upstream model ID.
func IsValidModelID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 160 || strings.Contains(id, "@") {
		return false
	}
	for _, r := range id {
		if r <= unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-._:/", r)) {
			continue
		}
		return false
	}
	return true
}

func uniqueModelIDs(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
