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
	if len(rec.inputs) != 1 || rec.inputs[0].Theme == nil || *rec.inputs[0].Theme != (ThemeSave{"Dusk", "dark"}) {
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
	if len(open) != 3 || open[0].Page != ProposalAnywhere || open[0].Target != TargetTheme || !sameJSON(open[0].After, json.RawMessage(`{"name":"Dusk","base":"dark","tokens":{"--color-paper":"#e8eef4"}}`)) {
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
