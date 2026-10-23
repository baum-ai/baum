package repos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// version1Database writes a registry as version 1 left it: no picked table.
// It has two repositories with a clone and one orphan repository row.
func version1Database(t *testing.T, version int) string {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "repos.sqlite")
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = db.Close() }()
	if _, e = db.Exec(`CREATE TABLE repositories(identity TEXT PRIMARY KEY,settings TEXT NOT NULL); CREATE TABLE clones(id TEXT PRIMARY KEY,path TEXT NOT NULL UNIQUE,common_dir TEXT NOT NULL,repository TEXT NOT NULL REFERENCES repositories(identity)); CREATE TABLE preferences(key TEXT PRIMARY KEY,value TEXT NOT NULL);`); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"one", "two"} {
		c, e := inspect(ctx, clone(t, "https://github.com/owner/"+name))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(`INSERT INTO repositories VALUES (?,?)`, c.Repository, `{"automatic":true}`); e != nil {
			t.Fatal(e)
		}
		if _, e = db.Exec(`INSERT INTO clones VALUES (?,?,?,?)`, c.ID, c.Path, c.CommonDir, c.Repository); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = db.Exec(`INSERT INTO repositories VALUES ('owner/orphan','{"automatic":null}')`); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("PRAGMA user_version=" + strconv.Itoa(version)); e != nil {
		t.Fatal(e)
	}
	return path
}

func pickedIdentities(t *testing.T, path string) []string {
	t.Helper()
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = db.Close() }()
	rows, e := db.Query("SELECT identity FROM picked ORDER BY identity")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var s string
		if e = rows.Scan(&s); e != nil {
			t.Fatal(e)
		}
		got = append(got, s)
	}
	return got
}

func userVersion(t *testing.T, path string) int {
	t.Helper()
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = db.Close() }()
	var v int
	if e = db.QueryRow("PRAGMA user_version").Scan(&v); e != nil {
		t.Fatal(e)
	}
	return v
}

func identities(list []Repository) []string {
	var got []string
	for _, v := range list {
		got = append(got, v.Repository)
	}
	return got
}

func TestMigrationBackfillsPicked(t *testing.T) {
	ctx := context.Background()
	for _, version := range []int{0, 1} {
		path := version1Database(t, version)
		r, e := Open(path)
		if e != nil {
			t.Fatal(e)
		}
		_ = r.Close()
		if v := userVersion(t, path); v != 2 {
			t.Fatalf("version %d migrated to %d", version, v)
		}
		want := []string{"owner/one", "owner/two"}
		if got := pickedIdentities(t, path); !slices.Equal(got, want) {
			t.Fatalf("version %d picked %v", version, got)
		}
		// A second open changes nothing: an unpicked repository stays so.
		r, e = Open(path)
		if e != nil {
			t.Fatal(e)
		}
		list, e := r.Repositories(ctx)
		_ = r.Close()
		if e != nil || !slices.Equal(identities(list), want) {
			t.Fatalf("repositories %v %v", list, e)
		}
		if got := pickedIdentities(t, path); !slices.Equal(got, want) {
			t.Fatalf("reopen picked %v", got)
		}
	}
}

// Version 0 stored automatic=false for every repository; the backfill must
// not undo the version 1 step.
func TestMigrationFromVersionZeroKeepsAutomaticMigration(t *testing.T) {
	path := version1Database(t, 0)
	db, e := sql.Open("sqlite", path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`UPDATE repositories SET settings='{"automatic":false}' WHERE identity='owner/one'`); e != nil {
		t.Fatal(e)
	}
	_ = db.Close()
	r, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close() }()
	s, ok, e := r.RepositorySettings(context.Background(), "owner/one")
	if e != nil || !ok || s.Automatic != nil {
		t.Fatalf("%#v %v %v", s, ok, e)
	}
}

func TestOpenReadOnlyToleratesAMissingPickedTable(t *testing.T) {
	ctx := context.Background()
	path := version1Database(t, 1)
	r, e := OpenReadOnly(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close() }()
	list, e := r.Repositories(ctx)
	if e != nil || !slices.Equal(identities(list), []string{"owner/one", "owner/two"}) {
		t.Fatalf("%v %v", list, e)
	}
	// A version 1 explicit false is an explicit off, not the old default.
	for _, v := range list {
		if len(v.Clones) != 1 || !v.Clones[0].Available || !explicitlyOn(v.Settings.Automatic) {
			t.Fatalf("%#v", v)
		}
	}
	if _, ok, e := r.RepositorySettings(ctx, "owner/one"); e != nil || !ok {
		t.Fatalf("registered %v %v", ok, e)
	}
	if _, ok, e := r.RepositorySettings(ctx, "owner/orphan"); e != nil || ok {
		t.Fatalf("orphan registered %v %v", ok, e)
	}
	if v := userVersion(t, path); v != 1 {
		t.Fatalf("read-only open migrated to %d", v)
	}
}

