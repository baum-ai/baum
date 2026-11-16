package replay_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/replay"
	"github.com/baum-ai/baum/review"
)

func ptr[T any](v T) *T { return &v }

// bundleWith is the source bundle with its diff and title replaced.
func bundleWith(t *testing.T, src source, title string, hunks []bundle.Hunk) []byte {
	t.Helper()
	b := src.result.Bundle
	b.PullRequest.Title = title
	if hunks != nil {
		b.Diff = hunks
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

// addTitled adds a case whose bundle title is its id, so a fake backend can tell
// the cases apart.
func (d *dataset) addTitled(t *testing.T, id string, facts map[string]any) {
	t.Helper()
	d.addCaseJSON(t, id, facts, bundleWith(t, d.src, id, nil))
}

func TestSelectionByBundleFacts(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	schema := []bundle.Hunk{hunk("store/store.go", "+const q = `CREATE TABLE t (a int)`")}
	api := []bundle.Hunk{hunk("server/routes.go", `+	mux.HandleFunc("GET /x", h)`)}
	d.addCaseJSON(t, "c1", map[string]any{"attachments": 2}, bundleWith(t, src, "c1", nil))
	d.addCaseJSON(t, "c2", map[string]any{"threads": 3}, bundleWith(t, src, "c2", nil))
	d.addCaseJSON(t, "c3", map[string]any{"previous_version": 1}, bundleWith(t, src, "c3", nil))
	d.addCaseJSON(t, "c4", nil, bundleWith(t, src, "c4", schema))
	d.addCaseJSON(t, "c5", nil, bundleWith(t, src, "c5", api))
	d.addCaseJSON(t, "c6", nil, bundleWith(t, src, "c6", append(slices.Clone(schema), api...)))
	ds, err := replay.OpenDataset(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		sel  replay.Selection
		want []string
	}{
		{"images", replay.Selection{Images: ptr(true)}, []string{"c1"}},
		{"no images", replay.Selection{Images: ptr(false)}, []string{"c2", "c3", "c4", "c5", "c6"}},
		{"threads", replay.Selection{Threads: ptr(true)}, []string{"c2"}},
		{"previous", replay.Selection{Previous: ptr(true)}, []string{"c3"}},
		{"no previous", replay.Selection{Previous: ptr(false), Images: ptr(false), Threads: ptr(false)}, []string{"c4", "c5", "c6"}},
		{"schema", replay.Selection{Detector: "schema"}, []string{"c4", "c6"}},
		{"api", replay.Selection{Detector: "api"}, []string{"c5", "c6"}},
		{"any", replay.Selection{Detector: "any"}, []string{"c4", "c5", "c6"}},
		{"none", replay.Selection{Detector: "none"}, []string{"c1", "c2", "c3"}},
		{"combined with the index filters", replay.Selection{Detector: "any", Primary: true, Limit: 2}, []string{"c4", "c5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ds.Select(tc.sel)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Errorf("got %v (%v), want %v", got, err, tc.want)
			}
		})
	}
	if _, err := ds.Select(replay.Selection{Detector: "tables"}); err == nil {
		t.Error("an unknown detector is accepted")
	}
	if _, err := ds.Select(replay.Selection{IDs: []string{"c1"}, Images: ptr(true)}); err == nil {
		t.Error("named cases combine with a filter")
	}

	// A dry run says how many cases each combination leaves, with ids only.
	_, printed, err := execute(t, replay.Plan{Dataset: ds, DryRun: true, Selection: replay.Selection{Detector: "schema"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"6 case(s) pass the index filters", "images yes, threads no, previous no: 1", "images no, threads no, previous no: 3",
		"by detector (heuristics, not ground truth): schema 2, api 2, either 3, neither 3", "resting on text alone, no path rule: schema 2, api 2", "api:route 2, schema:ddl 2", "2 case(s) also pass", "c4\n", "c6\n"} {
		if !strings.Contains(printed, want) {
			t.Errorf("the dry run lacks %q:\n%s", want, printed)
		}
	}
}

func TestChainsAreGroupedAndOrdered(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	pr := func(number int, id string, extra map[string]any) {
		facts := map[string]any{"repository": "acme/demo", "number": number, "revision": id}
		for k, v := range extra {
			facts[k] = v
		}
		d.addTitled(t, id, facts)
	}
	// Pull request 7: the ids do not sort in revision order. Two revisions
	// carry the same previous version number, so capture time orders them.
	pr(7, "r-c", map[string]any{"previous_version": 2, "captured_at": "2026-01-01T00:00:00Z"})
	pr(7, "r-b", map[string]any{"previous_version": 1, "captured_at": "2026-01-03T00:00:00Z"})
	pr(7, "r-d", map[string]any{"previous_version": 1, "captured_at": "2026-01-02T00:00:00Z"})
	pr(7, "r-a", map[string]any{"captured_at": "2026-02-01T00:00:00Z"})
	// The same revision captured again: not primary.
	pr(7, "r-a2", map[string]any{"primary": false, "revision": "r-a"})
	// Pull request 8 has one revision, 9 two without any previous version.
	pr(8, "s-a", nil)
	pr(9, "t-b", map[string]any{"captured_at": "2026-03-02T00:00:00Z"})
	pr(9, "t-a", map[string]any{"captured_at": "2026-03-01T00:00:00Z"})
	ds, _ := replay.OpenDataset(d.dir)

	chains, err := ds.SelectChains(replay.Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if len(chains) != 2 || chains[0].Name() != "acme~demo-7" || !slices.Equal(chains[0].IDs, []string{"r-a", "r-d", "r-b", "r-c"}) || !slices.Equal(chains[1].IDs, []string{"t-a", "t-b"}) {
		t.Errorf("chains %+v", chains)
	}
	// A filter chooses pull requests that have a revision passing it, and all
	// of the pull request's primary revisions run.
	chains, _ = ds.SelectChains(replay.Selection{Previous: ptr(true)})
	if len(chains) != 1 || len(chains[0].IDs) != 4 {
		t.Errorf("with a previous version: %+v", chains)
	}
	// The limit counts cases and takes whole chains.
	chains, _ = ds.SelectChains(replay.Selection{Limit: 3})
	if len(chains) != 1 || chains[0].Name() != "acme~demo-9" {
		t.Errorf("limit 3: %+v", chains)
	}
	// Named cases are the chain as given, still in revision order.
	chains, err = ds.SelectChains(replay.Selection{IDs: []string{"r-c", "r-a", "r-a2", "s-a"}})
	if err != nil || len(chains) != 2 || !slices.Equal(chains[0].IDs, []string{"r-a", "r-a2", "r-c"}) && !slices.Equal(chains[0].IDs, []string{"r-a2", "r-a", "r-c"}) {
		t.Errorf("named: %+v %v", chains, err)
	}
	if _, err := ds.SelectChains(replay.Selection{IDs: []string{"nope"}}); err == nil {
		t.Error("an unknown case is accepted")
	}
}

// Without an index line, a case is placed by its bundle: the pull request and
// the previous version number it carries.
func TestChainsReadTheBundleWithoutAnIndex(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	prev, _ := review.PreviousVersionOf(mustDocument(t, src), 3)
	later := src.result.Bundle
	later.Previous = prev
	encoded, _ := json.Marshal(later)
	d.addCaseJSON(t, "x-later", map[string]any{"repository": "", "number": 0}, append(encoded, '\n'))
	d.addCaseJSON(t, "y-first", map[string]any{"repository": "", "number": 0}, src.result.JSON)
	ds, _ := replay.OpenDataset(d.dir)
	chains, err := ds.SelectChains(replay.Selection{IDs: []string{"x-later", "y-first"}})
	if err != nil || len(chains) != 1 || !slices.Equal(chains[0].IDs, []string{"y-first", "x-later"}) {
		t.Errorf("%+v %v", chains, err)
	}
}

// mustDocument generates a document for the source bundle with the stub.
func mustDocument(t *testing.T, src source) []byte {
	t.Helper()
	d := newDataset(t, src)
	d.addCase(t, "doc", nil)
	ds, _ := replay.OpenDataset(d.dir)
	runner, err := replay.NewRunner(ctx(), staged(&generatetest.Backend{RunFunc: generatetest.StagedReplies(nil)}))
	if err != nil {
		t.Fatal(err)
	}
	c, _ := ds.LoadCase(ctx(), "doc")
	out, err := runner.Run(ctx(), c)
	if err != nil || !out.OK() {
		t.Fatal(err, out.Error)
	}
	return out.Document
}

// chainDataset has pull request 7 in three revisions r-a, r-b and r-c (the
// frozen previous version of r-b and r-c names sections of its own) and pull
// request 8 in two, s-a and s-b.
func chainDataset(t *testing.T) (*dataset, source, []byte) {
	t.Helper()
	src := assembleSource(t)
	d := newDataset(t, src)
	frozen := mustDocument(t, src)
	withFrozen := func(title string) []byte {
		previous, err := review.PreviousVersionOf(frozen, 1)
		if err != nil {
			t.Fatal(err)
		}
		for i := range previous.Sections {
			previous.Sections[i].ID = "sec_frozen_" + string(rune('a'+i))
		}
		b := src.result.Bundle
		b.PullRequest.Title = title
		b.Previous = previous
		encoded, _ := json.Marshal(b)
		return append(encoded, '\n')
	}
	add := func(number int, id string, previous bool, captured string) {
		facts := map[string]any{"repository": "acme/demo", "number": number, "revision": id, "captured_at": captured}
		content := bundleWith(t, src, id, nil)
		if previous {
			facts["previous_version"] = 1
			content = withFrozen(id)
		}
		d.addCaseJSON(t, id, facts, content)
	}
	add(7, "r-a", false, "2026-01-01T00:00:00Z")
	add(7, "r-b", true, "2026-01-02T00:00:00Z")
	add(7, "r-c", true, "2026-01-03T00:00:00Z")
	add(8, "s-a", false, "2026-01-01T00:00:00Z")
	add(8, "s-b", true, "2026-01-02T00:00:00Z")
	return d, src, frozen
}

// bundleSeen records the distinct bundles the agent was handed, in order, and
// answers like the stub does.
type bundleSeen struct {
	bundles [][]byte
	// fail names the bundle title whose cases fail.
	fail string
}

func (s *bundleSeen) backend() *generatetest.Backend {
	replies := generatetest.StagedReplies(nil)
	return &generatetest.Backend{RunFunc: func(c context.Context, r agent.Request, n int) (agent.Result, error) {
		raw, _ := os.ReadFile(filepath.Join(r.Directory, "bundle.json"))
		if len(s.bundles) == 0 || !bytes.Equal(s.bundles[len(s.bundles)-1], raw) {
			s.bundles = append(s.bundles, raw)
		}
		if s.fail != "" && bytes.Contains(raw, []byte(`"title":"`+s.fail+`"`)) {
			return agent.Result{Calls: 1}, os.ErrDeadlineExceeded
		}
		return replies(c, r, n)
	}}
}

func previousIDs(t *testing.T, raw []byte) []string {
	t.Helper()
	var b struct {
		Previous *struct {
			Version  int
			Sections []struct{ ID string }
		}
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if b.Previous == nil {
		return nil
	}
	ids := []string{}
	for _, s := range b.Previous.Sections {
		ids = append(ids, s.ID)
	}
	return ids
}

func sectionIDs(t *testing.T, document []byte) []string {
	t.Helper()
	var d struct{ Sections []struct{ ID string } }
	if err := json.Unmarshal(document, &d); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range d.Sections {
		ids = append(ids, s.ID)
	}
	return ids
}

func TestChainFeedsEachRevisionThePreviousOutput(t *testing.T) {
	d, _, _ := chainDataset(t)
	ds, _ := replay.OpenDataset(d.dir)
	seen := &bundleSeen{}
	before := snapshot(t, d.dir)
	outDir := t.TempDir()
	summary, printed, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Chain: true, Settings: staged(seen.backend()),
		Selection: replay.Selection{IDs: []string{"r-a", "r-b", "r-c"}}})
	if err != nil || summary.Totals.OK != 3 {
		t.Fatalf("%v %+v\n%s", err, summary.Totals, printed)
	}
	if len(seen.bundles) != 3 {
		t.Fatalf("the agent read %d distinct bundles, want 3", len(seen.bundles))
	}
	cases := filepath.Join(outDir, "run", "cases")
	doc := func(id string) []byte {
		raw, err := os.ReadFile(filepath.Join(cases, id, "document.json"))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	// The first revision keeps what its bundle had: nothing here.
	if ids := previousIDs(t, seen.bundles[0]); ids != nil {
		t.Errorf("the first revision has a previous version: %v", ids)
	}
	// The second reads the first's output, not the frozen previous version.
	for n, from := range []string{"r-a", "r-b"} {
		got := previousIDs(t, seen.bundles[n+1])
		if !slices.Equal(got, sectionIDs(t, doc(from))) || slices.Contains(got, "sec_frozen_a") {
			t.Errorf("revision %d read previous sections %v, want those of %s: %v", n+2, got, from, sectionIDs(t, doc(from)))
		}
	}
	// The version numbers count the revisions, and the digest is the bundle's.
	var second struct{ Previous struct{ Version int } }
	_ = json.Unmarshal(seen.bundles[1], &second)
	third := struct{ Previous struct{ Version int } }{}
	_ = json.Unmarshal(seen.bundles[2], &third)
	if second.Previous.Version != 1 || third.Previous.Version != 2 {
		t.Errorf("previous versions %d and %d", second.Previous.Version, third.Previous.Version)
	}
	sum := sha256.Sum256(bytes.TrimSuffix(seen.bundles[1], []byte("\n")))
	if out, _ := replay.ReadOutcome(filepath.Join(cases, "r-b")); out.BundleDigest != hex.EncodeToString(sum[:]) || out.BundleDigest == "" {
		t.Errorf("the outcome's bundle digest %s is not the digest of the bundle the agent read", out.BundleDigest)
	}
	if snapshot(t, d.dir) != before {
		t.Error("the dataset changed")
	}

	// The run records the chain: its links in order, and a pair record per step.
	var record replay.ChainRecord
	raw, err := os.ReadFile(filepath.Join(outDir, "run", "chains", "acme~demo-7", "chain.json"))
	if err != nil || json.Unmarshal(raw, &record) != nil {
		t.Fatal(err)
	}
	if len(record.Revisions) != 3 || record.Revisions[1].CaseID != "r-b" || record.Revisions[1].PreviousFrom != "r-a" || record.Revisions[0].PreviousFrom != "" || record.Stopped != "" || record.Order == "" {
		t.Errorf("%+v", record)
	}
	for _, name := range []string{"01-r-a", "02-r-b", "03-r-c"} {
		if _, err := os.Stat(filepath.Join(outDir, "run", "chains", "acme~demo-7", name, "outcome.json")); err != nil {
			t.Errorf("the link %s does not lead to the case: %v", name, err)
		}
	}
	var pair replay.PairRecord
	raw, err = os.ReadFile(filepath.Join(outDir, "run", "chains", "acme~demo-7", record.Revisions[2].Pair))
	if err != nil || json.Unmarshal(raw, &pair) != nil || pair.From != "r-b" || pair.To != "r-c" || pair.Values["sections_next"] == 0 {
		t.Errorf("pair %+v %v", pair, err)
	}
	var manifest replay.Manifest
	raw, _ = os.ReadFile(filepath.Join(outDir, "run", "manifest.json"))
	_ = json.Unmarshal(raw, &manifest)
	if !manifest.Chain {
		t.Error("the manifest does not say the run was a chain")
	}
}

func TestFailedRevisionEndsItsChain(t *testing.T) {
	d, _, _ := chainDataset(t)
	ds, _ := replay.OpenDataset(d.dir)
	seen := &bundleSeen{fail: "r-b"}
	outDir := t.TempDir()
	summary, printed, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Chain: true, Settings: staged(seen.backend())})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Totals.OK != 3 || summary.Totals.Failed != 1 || summary.Totals.Skipped != 1 {
		t.Fatalf("%+v\n%s", summary.Totals, printed)
	}
	for _, row := range summary.Rows {
		if row.CaseID == "r-c" && (row.Status != "skipped" || row.Reason != "chain") {
			t.Errorf("r-c: %+v", row)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "run", "cases", "r-c", "outcome.json")); err == nil {
		t.Error("the skipped revision has an outcome")
	}
	var record replay.ChainRecord
	raw, _ := os.ReadFile(filepath.Join(outDir, "run", "chains", "acme~demo-7", "chain.json"))
	_ = json.Unmarshal(raw, &record)
	if !strings.Contains(record.Stopped, "r-b") || record.Revisions[2].Status != "skipped" || record.Revisions[1].Status != "failed" {
		t.Errorf("%+v", record)
	}
	// The other pull request's chain is not affected.
	if out, err := replay.ReadOutcome(filepath.Join(outDir, "run", "cases", "s-b")); err != nil || !out.OK() {
		t.Errorf("s-b: %v %+v", err, out)
	}

	// Resumed, the chain continues from the revision that succeeded, and the
	// next one reads that revision's stored document.
	good := &bundleSeen{}
	summary, _, err = execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Chain: true, Resume: true, Settings: staged(good.backend())})
	if err != nil || summary.Totals.Failed != 0 || summary.Totals.Skipped != 0 || summary.Totals.Resumed != 3 || summary.Totals.OK != 2 {
		t.Fatalf("%v %+v", err, summary.Totals)
	}
	first, _ := os.ReadFile(filepath.Join(outDir, "run", "cases", "r-a", "document.json"))
	if len(good.bundles) == 0 || !slices.Equal(previousIDs(t, good.bundles[0]), sectionIDs(t, first)) {
		t.Errorf("the resumed revision did not read the earlier revision's document")
	}
}

