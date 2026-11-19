package uiserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/agentcontext"
)

type themeAppearance struct {
	Theme      string `json:"theme"`
	LightTheme string `json:"light_theme"`
	DarkTheme  string `json:"dark_theme"`
	Themes     []struct {
		ID     string            `json:"id"`
		Name   string            `json:"name"`
		Base   string            `json:"base"`
		Tokens map[string]string `json:"tokens"`
	} `json:"themes"`
}

func (f *proposalFixture) appearance() themeAppearance {
	f.t.Helper()
	var a themeAppearance
	if err := json.Unmarshal([]byte(f.must("GET", "/api/settings/appearance", "")), &a); err != nil {
		f.t.Fatal(err)
	}
	return a
}

// proposeTheme proposes a theme through the tool and returns the proposal.
func (f *proposalFixture) proposeTheme(name, base string) agentcontext.Proposal {
	f.t.Helper()
	text, failed := (&readFixture{mcp: f.mcp}).callTool(f.t, f.token, "propose_theme", map[string]any{
		"name": name, "base": base, "tokens": map[string]string{"--color-paper": "#e8eef4"}})
	if failed {
		f.t.Fatalf("propose_theme: %s", text)
	}
	var answer struct {
		ID               string            `json:"id"`
		Page             string            `json:"page"`
		Target           string            `json:"target"`
		ContrastWarnings []json.RawMessage `json:"contrast_warnings"`
	}
	if err := json.Unmarshal([]byte(text), &answer); err != nil || answer.Page != agentcontext.ProposalAnywhere || answer.Target != agentcontext.TargetTheme || answer.ContrastWarnings == nil {
		f.t.Fatalf("answer %s: %v", text, err)
	}
	for _, p := range f.mcp.Proposals.List(f.id) {
		if p.ID == answer.ID {
			return p
		}
	}
	f.t.Fatal("proposal not listed")
	return agentcontext.Proposal{}
}

func TestThemeProposalSavesLikeThePostRoute(t *testing.T) {
	f := newProposalFixture(t)
	f.page(agentcontext.PageContext{Page: agentcontext.PageGroup, Group: f.group})
	p := f.proposeTheme("Midnight", "dark")
	if string(p.Before) != `{"light_theme":"Default light","dark_theme":"Default dark"}` {
		t.Errorf("before %s", p.Before)
	}
	if got := f.appearance(); len(got.Themes) != 2 {
		t.Fatalf("a proposal stored a theme: %+v", got)
	}
	// Saved from another page, with no input: the proposal's name and its
	// base's slot.
	f.page(agentcontext.PageContext{Page: agentcontext.PageInbox})
	if err := f.save(p, agentcontext.SaveInput{}); err != nil {
		t.Fatal(err)
	}
	got := f.appearance()
	if len(got.Themes) != 3 || got.Themes[2].Name != "Midnight" || got.Themes[2].Base != "dark" || got.Themes[2].Tokens["--color-paper"] != "#e8eef4" ||
		got.DarkTheme != got.Themes[2].ID || got.LightTheme != "default-light" {
		t.Fatalf("saved %+v", got)
	}
	// The same body through the route gives the same theme.
	other := newProposalFixture(t)
	other.must("POST", "/api/settings/appearance/themes", `{"name":"Midnight","base":"dark","tokens":{"--color-paper":"#e8eef4"},"slot":"dark"}`)
	want := other.appearance()
	if want.Themes[2].Name != got.Themes[2].Name || want.Themes[2].Base != got.Themes[2].Base || want.DarkTheme != want.Themes[2].ID ||
		!maps.Equal(want.Themes[2].Tokens, got.Themes[2].Tokens) {
		t.Errorf("the route saved %+v", want)
	}
}

