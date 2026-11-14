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
		"by detector: schema 2, api 2, either 3, neither 3", "2 case(s) also pass", "c4\n", "c6\n"} {
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
	if len(chains) != 2 || chains[0].Name() != "acme-demo-7" || !slices.Equal(chains[0].IDs, []string{"r-a", "r-d", "r-b", "r-c"}) || !slices.Equal(chains[1].IDs, []string{"t-a", "t-b"}) {
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
	if len(chains) != 1 || chains[0].Name() != "acme-demo-9" {
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
	raw, err := os.ReadFile(filepath.Join(outDir, "run", "chains", "acme-demo-7", "chain.json"))
	if err != nil || json.Unmarshal(raw, &record) != nil {
		t.Fatal(err)
	}
	if len(record.Revisions) != 3 || record.Revisions[1].CaseID != "r-b" || record.Revisions[1].PreviousFrom != "r-a" || record.Revisions[0].PreviousFrom != "" || record.Stopped != "" || record.Order == "" {
		t.Errorf("%+v", record)
	}
	for _, name := range []string{"01-r-a", "02-r-b", "03-r-c"} {
		if _, err := os.Stat(filepath.Join(outDir, "run", "chains", "acme-demo-7", name, "outcome.json")); err != nil {
			t.Errorf("the link %s does not lead to the case: %v", name, err)
		}
	}
	var pair replay.PairRecord
	raw, err = os.ReadFile(filepath.Join(outDir, "run", "chains", "acme-demo-7", record.Revisions[2].Pair))
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
	raw, _ := os.ReadFile(filepath.Join(outDir, "run", "chains", "acme-demo-7", "chain.json"))
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
	if err != nil || !strings.Contains(printed, "5 case(s) would run") || strings.Index(printed, "r-a\n") > strings.Index(printed, "r-b\n") || !strings.Contains(printed, "chain acme-demo-7: 3 revision(s)") {
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

// pricedAs is pricedReplies with the output tokens given.
func pricedAs(output int64) func(context.Context, agent.Request, int) (agent.Result, error) {
	inner := pricedReplies(nil)
	return func(c context.Context, r agent.Request, n int) (agent.Result, error) {
		result, err := inner(c, r, n)
		result.Usage.Output = output
		return result, err
	}
}

func TestMetricsOfARun(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", map[string]any{"size": "s", "session_vendors": []string{"claude_code"}})
	d.addCase(t, "c2", map[string]any{"size": "m", "session_vendors": []string{"codex"}})
	d.addCase(t, "c3", map[string]any{"size": "m"})
	_ = os.Remove(filepath.Join(d.dir, "cases", "c3", "case.json"))
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	priced := replay.Settings{Backends: fakeBackends(&generatetest.Backend{RunFunc: pricedAs(20000)}), Generator: &replay.ReviewGenerator{Staged: true}}
	summary, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: priced})
	if err != nil || summary.Totals.OK != 2 || summary.Totals.Failed != 1 {
		t.Fatalf("%v %+v", err, summary.Totals)
	}
	runDir := filepath.Join(outDir, "run")
	m, err := replay.ReadMetrics(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Generator != "review" || len(m.Cases) != 3 || m.All.Statuses["ok"] != 2 || m.All.Statuses["failed"] != 1 || m.All.Reasons["case"] != 1 {
		t.Errorf("%+v", m.All)
	}
	cost := m.All.Stats[replay.MetricCost]
	if cost.N != 2 || cost.Sum != summary.Totals.CostUSD || cost.Median <= 0 || cost.P90 < cost.Median {
		t.Errorf("cost %+v, summary %v", cost, summary.Totals.CostUSD)
	}
	for _, key := range []string{replay.MetricWall, replay.MetricCalls, replay.MetricTokens, replay.MetricDocument, "sections", "risks", "hunks", "hunks_covered", "evidence"} {
		if m.All.Stats[key].N == 0 {
			t.Errorf("no %s", key)
		}
	}
	if m.All.Stats["sections"].Min < 1 || m.All.Stats["risk_lifecycle_violations"].Sum != 0 {
		t.Errorf("sections %+v violations %+v", m.All.Stats["sections"], m.All.Stats["risk_lifecycle_violations"])
	}
	if m.BySize["m"].Cases != 2 || m.BySize["s"].Cases != 1 || m.ByAgent["claude_code"].Cases != 1 || m.ByAgent["codex"].Cases != 1 || m.ByAgent["none"].Cases != 1 {
		t.Errorf("by size %v by agent %v", m.BySize, m.ByAgent)
	}
	if m.Pairs != nil || len(m.Chains) != 0 {
		t.Error("a run without chains has chain metrics")
	}
	var printed bytes.Buffer
	m.Print(&printed)
	for _, want := range []string{"run run, generator review", "status: failed 1, ok 2", "cost_usd", "sections", "by size", "by agent"} {
		if !strings.Contains(printed.String(), want) {
			t.Errorf("the report lacks %q:\n%s", want, printed.String())
		}
	}
	if err := replay.WriteMetrics(runDir, m); err != nil {
		t.Fatal(err)
	}
	var back replay.RunMetrics
	raw, _ := os.ReadFile(filepath.Join(runDir, "metrics.json"))
	if err := json.Unmarshal(raw, &back); err != nil || back.All.Cases != 3 {
		t.Errorf("metrics.json: %v", err)
	}
}

func TestMetricsOfAChainRun(t *testing.T) {
	d, _, _ := chainDataset(t)
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	seen := &bundleSeen{fail: "r-c"}
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Chain: true, Settings: staged(seen.backend())}); err != nil {
		t.Fatal(err)
	}
	m, err := replay.ReadMetrics(filepath.Join(outDir, "run"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Chains) != 2 || m.Pairs == nil {
		t.Fatalf("%+v", m.Chains)
	}
	// Pull request 7: r-a to r-b measured, r-b to r-c not (r-c failed).
	var seven ChainReportView
	for _, c := range m.Chains {
		if strings.HasSuffix(c.PullRequest, "#7") {
			seven.pairs, seven.stopped = c.Pairs, c.Stopped
		}
	}
	if len(seven.pairs) != 2 || seven.pairs[0].Skipped != "" || seven.pairs[1].Skipped == "" || seven.stopped == "" {
		t.Errorf("%+v", seven)
	}
	if m.Pairs.Cases != 2 {
		t.Errorf("%d pairs measured, want 2 (7: a-b, 8: a-b)", m.Pairs.Cases)
	}
	// The stub writes the same sections for every revision, so every ID is kept.
	if matched := m.Pairs.Stats["sections_matched"]; matched.N != 2 || matched.Sum == 0 || m.Pairs.Stats["section_ids_retained"].Sum != matched.Sum ||
		m.Pairs.Stats["section_id_retention"].Min != 1 || m.Pairs.Derived["section_id_retention_pooled"] != 1 {
		t.Errorf("%+v %v", m.Pairs.Stats, m.Pairs.Derived)
	}
	if m.Pairs.Stats["risk_lifecycle_violations"].Sum != 0 {
		t.Error("lifecycle violations in the stub's documents")
	}
	var printed bytes.Buffer
	m.Print(&printed)
	if !strings.Contains(printed.String(), "chains: 2 pull request(s), 2 pair(s) measured") || !strings.Contains(printed.String(), "section_id_retention_pooled") {
		t.Errorf("%s", printed.String())
	}
}

// ChainReportView holds what the chain test reads of a report.
type ChainReportView struct {
	pairs   []replay.PairRecord
	stopped string
}

func TestMetricsOfAGeneratorWithoutMeter(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: replay.Settings{Backends: fakeBackends(&generatetest.Backend{}), Generator: echo{valid: true}}}); err != nil {
		t.Fatal(err)
	}
	m, err := replay.ReadMetrics(filepath.Join(outDir, "run"))
	if err != nil || m.All.Stats[replay.MetricDocument].N != 1 || m.All.Stats[replay.MetricFailed].Sum != 1 || m.All.Stats["sections"].N != 0 {
		t.Fatalf("%v %+v", err, m.All)
	}
	var printed bytes.Buffer
	m.Print(&printed)
	if !strings.Contains(printed.String(), "has no metrics of its own") {
		t.Error(printed.String())
	}
}

