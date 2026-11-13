package uiserver

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/baum-ai/baum/internal/repos"
)

type appearanceSettings struct {
	Theme        string `json:"theme"`
	KeyHints     bool   `json:"key_hints"`
	PageModifier string `json:"page_modifier"`
	Sidebar      string `json:"sidebar"`
}

func appearanceOf(p repos.Preferences) appearanceSettings {
	theme := p.Appearance.Theme
	if theme == "" {
		theme = "system"
	}
	sidebar := p.Appearance.Sidebar
	if sidebar != "folded" {
		sidebar = "pinned"
	}
	return appearanceSettings{
		Theme:        theme,
		KeyHints:     p.Appearance.KeyHints == nil || *p.Appearance.KeyHints,
		PageModifier: p.Appearance.PageModifier,
		Sidebar:      sidebar,
	}
}

func (a LocalAPI) getAppearance(w http.ResponseWriter, r *http.Request) {
	prefs, err := a.Registry.Preferences(r.Context())
	if err != nil {
		http.Error(w, "Could not read appearance settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, appearanceOf(prefs))
}

// appearanceInput is the body of PUT /api/settings/appearance: each field set
// is a change.
type appearanceInput struct {
	Theme        *string `json:"theme,omitempty"`
	KeyHints     *bool   `json:"key_hints,omitempty"`
	PageModifier *string `json:"page_modifier,omitempty"`
	Sidebar      *string `json:"sidebar,omitempty"`
}

// check refuses what the route refuses (400).
func (in appearanceInput) check() error {
	if in.Theme != nil && *in.Theme != "system" && *in.Theme != "light" && *in.Theme != "dark" {
		return errors.New("theme must be system, light or dark")
	}
	if in.PageModifier != nil && *in.PageModifier != "" && *in.PageModifier != "meta" &&
		*in.PageModifier != "ctrl" && *in.PageModifier != "alt" {
		return errors.New("page_modifier must be empty, meta, ctrl or alt")
	}
	if in.Sidebar != nil && *in.Sidebar != "pinned" && *in.Sidebar != "folded" {
		return errors.New("sidebar must be pinned or folded")
	}
	return nil
}

// edit is the route's change of the saved preferences.
func (in appearanceInput) edit(p repos.Preferences) repos.Preferences {
	if in.Theme != nil {
		p.Appearance.Theme = *in.Theme
	}
	if in.KeyHints != nil {
		p.Appearance.KeyHints = in.KeyHints
	}
	if in.PageModifier != nil {
		p.Appearance.PageModifier = *in.PageModifier
	}
	if in.Sidebar != nil {
		p.Appearance.Sidebar = *in.Sidebar
	}
	return p
}

func (a LocalAPI) putAppearance(w http.ResponseWriter, r *http.Request) {
	var input appearanceInput
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
		http.Error(w, "Could not save appearance settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, appearanceOf(prefs))
}
