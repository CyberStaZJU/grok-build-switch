package server

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"grok_switch/internal/codebuddy"
	"grok_switch/internal/profiles"
	"grok_switch/internal/routing"
)

// CodeBuddyProxyBaseURL returns the in-process OpenAI-compatible root.
func (s *Server) CodeBuddyProxyBaseURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/codebuddy-proxy/v1", s.ActualPort)
}

func (s *Server) codeBuddyCatalog() codebuddy.Catalog {
	home, err := os.UserHomeDir()
	if err != nil {
		return codebuddy.BuiltInCatalog()
	}
	return codebuddy.LoadCatalog(home)
}

func (s *Server) handleCodeBuddyProxy(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		http.Error(w, "仅允许本机访问", http.StatusForbidden)
		return
	}
	// Resolve fallback key and advertised ids (with @Provider when names clash).
	fallback := ""
	var offers []codebuddy.ModelOffer
	if s.Profiles != nil {
		if list, err := s.Profiles.List(); err == nil {
			if profile, ok := selectCodeBuddyProfile(list); ok {
				if strings.TrimSpace(profile.APIKey) != "" {
					fallback = profile.APIKey
				}
				offers = codeBuddyModelOffers(profile, list)
			}
		}
	}
	if offers == nil {
		offers = []codebuddy.ModelOffer{}
	}
	h := &codebuddy.Handler{FallbackKey: fallback, Offers: offers, AllowedModels: s.codeBuddyCatalog().IDs()}
	h.ServeHTTP(w, r)
}

// EnsureCodeBuddyRoutes rewrites managed profile base URLs onto the current
// Switch listen port (same pattern as subscription-proxy).
func (s *Server) EnsureCodeBuddyRoutes() error {
	if s.Profiles == nil || s.Switcher == nil || s.ActualPort == 0 {
		return nil
	}
	list, err := s.Profiles.List()
	if err != nil {
		return err
	}
	target := s.CodeBuddyProxyBaseURL()
	changed := false
	for _, profile := range list {
		if !isCodeBuddyManagedProfile(profile) {
			continue
		}
		profileChanged := profile.BaseURL != target || profile.Source != codebuddy.SourceTag
		profile.Source = codebuddy.SourceTag
		profile.BaseURL = target
		for i := range profile.Models {
			if profile.Models[i].BaseURL != target {
				profile.Models[i].BaseURL = target
				profileChanged = true
			}
			if profile.Models[i].StreamToolCalls == nil || *profile.Models[i].StreamToolCalls {
				profile.Models[i].StreamToolCalls = profiles.BoolPtr(false)
				profileChanged = true
			}
		}
		if !profileChanged {
			continue
		}
		if _, updateErr := s.Profiles.Update(profile.ID, profile); updateErr != nil {
			return updateErr
		}
		changed = true
	}
	if changed {
		if s.Routing != nil {
			if err := s.ApplyCurrentRouting(); err != nil {
				return err
			}
		}
		s.changed()
	}
	return nil
}

// CodeBuddyEnsureOptions controls managed profile create/update/activation.
type CodeBuddyEnsureOptions struct {
	APIKey        string
	DefaultModel  string
	EnabledModels []string
	Activate      bool
	SyncCatalog   bool
}

// EnsureCodeBuddyProvider creates or updates the managed CodeBuddy profile and
// optionally activates it as the active Grok routing provider.
func (s *Server) EnsureCodeBuddyProvider(apiKey string, activate bool) (profiles.Profile, error) {
	return s.EnsureCodeBuddyProviderOpts(CodeBuddyEnsureOptions{APIKey: apiKey, Activate: activate})
}