// meterEcho is a generator that contributes its own metrics.
type meterEcho struct{ echo }

func (meterEcho) Name() string { return "meterecho" }
func (meterEcho) DocumentMetrics(document []byte) (map[string]float64, error) {
	return map[string]float64{"tiles": float64(len(document))}, nil
}
func (meterEcho) PairMetrics(previous, next []byte) (map[string]float64, error) {
	return map[string]float64{"tile_id_retention": 1}, nil
}

// A generator adds its metrics by registering a Meter; the command changes
// nothing.
func TestAnotherGeneratorContributesItsMetrics(t *testing.T) {
	replay.RegisterMeter("meterecho", meterEcho{})
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: replay.Settings{Backends: fakeBackends(&generatetest.Backend{}), Generator: meterEcho{echo{valid: true}}}}); err != nil {
		t.Fatal(err)
	}
	m, err := replay.ReadMetrics(filepath.Join(outDir, "run"))
	if err != nil || m.All.Stats["tiles"].N != 1 || m.All.Stats["tiles"].Sum == 0 {
		t.Fatalf("%v %+v", err, m.All.Stats)
	}
}

func TestCompareAlignsCasesByID(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	for _, id := range []string{"c1", "c2", "c3"} {
		d.addCase(t, id, nil)
	}
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	runWith := func(name string, ids []string, output int64, backend func() *generatetest.Backend) replay.RunMetrics {
		t.Helper()
		settings := replay.Settings{Backends: fakeBackends(backend()), Generator: &replay.ReviewGenerator{Staged: true}}
		if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Name: name, Settings: settings, Selection: replay.Selection{IDs: ids}}); err != nil {
			t.Fatal(err)
		}
		m, err := replay.ReadMetrics(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	base := runWith("base", []string{"c1", "c2", "c3"}, 20000, func() *generatetest.Backend { return &generatetest.Backend{RunFunc: pricedAs(20000)} })
	// The same analysis, at twice the output tokens (cost) and with c3 left out.
	same := runWith("same", []string{"c1", "c2"}, 40000, func() *generatetest.Backend { return &generatetest.Backend{RunFunc: pricedAs(40000)} })
	c := replay.Compare(same, base)
	if c.Aligned != 2 || c.Completed != 2 || !slices.Equal(c.OnlyOther, []string{"c3"}) || len(c.OnlyRun) != 0 || len(c.StatusChanges) != 0 {
		t.Errorf("%+v", c)
	}
	cost := c.Metrics[replay.MetricCost]
	if cost.N != 2 || cost.Median <= 0 || cost.MedianRatio <= 1 || cost.Changed != 2 || c.SameDocumentMetrics != 2 || c.Metrics["sections"].Changed != 0 {
		t.Errorf("cost %+v same %d", cost, c.SameDocumentMetrics)
	}
	// Another analysis: c2 fails, and a document differs in what it measures.
	failing := runWith("failing", []string{"c1", "c2"}, 20000, func() *generatetest.Backend {
		return &generatetest.Backend{RunFunc: func(cx context.Context, r agent.Request, n int) (agent.Result, error) {
			if n >= 3 {
				return agent.Result{Calls: 1}, os.ErrDeadlineExceeded
			}
			return pricedAs(20000)(cx, r, n)
		}}
	})
	c = replay.Compare(failing, base)
	if len(c.StatusChanges) != 1 || c.StatusChanges[0].CaseID != "c2" || c.StatusChanges[0].Base != "failed" || c.StatusChanges[0].Other != "ok" || c.Completed != 1 {
		t.Errorf("%+v", c)
	}
	changed := base
	changed.Cases = slices.Clone(base.Cases)
	for i := range changed.Cases {
		values := map[string]float64{}
		for k, v := range changed.Cases[i].Values {
			values[k] = v
		}
		values["sections"]++
		changed.Cases[i].Values = values
	}
	c = replay.Compare(changed, base)
	if c.SameDocumentMetrics != 0 || c.Metrics["sections"].Median != 1 || c.Metrics["sections"].Changed != 3 || c.Metrics[replay.MetricCost].Changed != 0 {
		t.Errorf("%+v", c.Metrics["sections"])
	}
	var printed bytes.Buffer
	c.Print(&printed)
	if !strings.Contains(printed.String(), "document metrics identical in 0 of 3") || !strings.Contains(printed.String(), "sections") {
		t.Error(printed.String())
	}
}

