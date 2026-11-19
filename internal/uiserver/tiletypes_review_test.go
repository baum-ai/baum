package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/agentcontext"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

// savedTypeChange is a type written as the built-in schema_change type is,
// under another name, with its synthetic samples; note is added to the fill
// instructions, so a different note is a different version.
func savedTypeChange(t testing.TB, name, note string) tilelibrary.Change {
	t.Helper()
	entry, _ := library.Get("schema_change")
	def := entry.Definition
	def.Name, def.Title, def.Fill = name, "Saved "+name, def.Fill+note
	encoded, err := library.Seal(&def)
	if err != nil {
		t.Fatal(err)
	}
	c := tilelibrary.Change{Definition: encoded, Glance: entry.Glance, ReviewChecks: entry.ReviewChecks, AppliesWith: entry.AppliesWith,
		Paths: []string{"**/*.sql"}, Origin: tilelibrary.OriginChat}
	for _, s := range entry.Samples {
		if tilelibrary.IsSynthetic(s) && len(c.Samples) < 1 {
			c.Samples = append(c.Samples, s)
		}
	}
	if len(c.Samples) == 0 {
		t.Fatal("no synthetic samples")
	}
	return c
}

// A review stored with a tile of a saved type is served byte for byte the
// same after the type is edited, renamed, deleted and purged: the stored
// composition embeds the definition it was made with. Each answer comes from
// a fresh handler, so none is a cached one.
func TestEditingAndDeletingATypeKeepsAStoredReviewsTiles(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	lib := &tilelibrary.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite"), Now: func() time.Time { return now }}
	var api LocalAPI
	f := newTilesFixture(t, func(a *LocalAPI) {
		a.TileTypes = lib
		api = *a
	})
	saved, err := lib.Create(ctx, savedTypeChange(t, "migrations", ""), nil)
	if err != nil {
		t.Fatal(err)
	}

	// The review: a document whose composition has a library tile of the
	// saved type, with data citing one of the document's hunks.
	document := withVersion(t, tileviewtest.Encode(t, tileviewtest.Base(t)), 1)
	normalized, err := review.NormalizeStored(document)
	if err != nil {
		t.Fatal(err)
	}
	r, err := view.ParseReview(normalized)
	if err != nil {
		t.Fatal(err)
	}
	var hunk string
	for _, s := range r.Sections {
		if len(s.Hunks) > 0 {
			hunk = s.Hunks[0]
			break
		}
	}
	if hunk == "" {
		t.Fatal("no hunk")
	}
	data := `{"engine":"sqlite","migrations":[],"tables":[{"name":"widgets","change":"created","renamed_from":null,"columns":[],"indexes":[],"cites":[{"type":"hunk","id":"` + hunk + `"}]}],"data_changes":[],"readers":[]}`
	entry, err := lib.Entry(ctx, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	composition, err := json.Marshal(view.Composition{Schema: view.CompositionName, Library: []tiles.LibraryTile{tilelibrary.LibraryTile(entry, json.RawMessage(data))}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.versions.Put(ctx, f.key, store.Version{}, store.Version{Metadata: store.Metadata{Version: 1}, Document: document, Composition: composition}); err != nil {
		t.Fatal(err)
	}

	serve := func() string {
		handler := api.Handler()
		defer handler.Close()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "http://local/api/tiles?clone="+f.clone.ID+"&pr=42", nil))
		if w.Code != 200 {
			t.Fatalf("status %d: %.300s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	before := serve()
	if !strings.Contains(before, saved.Entry.Definition.Digest) || !strings.Contains(before, `"state":"ready"`) {
		t.Fatalf("the saved type's tile is not served ready: %.400s", before)
	}

	now = now.Add(time.Hour)
	edited, err := lib.Update(ctx, savedTypeChange(t, "migrations", " Changed."), nil)
	if err != nil || edited.Entry.Definition.Digest == saved.Entry.Definition.Digest {
		t.Fatalf("edit: %v", err)
	}
	if _, err := lib.Rename(ctx, "migrations", "Renamed"); err != nil {
		t.Fatal(err)
	}
	if after := serve(); after != before {
		t.Errorf("the served tiles changed after an edit and a rename:\n%.400s\n%.400s", before, after)
	}
	if err := lib.Delete(ctx, "migrations"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(40 * 24 * time.Hour)
	if err := lib.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Get(ctx, "migrations"); !errors.Is(err, tilelibrary.ErrUnknown) {
		t.Fatal("the type is still there")
	}
	if after := serve(); after != before {
		t.Errorf("the served tiles changed after a delete and a purge:\n%.400s\n%.400s", before, after)
	}
}

// onePass is a render check, with the id chk_ok, that passed on every sample
// of a change.
type onePass struct{ result tilelibrary.CheckResult }

func (o onePass) Result(context.Context, string) (tilelibrary.CheckResult, bool) {
	return o.result, true
}

func passOf(t testing.TB, c tilelibrary.Change) onePass {
	t.Helper()
	var def tiles.TypeDefinition
	if err := json.Unmarshal(c.Definition, &def); err != nil {
		t.Fatal(err)
	}
	r := tilelibrary.CheckResult{ID: "chk_ok", Verdict: tilelibrary.VerdictPass, Digest: def.Digest, Definition: c.Definition}
	for _, s := range c.Samples {
		r.Samples = append(r.Samples, tilelibrary.CheckedSample{Sample: s, Passed: true, Synthetic: true})
	}
	return onePass{r}
}

// A proposal made from a version of a type is stale once the person saved
// another, even when it is recorded after: its texts started from the old one
// and would silently revert the new one. The agent's version is refused
// when it is recorded and when it is saved.
func TestProposalBasedOnAnOlderVersionIsStale(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	lib := &tilelibrary.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite"), Now: func() time.Time { return now }}
	if _, err := lib.Create(ctx, savedTypeChange(t, "env_vars", ""), nil); err != nil {
		t.Fatal(err)
	}
	proposals := TileTypes{Store: lib, Checks: passOf(t, savedTypeChange(t, "env_vars", " Agent."))}
	propose := func() json.RawMessage {
		_, after, err := proposals.Propose(ctx, agentcontext.TileTypeRequest{FromName: "env_vars", Check: "chk_ok"})
		if err != nil {
			t.Fatal(err)
		}
		return after
	}
	target, err := (&Proposers{TileTypes: lib}).tileTypeTarget("env_vars")
	if err != nil {
		t.Fatal(err)
	}
	unchanged := func(any) error { return nil }

	// Nothing changed: the proposal is recorded and saved.
	if err := target.validate(ctx, propose()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// The person saves new texts after the agent read the type, and before
	// the proposal is recorded.
	after := propose()
	now = now.Add(time.Minute)
	person := savedTypeChange(t, "env_vars", " Person.")
	person.Glance = "The person's glance."
	if _, err := lib.Update(ctx, person, nil); err != nil {
		t.Fatal(err)
	}
	if err := target.validate(ctx, after); !errors.Is(err, agentcontext.ErrStale) {
		t.Fatalf("validate: %v", err)
	}
	// The write refuses it too, even when the revision it was read at matches.
	if err := target.apply(ctx, after, unchanged, agentcontext.SaveInput{}); !errors.Is(err, agentcontext.ErrStale) {
		t.Fatalf("apply: %v", err)
	}
	got, _ := lib.Get(ctx, "env_vars")
	if got.Entry.Glance != "The person's glance." || !strings.Contains(got.Entry.Definition.Fill, "Person.") || len(got.Versions) != 2 {
		t.Errorf("the person's texts were reverted: %q %d versions", got.Entry.Glance, len(got.Versions))
	}
	// A proposal made now is based on the new version and saves.
	if err := target.apply(ctx, propose(), unchanged, agentcontext.SaveInput{}); err != nil {
		t.Fatal(err)
	}
	// Texts that are not the definition's are not a proposal.
	var card map[string]any
	if err := json.Unmarshal(propose(), &card); err != nil {
		t.Fatal(err)
	}
	card["fill"] = "Other instructions than the definition has."
	forged, _ := json.Marshal(card)
	if err := target.validate(ctx, forged); err == nil {
		t.Error("a card whose fill is not its definition's was taken")
	}
}
