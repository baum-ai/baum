package repos

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func explicitlyOn(b *bool) bool { return b != nil && *b }

// New registrations follow the global preference, which is on by default; an
// explicit off wins over a global on, nothing wins over a global off, and
// choices survive reopening.
func TestAutomaticDefaultsOnAndExplicitChoicesWin(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "repos.sqlite")
	r, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	c, e := r.Add(ctx, clone(t, "https://github.com/owner/default"), nil)
	if e != nil {
		t.Fatal(e)
	}
	prefs, e := r.Preferences(ctx)
	if e != nil || !prefs.Automatic || prefs.CursorAgentAcknowledged {
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
		p.CursorAgentAcknowledged = true
		return p
	})
	if e != nil || prefs.Automatic || !prefs.CursorAgentAcknowledged {
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
	if prefs, e = r.Preferences(ctx); e != nil || prefs.Automatic || !prefs.CursorAgentAcknowledged {
		t.Fatalf("preferences after reopen %#v %v", prefs, e)
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

// A registry written before automatic generation stored automatic=false for
// every repository; those rows follow the new default after migration, while
// an explicit true (only reachable with `baum internal repos add --automatic`) stays.
func TestLegacyAutomaticFalseFollowsTheDefault(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "repos.sqlite")
	legacyPath, explicitPath := clone(t, "https://github.com/owner/legacy"), clone(t, "https://github.com/owner/explicit")
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`CREATE TABLE repositories(identity TEXT PRIMARY KEY,settings TEXT NOT NULL); CREATE TABLE clones(id TEXT PRIMARY KEY,path TEXT NOT NULL UNIQUE,common_dir TEXT NOT NULL,repository TEXT NOT NULL REFERENCES repositories(identity));`); e != nil {
		t.Fatal(e)
	}
	for _, row := range []struct{ path, identity, settings string }{
		{legacyPath, "owner/legacy", `{"automatic":false,"daily_cap":3}`},
		{explicitPath, "owner/explicit", `{"automatic":true}`},
	} {
		c, e := inspect(ctx, row.path)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(`INSERT INTO repositories VALUES (?,?)`, row.identity, row.settings); e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(`INSERT INTO clones VALUES (?,?,?,?)`, c.ID, c.Path, c.CommonDir, row.identity); e != nil {
			t.Fatal(e)
		}
	}
	_ = db.Close()

	check := func(r *Registry) {
		t.Helper()
		list, e := r.List(ctx)
		if e != nil || len(list) != 2 {
			t.Fatalf("%v %v", list, e)
		}
		for _, c := range list {
			switch c.Repository {
			case "owner/legacy":
				if c.Settings.Automatic != nil || c.Settings.DailyCap == nil || *c.Settings.DailyCap != 3 {
					t.Fatalf("legacy row %#v", c.Settings)
				}
			case "owner/explicit":
				if !explicitlyOn(c.Settings.Automatic) {
					t.Fatalf("explicit row %#v", c.Settings)
				}
			}
		}
		if prefs, e := r.Preferences(ctx); e != nil || !prefs.Automatic {
			t.Fatalf("preferences %#v %v", prefs, e)
		}
	}
	// A read-only registry (baum internal doctor) reads the legacy file the same way
	// without migrating it.
	ro, e := OpenReadOnly(path)
	if e != nil {
		t.Fatal(e)
	}
	check(ro)
	_ = ro.Close()

	r, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	check(r)
	// After migration an explicit off is stored and kept.
	list, _ := r.List(ctx)
	for _, c := range list {
		if c.Repository == "owner/legacy" {
			if e = r.Update(ctx, c.ID, Settings{Automatic: new(false)}); e != nil {
				t.Fatal(e)
			}
		}
	}
	_ = r.Close()
	r, e = Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close() }()
	list, _ = r.List(ctx)
	for _, c := range list {
		if c.Repository == "owner/legacy" && (c.Settings.Automatic == nil || *c.Settings.Automatic) {
			t.Fatalf("explicit off reset by a second open %#v", c.Settings)
		}
	}
}
