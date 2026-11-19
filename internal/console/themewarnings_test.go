package console

import (
	"encoding/json"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
)

// A theme proposal lists with its contrast warnings, so the app can show what
// propose_theme told the agent; the stored proposal and other proposals are
// left as they are.
func TestWithContrastWarnings(t *testing.T) {
	glare := json.RawMessage(`{"name":"Glare","base":"light","tokens":{"--surface-page":"#222222","--text-primary":"#333333"}}`)
	plain := json.RawMessage(`{"name":"Plain","base":"light","tokens":{}}`)
	other := json.RawMessage(`{"review_page":"tiles"}`)
	list := []agentcontext.Proposal{
		{ID: "a", Page: agentcontext.ProposalAnywhere, Target: agentcontext.TargetTheme, After: glare},
		{ID: "b", Page: agentcontext.ProposalAnywhere, Target: agentcontext.TargetTheme, After: plain},
		{ID: "c", Page: agentcontext.PageSettings, Target: "appearance", After: other},
	}
	got := withContrastWarnings(list)
	warnings := func(p agentcontext.Proposal) []map[string]any {
		var after struct {
			Warnings []map[string]any `json:"contrast_warnings"`
		}
		if err := json.Unmarshal(p.After, &after); err != nil {
			t.Fatal(err)
		}
		return after.Warnings
	}
	if len(warnings(got[0])) == 0 {
		t.Errorf("no warning for hard-to-read colours: %s", got[0].After)
	}
	if w := warnings(got[1]); w == nil || len(w) != 0 {
		t.Errorf("a readable theme carries %v, want an empty list", w)
	}
	if string(got[2].After) != string(other) {
		t.Errorf("another proposal changed: %s", got[2].After)
	}
	if string(list[0].After) != string(glare) {
		t.Errorf("the recorded proposal changed: %s", list[0].After)
	}
}
