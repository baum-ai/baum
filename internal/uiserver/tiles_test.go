package uiserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/tiles/view"
)

// tilesFixture is a registered clone with a saved review of acme/widgets#42.
type tilesFixture struct {
	handler  *LocalHandler
	clone    repos.Clone
	versions *store.Store
	key      store.Key
}

func newTilesFixture(t *testing.T, options ...func(*LocalAPI)) *tilesFixture {
	t.Helper()
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	dir := appdbtest.TempDir(t)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", "https://github.com/acme/widgets"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(out), err)
		}
	}
	clone, err := registry.Add(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := store.Open(clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	api := LocalAPI{Registry: registry, Config: config.Default()}
	for _, option := range options {
		option(&api)
	}
	handler := api.Handler()
	t.Cleanup(handler.Close)
	return &tilesFixture{handler: handler, clone: clone, versions: versions, key: store.Key{Repository: "acme/widgets", Number: 42}}
}

// put saves doc as the next version of the pull request.
func (f *tilesFixture) put(t *testing.T, doc []byte) {
	t.Helper()
	ctx := context.Background()
	var previous store.Version
	if latest, err := f.versions.Latest(ctx, f.key); err == nil {
		previous = latest
	}
	n := previous.Metadata.Version + 1
	next := store.Version{Metadata: store.Metadata{Version: n}, Document: withVersion(t, doc, n)}
	if err := f.versions.Put(ctx, f.key, previous, next); err != nil {
		t.Fatal(err)
	}
}

// withVersion names the version a document says it is, as a stored one does.
func withVersion(t *testing.T, doc []byte, n int) []byte {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(doc, &value); err != nil {
		t.Fatal(err)
	}
	value["review"].(map[string]any)["version"] = n
	delete(value["review"].(map[string]any), "previous_version")
	return tileviewtest.Encode(t, value)
}

// putComposed saves doc as the next version with a review view composition
// (tileviewtest.ViewComposition).
func (f *tilesFixture) putComposed(t *testing.T, doc []byte) {
	t.Helper()
	ctx := context.Background()
	var previous store.Version
	if latest, err := f.versions.Latest(ctx, f.key); err == nil {
		previous = latest
	}
	n := previous.Metadata.Version + 1
	document := withVersion(t, doc, n)
	next := store.Version{Metadata: store.Metadata{Version: n}, Document: document, Composition: tileviewtest.ViewComposition(t)}
	if err := f.versions.Put(ctx, f.key, previous, next); err != nil {
		t.Fatal(err)
	}
}

func (f *tilesFixture) get(path, query, etag string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://local"+path+"?clone="+f.clone.ID+"&pr=42"+query, nil)
	if etag != "" {
		r.Header.Set("If-None-Match", etag)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

type tilesBody struct {
	Document map[string]any `json:"document"`
	View     *view.View     `json:"view"`
	Problem  string         `json:"problem"`
}

func decodeTiles(t *testing.T, w *httptest.ResponseRecorder) tilesBody {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body tilesBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func titled(t *testing.T, title string) []byte {
	t.Helper()
	doc := tileviewtest.Base(t)
	doc["summary"].(map[string]any)["title"] = title
	return tileviewtest.Encode(t, doc)
}

func TestTilesServesTheLatestAndAChosenVersion(t *testing.T) {
	f := newTilesFixture(t)
	f.put(t, titled(t, "First title"))
	f.put(t, titled(t, "Second title"))

	latest := f.get("/api/tiles", "", "")
	body := decodeTiles(t, latest)
	if latest.Header().Get("Cache-Control") != "no-store" || latest.Header().Get("ETag") == "" || latest.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("headers %v", latest.Header())
	}
	if body.View == nil || body.View.Schema != view.Name || body.Problem != "" {
		t.Fatalf("latest: %+v", body)
	}
	if got := body.Document["summary"].(map[string]any)["title"]; got != "Second title" {
		t.Fatalf("latest title %v", got)
	}

	first := decodeTiles(t, f.get("/api/tiles", "&version=1", ""))
	if first.Document["summary"].(map[string]any)["title"] != "First title" || first.View == nil {
		t.Fatalf("version 1: %+v", first.Document["summary"])
	}
	if w := f.get("/api/tiles", "&version=2", ""); w.Body.String() != latest.Body.String() {
		t.Fatal("version 2 differs from the latest")
	}
}

// The document of /api/tiles is the document /api/document serves, so the
// page's chrome and its tiles agree.
func TestTilesDocumentIsTheDocumentRoute(t *testing.T) {
	f := newTilesFixture(t)
	f.put(t, titled(t, "Same bytes"))
	var fromDocument, fromTiles any
	if err := json.Unmarshal(f.get("/api/document", "", "").Body.Bytes(), &fromDocument); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(f.get("/api/tiles", "", "").Body.Bytes(), &struct {
		Document *any `json:"document"`
	}{&fromTiles}); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(fromDocument)
	b, _ := json.Marshal(fromTiles)
	if string(a) != string(b) {
		t.Fatalf("/api/document and /api/tiles disagree:\n%s\n%s", a, b)
	}
}

func TestTilesRefusesWhatDocumentRefuses(t *testing.T) {
	f := newTilesFixture(t)
	// No review yet, an unknown clone: the same answers as /api/document.
	if w, d := f.get("/api/tiles", "", ""), f.get("/api/document", "", ""); w.Code != 404 || d.Code != 404 {
		t.Errorf("no review: tiles %d, document %d, want 404", w.Code, d.Code)
	}
	for _, path := range []string{"/api/tiles", "/api/document"} {
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, httptest.NewRequest("GET", "http://local"+path+"?clone=nope&pr=42", nil))
		if w.Code != 404 {
			t.Errorf("%s of an unknown clone: %d", path, w.Code)
		}
	}
	f.put(t, titled(t, "One"))
	for _, query := range []string{"&version=2", "&version=9"} {
		if w := f.get("/api/tiles", query, ""); w.Code != 404 || f.get("/api/document", query, "").Code != 404 {
			t.Errorf("%s: %d", query, w.Code)
		}
	}
	if w := f.get("/api/tiles", "&version=-1", ""); w.Code != 400 {
		t.Errorf("negative version: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "http://local/api/tiles", nil)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Errorf("no selector: %d", w.Code)
	}
}

func TestTilesConversionFailureLeavesTheViewOut(t *testing.T) {
	f := newTilesFixture(t)
	const document = `{"review":{"repository":"acme/widgets","pull_request":42,"version":1,"head":"aaaaaaaa","base":"bbbbbbbb"},"notes":"PRIVATE_TEXT"}`
	f.put(t, []byte(document))
	w := f.get("/api/tiles", "", "")
	body := decodeTiles(t, w)
	if body.View != nil || body.Problem == "" || strings.Contains(body.Problem, "PRIVATE_TEXT") {
		t.Fatalf("view %v, problem %q", body.View != nil, body.Problem)
	}
	if strings.Contains(w.Body.String(), `"view"`) || body.Document["notes"] != "PRIVATE_TEXT" {
		t.Fatalf("body %s", w.Body.String())
	}
}

func TestTilesAnswersConditionalRequestsAndFollowsRewrites(t *testing.T) {
	ctx := context.Background()
	f := newTilesFixture(t)
	f.put(t, titled(t, "Before"))
	first := f.get("/api/tiles", "", "")
	etag := first.Header().Get("ETag")
	for range 2 {
		w := f.get("/api/tiles", "", etag)
		if w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("ETag") != etag || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unchanged: %d %q %v", w.Code, w.Body.String(), w.Header())
		}
	}
	// A rewrite of the version in place is never answered from the old one.
	previous, err := f.versions.Latest(ctx, f.key)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := tileviewtest.Base(t)
	rewritten["summary"].(map[string]any)["title"] = "After"
	rewritten["review"].(map[string]any)["evidence_refreshed_at"] = "2026-11-02T10:00:00Z"
	if err := f.versions.RefreshCurrent(ctx, f.key, previous, withVersion(t, tileviewtest.Encode(t, rewritten), 1)); err != nil {
		t.Fatal(err)
	}
	w := f.get("/api/tiles", "", etag)
	if w.Code != 200 || w.Header().Get("ETag") == etag {
		t.Fatalf("rewritten: %d %q", w.Code, w.Header().Get("ETag"))
	}
	if got := decodeTiles(t, w).Document["review"].(map[string]any)["evidence_refreshed_at"]; got != "2026-11-02T10:00:00Z" {
		t.Fatalf("served %v", got)
	}
}

// A stored-document rule changes the document beside the tiles: an identifier
// longer than 128 characters is shortened and a blank section title is
// labelled there, while /api/document serves the stored bytes (with hunk URLs).
func TestTilesDocumentIsNormalisedWhereDocumentIsNot(t *testing.T) {
	f := newTilesFixture(t)
	long := "sec_" + strings.Repeat("a", 140)
	raw := strings.ReplaceAll(string(tileviewtest.Encode(t, tileviewtest.Base(t))), "sec_config_flag", long)
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	for _, s := range doc["sections"].([]any) {
		if s := s.(map[string]any); s["id"] == long {
			s["title"] = "  "
		}
	}
	f.put(t, tileviewtest.Encode(t, doc))

	sectionIDs := func(document map[string]any) (ids []string, blank int) {
		for _, s := range document["sections"].([]any) {
			s := s.(map[string]any)
			ids = append(ids, s["id"].(string))
			if strings.TrimSpace(s["title"].(string)) == "" {
				blank++
			}
		}
		return ids, blank
	}
	var plain map[string]any
	if err := json.Unmarshal(f.get("/api/document", "", "").Body.Bytes(), &plain); err != nil {
		t.Fatal(err)
	}
	plainIDs, plainBlank := sectionIDs(plain)
	body := decodeTiles(t, f.get("/api/tiles", "", ""))
	tileIDs, tileBlank := sectionIDs(body.Document)
	if !slices.Contains(plainIDs, long) || plainBlank != 1 {
		t.Fatalf("the plain route changed the stored document: %v, %d blank", plainIDs, plainBlank)
	}
	if slices.Contains(tileIDs, long) || tileBlank != 0 {
		t.Fatalf("the tiles route's document is not normalised: %v, %d blank", tileIDs, tileBlank)
	}
	for _, id := range tileIDs {
		if len(id) > 128 {
			t.Errorf("identifier of %d characters", len(id))
		}
	}
	if body.View == nil || body.Problem != "" {
		t.Fatalf("view %v, problem %q", body.View != nil, body.Problem)
	}
}

// A version asked for again is answered from the kept answer, not converted
// again; a rewrite of it is converted.
func TestTilesConvertsAnUnchangedVersionOnce(t *testing.T) {
	ctx := context.Background()
	f := newTilesFixture(t)
	f.put(t, titled(t, "Once"))
	before := tileview.Builds.Load()
	for range 3 {
		if w := f.get("/api/tiles", "", ""); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if got := tileview.Builds.Load() - before; got != 1 {
		t.Fatalf("%d conversions of an unchanged version", got)
	}
	previous, err := f.versions.Latest(ctx, f.key)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := tileviewtest.Base(t)
	rewritten["summary"].(map[string]any)["title"] = "Rewritten"
	if err := f.versions.RefreshCurrent(ctx, f.key, previous, withVersion(t, tileviewtest.Encode(t, rewritten), 1)); err != nil {
		t.Fatal(err)
	}
	f.get("/api/tiles", "", "")
	f.put(t, titled(t, "Twice"))
	f.get("/api/tiles", "", "")
	if got := tileview.Builds.Load() - before; got != 3 {
		t.Fatalf("%d conversions after a rewrite and a new version", got)
	}
}

// A version's stored composition is applied to the view served for it, the
// document served beside it is unchanged, and a version without one is
// served the empty view.
func TestTilesServesTheStoredComposition(t *testing.T) {
	f := newTilesFixture(t)
	f.put(t, titled(t, "Plain"))
	plain := decodeTiles(t, f.get("/api/tiles", "", ""))
	f.putComposed(t, titled(t, "Plain"))
	composed := decodeTiles(t, f.get("/api/tiles", "", ""))
	if composed.Problem != "" || composed.View == nil || plain.View == nil {
		t.Fatalf("problem %q", composed.Problem)
	}
	if composed.View.Overview.RiskSummary != tileviewtest.RiskSummary || plain.View.Overview.RiskSummary != "" {
		t.Fatalf("risk summaries %q and %q", composed.View.Overview.RiskSummary, plain.View.Overview.RiskSummary)
	}
	// Version 1 still has no composition; asking for it by number is the
	// empty view, byte for byte.
	one := f.get("/api/tiles", "&version=1", "")
	again := f.get("/api/tiles", "&version=1", "")
	if one.Code != 200 || one.Body.String() != again.Body.String() || decodeTiles(t, one).View.Overview.RiskSummary != "" {
		t.Error("version 1 is not served the empty view")
	}
	// The same document with and without a composition is answered with
	// another body and ETag: two clones hold it as version 1, one with a
	// composition.
	other, composedOne := newTilesFixture(t), newTilesFixture(t)
	other.put(t, titled(t, "Plain"))
	composedOne.putComposed(t, titled(t, "Plain"))
	same := composedOne.get("/api/tiles", "", "")
	without := other.get("/api/tiles", "", "")
	if same.Code != 200 || without.Code != 200 || same.Header().Get("ETag") == without.Header().Get("ETag") || same.Body.String() == without.Body.String() {
		t.Error("a composition does not change the answer or its ETag")
	}
}

// A composition the daemon cannot use is served as no composition, with no
// problem for the reader.
func TestTilesIgnoresAnUnusableStoredComposition(t *testing.T) {
	f := newTilesFixture(t)
	f.put(t, titled(t, "Plain"))
	want := f.get("/api/tiles", "", "").Body.String()
	db, err := sql.Open("sqlite", filepath.Join(f.versions.Root(), "versions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE versions SET composition='{"schema":"baum.composition/9"}'`); err != nil {
		t.Fatal(err)
	}
	// The write renews the version's revision, so the answer is built again.
	before := tileview.Builds.Load()
	got := f.get("/api/tiles", "", "")
	if tileview.Builds.Load() != before+1 {
		t.Error("the answer was not built again")
	}
	if got.Body.String() != want {
		t.Error("an unusable composition changed the answer")
	}
}