// EnsureCodeBuddyProviderOpts is the full form of EnsureCodeBuddyProvider.
func (s *Server) EnsureCodeBuddyProviderOpts(opts CodeBuddyEnsureOptions) (profiles.Profile, error) {
	if s.Profiles == nil || s.Switcher == nil || s.ActualPort == 0 {
		return profiles.Profile{}, fmt.Errorf("server not ready")
	}
	apiKey := strings.TrimSpace(opts.APIKey)
	if apiKey == "" {
		return profiles.Profile{}, fmt.Errorf("缺少 CodeBuddy API Key")
	}
	catalog := s.codeBuddyCatalog()
	defaultModel := strings.TrimSpace(opts.DefaultModel)
	if defaultModel == "" {
		defaultModel = codebuddy.DefaultModel
		if _, ok := catalog.Find(defaultModel); !ok {
			ids := catalog.IDs()
			if len(ids) == 0 {
				return profiles.Profile{}, fmt.Errorf("CodeBuddy 模型目录为空")
			}
			defaultModel = ids[0]
		}
	}
	if _, ok := catalog.Find(defaultModel); !ok {
		return profiles.Profile{}, fmt.Errorf("当前 CodeBuddy 模型目录不包含 %q；请先刷新模型目录", defaultModel)
	}
	baseURL := s.CodeBuddyProxyBaseURL()
	desired := codebuddy.NewProfileFromCatalog(baseURL, apiKey, catalog)
	desired.DefaultModel = defaultModel
	if opts.EnabledModels != nil {
		selected, selectErr := selectedCodeBuddyModels(catalog, opts.EnabledModels, baseURL, apiKey)
		if selectErr != nil {
			return profiles.Profile{}, selectErr
		}
		if !codeBuddyHasModel(selected, defaultModel) {
			return profiles.Profile{}, fmt.Errorf("CodeBuddy 默认模型 %q 必须属于已选择的暴露模型", defaultModel)
		}
		desired.Models = selected
		desired.AvailableModels = codeBuddyModelNames(selected)
	}

	list, err := s.Profiles.List()
	if err != nil {
		return profiles.Profile{}, err
	}
	var profile profiles.Profile
	var previous *profiles.Profile
	createdNew := false
	if existing, ok := selectCodeBuddyProfile(list); ok {
		previousCopy := existing
		previous = &previousCopy
		desired.ID = existing.ID
		desired.CreatedAt = existing.CreatedAt
		// Startup keeps the existing enabled subset when the caller did not submit
		// an explicit selection. Catalog sync replaces it only in that legacy case.
		if len(existing.Models) > 0 && !opts.SyncCatalog && opts.EnabledModels == nil {
			desired.Models = restampCodeBuddyModels(existing.Models, baseURL, apiKey)
			if !codeBuddyHasModel(desired.Models, defaultModel) {
				if model, ok := catalog.Find(defaultModel); ok {
					desired.Models = append(desired.Models, codebuddy.ModelDefinition(model, baseURL, apiKey))
				}
			}
			if len(desired.Models) == 0 {
				desired.Models = []profiles.ModelDef{codeBuddyModelDefinition(desired.DefaultModel, baseURL, apiKey, catalog)}
			}
			if strings.TrimSpace(opts.DefaultModel) != "" && !codeBuddyHasModel(desired.Models, desired.DefaultModel) {
				desired.Models = append(desired.Models, codeBuddyModelDefinition(desired.DefaultModel, baseURL, apiKey, catalog))
			}
			desired.AvailableModels = codeBuddyModelNames(desired.Models)
			if codeBuddyHasModel(desired.Models, existing.DefaultModel) && strings.TrimSpace(opts.DefaultModel) == "" {
				desired.DefaultModel = canonicalCodeBuddyModelID(existing.DefaultModel, catalog)
			} else if !codeBuddyHasModel(desired.Models, desired.DefaultModel) {
				desired.DefaultModel = desired.Models[0].Name
				if codeBuddyHasModel(desired.Models, codebuddy.DefaultModel) {
					desired.DefaultModel = codebuddy.DefaultModel
				}
			}
		}
		if effort := strings.TrimSpace(existing.DefaultReasoningEffort); effort != "" {
			desired.DefaultReasoningEffort = effort
		}
		updated, updateErr := s.Profiles.Update(existing.ID, desired)
		if updateErr != nil {
			return profiles.Profile{}, updateErr
		}
		profile = updated
	} else {
		created, createErr := s.Profiles.Create(desired)
		if createErr != nil {
			return profiles.Profile{}, createErr
		}
		profile = created
		createdNew = true
	}
	rollbackProfile := func(cause error) error {
		var rollbackErr error
		if createdNew {
			rollbackErr = s.Profiles.Delete(profile.ID)
		} else if previous != nil {
			rollbackErr = s.Profiles.Restore(*previous)
		}
		if rollbackErr != nil {
			return fmt.Errorf("CodeBuddy 路由更新失败: %v；恢复原供应商失败: %w", cause, rollbackErr)
		}
		return cause
	}

	if !opts.Activate {
		if s.Routing != nil {
			if err := s.ApplyCurrentRouting(); err != nil {
				return profiles.Profile{}, rollbackProfile(err)
			}
		}
		s.changed()
		return profile, nil
	}

	if s.Routing == nil {
		// Legacy single-profile path.
		if _, err := s.Switcher.Activate(profile.ID); err != nil {
			return profiles.Profile{}, rollbackProfile(err)
		}
		s.changed()
		return profile, nil
	}

	// Activate via routing policy so the combined catalog stays consistent.
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	stored, err := s.Routing.Snapshot()
	if err != nil {
		return profiles.Profile{}, rollbackProfile(err)
	}
	profileList, err := s.Profiles.List()
	if err != nil {
		return profiles.Profile{}, rollbackProfile(err)
	}
	activeDefault := strings.TrimSpace(profile.DefaultModel)
	if activeDefault == "" {
		activeDefault = defaultModel
	}
	defaultRef := profile.ID + ":" + activeDefault
	stored.ActiveProviderID = profile.ID
	if stored.ProviderPolicies == nil {
		stored.ProviderPolicies = map[string]routing.RoutingPolicy{}
	}
	// Clean default-only policy for this provider; web_search is not supported.
	policy := routing.RoutingPolicy{
		Default:          defaultRef,
		WebSearchCapable: false,
	}
	if effort := strings.TrimSpace(profile.DefaultReasoningEffort); effort != "" {
		policy.DefaultReasoningEffort = effort
	}
	stored.ProviderPolicies[profile.ID] = policy
	if _, err := s.applyRoutingSnapshotTransaction(profileList, stored); err != nil {
		return profiles.Profile{}, rollbackProfile(err)
	}
	s.changed()
	return profile, nil
}

func isCodeBuddyProxyBaseURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	path := strings.TrimSuffix(u.Path, "/")
	return path == "/codebuddy-proxy/v1" || strings.HasSuffix(path, "/codebuddy-proxy/v1")
}

func isCodeBuddyLegacyLookalike(profile profiles.Profile) bool {
	if strings.TrimSpace(profile.Source) != "" {
		return false
	}
	if strings.TrimSpace(profile.Name) != codebuddy.ProfileName {
		return false
	}
	return isCodeBuddyProxyBaseURL(profile.BaseURL)
}

func isCodeBuddyManagedProfile(profile profiles.Profile) bool {
	return profile.Source == codebuddy.SourceTag || isCodeBuddyLegacyLookalike(profile)
}

func selectCodeBuddyProfile(list []profiles.Profile) (profiles.Profile, bool) {
	var tagged []profiles.Profile
	var legacy []profiles.Profile
	for _, profile := range list {
		switch {
		case profile.Source == codebuddy.SourceTag:
			tagged = append(tagged, profile)
		case isCodeBuddyLegacyLookalike(profile):
			legacy = append(legacy, profile)
		}
	}
	sortCodeBuddyProfilesByCreated(tagged)
	sortCodeBuddyProfilesByCreated(legacy)
	if len(tagged) > 0 {
		return tagged[0], true
	}
	if len(legacy) == 1 {
		return legacy[0], true
	}
	return profiles.Profile{}, false
}

func sortCodeBuddyProfilesByCreated(list []profiles.Profile) {
	sort.SliceStable(list, func(i, j int) bool {
		ti, tj := list[i].CreatedAt, list[j].CreatedAt
		if ti.IsZero() && tj.IsZero() {
			return list[i].ID < list[j].ID
		}
		if ti.IsZero() {
			return false
		}
		if tj.IsZero() {
			return true
		}
		if ti.Equal(tj) {
			return list[i].ID < list[j].ID
		}
		return ti.Before(tj)
	})
}

func restampCodeBuddyModels(models []profiles.ModelDef, baseURL, apiKey string) []profiles.ModelDef {
	out := make([]profiles.ModelDef, 0, len(models))
	seen := map[string]bool{}
	for _, model := range models {
		upstream := storedCodeBuddyModelID(model.Model)
		if upstream == "" {
			upstream = storedCodeBuddyModelID(model.Name)
		}
		if upstream == "" || seen[upstream] {
			continue
		}
		seen[upstream] = true
		next := model
		next.Name = upstream
		next.Model = upstream
		next.BaseURL = baseURL
		next.APIKey = apiKey
		if strings.TrimSpace(next.APIBackend) == "" {
			next.APIBackend = "chat_completions"
		}
		if next.StreamToolCalls == nil || *next.StreamToolCalls {
			next.StreamToolCalls = profiles.BoolPtr(false)
		}
		out = append(out, next)
	}
	return out
}

func storedCodeBuddyModelID(id string) string {
	id = strings.TrimSpace(id)
	if i := strings.LastIndex(id, "@"); i > 0 {
		id = strings.TrimSpace(id[:i])
	}
	if codebuddy.IsValidModelID(id) {
		return id
	}
	return ""
}

func codeBuddyModelDefinition(id, baseURL, apiKey string, catalog codebuddy.Catalog) profiles.ModelDef {
	id = canonicalCodeBuddyModelID(id, catalog)
	if model, ok := catalog.Find(id); ok {
		return codebuddy.ModelDefinition(model, baseURL, apiKey)
	}
	return profiles.ModelDef{}
}