func TestThemeProposalTakesTheNameAndSlotTheSaverChose(t *testing.T) {
	f := newProposalFixture(t)
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings})
	p := f.proposeTheme("Midnight", "dark")
	if err := f.save(p, agentcontext.SaveInput{Theme: &agentcontext.ThemeSave{Name: "  Dusk  ", Slot: "light"}}); err != nil {
		t.Fatal(err)
	}
	got := f.appearance()
	if got.Themes[2].Name != "Dusk" || got.Themes[2].Base != "dark" || got.LightTheme != got.Themes[2].ID || got.DarkTheme != "default-dark" {
		t.Fatalf("saved %+v", got)
	}
	// Only the name, then only the slot.
	q := f.proposeTheme("Third", "light")
	if err := f.save(q, agentcontext.SaveInput{Theme: &agentcontext.ThemeSave{Name: "Renamed"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.appearance(); got.Themes[3].Name != "Renamed" || got.LightTheme != got.Themes[3].ID {
		t.Fatalf("saved %+v", got)
	}
	r := f.proposeTheme("Fourth", "light")
	if err := f.save(r, agentcontext.SaveInput{Theme: &agentcontext.ThemeSave{Slot: "dark"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.appearance(); got.Themes[4].Name != "Fourth" || got.DarkTheme != got.Themes[4].ID {
		t.Fatalf("saved %+v", got)
	}
}

// The slots changing while the person previews the proposal do not make its
// save stale: the revision is constant.
func TestThemeProposalIsNotStaleAfterOtherChanges(t *testing.T) {
	f := newProposalFixture(t)
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings})
	p := f.proposeTheme("Midnight", "dark")
	f.must("POST", "/api/settings/appearance/themes", `{"name":"Other","base":"light","tokens":{},"slot":"light"}`)
	f.must("PUT", "/api/settings/appearance", `{"theme":"dark","dark_theme":"default-dark"}`)
	if err := f.save(p, agentcontext.SaveInput{}); err != nil {
		t.Fatalf("saved after other changes: %v", err)
	}
	if got := f.appearance(); len(got.Themes) != 4 {
		t.Fatalf("saved %+v", got)
	}
}

func TestThemeProposalRefusals(t *testing.T) {
	f := newProposalFixture(t)
	ctx := context.Background()
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "general"})
	p := f.proposeTheme("Midnight", "dark")
	themes := len(f.appearance().Themes)
	secret := "x"
	for name, input := range map[string]agentcontext.SaveInput{
		"a value":             {Value: &secret},
		"a bad slot":          {Theme: &agentcontext.ThemeSave{Slot: "system"}},
		"a bad name":          {Theme: &agentcontext.ThemeSave{Name: strings.Repeat("n", 61)}},
		"a control character": {Theme: &agentcontext.ThemeSave{Name: "a\x00b"}},
	} {
		if err := f.save(p, input); err == nil {
			t.Errorf("saved with %s", name)
		}
	}
	if len(f.appearance().Themes) != themes || len(f.mcp.Proposals.List(f.id)) != 1 {
		t.Error("a refused save changed something")
	}
	// Another target refuses a theme name or slot.
	a, err := f.propose("propose_appearance", map[string]any{"key_hints": false})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.save(a, agentcontext.SaveInput{Theme: &agentcontext.ThemeSave{Name: "x"}}); err == nil {
		t.Error("an appearance proposal took a theme name")
	}
	// The exception is the theme's alone, in both directions.
	for _, proposal := range []agentcontext.Proposal{
		{Page: agentcontext.ProposalAnywhere, Target: agentcontext.TargetAppearance, After: json.RawMessage(`{"key_hints":false}`)},
		{Page: agentcontext.ProposalAnywhere, Target: agentcontext.TargetReviews, After: json.RawMessage(`{"enabled":false}`)},
		{Page: agentcontext.PageSettings, Target: agentcontext.TargetTheme, After: json.RawMessage(`{"name":"N","base":"dark","tokens":{}}`)},
	} {
		if _, err := f.mcp.Proposals.Propose(ctx, f.id, proposal); !errors.Is(err, agentcontext.ErrNoProposer) {
			t.Errorf("%s under %s: %v", proposal.Target, proposal.Page, err)
		}
	}
	// A theme the validator refuses is not recorded.
	for _, after := range []string{`{"name":"N","base":"dark","tokens":{"--nope":"#fff"}}`, `{"name":"N","base":"dark","tokens":{},"slot":"dark"}`, `{"name":"","base":"dark"}`} {
		if _, err := f.mcp.Proposals.Propose(ctx, f.id, agentcontext.Proposal{Page: agentcontext.ProposalAnywhere, Target: agentcontext.TargetTheme, After: json.RawMessage(after)}); err == nil {
			t.Errorf("proposed %s", after)
		}
	}
}

func TestAppearanceProposalSetsThemeSlots(t *testing.T) {
	f := newProposalFixture(t)
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "appearance"})
	f.must("POST", "/api/settings/appearance/themes", `{"name":"Mine","base":"dark","tokens":{}}`)
	id := f.appearance().Themes[2].ID
	p, err := f.propose("propose_theme_choice", map[string]any{"dark_theme": id})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.save(p, agentcontext.SaveInput{}); err != nil || f.appearance().DarkTheme != id {
		t.Fatalf("%v %+v", err, f.appearance())
	}
	// An id the preferences do not hold is refused when proposed.
	if _, err := f.propose("propose_theme_choice", map[string]any{"light_theme": "c-nope"}); err == nil {
		t.Error("proposed an unknown theme for a slot")
	}
}