func docOf(sections ...string) []byte {
	return []byte(`{"sections":[` + strings.Join(sections, ",") + `],"index":{"hunks":[{"id":"h1"},{"id":"h2"},{"id":"h3"},{"id":"h4"}]},"unassigned_hunks":["h4"]}`)
}

func sec(id string, hunks string, risks ...string) string {
	return `{"id":"` + id + `","hunks":[` + hunks + `],"risks":[` + strings.Join(risks, ",") + `]}`
}

func risk(id, tier, state string, mitigated string) string {
	return `{"id":"` + id + `","tier":"` + tier + `","state":"` + state + `","mitigated_version":` + mitigated + `}`
}

func TestReviewMetrics(t *testing.T) {
	g := &replay.ReviewGenerator{}
	doc := docOf(
		sec("a", `"h1","h2"`, risk("r1", "high", "active", "null"), risk("r2", "low", "mitigated", "2")),
		sec("b", `"h3"`, risk("r3", "critical", "active", "null")),
	)
	m, err := g.DocumentMetrics(doc)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{"sections": 2, "risks": 3, "risks_active": 2, "risks_active_high": 1, "risks_active_critical": 1, "risks_active_low": 0,
		"risks_mitigated": 1, "hunks": 4, "hunks_covered": 3, "hunks_unassigned": 1, "hunks_covered_ratio": 0.75, "risk_lifecycle_violations": 0} {
		if m[key] != want {
			t.Errorf("%s = %v, want %v", key, m[key], want)
		}
	}
	// A risk both active and mitigated, in two sections or by its own fields.
	bad := docOf(
		sec("a", `"h1"`, risk("r1", "high", "active", "3"), risk("r2", "low", "mitigated", "null")),
		sec("b", `"h2"`, risk("r2", "low", "active", "null"), risk("r9", "low", "unknown", "null")),
	)
	if m, _ := g.DocumentMetrics(bad); m["risk_lifecycle_violations"] != 4 {
		t.Errorf("violations %v, want 4", m["risk_lifecycle_violations"])
	}
	if _, err := g.DocumentMetrics([]byte("not json")); err == nil {
		t.Error("a document that is not JSON is measured")
	}
}