func TestPickWithoutAClone(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	v, e := r.Pick(ctx, "Owner/Repo", &Settings{Automatic: new(false)})
	if e != nil || v.Repository != "owner/repo" || len(v.Clones) != 0 || v.Clones == nil {
		t.Fatalf("%#v %v", v, e)
	}
	// Picking again keeps the settings unless new ones are given.
	if v, e = r.Pick(ctx, "owner/repo", nil); e != nil || v.Settings.Automatic == nil || *v.Settings.Automatic {
		t.Fatalf("%#v %v", v, e)
	}
	list, e := r.Repositories(ctx)
	if e != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, e)
	}
	data, _ := json.Marshal(list)
	if !strings.Contains(string(data), `"clones":[]`) {
		t.Fatal(string(data))
	}
	s, ok, e := r.RepositorySettings(ctx, "OWNER/repo")
	if e != nil || !ok || s.Automatic == nil || *s.Automatic {
		t.Fatalf("%#v %v %v", s, ok, e)
	}
	if clones, e := r.List(ctx); e != nil || len(clones) != 0 {
		t.Fatalf("clones %v %v", clones, e)
	}
	if e = r.UpdateRepository(ctx, "owner/repo", Settings{Automatic: new(true)}); e != nil {
		t.Fatal(e)
	}
	if s, _, _ = r.RepositorySettings(ctx, "owner/repo"); !explicitlyOn(s.Automatic) {
		t.Fatalf("%#v", s)
	}
	if e = r.UpdateRepository(ctx, "owner/other", Settings{}); !errors.Is(e, ErrNotFound) {
		t.Fatalf("unpicked update %v", e)
	}
	if e = r.UpdateRepository(ctx, "owner/repo", Settings{Model: new("")}); e == nil {
		t.Fatal("invalid settings accepted")
	}
	if _, e = r.Pick(ctx, "owner/bad", &Settings{Model: new("")}); e == nil {
		t.Fatal("invalid settings accepted")
	}
	if list, _ = r.Repositories(ctx); len(list) != 1 {
		t.Fatalf("failed pick registered %v", list)
	}
}

func TestPickRejectsInvalidNames(t *testing.T) {
	r := registry(t)
	for _, name := range []string{"", "a", "a/b/c", "../x", "owner/..", "git.example.com/owner/repo", "ow ner/repo"} {
		if _, e := r.Pick(context.Background(), name, nil); !errors.Is(e, ErrInvalidRepository) {
			t.Errorf("%q: %v", name, e)
		}
	}
	if list, _ := r.Repositories(context.Background()); len(list) != 0 {
		t.Fatal(list)
	}
}

func TestRemoveKeepsPickedAndUnpickRemovesClones(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	c, e := r.Add(ctx, clone(t, "https://github.com/owner/repo"), &Settings{DailyCap: new(2.0)})
	if e != nil {
		t.Fatal(e)
	}
	list, e := r.Repositories(ctx)
	if e != nil || len(list) != 1 || len(list[0].Clones) != 1 || !list[0].Clones[0].Available {
		t.Fatalf("%v %v", list, e)
	}
	if e = r.Remove(ctx, c.ID); e != nil {
		t.Fatal(e)
	}
	list, e = r.Repositories(ctx)
	if e != nil || len(list) != 1 || len(list[0].Clones) != 0 {
		t.Fatalf("remove unpicked %v %v", list, e)
	}
	if _, ok, _ := r.RepositorySettings(ctx, "owner/repo"); !ok {
		t.Fatal("not registered after remove")
	}
	if _, e = r.Add(ctx, c.Path, nil); e != nil {
		t.Fatal(e)
	}
	if e = r.Unpick(ctx, "Owner/Repo"); e != nil {
		t.Fatal(e)
	}
	if list, _ = r.Repositories(ctx); len(list) != 0 {
		t.Fatal(list)
	}
	if clones, _ := r.List(ctx); len(clones) != 0 {
		t.Fatal(clones)
	}
	if _, ok, _ := r.RepositorySettings(ctx, "owner/repo"); ok {
		t.Fatal("registered after unpick")
	}
	if e = r.Unpick(ctx, "owner/repo"); !errors.Is(e, ErrNotFound) {
		t.Fatalf("second unpick %v", e)
	}
	if e = r.Unpick(ctx, "owner/unknown"); !errors.Is(e, ErrNotFound) {
		t.Fatalf("unknown %v", e)
	}
	// The settings survive a re-pick.
	v, e := r.Pick(ctx, "owner/repo", nil)
	if e != nil || v.Settings.DailyCap == nil || *v.Settings.DailyCap != 2 {
		t.Fatalf("%#v %v", v, e)
	}
}

