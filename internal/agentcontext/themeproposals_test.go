package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/themes"
)

// The theme is proposed from any page, saved after navigating, and the
// exception is for that target alone.
func TestThemeProposalIsForAnyPageAndNoOtherTarget(t *testing.T) {
	ctx := context.Background()
	fake := newFake(TargetTheme, "current")
	s := New(Options{API: http.NotFoundHandler(), Proposers: map[string]Proposer{ProposalAnywhere: fake}})
	if _, ok := s.page(ProposalAnywhere); ok {
		t.Fatal("the sentinel is registered as a page")
	}
	_, id := chat(t, s)
	after := json.RawMessage(`"x"`)
	if _, err := s.Proposals.Propose(ctx, id, Proposal{Page: ProposalAnywhere, Target: TargetTheme, After: after}); !errors.Is(err, ErrNotCurrentPage) {
		t.Fatal("proposed without any page open:", err)
	}
	setPage(t, s, id, PageContext{Page: PageGroup, Group: "g1"})
	p, err := s.Proposals.Propose(ctx, id, Proposal{Page: ProposalAnywhere, Target: TargetTheme, After: after})
	if err != nil {
		t.Fatal(err)
	}
	// Saved from another page.
	setPage(t, s, id, PageContext{Page: PageInbox})
	if err := s.Proposals.Save(ctx, id, p.ID, SaveInput{}); err != nil || fake.value(TargetTheme) != "x" {
		t.Fatal(err)
	}
	// A target that is not the theme, under the sentinel, is refused; so is
	// the theme under a real page.
	if _, err := s.Proposals.Propose(ctx, id, Proposal{Page: ProposalAnywhere, Target: TargetAppearance, After: after}); !errors.Is(err, ErrNoProposer) {
		t.Errorf("another target under the sentinel: %v", err)
	}
	if err := s.RegisterPage(PageSpec{Name: PageInbox, Proposer: fake}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Proposals.Propose(ctx, id, Proposal{Page: PageInbox, Target: TargetTheme, After: after}); !errors.Is(err, ErrNoProposer) {
		t.Errorf("the theme under a real page: %v", err)
	}
	// Another page's proposal still needs its page open.
	setPage(t, s, id, PageContext{Page: PageSettings})
	if _, err := s.Proposals.Propose(ctx, id, Proposal{Page: PageInbox, Target: "x", After: after}); !errors.Is(err, ErrNotCurrentPage) {
		t.Errorf("another page's proposal: %v", err)
	}
}

func TestSaveInputThemeReachesApplyAndIsRedacted(t *testing.T) {
	ctx := context.Background()
	rec := &takeAll{}
	s := New(Options{API: http.NotFoundHandler(), Proposers: map[string]Proposer{ProposalAnywhere: rec}})
	_, id := chat(t, s)
	setPage(t, s, id, PageContext{Page: PageInbox})
	p, err := s.Proposals.Propose(ctx, id, Proposal{Page: ProposalAnywhere, Target: TargetTheme, After: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Proposals.Save(ctx, id, p.ID, SaveInput{Theme: &ThemeSave{Name: "Dusk", Slot: "dark"}}); err != nil {
		t.Fatal(err)
	}
	if len(rec.inputs) != 1 || rec.inputs[0].Theme == nil || *rec.inputs[0].Theme != (ThemeSave{Name: "Dusk", Slot: "dark"}) {
		t.Fatalf("Apply got %v", rec.inputs)
	}
	var in SaveInput
	if err := json.Unmarshal([]byte(`{"theme":{"name":"A","slot":"light"}}`), &in); err != nil || in.Theme == nil || in.Theme.Slot != "light" {
		t.Fatal(err, in)
	}
	secret := "TYPED_SECRET"
	both := SaveInput{Value: &secret, Theme: &ThemeSave{Name: "Dusk"}}
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).Info("save", "input", both)
	if out := fmt.Sprintf("%v %+v %#v %s", both, both, both, both) + logged.String(); strings.Contains(out, secret) || strings.Contains(out, "Dusk") {
		t.Errorf("a save input printed its fields: %s", out)
	}
}

func TestProposeThemeTool(t *testing.T) {
	rec := &takeAll{}
	s := New(Options{API: http.NotFoundHandler(), Proposers: map[string]Proposer{ProposalAnywhere: rec}})
	token, id := chat(t, s)
	// No page open: refused. Then any page will do.
	args := map[string]any{"name": "Dusk", "base": "dark", "tokens": map[string]string{"--color-paper": "#e8eef4"}}
	if text, failed := data(t, s, token, "propose_theme", args); !failed {
		t.Fatalf("proposed with no page open: %s", text)
	}
	for _, page := range []PageContext{{Page: PageInbox}, {Page: PagePullRequest, Repository: "acme/web", PullRequest: 3}, {Page: PageSettings, Section: "fixes"}} {
		setPage(t, s, id, page)
		text, failed := data(t, s, token, "propose_theme", args)
		if failed {
			t.Fatalf("%+v: %s", page, text)
		}
		var got struct {
			ID               string           `json:"id"`
			Status           string           `json:"status"`
			ContrastWarnings []themes.Warning `json:"contrast_warnings"`
		}
		if err := json.Unmarshal([]byte(text), &got); err != nil || got.ID == "" || got.Status != proposedStatus || got.ContrastWarnings == nil {
			t.Fatalf("answer %s: %v", text, err)
		}
		if strings.Contains(text, "current") {
			t.Errorf("the answer carries the current value: %s", text)
		}
	}
	open := s.Proposals.List(id)
	// Each theme replaced the one before it, so only the last is open.
	if len(open) != 1 || open[0].Page != ProposalAnywhere || open[0].Target != TargetTheme || !sameJSON(open[0].After, json.RawMessage(`{"name":"Dusk","base":"dark","tokens":{"--color-paper":"#e8eef4"}}`)) {
		t.Fatalf("proposals %+v", open)
	}
	// A light theme whose page surface and primary text are both dark (#222222
	// and #333333) is hard to read, so the answer carries contrast warnings.
	text, _ := data(t, s, token, "propose_theme", map[string]any{"name": "Glare", "base": "light", "tokens": map[string]string{"--surface-page": "#222222", "--text-primary": "#333333"}})
	var warned struct {
		ContrastWarnings []themes.Warning `json:"contrast_warnings"`
	}
	if err := json.Unmarshal([]byte(text), &warned); err != nil || len(warned.ContrastWarnings) == 0 {
		t.Errorf("no contrast warning for %s", text)
	}
	// What the validator refuses reaches the agent, and nothing is recorded.
	before := len(s.Proposals.List(id))
	for name, a := range map[string]map[string]any{
		"unknown token": {"name": "N", "base": "dark", "tokens": map[string]string{"--nope": "#fff"}},
		"not a colour":  {"name": "N", "base": "dark", "tokens": map[string]string{"--color-ink": "var(--x)"}},
		"bad base":      {"name": "N", "base": "sepia", "tokens": map[string]string{}},
		"empty name":    {"name": " ", "base": "dark", "tokens": map[string]string{}},
		"long name":     {"name": strings.Repeat("a", 61), "base": "dark", "tokens": map[string]string{}},
		"extra field":   {"name": "N", "base": "dark", "tokens": map[string]string{}, "slot": "dark"},
		"no tokens key": {"name": "N", "base": "dark"},
	} {
		if text, failed := data(t, s, token, "propose_theme", a); !failed {
			t.Errorf("%s: %s", name, text)
		}
	}
	if len(s.Proposals.List(id)) != before {
		t.Error("a refused theme was recorded")
	}
	// The schema bounds the tokens at the allowlist's size.
	tool, _ := s.tool("propose_theme")
	if !strings.Contains(string(tool.InputSchema), fmt.Sprintf(`"maxProperties":%d`, len(themes.Tokens))) || !tool.Proposes || tool.Scope != ScopeGlobal || len(tool.Reads) != 0 || len(tool.Projected) != 0 {
		t.Errorf("propose_theme is declared %+v", tool)
	}
	for _, seed := range themes.Seeds {
		if !themes.Tokens[seed] || !strings.Contains(tool.Description, seed) {
			t.Errorf("seed %s is not an allowlisted token the description names", seed)
		}
	}
	// Every page lists it.
	if !slices.Contains(commonToolNames, "propose_theme") {
		t.Error("propose_theme is not a common tool")
	}
}

// A newer theme proposal closes the open ones before it, so an older one never
// resurfaces when the newer is saved or dismissed; other proposals stay.
func TestNewThemeProposalReplacesOpenThemes(t *testing.T) {
	ctx := context.Background()
	rec := &takeAll{}
	s := New(Options{API: http.NotFoundHandler(), Proposers: map[string]Proposer{ProposalAnywhere: rec}})
	_, id := chat(t, s)
	_, other := chat(t, s)
	setPage(t, s, id, PageContext{Page: PageInbox})
	setPage(t, s, other, PageContext{Page: PageInbox})
	propose := func(consumer, name string) Proposal {
		t.Helper()
		p, err := s.Proposals.Propose(ctx, consumer, Proposal{Page: ProposalAnywhere, Target: TargetTheme, After: json.RawMessage(`{"name":"` + name + `"}`)})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	var changes []string
	s.Proposals.OnChange(func(consumer string) { changes = append(changes, consumer) })
	a := propose(id, "A")
	elsewhere := propose(other, "Z")
	b := propose(id, "B")
	if open := s.Proposals.List(id); len(open) != 1 || open[0].ID != b.ID {
		t.Fatalf("open after B: %+v", open)
	}
	if err := s.Proposals.Dismiss(id, a.ID); !errors.Is(err, ErrUnknownProposal) {
		t.Errorf("A is still open: %v", err)
	}
	if open := s.Proposals.List(other); len(open) != 1 || open[0].ID != elsewhere.ID {
		t.Errorf("another conversation's theme was closed: %+v", open)
	}
	if err := s.Proposals.Dismiss(id, b.ID); err != nil {
		t.Fatal(err)
	}
	if open := s.Proposals.List(id); len(open) != 0 {
		t.Errorf("A came back: %+v", open)
	}
	if slices.Contains(changes, "") || len(changes) != 4 {
		t.Errorf("changes %v", changes)
	}
}

// propose_theme keeps theme_id in the proposal's After, and the tool says
// what it is for.
func TestProposeThemeToolKeepsThemeID(t *testing.T) {
	rec := &takeAll{}
	s := New(Options{API: http.NotFoundHandler(), Proposers: map[string]Proposer{ProposalAnywhere: rec}})
	token, id := chat(t, s)
	setPage(t, s, id, PageContext{Page: PageInbox})
	text, failed := data(t, s, token, "propose_theme", map[string]any{"theme_id": "c-1", "name": "Dusk", "base": "dark", "tokens": map[string]string{}})
	if failed {
		t.Fatal(text)
	}
	open := s.Proposals.List(id)
	if len(open) != 1 || !sameJSON(open[0].After, json.RawMessage(`{"theme_id":"c-1","name":"Dusk","base":"dark","tokens":{}}`)) {
		t.Fatalf("proposals %+v", open)
	}
	if d := proposeThemeTool().Description; !strings.Contains(d, "pass its id as theme_id") || !strings.Contains(d, "Omit theme_id") {
		t.Errorf("the description does not explain theme_id: %s", d)
	}
}

// propose_theme_choice records an omitted slot as in use and names the slot
// in its answer when the mode is light or dark.
func TestProposeThemeChoiceTool(t *testing.T) {
	rec := &takeAll{}
	s := New(Options{API: http.NotFoundHandler(), Proposers: map[string]Proposer{ProposalAnywhere: rec}})
	token, id := chat(t, s)
	args := map[string]any{"theme_id": "pine"}
	if text, failed := data(t, s, token, "propose_theme_choice", args); !failed {
		t.Fatalf("proposed with no page open: %s", text)
	}
	for _, tt := range []struct {
		page    PageContext
		args    map[string]any
		after   string
		applies string
	}{
		{PageContext{Page: PageInbox}, map[string]any{"theme_id": "pine"}, `{"theme_id":"pine","slot":"in_use"}`, "the slot in use"},
		{PageContext{Page: PagePullRequest, Repository: "acme/web", PullRequest: 3}, map[string]any{"theme_id": "pine", "theme": "dark"}, `{"theme_id":"pine","slot":"in_use","theme":"dark"}`, "the dark theme (the slot in use)"},
		{PageContext{Page: PageSettings, Section: "fixes"}, map[string]any{"theme_id": "pine", "theme": "system"}, `{"theme_id":"pine","slot":"in_use","theme":"system"}`, "the slot in use"},
		{PageContext{Page: PageInbox}, map[string]any{"theme_id": "pine", "slot": "light", "theme": "dark"}, `{"theme_id":"pine","slot":"light","theme":"dark"}`, "the light theme"},
		{PageContext{Page: PageInbox}, map[string]any{"theme": "light"}, `{"theme":"light"}`, ""},
	} {
		setPage(t, s, id, tt.page)
		text, failed := data(t, s, token, "propose_theme_choice", tt.args)
		if failed {
			t.Fatalf("%v: %s", tt.args, text)
		}
		var got struct {
			ID        string `json:"id"`
			Page      string `json:"page"`
			Target    string `json:"target"`
			Status    string `json:"status"`
			AppliesTo string `json:"applies_to"`
		}
		if err := json.Unmarshal([]byte(text), &got); err != nil || got.ID == "" || got.Page != ProposalAnywhere || got.Target != TargetThemeChoice ||
			got.Status != proposedStatus || got.AppliesTo != tt.applies || strings.Contains(text, "current") {
			t.Fatalf("answer %s: %v", text, err)
		}
		open := s.Proposals.List(id)
		if len(open) != 1 || !sameJSON(open[0].After, json.RawMessage(tt.after)) {
			t.Fatalf("recorded %+v, want %s", open, tt.after)
		}
	}
	for name, a := range map[string]map[string]any{
		"nothing":     {},
		"slot only":   {"slot": "dark"},
		"bad slot":    {"theme_id": "pine", "slot": "in_use"},
		"bad mode":    {"theme": "neon"},
		"empty id":    {"theme_id": ""},
		"old field":   {"light_theme": "pine"},
		"wrong types": {"theme_id": 1},
	} {
		if text, failed := data(t, s, token, "propose_theme_choice", a); !failed {
			t.Errorf("%s was proposed: %s", name, text)
		}
	}
	d := proposeThemeChoiceTool().Description
	if !strings.Contains(d, "omit slot unless the person names the other mode") || strings.Contains(d, "Appearance") {
		t.Errorf("description: %s", d)
	}
}
