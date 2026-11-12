package uiserver

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode"

	"github.com/baum-ai/baum/internal/repos"
)

// maxPanelModel bounds the panel's model name.
const maxPanelModel = 200

// panelInput is the body of PUT /api/settings/panel: each field present is a
// change. An empty agent goes back to following the fix agent; any change of
// agent drops a chosen model unless the same request sets one.
type panelInput struct {
	Agent    *string `json:"agent,omitempty"`
	Model    *string `json:"model,omitempty"`
	Mode     *string `json:"mode,omitempty"`
	Position *string `json:"position,omitempty"`
}

// check refuses what the route refuses (400).
func (in panelInput) check() error {
	if in.Agent != nil && *in.Agent != "" && *in.Agent != repos.PanelAgentClaudeCode && *in.Agent != repos.PanelAgentCodex {
		return errors.New("agent must be empty, claude-code or codex")
	}
	if in.Model != nil && (len(strings.TrimSpace(*in.Model)) > maxPanelModel || strings.ContainsFunc(*in.Model, unicode.IsControl)) {
		return errors.New("model must be one line of at most 200 characters")
	}
	if in.Mode != nil && *in.Mode != repos.PanelModeFull && *in.Mode != repos.PanelModeWorkspace {
		return errors.New("mode must be full or workspace")
	}
	if in.Position != nil && *in.Position != repos.PanelBottom && *in.Position != repos.PanelTop {
		return errors.New("position must be bottom or top")
	}
	return nil
}

// edit is the route's change of the saved preferences.
func (in panelInput) edit(p repos.Preferences) repos.Preferences {
	// A model names one agent's model: another agent, or following the fix
	// agent again, drops a chosen model unless the same change sets one.
	if in.Agent != nil {
		if *in.Agent != p.Panel.Agent || *in.Agent == "" {
			p.Panel.Model = nil
		}
		p.Panel.Agent = *in.Agent
	}
	if in.Model != nil {
		model := strings.TrimSpace(*in.Model)
		p.Panel.Model = &model
	}
	if in.Mode != nil {
		p.Panel.Mode = *in.Mode
	}
	if in.Position != nil {
		p.Panel.Position = *in.Position
	}
	return p
}

// getPanel answers the session panel's defaults in force (repos.ResolvePanel).
func (a LocalAPI) getPanel(w http.ResponseWriter, r *http.Request) {
	prefs, err := a.Registry.Preferences(r.Context())
	if err != nil {
		http.Error(w, "Could not read the panel settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, prefs.ResolvePanel(prefs.Resolve(a.Config)))
}

func (a LocalAPI) putPanel(w http.ResponseWriter, r *http.Request) {
	var input panelInput
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	if err := input.check(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	prefs, err := a.updatePreferences(r.Context(), nil, input.edit)
	if err != nil {
		http.Error(w, "Could not save the panel settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, prefs.ResolvePanel(prefs.Resolve(a.Config)))
}
