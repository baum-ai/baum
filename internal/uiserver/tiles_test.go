package uiserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/etagjson"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/tiles"
)

// tilesFixture is a registered clone with a saved review of acme/widgets#42.
type tilesFixture struct {
	handler  *LocalHandler
	clone    repos.Clone
	versions *store.Store
	key      store.Key
}

func newTilesFixture(t *testing.T) *tilesFixture {
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
	handler := LocalAPI{Registry: registry, Config: config.Default()}.Handler()
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
	Tiles    *tiles.Document
	Problem  string `json:"problem"`
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

func tileState(doc *tiles.Document, id string) string {
	for _, tile := range doc.Tiles {
		if tile.ID == id {
			return tile.State
		}
	}
	return "missing"
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
	if body.Tiles == nil || body.Tiles.Schema != "baum.tiles/1" || body.Problem != "" {
		t.Fatalf("latest: %+v", body)
	}
	if got := body.Document["summary"].(map[string]any)["title"]; got != "Second title" {
		t.Fatalf("latest title %v", got)
	}
	if got := body.Tiles.Facts.Title; got != "Second title" {
		t.Fatalf("tile document title %q", got)
	}
	if got := tileState(body.Tiles, "overview"); got != tiles.StateReady {
		t.Fatalf("overview %s", got)
	}

	first := decodeTiles(t, f.get("/api/tiles", "&version=1", ""))
	if first.Document["summary"].(map[string]any)["title"] != "First title" || first.Tiles.Facts.Title != "First title" {
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

func TestTilesConversionFailureLeavesTilesNull(t *testing.T) {
	f := newTilesFixture(t)
	const document = `{"review":{"repository":"acme/widgets","pull_request":42,"version":1,"head":"aaaaaaaa","base":"bbbbbbbb"},"notes":"PRIVATE_TEXT"}`
	f.put(t, []byte(document))
	w := f.get("/api/tiles", "", "")
	body := decodeTiles(t, w)
	if body.Tiles != nil || body.Problem == "" || strings.Contains(body.Problem, "PRIVATE_TEXT") {
		t.Fatalf("tiles %v, problem %q", body.Tiles != nil, body.Problem)
	}
	if !strings.Contains(w.Body.String(), `"tiles":null`) || body.Document["notes"] != "PRIVATE_TEXT" {
		t.Fatalf("body %s", w.Body.String())
	}
}

// A tile whose data does not have its type's shape is marked unavailable and
// the response is still served, with the other tiles ready.
func TestTilesUnavailableTileDoesNotFailTheResponse(t *testing.T) {
	f := newTilesFixture(t)
	doc := tileviewtest.Base(t)
	doc["summary"].(map[string]any)["change_kind"] = "not-a-kind"
	f.put(t, tileviewtest.Encode(t, doc))
	body := decodeTiles(t, f.get("/api/tiles", "", ""))
	if body.Tiles == nil || body.Problem != "" {
		t.Fatalf("%+v", body)
	}
	if got := tileState(body.Tiles, "overview"); got != tiles.StateUnavailable {
		t.Fatalf("overview is %s", got)
	}
	if got := tileState(body.Tiles, "risks"); got != tiles.StateReady {
		t.Fatalf("risks is %s", got)
	}
	for _, tile := range body.Tiles.Tiles {
		if tile.ID == "overview" && len(tile.Problems) == 0 {
			t.Error("the unavailable tile names no problem")
		}
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

func TestTileCacheKeepsTheLeastRecentlyUsedOut(t *testing.T) {
	var c tileCache
	for i := range maxTileAnswers + 3 {
		c.put(string(rune('a'+i)), etagjsonBody("x"))
	}
	if _, ok := c.get("a"); ok {
		t.Error("the oldest answer was kept")
	}
	if _, ok := c.get(string(rune('a' + maxTileAnswers + 2))); !ok {
		t.Error("the newest answer was dropped")
	}
	if _, ok := c.get(""); ok {
		t.Error("an answer without a revision was kept")
	}
	c.put("", etagjsonBody("x"))
	if len(c.answers) != maxTileAnswers {
		t.Errorf("%d answers", len(c.answers))
	}
}

func etagjsonBody(s string) etagjson.Body { return etagjson.Body{Data: []byte(s), ETag: `"` + s + `"`} }
