package replay_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/replay"
	"github.com/baum-ai/baum/schema"
)

// savedReviews writes a saved review store for the case's pull request at
// reviews/<machine>/repo.versions.sqlite: a version per document, numbered
// from 1, the one at matched carrying the case's bundle digest.
func savedReviews(t *testing.T, d *dataset, caseID, machine string, matched int, documents ...[]byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(d.dir, "cases", caseID, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(strings.TrimSuffix(string(raw), "\n")))
	var b struct {
		Repository  string `json:"repository"`
		PullRequest struct {
			Number int `json:"number"`
		} `json:"pull_request"`
	}
	_ = json.Unmarshal(raw, &b)
	path := filepath.Join(d.dir, "reviews", machine, "repo.versions.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE versions (repository TEXT NOT NULL, pr INTEGER NOT NULL, version INTEGER NOT NULL, metadata BLOB NOT NULL, document BLOB NOT NULL, PRIMARY KEY(repository,pr,version))`); err != nil {
		t.Fatal(err)
	}
	for i, document := range documents {
		digest := "other"
		if i+1 == matched {
			digest = hex.EncodeToString(sum[:])
		}
		meta, _ := json.Marshal(map[string]string{"bundle_digest": digest})
		if _, err := db.Exec(`INSERT INTO versions VALUES (?, ?, ?, ?, ?)`, b.Repository, b.PullRequest.Number, i+1, meta, document); err != nil {
			t.Fatal(err)
		}
	}
}

// A compose run over saved reviews shows the saved version it composed,
// from the store that holds it, with both layouts.
func TestScreenInputsOfAComposeRunOverSavedReviews(t *testing.T) {
	d := newDataset(t, assembleSource(t))
	d.addCase(t, "c1", nil)
	savedReviews(t, d, "c1", "elsewhere", 0, []byte(`{"v":1}`))
	savedReviews(t, d, "c1", "machine", 3, []byte(`{"v":1}`), []byte(`{"v":2}`), schema.Example)
	ds, _ := replay.OpenDataset(d.dir)
	backend := &completing{Backend: &generatetest.Backend{Version: "1.0.0"}}
	outDir := t.TempDir()
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: composeSettings(t, backend, map[string]string{"source": replay.ComposeFromStore, "dataset": d.dir})}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	cases, err := replay.ScreenInputs(ctx(), d.dir, filepath.Join(outDir, "run"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 1 {
		t.Fatalf("%+v", cases)
	}
	c := cases[0]
	if c.Problem != "" || !c.Saved || c.Version != 3 || c.Store != "machine/repo.versions.sqlite" || string(c.Document) != string(schema.Example) ||
		len(c.Composition) == 0 || len(c.Template) == 0 || c.Number == 0 {
		t.Fatalf("%+v", c)
	}
	if left, _ := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "baum-replay-reviews-*")); len(left) != 0 {
		t.Errorf("the copies of the stores were left: %v", left)
	}
}

// A compose run over an earlier run's documents shows those documents, with
// their history from a saved store; one whose history no store holds says
// so.
func TestScreenInputsOfAComposeRunOverAnEarlierRun(t *testing.T) {
	d := newDataset(t, assembleSource(t))
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	earlier := filepath.Join(outDir, "earlier", "cases", "c1")
	if err := os.MkdirAll(earlier, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(earlier, "document.json"), schema.Example, 0o644); err != nil {
		t.Fatal(err)
	}
	backend := &completing{Backend: &generatetest.Backend{Version: "1.0.0"}}
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: composeSettings(t, backend, map[string]string{"source": replay.ComposeFromRun, "run": filepath.Join(outDir, "earlier")})}); err != nil {
		t.Fatal(err)
	}
	cases, err := replay.ScreenInputs(ctx(), d.dir, filepath.Join(outDir, "run"))
	if err != nil || len(cases) != 1 || !strings.Contains(cases[0].Problem, "no saved review store holds versions 1 to 2") {
		t.Fatalf("%v %+v", err, cases)
	}
	savedReviews(t, d, "c1", "machine", 0, []byte(`{"v":1}`), []byte(`{"v":2}`))
	cases, err = replay.ScreenInputs(ctx(), d.dir, filepath.Join(outDir, "run"))
	if err != nil || len(cases) != 1 {
		t.Fatalf("%v %+v", err, cases)
	}
	c := cases[0]
	if c.Problem != "" || c.Saved || c.Version != 3 || c.Store != "machine/repo.versions.sqlite" || string(c.Document) != string(schema.Example) ||
		c.Metadata.Version != 3 || c.Metadata.Head == "" || len(c.Composition) == 0 || len(c.Template) == 0 {
		t.Fatalf("%+v", c)
	}
}

// A tiles run shows the review it generated, with the composition its
// compose stage stored and the template.
func TestScreenInputsOfATilesRun(t *testing.T) {
	d := newDataset(t, assembleSource(t))
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	backend := &completing{Backend: &generatetest.Backend{Version: "1.0.0", RunFunc: pricedReplies(nil)}}
	outDir := t.TempDir()
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: tilesSettings(t, backend, replay.GeneratorConfig{})}); err != nil {
		t.Fatal(err)
	}
	cases, err := replay.ScreenInputs(ctx(), d.dir, filepath.Join(outDir, "run"))
	if err != nil || len(cases) != 1 {
		t.Fatalf("%v %+v", err, cases)
	}
	c := cases[0]
	generated, _ := os.ReadFile(filepath.Join(outDir, "run", "cases", "c1", "document.json"))
	if c.Problem != "" || c.Saved || string(c.Document) != string(generated) || c.Metadata.Version != c.Version || c.Metadata.BundleDigest == "" ||
		len(c.Composition) == 0 || len(c.Template) == 0 {
		t.Fatalf("%+v", c)
	}
}

func TestScreenInputsRefusesARunWithoutLayouts(t *testing.T) {
	d := newDataset(t, assembleSource(t))
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: staged(&generatetest.Backend{Version: "1.0.0", RunFunc: pricedReplies(nil)})}); err != nil {
		t.Fatal(err)
	}
	if _, err := replay.ScreenInputs(ctx(), d.dir, filepath.Join(outDir, "run")); err == nil || !strings.Contains(err.Error(), "no layouts") {
		t.Fatalf("%v", err)
	}
}
