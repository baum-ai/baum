package replay_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/replay"
	"github.com/baum-ai/baum/review"
)

// carried builds the bundle a chain hands the second revision of a case: the
// case's own with a generated document as its previous version, and with one
// section's content grown by extra bytes of text and the given line.
func carried(t *testing.T, extra int, line string) (replay.Case, bundle.Bundle) {
	t.Helper()
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	c, err := ds.LoadCase(ctx(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := (&replay.ReviewGenerator{}).Next(c, mustDocument(t, src))
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	if err := json.Unmarshal(b.Previous.Sections[0].Content, &content); err != nil {
		t.Fatal(err)
	}
	content["padding"] = strings.Repeat("filler words ", extra/13)
	content["note"] = line
	b.Previous.Sections[0].Content, _ = json.Marshal(content)
	return c, b
}

func defaultCaps(t *testing.T) bundle.Caps {
	t.Helper()
	caps, err := config.Default().EffectiveCaps()
	if err != nil {
		t.Fatal(err)
	}
	return assemble.LimitsFrom(caps).Caps
}

// The bundle a chain hands the model passes through masking and the evidence
// budget as assembly does it: injected secrets are masked, a previous version
// that does not fit trims other evidence and the cuts are noted, and a bundle
// that cannot fit fails with the budget's reason.
func TestRefitMasksFitsAndStamps(t *testing.T) {
	secret := "AKIA" + "UJZDEGXDNCF32EPF"
	redactor, err := bundle.NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	caps := defaultCaps(t)

	c, b := carried(t, 0, "the key is "+secret)
	fitted, err := c.Refit(ctx(), b, caps, redactor)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(fitted.BundleJSON, []byte(secret)) || !bytes.Contains(fitted.BundleJSON, []byte("the key is")) {
		t.Error("the secret in the carried previous version reached the model")
	}
	if fitted.Digest == c.Digest || fitted.Fitted || len(fitted.FitNotes) != 0 {
		t.Errorf("digest changed %v, fitted %v, notes %v", fitted.Digest != c.Digest, fitted.Fitted, fitted.FitNotes)
	}
	if again, err := fitted.FreshBundle(); err != nil || again.Digest != fitted.Digest || again.Previous == nil || len(again.Previous.Sections) == 0 {
		t.Errorf("%v %+v", err, again.Previous)
	}

	// A previous version too large for the budget trims other evidence first.
	_, plain := carried(t, 0, "")
	small, err := c.Refit(ctx(), plain, caps, redactor)
	if err != nil {
		t.Fatal(err)
	}
	_, large := carried(t, 9000, "")
	tight := caps
	tight.MaxBytes = len(small.BundleJSON) + 7000
	trimmed, err := c.Refit(ctx(), large, tight, redactor)
	if err != nil {
		t.Fatalf("a bundle with 9000 more bytes of previous version and a budget 7000 above the plain one: %v", err)
	}
	if !trimmed.Fitted || len(trimmed.FitNotes) == 0 || len(trimmed.Bundle.Truncated) <= len(plain.Truncated) {
		t.Errorf("fitted %v notes %v", trimmed.Fitted, trimmed.FitNotes)
	}
	if trimmed.Bundle.Previous == nil || len(trimmed.BundleJSON) > tight.MaxBytes+len(diffIndexOf(trimmed)) {
		t.Errorf("size %d", len(trimmed.BundleJSON))
	}

	// One that cannot fit is the budget's error.
	impossible := caps
	impossible.MaxBytes = 2000
	if _, err := c.Refit(ctx(), large, impossible, redactor); err == nil || !strings.Contains(err.Error(), bundle.EvidenceBudgetExceededReason) {
		t.Errorf("error %v", err)
	}
}

func diffIndexOf(c replay.Case) []byte {
	encoded, _ := json.Marshal(c.Bundle.Diff)
	return encoded
}

// The version a revision carries is the previous document's own, which the
// pipeline set above the frozen previous version of the first revision.
func TestChainCarriesTheDocumentsOwnVersion(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	frozen, err := review.PreviousVersionOf(mustDocument(t, src), 3)
	if err != nil {
		t.Fatal(err)
	}
	first := src.result.Bundle
	first.PullRequest.Title = "r-a"
	first.Previous = frozen
	encoded, _ := json.Marshal(first)
	d.addCaseJSON(t, "r-a", map[string]any{"revision": "a", "captured_at": "2026-01-01T00:00:00Z", "previous_version": 3}, append(encoded, '\n'))
	d.addCaseJSON(t, "r-b", map[string]any{"revision": "b", "captured_at": "2026-01-02T00:00:00Z", "previous_version": 4}, bundleWith(t, src, "r-b", nil))
	ds, _ := replay.OpenDataset(d.dir)
	seen := &bundleSeen{}
	outDir := t.TempDir()
	if summary, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Chain: true, Settings: staged(seen.backend())}); err != nil || summary.Totals.OK != 2 {
		t.Fatalf("%v %+v", err, summary.Totals)
	}
	var doc struct{ Review struct{ Version int } }
	raw, _ := os.ReadFile(filepath.Join(outDir, "run", "cases", "r-a", "document.json"))
	_ = json.Unmarshal(raw, &doc)
	var second struct{ Previous struct{ Version int } }
	_ = json.Unmarshal(seen.bundles[len(seen.bundles)-1], &second)
	if doc.Review.Version != 4 || second.Previous.Version != 4 {
		t.Errorf("the first document is version %d and the second revision carried it as version %d, want 4 and 4", doc.Review.Version, second.Previous.Version)
	}
	var record replay.ChainRecord
	raw, _ = os.ReadFile(filepath.Join(outDir, "run", "chains", "acme~demo-7", "chain.json"))
	_ = json.Unmarshal(raw, &record)
	out, _ := replay.ReadOutcome(filepath.Join(outDir, "run", "cases", "r-b"))
	if record.Revisions[1].PreviousVersion != 4 || record.Revisions[1].BundleBytes != len(seen.bundles[len(seen.bundles)-1]) || out.BundleBytes != record.Revisions[1].BundleBytes || out.BundleFitted {
		t.Errorf("%+v bytes %d fitted %v", record.Revisions[1], out.BundleBytes, out.BundleFitted)
	}
}