// An older baum inserts rows without column lists and knows no picked table.
func TestOlderBinaryRowsArePicked(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "repos.sqlite")
	r, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close() }()
	c, e := inspect(ctx, clone(t, "https://github.com/owner/legacy"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.db.Exec(`INSERT INTO repositories VALUES (?,?) ON CONFLICT DO NOTHING`, c.Repository, `{"automatic":null}`); e != nil {
		t.Fatal(e)
	}
	if _, e = r.db.Exec(`INSERT INTO clones VALUES (?,?,?,?) ON CONFLICT(path) DO UPDATE SET common_dir=excluded.common_dir,repository=excluded.repository`, c.ID, c.Path, c.CommonDir, c.Repository); e != nil {
		t.Fatal(e)
	}
	list, e := r.Repositories(ctx)
	if e != nil || len(list) != 1 || list[0].Repository != "owner/legacy" || len(list[0].Clones) != 1 {
		t.Fatalf("%v %v", list, e)
	}
	if _, ok, _ := r.RepositorySettings(ctx, "owner/legacy"); !ok {
		t.Fatal("not registered")
	}
	// Removing the clone keeps such a repository picked too.
	if e = r.Remove(ctx, c.ID); e != nil {
		t.Fatal(e)
	}
	if list, _ = r.Repositories(ctx); len(list) != 1 || len(list[0].Clones) != 0 {
		t.Fatalf("%v", list)
	}
}

func TestRepositoriesHTTP(t *testing.T) {
	r := registry(t)
	h := r.Handler()
	request := func(method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	if code, body := request(http.MethodGet, "/api/repositories", ""); code != 200 || body != "[]" {
		t.Fatal(code, body)
	}
	for _, body := range []string{`{"repository":"a"}`, `{"repository":"a/b/c"}`, `{"repository":""}`, `{}`, `{"repository":"owner/repo","unknown":1}`, `{"repository":"owner/repo","settings":{"daily_cap":-1}}`, `{"repository":"owner/repo"} {}`} {
		if code, _ := request("POST", "/api/repositories", body); code != 400 {
			t.Fatal(body, code)
		}
	}
	code, body := request("POST", "/api/repositories", `{"repository":"Owner/Repo","settings":null}`)
	if code != 200 || body != `{"repository":"owner/repo","settings":{"automatic":null},"clones":[]}` {
		t.Fatal(code, body)
	}
	if code, body = request(http.MethodGet, "/api/repositories", ""); code != 200 || !strings.HasPrefix(body, `[{"repository":"owner/repo"`) || !strings.Contains(body, `"clones":[]`) {
		t.Fatal(code, body)
	}
	for _, body := range []string{`{"automatic":true}`, `{"daily_cap":null}`, `{"unknown":1,"automatic":null,"daily_cap":null}`, `{"automatic":null,"daily_cap":-1}`} {
		if code, _ = request("PUT", "/api/repositories/owner/repo/settings", body); code != 400 {
			t.Fatal(body, code)
		}
	}
	if code, _ = request("PUT", "/api/repositories/owner/repo/settings", `{"automatic":false,"daily_cap":1.5}`); code != 200 {
		t.Fatal(code)
	}
	if s, ok, _ := r.RepositorySettings(context.Background(), "owner/repo"); !ok || s.Automatic == nil || *s.Automatic || s.DailyCap == nil {
		t.Fatalf("%#v %v", s, ok)
	}
	if code, _ = request("PUT", "/api/repositories/owner/unknown/settings", `{"automatic":null,"daily_cap":null}`); code != 404 {
		t.Fatal(code)
	}
	if code, _ = request("DELETE", "/api/repositories/owner/repo", ""); code != 204 {
		t.Fatal(code)
	}
	if code, _ = request("DELETE", "/api/repositories/owner/repo", ""); code != 404 {
		t.Fatal(code)
	}
}
