package uiserver

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/baum-ai/baum/internal/repos"
)

// labsSettings is the body of GET and PUT /api/settings/labs: the
// experimental features in force, each off unless turned on.
type labsSettings struct {
	TileReviews bool `json:"tile_reviews"`
}

func labsOf(p repos.Preferences) labsSettings {
	return labsSettings{TileReviews: p.Labs.TileReviewsOn()}
}

// labsInput is the body of PUT /api/settings/labs: each field set is a
// change.
type labsInput struct {
	TileReviews *bool `json:"tile_reviews,omitempty"`
}

func (in labsInput) edit(p repos.Preferences) repos.Preferences {
	if in.TileReviews != nil {
		on := *in.TileReviews
		p.Labs.TileReviews = &on
	}
	return p
}

func (a LocalAPI) getLabs(w http.ResponseWriter, r *http.Request) {
	prefs, err := a.Registry.Preferences(r.Context())
	if err != nil {
		http.Error(w, "Could not read the labs settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, labsOf(prefs))
}

// putLabs saves a change of the labs settings; it applies to jobs that start
// after it.
func (a LocalAPI) putLabs(w http.ResponseWriter, r *http.Request) {
	var input labsInput
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return
	}
	prefs, err := a.updatePreferences(r.Context(), nil, input.edit)
	if err != nil {
		http.Error(w, "Could not save the labs settings", http.StatusInternalServerError)
		return
	}
	writeJSON(w, labsOf(prefs))
}
