package repos

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
)

func explicitlyOn(b *bool) bool { return b != nil && *b }

// New registrations follow the global preference, which is on by default; an
// explicit off wins over a global on, nothing wins over a global off, and
// choices survive reopening.
func TestAutomaticDefaultsOnAndExplicitChoicesWin(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(appdbtest.TempDir(t), "repos.sqlite")
	r, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	c, e := r.Add(ctx, clone(t, "https://github.com/owner/default"), nil)
	if e != nil {
		t.Fatal(e)
	}
	prefs, e := r.Preferences(ctx)
	if e != nil || !prefs.Automatic {
		t.Fatalf("default preferences %#v %v", prefs, e)
	}
	if c.Settings.Automatic != nil || !c.Settings.AutomaticEnabled(prefs) {
		t.Fatalf("new registration %#v", c.Settings)
	}
	off, e := r.Add(ctx, clone(t, "https://github.com/owner/off"), &Settings{Automatic: new(false)})
	if e != nil {
		t.Fatal(e)
	}
	on, e := r.Add(ctx, clone(t, "https://github.com/owner/on"), &Settings{Automatic: new(true)})
	if e != nil {
		t.Fatal(e)
	}
	if off.Settings.AutomaticEnabled(prefs) || !on.Settings.AutomaticEnabled(prefs) {
		t.Fatal("explicit choices ignored")
	}
	// The global switch is a master switch: off stops every repository.
	disabled := Preferences{}
	for _, s := range []Settings{c.Settings, off.Settings, on.Settings} {
		if s.AutomaticEnabled(disabled) {
			t.Fatalf("enabled under the global off switch: %#v", s)
		}
	}
	prefs, e = r.UpdatePreferences(ctx, func(p Preferences) Preferences {
		p.Automatic = false
		return p
	})
	if e != nil || prefs.Automatic {
		t.Fatalf("updated preferences %#v %v", prefs, e)
	}
	_ = r.Close()
	// Reopening runs the migration check again; it must not reset an explicit
	// off chosen after the migration.
	r, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close() }()
	if prefs, e = r.Preferences(ctx); e != nil || prefs.Automatic {
		t.Fatalf("preferences after reopen %#v %v", prefs, e)
	}
	// An older baum's cursor-agent acknowledgement is ignored, not an error.
	if _, e = r.db.ExecContext(ctx, "UPDATE preferences SET value=? WHERE key=?", `{"automatic":false,"cursor_agent_acknowledged":true}`, automaticPreferences); e != nil {
		t.Fatal(e)
	}
	if prefs, e = r.Preferences(ctx); e != nil || prefs.Automatic {
		t.Fatalf("preferences with an old field %#v %v", prefs, e)
	}
	list, e := r.List(ctx)
	if e != nil {
		t.Fatal(e)
	}
	got := map[string]*bool{}
	for _, c := range list {
		got[c.Repository] = c.Settings.Automatic
	}
	if got["owner/default"] != nil || got["owner/off"] == nil || *got["owner/off"] || !explicitlyOn(got["owner/on"]) {
		t.Fatalf("settings after reopen %#v", list)
	}
}
