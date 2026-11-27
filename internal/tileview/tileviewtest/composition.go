package tileviewtest

import (
	"encoding/json"
	"testing"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/view"
)

// RiskSummary is the overview's risk summary ViewComposition writes, which
// shows that a served view applied it.
const RiskSummary = "The fixture's risk summary."

// ViewComposition returns a review view composition (baum.composition/2, as
// JSON) for any review: only the overview's risk summary (RiskSummary), so
// a view built with it differs from one built without.
func ViewComposition(t testing.TB) []byte {
	t.Helper()
	c := view.Composition{Schema: view.CompositionName, Generator: &tiles.Generator{PromptVersion: "fixture"},
		Overview: view.Overview{RiskSummary: RiskSummary, Screenshots: []string{}, Intent: []view.Intent{}}, Sections: []view.Placement{}}
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