// A bundle that cannot be fitted fails its revision with the input category,
// as assembly would, and ends the chain.
func TestChainRevisionThatCannotFitFails(t *testing.T) {
	d, _, _ := chainDataset(t)
	ds, _ := replay.OpenDataset(d.dir)
	settings := staged(&generatetest.Backend{RunFunc: generatetest.StagedReplies(nil)})
	settings.Caps = map[string]int{"max_bytes": 3000}
	outDir := t.TempDir()
	summary, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Chain: true, Settings: settings, Selection: replay.Selection{IDs: []string{"r-a", "r-b", "r-c"}}})
	if err != nil || summary.Totals.OK != 1 || summary.FailuresByReason["input"] != 1 || summary.Totals.Skipped != 1 {
		t.Fatalf("%v %+v %v", err, summary.Totals, summary.FailuresByReason)
	}
	out, _ := replay.ReadOutcome(filepath.Join(outDir, "run", "cases", "r-b"))
	if out.Reason != "input" || !strings.Contains(out.Error, bundle.EvidenceBudgetExceededReason) {
		t.Errorf("%+v", out)
	}
}

// The guard is checked before each revision, not only before each chain.
func TestCostGuardStopsAChainBetweenRevisions(t *testing.T) {
	d, _, _ := chainDataset(t)
	ds, _ := replay.OpenDataset(d.dir)
	settings := replay.Settings{Backends: fakeBackends(&generatetest.Backend{RunFunc: pricedReplies(nil)}), Generator: &replay.ReviewGenerator{Staged: true}}
	outDir := t.TempDir()
	summary, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Chain: true, Settings: settings, MaxCostUSD: 0.01, Selection: replay.Selection{IDs: []string{"r-a", "r-b", "r-c"}}})
	if err != nil || summary.Totals.OK != 1 || summary.Totals.Skipped != 2 || !strings.Contains(summary.Stopped, "cost guard") {
		t.Fatalf("%v %+v %q", err, summary.Totals, summary.Stopped)
	}
	var record replay.ChainRecord
	raw, _ := os.ReadFile(filepath.Join(outDir, "run", "chains", "acme~demo-7", "chain.json"))
	_ = json.Unmarshal(raw, &record)
	if !strings.Contains(record.Stopped, "stopped before revision 2") || record.Revisions[1].Reason != "stopped" {
		t.Errorf("%+v", record)
	}
}

