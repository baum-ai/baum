package uiserver

import (
	"encoding/json"
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
	prefs, err := a.Registry.Preferences(r.Context())
	if err != nil {
		http.Error(w, "Could not read plan usage settings", http.StatusInternalServerError)
		return
	}
	shown := map[string]bool{}
	for _, e := range planEntries(prefs, a.Config, body.Plans) {
		shown[e.Provider] = e.Shown
	}
	kept := []json.RawMessage{}
	for _, raw := range body.Plans {
		if provider, _ := planProvider(raw); shown[provider] {
			kept = append(kept, raw)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"plans": kept})
}

func (a LocalAPI) getPlanUsageSettings(w http.ResponseWriter, r *http.Request) {
	prefs, err := a.Registry.Preferences(r.Context())
	if err != nil {
		http.Error(w, "Could not read plan usage settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"plans": planEntries(prefs, a.Config, a.plansFromHandler(r))})
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
	known := false
	for _, p := range planProviders {
		known = known || p.Provider == input.Provider
	}
	if !known {
		http.Error(w, "provider must be claude or codex", http.StatusBadRequest)
		return
	}
	prefs, err := a.Registry.UpdatePreferences(r.Context(), func(p repos.Preferences) repos.Preferences {
		show := map[string]bool{}
		for k, v := range p.PlanUsage.Show {
			show[k] = v
		}
		if input.Shown == nil {
			delete(show, input.Provider)
		} else {
			show[input.Provider] = *input.Shown
		}
		p.PlanUsage.Show = nil
		if len(show) > 0 {
			p.PlanUsage.Show = show
		}
		return p
	})
	if err != nil {
		http.Error(w, "Could not save plan usage settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"plans": planEntries(prefs, a.Config, a.plansFromHandler(r))})
}
