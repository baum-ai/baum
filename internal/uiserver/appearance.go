package uiserver

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/baum-ai/baum/internal/repos"
)

type appearanceSettings struct {
	Theme    string `json:"theme"`
	KeyHints bool   `json:"key_hints"`
}

func appearanceOf(p repos.Preferences) appearanceSettings {
	theme := p.Appearance.Theme
	if theme == "" {
		theme = "system"
	}
	return appearanceSettings{Theme: theme, KeyHints: p.Appearance.KeyHints == nil || *p.Appearance.KeyHints}
}

func (a LocalAPI) getAppearance(w http.ResponseWriter, r *http.Request) {
	prefs, err := a.Registry.Preferences(r.Context())
	if err != nil {
		http.Error(w, "Could not read appearance settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, appearanceOf(prefs))
}

func (a LocalAPI) putAppearance(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Theme    *string `json:"theme"`
		KeyHints *bool   `json:"key_hints"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	if input.Theme != nil && *input.Theme != "system" && *input.Theme != "light" && *input.Theme != "dark" {
		http.Error(w, "theme must be system, light or dark", http.StatusBadRequest)
		return
	}
	prefs, err := a.Registry.UpdatePreferences(r.Context(), func(p repos.Preferences) repos.Preferences {
		if input.Theme != nil {
			p.Appearance.Theme = *input.Theme
		}
		if input.KeyHints != nil {
			p.Appearance.KeyHints = input.KeyHints
		}
		return p
	})
	if err != nil {
		http.Error(w, "Could not save appearance settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, appearanceOf(prefs))
}
