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
	return f.proposeThemeWith(map[string]any{"name": name, "base": base, "tokens": map[string]string{"--color-paper": "#e8eef4"}})
}

// proposeThemeWith proposes a theme with these arguments, which must be
// accepted.
func (f *proposalFixture) proposeThemeWith(args map[string]any) agentcontext.Proposal {
	f.t.Helper()
	text, failed := (&readFixture{mcp: f.mcp}).callTool(f.t, f.token, "propose_theme", args)
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
	if got := f.appearance(); len(got.Themes) != 6 {
		t.Fatalf("a proposal stored a theme: %+v", got)
	}
	// Saved from another page, with no input: the proposal's name and its
	// base's slot.
	f.page(agentcontext.PageContext{Page: agentcontext.PageInbox})
	if err := f.save(p, agentcontext.SaveInput{}); err != nil {
		t.Fatal(err)
	}
	got := f.appearance()
	if len(got.Themes) != 7 || got.Themes[6].Name != "Midnight" || got.Themes[6].Base != "dark" || got.Themes[6].Tokens["--color-paper"] != "#e8eef4" ||
		got.DarkTheme != got.Themes[6].ID || got.LightTheme != "default-light" {
		t.Fatalf("saved %+v", got)
	}
	// The same body through the route gives the same theme.
	other := newProposalFixture(t)
	other.must("POST", "/api/settings/appearance/themes", `{"name":"Midnight","base":"dark","tokens":{"--color-paper":"#e8eef4"},"slot":"dark"}`)
	want := other.appearance()
	if want.Themes[6].Name != got.Themes[6].Name || want.Themes[6].Base != got.Themes[6].Base || want.DarkTheme != want.Themes[6].ID ||
		!maps.Equal(want.Themes[6].Tokens, got.Themes[6].Tokens) {
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
	if got.Themes[6].Name != "Dusk" || got.Themes[6].Base != "dark" || got.LightTheme != got.Themes[6].ID || got.DarkTheme != "default-dark" {
		t.Fatalf("saved %+v", got)
	}
	// Only the name, then only the slot.
	q := f.proposeTheme("Third", "light")
	if err := f.save(q, agentcontext.SaveInput{Theme: &agentcontext.ThemeSave{Name: "Renamed"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.appearance(); got.Themes[7].Name != "Renamed" || got.LightTheme != got.Themes[7].ID {
		t.Fatalf("saved %+v", got)
	}
	r := f.proposeTheme("Fourth", "light")
	if err := f.save(r, agentcontext.SaveInput{Theme: &agentcontext.ThemeSave{Slot: "dark"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.appearance(); got.Themes[8].Name != "Fourth" || got.DarkTheme != got.Themes[8].ID {
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
	if got := f.appearance(); len(got.Themes) != 8 {
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
	id := f.appearance().Themes[6].ID
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

// addCustom adds a custom theme through the route and returns its id.
func (f *proposalFixture) addCustom(name, base, slot string) string {
	f.t.Helper()
	f.must("POST", "/api/settings/appearance/themes", `{"name":"`+name+`","base":"`+base+`","tokens":{"--color-paper":"#111111"},"slot":"`+slot+`"}`)
	themes := f.appearance().Themes
	return themes[len(themes)-1].ID
}

func (f *proposalFixture) customTheme(id string) (name, base, paper string, ok bool) {
	for _, t := range f.appearance().Themes {
		if t.ID == id {
			return t.Name, t.Base, t.Tokens["--color-paper"], true
		}
	}
	return "", "", "", false
}

// A theme proposal that names a custom theme updates it on save: its id and
// slots stay, its base, tokens and (when given) name change.
func TestThemeProposalWithThemeIDUpdatesInPlace(t *testing.T) {
	f := newProposalFixture(t)
	f.page(agentcontext.PageContext{Page: agentcontext.PageInbox})
	id := f.addCustom("Mine", "dark", "dark")
	f.addCustom("Other", "light", "light")
	count := len(f.appearance().Themes)
	p := f.proposeThemeWith(map[string]any{"theme_id": id, "name": "Mine", "base": "light", "tokens": map[string]string{"--color-paper": "#e8eef4"}})
	if !strings.Contains(string(p.After), `"theme_id":"`+id+`"`) {
		t.Fatalf("the proposal lost its theme_id: %s", p.After)
	}
	// Saved with no input, the proposal is an update.
	if err := f.save(p, agentcontext.SaveInput{}); err != nil {
		t.Fatal(err)
	}
	got := f.appearance()
	name, base, paper, ok := f.customTheme(id)
	if !ok || name != "Mine" || base != "light" || paper != "#e8eef4" || len(got.Themes) != count || got.DarkTheme != id {
		t.Fatalf("updated %+v", got)
	}
	// Tweaked again, with a new name and the update chosen explicitly: the
	// count is the same, the slot too, and the slot input is ignored.
	q := f.proposeThemeWith(map[string]any{"theme_id": id, "name": "Mine", "base": "dark", "tokens": map[string]string{"--color-paper": "#222222"}})
	if err := f.save(q, agentcontext.SaveInput{Theme: &agentcontext.ThemeSave{Name: " Renamed ", Slot: "light", Mode: agentcontext.ThemeModeUpdate}}); err != nil {
		t.Fatal(err)
	}
	got = f.appearance()
	name, base, paper, _ = f.customTheme(id)
	if name != "Renamed" || base != "dark" || paper != "#222222" || len(got.Themes) != count || got.DarkTheme != id || got.LightTheme == id {
		t.Fatalf("updated %+v", got)
	}
}

func TestThemeProposalSaveAsNewKeepsTheOriginal(t *testing.T) {
	f := newProposalFixture(t)
	f.page(agentcontext.PageContext{Page: agentcontext.PageInbox})
	id := f.addCustom("Mine", "dark", "dark")
	count := len(f.appearance().Themes)
	p := f.proposeThemeWith(map[string]any{"theme_id": id, "name": "Mine", "base": "dark", "tokens": map[string]string{"--color-paper": "#e8eef4"}})
	if err := f.save(p, agentcontext.SaveInput{Theme: &agentcontext.ThemeSave{Name: "Copy", Slot: "light", Mode: agentcontext.ThemeModeNew}}); err != nil {
		t.Fatal(err)
	}
	got := f.appearance()
	_, _, paper, _ := f.customTheme(id)
	last := got.Themes[len(got.Themes)-1]
	if len(got.Themes) != count+1 || paper != "#111111" || last.ID == id || last.Name != "Copy" || got.LightTheme != last.ID || got.DarkTheme != id {
		t.Fatalf("saved %+v", got)
	}
	// A proposal without a theme_id cannot be an update.
	q := f.proposeTheme("Plain", "dark")
	if err := f.save(q, agentcontext.SaveInput{Theme: &agentcontext.ThemeSave{Mode: agentcontext.ThemeModeUpdate}}); err == nil {
		t.Error("updated with no theme to update")
	}
	if err := f.save(q, agentcontext.SaveInput{Theme: &agentcontext.ThemeSave{Mode: "replace"}}); err == nil {
		t.Error("saved with an unknown mode")
	}
}

func TestThemeProposalRefusesBuiltinAndUnknownThemeID(t *testing.T) {
	f := newProposalFixture(t)
	f.page(agentcontext.PageContext{Page: agentcontext.PageInbox})
	for _, id := range []string{"default-dark", "c-nope"} {
		text, failed := (&readFixture{mcp: f.mcp}).callTool(f.t, f.token, "propose_theme", map[string]any{
			"theme_id": id, "name": "N", "base": "dark", "tokens": map[string]string{}})
		if !failed || !strings.Contains(text, "omit theme_id") {
			t.Errorf("%s: %v %s", id, failed, text)
		}
	}
	if len(f.mcp.Proposals.List(f.id)) != 0 {
		t.Error("a refused theme_id was recorded")
	}
}

func TestThemeProposalUpdateOfADeletedThemeErrors(t *testing.T) {
	f := newProposalFixture(t)
	f.page(agentcontext.PageContext{Page: agentcontext.PageInbox})
	id := f.addCustom("Mine", "dark", "dark")
	p := f.proposeThemeWith(map[string]any{"theme_id": id, "name": "Mine", "base": "dark", "tokens": map[string]string{"--color-paper": "#e8eef4"}})
	f.must("DELETE", "/api/settings/appearance/themes/"+id, "")
	count := len(f.appearance().Themes)
	err := f.save(p, agentcontext.SaveInput{})
	if err == nil || !strings.Contains(err.Error(), "save it as a new theme") {
		t.Fatalf("update of a deleted theme: %v", err)
	}
	if len(f.appearance().Themes) != count || len(f.mcp.Proposals.List(f.id)) != 1 {
		t.Error("the failed update changed something")
	}
	// Save as new still works and closes the proposal.
	if err := f.save(p, agentcontext.SaveInput{Theme: &agentcontext.ThemeSave{Mode: agentcontext.ThemeModeNew}}); err != nil {
		t.Fatal(err)
	}
	if len(f.appearance().Themes) != count+1 {
		t.Error("save as new did not add the theme")
	}
}

// PATCH takes base and tokens, with the validation of a new theme.
func TestPatchThemeReplacesBaseAndTokens(t *testing.T) {
	f := newProposalFixture(t)
	id := f.addCustom("Mine", "dark", "dark")
	f.must("PATCH", "/api/settings/appearance/themes/"+id, `{"base":"light","tokens":{"--color-paper":"#e8eef4"}}`)
	name, base, paper, _ := f.customTheme(id)
	if name != "Mine" || base != "light" || paper != "#e8eef4" || f.appearance().DarkTheme != id {
		t.Errorf("patched %s %s %s", name, base, paper)
	}
	f.must("PATCH", "/api/settings/appearance/themes/"+id, `{"name":"New","base":"dark","tokens":{}}`)
	if name, base, _, _ := f.customTheme(id); name != "New" || base != "dark" {
		t.Errorf("patched %s %s", name, base)
	}
	for _, body := range []string{`{}`, `{"base":"dark"}`, `{"tokens":{}}`, `{"base":"blue","tokens":{}}`, `{"base":"dark","tokens":{"--nope":"#fff"}}`, `{"name":""}`} {
		if code, _ := f.do("PATCH", "/api/settings/appearance/themes/"+id, body); code != 400 {
			t.Errorf("%s: %d", body, code)
		}
	}
	if code, _ := f.do("PATCH", "/api/settings/appearance/themes/default-dark", `{"base":"dark","tokens":{}}`); code != 400 {
		t.Errorf("a built-in was patched: %d", code)
	}
	if code, _ := f.do("PATCH", "/api/settings/appearance/themes/c-nope", `{"base":"dark","tokens":{}}`); code != 404 {
		t.Errorf("an unknown theme: %d", code)
	}
}