func TestChainNeedsAGeneratorThatCanChain(t *testing.T) {
	d, _, _ := chainDataset(t)
	ds, _ := replay.OpenDataset(d.dir)
	_, _, err := execute(t, replay.Plan{Dataset: ds, Chain: true, Settings: replay.Settings{Backends: fakeBackends(&generatetest.Backend{}), Generator: echo{valid: true}}})
	if err == nil || !strings.Contains(err.Error(), "--chain needs") {
		t.Errorf("error %v", err)
	}
}

// A dry run of a chain lists its revisions in order.
func TestChainDryRun(t *testing.T) {
	d, _, _ := chainDataset(t)
	ds, _ := replay.OpenDataset(d.dir)
	_, printed, err := execute(t, replay.Plan{Dataset: ds, Chain: true, DryRun: true, Selection: replay.Selection{Previous: ptr(true)}})
	if err != nil || !strings.Contains(printed, "5 case(s) would run") || strings.Index(printed, "r-a\n") > strings.Index(printed, "r-b\n") || !strings.Contains(printed, "chain acme~demo-7: 3 revision(s)") {
		t.Errorf("%v\n%s", err, printed)
	}
}

// artefacts is a generator that returns extra files.
type artefacts struct{ names []string }

func (artefacts) Name() string { return "artefacts" }
func (artefacts) Describe(context.Context, string) (map[string]any, error) {
	return map[string]any{}, nil
}
func (a artefacts) Generate(_ context.Context, _ generate.Options, c replay.Case) (replay.Generated, error) {
	extra := map[string][]byte{}
	for _, name := range a.names {
		extra[name] = []byte("content of " + name)
	}
	return replay.Generated{Document: []byte(`{"kind":"x"}`), Outcome: agent.Outcome{Calls: 1}, Artifacts: extra}, nil
}
func (artefacts) Validate([]byte, replay.Case) []string { return nil }

