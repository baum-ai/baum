package repos

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
)

func TestForwardedRenameDeletesTheEarlierRows(t *testing.T) {
	ctx := context.Background()
	for _, stillPicked := range []bool{true, false} {
		r := registry(t)
		if _, e := r.Pick(ctx, "old/name", nil); e != nil {
			t.Fatal(e)
		}
		if _, e := r.SetPromptOverride(ctx, "old/name", prompt.IDFix, "old fix"); e != nil {
			t.Fatal(e)
		}
		if !stillPicked {
			if e := r.Unpick(ctx, "old/name"); e != nil {
				t.Fatal(e)
			}
		}
		work := clone(t, "https://github.com/old/name.git")
		if _, e := r.AddForwarded(ctx, work, "new/name", "old/name", nil); e != nil {
			t.Fatal(e)
		}
		if got, _ := r.PromptOverrides(ctx, "old/name"); len(got) != 0 {
			t.Errorf("picked=%v: earlier rows stay: %+v", stillPicked, got)
		}
		got, _ := r.PromptOverrides(ctx, "new/name")
		if stillPicked != (len(got) == 1) {
			t.Errorf("picked=%v: carried %+v", stillPicked, got)
		}
	}
}

func TestResolvePromptsSkipsStoredTextsThatFailValidation(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	if _, e := r.Pick(ctx, "owner/a", nil); e != nil {
		t.Fatal(e)
	}
	for _, scope := range []string{"owner/a", GlobalPrompts} {
		if _, e := r.db.Exec(`INSERT INTO prompt_overrides VALUES (?,?,?,?,?)`, scope, string(prompt.IDFix), "  ", "v", "2026-01-01T00:00:00Z"); e != nil {
			t.Fatal(e)
		}
	}
	ov, sources, e := r.ResolvePrompts(ctx, "owner/a")
	if e != nil || len(ov) != 0 || sources[prompt.IDFix] != PromptSourceDefault {
		t.Fatalf("%v %v %v", ov, sources, e)
	}
}

func TestOpenReadOnlySeesATableCreatedLater(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(appdbtest.TempDir(t), "repos.sqlite")
	w, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = w.Close() }()
	if _, e = w.db.Exec(`DROP TABLE prompt_overrides`); e != nil {
		t.Fatal(e)
	}
	ro, e := OpenReadOnly(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = ro.Close() }()
	if got, e := ro.PromptOverrides(ctx, GlobalPrompts); e != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, e)
	}
	if _, e = w.db.Exec(`CREATE TABLE prompt_overrides(repository TEXT NOT NULL,prompt_id TEXT NOT NULL,text TEXT NOT NULL,based_on TEXT NOT NULL,updated_at TEXT NOT NULL,PRIMARY KEY(repository,prompt_id))`); e != nil {
		t.Fatal(e)
	}
	if _, e = w.SetPromptOverride(ctx, GlobalPrompts, prompt.IDFix, "x"); e != nil {
		t.Fatal(e)
	}
	if got, e := ro.PromptOverrides(ctx, GlobalPrompts); e != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, e)
	}
}
