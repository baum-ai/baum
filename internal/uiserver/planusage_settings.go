package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/repos"
)

// planProviders are the plans plan usage can show, in display order, with
// the backend whose plan each is.
var planProviders = []struct {
	Provider string
	Name     string
	Backend  config.Backend
}{
	{"claude", "Claude Code", config.BackendClaude},
	{"codex", "Codex", config.BackendCodex},
}

type planEntry struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
	// Shown is whether the plan appears in the sidebar and tray.
	Shown bool `json:"shown"`
	// Default is what baum's own backends would show without a choice.
	Default bool `json:"default"`
	// Explicit is whether the user chose Shown.
	Explicit bool `json:"explicit"`
}

// plansFromHandler reads the plans the daemon would serve, unfiltered and
// from its cache. A missing or failing provider reads as no plans.
func (a LocalAPI) plansFromHandler(r *http.Request) []json.RawMessage {
	if a.PlanUsage == nil {
		return nil
	}
	req := r.Clone(r.Context())
	req.Method, req.URL.RawQuery = http.MethodGet, ""
	rec := httptest.NewRecorder()
	a.PlanUsage.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return nil
	}
	var body struct {
		Plans []json.RawMessage `json:"plans"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		return nil
	}
	return body.Plans
}

func planProvider(raw json.RawMessage) (provider string, available bool) {
	var plan struct {
		Provider  string `json:"provider"`
		Available bool   `json:"available"`
	}
	_ = json.Unmarshal(raw, &plan)
	return plan.Provider, plan.Available
}

// defaultPlans are the providers whose plans baum's backends use: the
// reviews', the fixes' and the decision capture's. Auto uses the first plan
// it can read, as it picks Claude Code before Codex. Backends without a plan
// (the Anthropic API, Cursor) add none.
func defaultPlans(prefs repos.Preferences, cfg config.Config, plans []json.RawMessage) map[string]bool {
	review := prefs.Resolve(cfg)
	decisions := review.Backend
	if prefs.Decisions.Backend != nil {
		decisions = *prefs.Decisions.Backend
	}
	backends := []config.Backend{review.Backend, prefs.ResolveFix(review).Backend, decisions}
	readable := map[string]bool{}
	for _, raw := range plans {
		if provider, ok := planProvider(raw); ok {
			readable[provider] = true
		}
	}
	use := map[string]bool{}
	for _, backend := range backends {
		for _, p := range planProviders {
			if backend == p.Backend {
				use[p.Provider] = true
			}
		}
		if backend == config.BackendAuto || backend == "" {
			for _, p := range planProviders {
				if readable[p.Provider] {
					use[p.Provider] = true
					break
				}
			}
		}
	}
	return use
}

func planEntries(prefs repos.Preferences, cfg config.Config, plans []json.RawMessage) []planEntry {
	use := defaultPlans(prefs, cfg, plans)
	out := make([]planEntry, 0, len(planProviders))
	for _, p := range planProviders {
		choice, explicit := prefs.PlanUsage.Show[p.Provider]
		shown := use[p.Provider]
		if explicit {
			shown = choice
		}
		out = append(out, planEntry{p.Provider, p.Name, shown, use[p.Provider], explicit})
	}
	return out
}

// planUsageHandler serves /api/plan-usage with only the plans the person has
// chosen, or by default those baum uses.
func (a LocalAPI) planUsageHandler(w http.ResponseWriter, r *http.Request) {
	rec := httptest.NewRecorder()
	a.PlanUsage.ServeHTTP(rec, r)
	var body struct {
		Plans []json.RawMessage `json:"plans"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return
	}
	kept, err := a.shownPlans(r.Context(), body.Plans)
	if err != nil {
		http.Error(w, "Could not read plan usage settings", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"plans": kept})
}

// shownPlans keeps the plans the person chose to show, or by default those
// baum uses.
func (a LocalAPI) shownPlans(ctx context.Context, plans []json.RawMessage) ([]json.RawMessage, error) {
	prefs, err := a.Registry.Preferences(ctx)
	if err != nil {
		return nil, err
	}
	shown := map[string]bool{}
	for _, e := range planEntries(prefs, a.Config, plans) {
		shown[e.Provider] = e.Shown
	}
	kept := []json.RawMessage{}
	for _, raw := range plans {
		if provider, _ := planProvider(raw); shown[provider] {
			kept = append(kept, raw)
		}
	}
	return kept, nil
}

// CachedPlans are the plans the app's sidebar shows, from the plan usage
// service's cache alone (PlanUsageCached): it never reads a provider, so it
// causes no fetch. None cached, or no such cache, reads as no plans.
func (a LocalAPI) CachedPlans(ctx context.Context) ([]json.RawMessage, error) {
	if a.PlanUsageCached == nil {
		return nil, nil
	}
	return a.shownPlans(ctx, a.PlanUsageCached())
}

// CachedPlanChoices are the entries GET /api/settings/plan-usage answers
// (provider, name, shown, default, explicit), with the default worked out from
// the plan usage cache alone (PlanUsageCached): the route asks PlanUsage, which
// reads Claude or Codex when its cache is stale, so the MCP server reads this
// instead and never causes a fetch.
func (a LocalAPI) CachedPlanChoices(ctx context.Context) ([]json.RawMessage, error) {
	prefs, err := a.Registry.Preferences(ctx)
	if err != nil {
		return nil, err
	}
	var plans []json.RawMessage
	if a.PlanUsageCached != nil {
		plans = a.PlanUsageCached()
	}
	out := []json.RawMessage{}
	for _, e := range planEntries(prefs, a.Config, plans) {
		raw, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}

func (a LocalAPI) getPlanUsageSettings(w http.ResponseWriter, r *http.Request) {
	prefs, err := a.Registry.Preferences(r.Context())
	if err != nil {
		http.Error(w, "Could not read plan usage settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"plans": planEntries(prefs, a.Config, a.plansFromHandler(r))})
}

// checkPlanProvider refuses a provider the plan usage choice does not list,
// as PUT /api/settings/plan-usage does (400).
func checkPlanProvider(provider string) error {
	for _, p := range planProviders {
		if p.Provider == provider {
			return nil
		}
	}
	return errors.New("provider must be claude or codex")
}

// setPlanShown is PUT /api/settings/plan-usage's change: provider's plan
// shown or hidden, or with nil following baum's backends again.
func setPlanShown(p repos.Preferences, provider string, shown *bool) repos.Preferences {
	show := map[string]bool{}
	for k, v := range p.PlanUsage.Show {
		show[k] = v
	}
	if shown == nil {
		delete(show, provider)
	} else {
		show[provider] = *shown
	}
	p.PlanUsage.Show = nil
	if len(show) > 0 {
		p.PlanUsage.Show = show
	}
	return p
}

// putPlanUsageSettings sets one plan's choice: shown true or false, or null to
// follow baum's backends again. Other plans keep theirs.
func (a LocalAPI) putPlanUsageSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Provider string `json:"provider"`
		Shown    *bool  `json:"shown"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	if err := checkPlanProvider(input.Provider); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	prefs, err := a.updatePreferences(r.Context(), nil, func(p repos.Preferences) repos.Preferences {
		return setPlanShown(p, input.Provider, input.Shown)
	})
	if err != nil {
		http.Error(w, "Could not save plan usage settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"plans": planEntries(prefs, a.Config, a.plansFromHandler(r))})
}
