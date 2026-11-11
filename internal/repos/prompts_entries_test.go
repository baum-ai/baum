package repos

import (
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent/prompt"
)

// sameSources fails unless every entry's source is what ResolvePrompts, which
// jobs use, reports.
func sameSources(t *testing.T, r *Registry, repository string, list []PromptEntry) {
	t.Helper()
	_, sources, e := r.ResolvePrompts(t.Context(), repository)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range list {
		if entry.Source != sources[prompt.ID(entry.ID)] {
			t.Fatalf("%s: entry source %q, jobs use %q", entry.ID, entry.Source, sources[prompt.ID(entry.ID)])
		}
	}
}

func storeRawPrompt(t *testing.T, r *Registry, scope, id, text string) {
	t.Helper()
	if _, e := r.db.Exec(`INSERT OR REPLACE INTO prompt_overrides VALUES (?,?,?,?,?)`, scope, id, text, "v", "2026-01-01T00:00:00Z"); e != nil {
		t.Fatal(e)
	}
}

func TestPromptEntriesFollowWhatJobsUse(t *testing.T) {
	r := registry(t)
	do := promptDo(promptServer(r))
	const base = "/api/repositories/Owner/Repo/prompts"
	if _, e := r.Pick(t.Context(), "Owner/Repo", nil); e != nil {
		t.Fatal(e)
	}
	fix, _ := prompt.Lookup(prompt.IDFix)

	// Invalid stored global row: the global view flags it, a repository
	// inherits the built-in text.
	storeRawPrompt(t, r, "", "fix", "   ")
	g := promptByID(t, promptList(t, do, "/api/settings/prompts"), prompt.IDFix)
	if g.CustomText == nil || *g.CustomText != "   " || !g.Invalid || g.InvalidReason == nil || !strings.Contains(*g.InvalidReason, "empty") || strings.Contains(*g.InvalidReason, "invalid prompt") {
		t.Fatalf("%+v", g)
	}
	if len(g.MissingRules) != 0 {
		t.Fatalf("effective is the built-in text: %v", g.MissingRules)
	}
	list := promptList(t, do, base)
	e := promptByID(t, list, prompt.IDFix)
	if e.Source != PromptSourceDefault || *e.InheritedText != fix.Default || e.Invalid || e.InvalidReason != nil || e.CustomText != nil {
		t.Fatalf("%+v", e)
	}
	sameSources(t, r, "owner/repo", list)

	// Valid rows show invalid false and a null reason.
	if _, body := do("PUT", "/api/settings/prompts/fix", `{"text":"global no rules"}`); !strings.Contains(string(body), `"invalid":false,"invalid_reason":null`) {
		t.Fatalf("%s", body)
	}
	// A global text lacking the rules counts against a repository that
	// inherits it.
	list = promptList(t, do, base)
	e = promptByID(t, list, prompt.IDFix)
	if e.Source != PromptSourceGlobal || *e.InheritedText != "global no rules" || len(e.MissingRules) != len(fix.SafetyRules) {
		t.Fatalf("%+v", e)
	}
	sameSources(t, r, "owner/repo", list)

	// Invalid stored repository row: shown, not used; source falls back to
	// global, then to the built-in text.
	storeRawPrompt(t, r, "owner/repo", "fix", "\n ")
	list = promptList(t, do, base)
	e = promptByID(t, list, prompt.IDFix)
	if e.CustomText == nil || !e.Invalid || e.InvalidReason == nil || e.Source != PromptSourceGlobal || *e.InheritedText != "global no rules" || len(e.MissingRules) != len(fix.SafetyRules) {
		t.Fatalf("%+v", e)
	}
	sameSources(t, r, "owner/repo", list)
	do("PUT", "/api/settings/prompts/fix", `{"text":null}`)
	list = promptList(t, do, base)
	e = promptByID(t, list, prompt.IDFix)
	if !e.Invalid || e.Source != PromptSourceDefault || *e.InheritedText != fix.Default || len(e.MissingRules) != 0 {
		t.Fatalf("%+v", e)
	}
	sameSources(t, r, "owner/repo", list)

	// A repository text that validates replaces the bad one.
	if code, body := do("PUT", base+"/fix", `{"text":"repo"}`); code != 200 || strings.Contains(string(body), `"invalid":true`) {
		t.Fatalf("%d %s", code, body)
	}
	list = promptList(t, do, base)
	if e = promptByID(t, list, prompt.IDFix); e.Source != PromptSourceRepository || e.Invalid {
		t.Fatalf("%+v", e)
	}
	sameSources(t, r, "owner/repo", list)

	// Unpicked: rows are kept but not visible; they return once picked again.
	if e := r.Unpick(t.Context(), "owner/repo"); e != nil {
		t.Fatal(e)
	}
	if code, _ := do("GET", base, ""); code != 404 {
		t.Fatalf("unpicked %d", code)
	}
	if _, e := r.Pick(t.Context(), "owner/repo", nil); e != nil {
		t.Fatal(e)
	}
	if e = promptByID(t, promptList(t, do, base), prompt.IDFix); e.Source != PromptSourceRepository || *e.CustomText != "repo" {
		t.Fatalf("after re-pick %+v", e)
	}
}