// The appearance preferences are proposed in two halves, as Settings shows
// them in two sections: General's keys, Appearance's theme mode and slots.
func TestThemeChoiceAndAppearanceProposalsAreSeparateSections(t *testing.T) {
	f := newProposalFixture(t)
	ctx := context.Background()
	// The theme choice is Appearance's, and the appearance proposal General's.
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "general"})
	_, err := f.propose("propose_theme_choice", map[string]any{"theme": "dark"})
	if err == nil || !strings.Contains(err.Error(), "Appearance") || !strings.Contains(err.Error(), "General") {
		t.Errorf("a theme choice on General: %v", err)
	}
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings})
	if _, err := f.propose("propose_theme_choice", map[string]any{"theme": "dark"}); err == nil {
		t.Error("a theme choice with no section open, which shows General")
	}
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "appearance"})
	if _, err := f.propose("propose_appearance", map[string]any{"key_hints": false}); err == nil {
		t.Error("an appearance proposal on Appearance")
	}
	p, err := f.propose("propose_theme_choice", map[string]any{"theme": "dark"})
	if err != nil || p.Target != agentcontext.TargetThemeChoice {
		t.Fatalf("%v %+v", err, p)
	}
	if err := f.save(p, agentcontext.SaveInput{}); err != nil || f.appearance().Theme != "dark" {
		t.Fatalf("%v %+v", err, f.appearance())
	}
	// Each target refuses the other's fields, as a proposal made without the tool's schema.
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "appearance"})
	if _, err := f.mcp.Proposals.Propose(ctx, f.id, agentcontext.Proposal{Page: agentcontext.PageSettings, Target: agentcontext.TargetThemeChoice, After: json.RawMessage(`{"key_hints":false}`)}); err == nil {
		t.Error("a theme choice took key_hints")
	}
	f.page(agentcontext.PageContext{Page: agentcontext.PageSettings, Section: "general"})
	for _, after := range []string{`{"theme":"dark"}`, `{"light_theme":""}`, `{"dark_theme":""}`} {
		if _, err := f.mcp.Proposals.Propose(ctx, f.id, agentcontext.Proposal{Page: agentcontext.PageSettings, Target: agentcontext.TargetAppearance, After: json.RawMessage(after)}); err == nil {
			t.Errorf("General's appearance proposal took %s", after)
		}
	}
}