func canonicalCodeBuddyModelID(id string, catalog codebuddy.Catalog) string {
	id = strings.TrimSpace(id)
	if _, ok := catalog.Find(id); ok {
		return id
	}
	if i := strings.LastIndex(id, "@"); i > 0 {
		bare := strings.TrimSpace(id[:i])
		if _, ok := catalog.Find(bare); ok {
			return bare
		}
	}
	return ""
}

func codeBuddyModelNames(models []profiles.ModelDef) []string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			name = strings.TrimSpace(model.Model)
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func selectedCodeBuddyModels(catalog codebuddy.Catalog, ids []string, baseURL, apiKey string) ([]profiles.ModelDef, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("请至少选择一个暴露给 Grok Build 的 CodeBuddy 模型")
	}
	out := make([]profiles.ModelDef, 0, len(ids))
	seen := map[string]bool{}
	for _, raw := range ids {
		id := canonicalCodeBuddyModelID(raw, catalog)
		if id == "" {
			return nil, fmt.Errorf("当前 CodeBuddy 模型目录不包含 %q；请先刷新模型目录", strings.TrimSpace(raw))
		}
		if seen[id] {
			continue
		}
		model, ok := catalog.Find(id)
		if !ok {
			return nil, fmt.Errorf("当前 CodeBuddy 模型目录不包含 %q；请先刷新模型目录", id)
		}
		seen[id] = true
		out = append(out, codebuddy.ModelDefinition(model, baseURL, apiKey))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("请至少选择一个暴露给 Grok Build 的 CodeBuddy 模型")
	}
	return out, nil
}

// codeBuddyModelOffers builds /v1/models ids that match routing/config aliases
// so Grok /m does not also show a bare duplicate like "deepseek-v4-flash".
func codeBuddyModelOffers(profile profiles.Profile, all []profiles.Profile) []codebuddy.ModelOffer {
	nameCounts := map[string]int{}
	for _, item := range all {
		for _, model := range profiles.Normalize(item).Models {
			local := strings.TrimSpace(model.Name)
			if local == "" {
				local = strings.TrimSpace(model.Model)
			}
			if local != "" {
				nameCounts[local]++
			}
		}
	}
	providerName := strings.TrimSpace(profile.Name)
	if providerName == "" {
		providerName = codebuddy.ProfileName
	}
	offers := make([]codebuddy.ModelOffer, 0, len(profile.Models))
	for _, model := range profile.Models {
		local := strings.TrimSpace(model.Name)
		if local == "" {
			local = strings.TrimSpace(model.Model)
		}
		upstream := strings.TrimSpace(model.Model)
		if upstream == "" {
			upstream = local
		}
		if local == "" || upstream == "" {
			continue
		}
		id := local
		if nameCounts[local] > 1 {
			id = local + "@" + providerName
		}
		offers = append(offers, codebuddy.ModelOffer{ID: id, Upstream: upstream})
	}
	return offers
}

func codeBuddyHasModel(models []profiles.ModelDef, name string) bool {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, "@"); i > 0 {
		name = strings.TrimSpace(name[:i])
	}
	if !codebuddy.IsValidModelID(name) {
		return false
	}
	for _, model := range models {
		for _, candidate := range []string{model.Name, model.Model} {
			candidate = strings.TrimSpace(candidate)
			if i := strings.LastIndex(candidate, "@"); i > 0 {
				candidate = strings.TrimSpace(candidate[:i])
			}
			if candidate == name {
				return true
			}
		}
	}
	return false
}

// LoadCodeBuddyAPIKey resolves a key from env, managed profile, or optional
// secret file written by the temporary ~/.grok bridge helper.
func LoadCodeBuddyAPIKey(profileStore *profiles.Store) string {
	if v := strings.TrimSpace(os.Getenv("CODEBUDDY_API_KEY")); v != "" {
		return v
	}
	if profileStore != nil {
		if list, err := profileStore.List(); err == nil {
			if profile, ok := selectCodeBuddyProfile(list); ok && strings.TrimSpace(profile.APIKey) != "" {
				return profile.APIKey
			}
			// Fall back to any tagged/legacy key even when selection is ambiguous.
			for _, p := range list {
				if isCodeBuddyManagedProfile(p) && strings.TrimSpace(p.APIKey) != "" {
					return p.APIKey
				}
			}
		}
	}
	// Optional local helper used during early wiring (not committed).
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, rel := range []string{
		filepath.Join(".grok", "codebuddy-bridge", ".env"),
		filepath.Join("Library", "Application Support", "Grok Build Switch", "codebuddy.env"),
	} {
		path := filepath.Join(home, rel)
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")
			if strings.HasPrefix(line, "CODEBUDDY_API_KEY=") {
				val := strings.Trim(strings.TrimPrefix(line, "CODEBUDDY_API_KEY="), `"'`)
				if val != "" {
					return val
				}
			}
		}
	}
	return ""
}
