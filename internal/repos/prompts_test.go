package repos

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
)

func TestPromptOverrideSetGetDelete(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	if _, e := r.Pick(ctx, "Owner/Repo", nil); e != nil {
		t.Fatal(e)
	}
	o, e := r.SetPromptOverride(ctx, "Owner/Repo", prompt.IDFix, "my fix prompt")
	entry, _ := prompt.Lookup(prompt.IDFix)
	if e != nil || o.BasedOn != entry.DefaultVersion || o.BasedOn == "" || o.UpdatedAt.IsZero() {
		t.Fatalf("%+v %v", o, e)
	}
	if _, e = r.SetPromptOverride(ctx, "owner/repo", prompt.IDFix, "second"); e != nil {
		t.Fatal(e)
	}
	got, e := r.PromptOverrides(ctx, "owner/repo")
	if e != nil || len(got) != 1 || got[prompt.IDFix].Text != "second" || got[prompt.IDFix].BasedOn != entry.DefaultVersion {
		t.Fatalf("%+v %v", got, e)
	}
	if g, _ := r.PromptOverrides(ctx, ""); len(g) != 0 {
		t.Fatalf("global %+v", g)
	}
	if e = r.DeletePromptOverride(ctx, "owner/repo", prompt.IDFix); e != nil {
		t.Fatal(e)
	}
	if e = r.DeletePromptOverride(ctx, "owner/repo", prompt.IDFix); e != nil {
		t.Fatalf("deleting a missing row: %v", e)
	}
	if got, _ = r.PromptOverrides(ctx, "owner/repo"); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if e = r.DeletePromptOverride(ctx, "", "nope"); !errors.Is(e, ErrInvalidPrompt) {
		t.Fatalf("unknown id delete: %v", e)
	}
}

func TestPromptOverrideValidation(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	for name, c := range map[string]struct {
		id   prompt.ID
		text string
	}{
		"unknown":    {"nope", "text"},
		"empty":      {prompt.IDFix, "  \n"},
		"too large":  {prompt.IDFix, strings.Repeat("a", prompt.MaxOverrideBytes+1)},
		"submitting": {prompt.IDReview, "Review.\n\n## Submitting\nDo it."},
	} {
		_, e := r.SetPromptOverride(ctx, "", c.id, c.text)
		if !errors.Is(e, ErrInvalidPrompt) || e.Error() == ErrInvalidPrompt.Error() {
			t.Errorf("%s: %v", name, e)
		}
	}
	if got, _ := r.PromptOverrides(ctx, ""); len(got) != 0 {
		t.Fatalf("stored %+v", got)
	}
}

func TestPromptOverrideRequiresPickedRepository(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	if _, e := r.SetPromptOverride(ctx, "owner/repo", prompt.IDFix, "x"); !errors.Is(e, ErrNotPicked) {
		t.Fatalf("set: %v", e)
	}
	if e := r.DeletePromptOverride(ctx, "owner/repo", prompt.IDFix); !errors.Is(e, ErrNotPicked) {
		t.Fatalf("delete: %v", e)
	}
	if _, e := r.SetPromptOverride(ctx, "", prompt.IDFix, "x"); e != nil {
		t.Fatalf("global needs no pick: %v", e)
	}
}