func TestReviewPairMetrics(t *testing.T) {
	g := &replay.ReviewGenerator{}
	previous := docOf(
		sec("a", `"h1","h2"`, risk("r1", "high", "active", "null"), risk("r2", "low", "active", "null"), risk("r3", "low", "active", "null")),
		sec("b", `"h3"`),
		sec("gone", `"h9"`),
	)
	next := docOf(
		sec("a", `"h1"`, risk("r1", "high", "mitigated", "2"), risk("r2", "low", "active", "null")),
		sec("renamed", `"h3"`),
		sec("new", `"h4"`, risk("r4", "low", "active", "null")),
	)
	m, err := g.PairMetrics(previous, next)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{"sections_previous": 3, "sections_next": 3, "sections_matched": 2, "section_ids_retained": 1, "section_id_retention": 0.5,
		"sections_dropped": 1, "sections_added": 1, "risks_introduced": 1, "risks_newly_mitigated": 1, "risks_carried": 1, "risks_vanished": 1, "risk_lifecycle_violations": 0} {
		if m[key] != want {
			t.Errorf("%s = %v, want %v", key, m[key], want)
		}
	}
	// A split keeps the ID on the part that shares most hunks; with equal
	// shares the section with the same ID wins.
	split, _ := g.PairMetrics(docOf(sec("a", `"h1","h2"`)), docOf(sec("x", `"h1"`), sec("a", `"h2"`)))
	if split["sections_matched"] != 1 || split["section_ids_retained"] != 1 || split["sections_added"] != 1 {
		t.Errorf("split %v", split)
	}
	// No overlap at all gives no retention figure, not a zero.
	none, _ := g.PairMetrics(docOf(sec("a", `"h1"`)), docOf(sec("b", `"h2"`)))
	if _, ok := none["section_id_retention"]; ok || none["sections_dropped"] != 1 {
		t.Errorf("no overlap: %v", none)
	}
	derived := g.Derived(map[string]float64{"sections_matched": 4, "section_ids_retained": 3})
	if derived["section_id_retention_pooled"] != 0.75 {
		t.Errorf("%v", derived)
	}
}