// A rerun does not leave the links of an earlier run, and a resume keeps the
// pair records of the revisions it keeps.
func TestChainDirectoryAfterARerunAndAResume(t *testing.T) {
	d, _, _ := chainDataset(t)
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	chain := filepath.Join(outDir, "run", "chains", "acme~demo-7")
	failing := &bundleSeen{fail: "r-c"}
	all := replay.Selection{IDs: []string{"r-a", "r-b", "r-c"}}
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Chain: true, Settings: staged(failing.backend()), Selection: all}); err != nil {
		t.Fatal(err)
	}
	good := &bundleSeen{}
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Chain: true, Resume: true, Settings: staged(good.backend()), Selection: all}); err != nil {
		t.Fatal(err)
	}
	var record replay.ChainRecord
	raw, _ := os.ReadFile(filepath.Join(chain, "chain.json"))
	_ = json.Unmarshal(raw, &record)
	for i, rev := range record.Revisions {
		if rev.Pair == "" && i > 0 {
			t.Errorf("revision %d lost its pair record: %+v", i+1, record.Revisions)
		} else if i > 0 {
			if _, err := os.Stat(filepath.Join(chain, rev.Pair)); err != nil {
				t.Error(err)
			}
		}
	}
	if record.Revisions[1].Status != "resumed" || record.Revisions[1].PreviousFrom != "r-a" || record.Revisions[1].PreviousVersion == 0 || record.Revisions[2].Status != "ok" {
		t.Errorf("%+v", record.Revisions)
	}
	// A rerun of two revisions leaves no link to the third.
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Chain: true, Settings: staged((&bundleSeen{}).backend()), Selection: replay.Selection{IDs: []string{"r-a", "r-b"}}}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(chain)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(n, "03-") }) || slices.Contains(names, "pair-03.json") {
		t.Errorf("stale entries: %v", names)
	}
}

func TestArtifactNamesAreChecked(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	run := func(names ...string) (replay.Summary, string) {
		dir := t.TempDir()
		summary, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: dir, Settings: replay.Settings{Backends: fakeBackends(&generatetest.Backend{}), Generator: artefacts{names}}})
		if err != nil {
			t.Fatal(err)
		}
		return summary, dir
	}
	for name, names := range map[string][]string{
		"runner file, other case": {"Outcome.JSON"},
		"two names, one file":     {"Fills/a.json", "fills/A.json"},
		"file and directory":      {"fills", "fills/a.json"},
	} {
		if summary, _ := run(names...); summary.Totals.Failed != 1 {
			t.Errorf("%s: %+v", name, summary.Totals)
		}
	}
	if summary, _ := run("a.json", "b/a.json", "ab.json"); summary.Totals.OK != 1 {
		t.Errorf("distinct names fail: %+v", summary.Totals)
	}
	// A rerun starts the case's directory over, whatever the earlier outcome said.
	dir := t.TempDir()
	settings := replay.Settings{Backends: fakeBackends(&generatetest.Backend{}), Generator: artefacts{[]string{"keep.json"}}}
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: dir, Settings: settings}); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(dir, "run", "cases", "c1", "stray.txt")
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: dir, Settings: settings}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stray); err == nil {
		t.Error("a file of the earlier run is still in the case directory")
	}
}