func TestResolvePrompts(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	for _, name := range []string{"owner/a", "owner/b"} {
		if _, e := r.Pick(ctx, name, nil); e != nil {
			t.Fatal(e)
		}
	}
	for _, s := range []struct {
		repo string
		id   prompt.ID
		text string
	}{{"owner/a", prompt.IDFix, "repo fix"}, {"", prompt.IDReview, "global review"}} {
		if _, e := r.SetPromptOverride(ctx, s.repo, s.id, s.text); e != nil {
			t.Fatal(e)
		}
	}
	check := func(repo string, want map[prompt.ID]string, src map[prompt.ID]PromptSource) {
		t.Helper()
		ov, sources, e := r.ResolvePrompts(ctx, repo)
		if e != nil || len(ov) != len(want) || len(sources) != len(prompt.Catalogue()) {
			t.Fatalf("%s: %v %v %v", repo, ov, sources, e)
		}
		for id, text := range want {
			if ov[id] != text {
				t.Errorf("%s %s = %q", repo, id, ov[id])
			}
		}
		for _, entry := range prompt.Catalogue() {
			want, ok := src[entry.ID]
			if !ok {
				want = PromptSourceDefault
			}
			if sources[entry.ID] != want {
				t.Errorf("%s %s source %q, want %q", repo, entry.ID, sources[entry.ID], want)
			}
		}
	}
	ownText := map[prompt.ID]string{prompt.IDFix: "repo fix", prompt.IDReview: "global review"}
	ownSrc := map[prompt.ID]PromptSource{prompt.IDFix: PromptSourceRepository, prompt.IDReview: PromptSourceGlobal}
	globalText := map[prompt.ID]string{prompt.IDReview: "global review"}
	globalSrc := map[prompt.ID]PromptSource{prompt.IDReview: PromptSourceGlobal}
	check("OWNER/A", ownText, ownSrc)
	check("owner/b", globalText, globalSrc)
	check("", globalText, globalSrc)
	// A repository that is not picked gets only the global overrides.
	check("owner/unpicked", globalText, globalSrc)
	// Unpick keeps the rows; re-picking applies them again.
	if e := r.Unpick(ctx, "owner/a"); e != nil {
		t.Fatal(e)
	}
	check("owner/a", globalText, globalSrc)
	if got, _ := r.PromptOverrides(ctx, "owner/a"); len(got) != 1 {
		t.Fatalf("unpick dropped rows: %+v", got)
	}
	if _, e := r.Pick(ctx, "owner/a", nil); e != nil {
		t.Fatal(e)
	}
	check("owner/a", ownText, ownSrc)
}

func TestForwardedRenameCarriesPromptOverrides(t *testing.T) {
	for _, currentHasRows := range []bool{false, true} {
		ctx := context.Background()
		r := registry(t)
		if _, e := r.Pick(ctx, "old/name", nil); e != nil {
			t.Fatal(e)
		}
		if _, e := r.SetPromptOverride(ctx, "old/name", prompt.IDFix, "old fix"); e != nil {
			t.Fatal(e)
		}
		if currentHasRows {
			if _, e := r.Pick(ctx, "new/name", nil); e != nil {
				t.Fatal(e)
			}
			if _, e := r.SetPromptOverride(ctx, "new/name", prompt.IDReview, "new review"); e != nil {
				t.Fatal(e)
			}
		}
		work := clone(t, "https://github.com/old/name.git")
		if _, e := r.AddForwarded(ctx, work, "new/name", "old/name", nil); e != nil {
			t.Fatal(e)
		}
		got, e := r.PromptOverrides(ctx, "new/name")
		if e != nil {
			t.Fatal(e)
		}
		if currentHasRows {
			if len(got) != 1 || got[prompt.IDReview].Text != "new review" {
				t.Errorf("rows were merged: %+v", got)
			}
		} else if len(got) != 1 || got[prompt.IDFix].Text != "old fix" || got[prompt.IDFix].BasedOn == "" {
			t.Errorf("rows not carried: %+v", got)
		}
	}
}

func TestOpenReadOnlyWithoutPromptTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(appdbtest.TempDir(t), "repos.sqlite")
	w, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.SetPromptOverride(ctx, "", prompt.IDFix, "x"); e != nil {
		t.Fatal(e)
	}
	if _, e = w.db.Exec(`DROP TABLE prompt_overrides`); e != nil {
		t.Fatal(e)
	}
	_ = w.Close()
	ro, e := OpenReadOnly(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = ro.Close() }()
	if got, e := ro.PromptOverrides(ctx, ""); e != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, e)
	}
	ov, sources, e := ro.ResolvePrompts(ctx, "owner/repo")
	if e != nil || len(ov) != 0 || sources[prompt.IDFix] != PromptSourceDefault {
		t.Fatalf("%v %v %v", ov, sources, e)
	}
}