func TestArtifactsAreWrittenBesideTheDocument(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	settings := func(names ...string) replay.Settings {
		return replay.Settings{Backends: fakeBackends(&generatetest.Backend{}), Generator: artefacts{names}}
	}
	summary, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: settings("composition.json", "fills/library.json")})
	if err != nil || summary.Totals.OK != 1 {
		t.Fatalf("%v %+v", err, summary.Totals)
	}
	caseDir := filepath.Join(outDir, "run", "cases", "c1")
	for _, name := range []string{"composition.json", "fills/library.json"} {
		if raw, err := os.ReadFile(filepath.Join(caseDir, name)); err != nil || string(raw) != "content of "+name {
			t.Errorf("%s: %q %v", name, raw, err)
		}
	}
	out, _ := replay.ReadOutcome(caseDir)
	if !slices.Equal(out.Artifacts, []string{"composition.json", "fills/library.json"}) {
		t.Errorf("the outcome lists %v", out.Artifacts)
	}
	// A rerun replaces the earlier artefacts.
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: settings("composition.json")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(caseDir, "fills", "library.json")); err == nil {
		t.Error("an artefact of the earlier run is still there")
	}

	// A name outside the case directory, or one of the runner's own files, fails
	// the case and writes nothing.
	for _, bad := range []string{"../escape.json", "/abs.json", "outcome.json", "./document.json", `a\b`} {
		t.Run(bad, func(t *testing.T) {
			dir := t.TempDir()
			summary, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: dir, Settings: settings(bad)})
			if err != nil || summary.Totals.Failed != 1 || summary.FailuresByReason["internal"] != 1 {
				t.Errorf("%v %+v", err, summary)
			}
			if _, err := os.Stat(filepath.Join(dir, "run", "escape.json")); err == nil {
				t.Error("an artefact left the case directory")
			}
		})
	}
}
