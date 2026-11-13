package replay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/replay"
)

func fakeBackends(b *generatetest.Backend) generate.Backends {
	return generate.Backends{Claude: func(string) generate.CheckedBackend { return b }}
}

func ctx() context.Context { return context.Background() }

// pricedReplies answers every stage as the stub does, under a model the price
// list knows and with usage it can price.
func pricedReplies(answer generatetest.Answer) func(context.Context, agent.Request, int) (agent.Result, error) {
	inner := generatetest.StagedReplies(answer)
	return func(c context.Context, r agent.Request, n int) (agent.Result, error) {
		result, err := inner(c, r, n)
		speed, hour, size := "standard", int64(0), int64(1000)
		result.Model = "claude-opus-5-5"
		result.Usage = agent.Usage{Input: 100000, Output: 20000, Speed: &speed, CacheCreate1h: &hour, MinPrompt: &size, MaxPrompt: &size}
		return result, err
	}
}

func staged(b *generatetest.Backend) replay.Settings {
	return replay.Settings{Staged: true, Backends: fakeBackends(b)}
}

func TestLoadCaseKeepsTheBundleBytesAndItsDigest(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "acme-demo-7-aaaa", nil)
	ds, err := replay.OpenDataset(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ds.LoadCase(ctx(), "acme-demo-7-aaaa")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.BundleJSON, src.result.JSON) {
		t.Error("the bundle bytes changed")
	}
	// The digest the pipeline's own assembly stamped.
	if c.Digest != src.result.Bundle.Digest || c.Bundle.Digest != c.Digest {
		t.Errorf("digest %s, want %s", c.Digest, src.result.Bundle.Digest)
	}
	if !c.Lossless || c.Entry == nil || c.Repository.String() == "" {
		t.Errorf("lossless=%v entry=%v repository=%v", c.Lossless, c.Entry, c.Repository)
	}
}

// A case runs through the production pipeline with the fake backend: the agent
// sees the saved bundle byte for byte and the saved trees, and the files of the
// run are written without touching the dataset.
func TestRunGeneratesFromTheSavedCase(t *testing.T) {
	for _, stagedRun := range []bool{true, false} {
		name := map[bool]string{true: "staged", false: "one pass"}[stagedRun]
		t.Run(name, func(t *testing.T) {
			src := assembleSource(t)
			d := newDataset(t, src)
			d.addCase(t, "acme-demo-7-aaaa", nil)
			ds, _ := replay.OpenDataset(d.dir)
			c, err := ds.LoadCase(ctx(), "acme-demo-7-aaaa")
			if err != nil {
				t.Fatal(err)
			}
			seen := map[string]string{}
			var bundleSeen []byte
			var imagesSeen int
			backend := &generatetest.Backend{Version: "9.9.9"}
			replies := generatetest.StagedReplies(nil)
			backend.RunFunc = func(cx context.Context, r agent.Request, n int) (agent.Result, error) {
				if n == 0 {
					bundleSeen, _ = os.ReadFile(filepath.Join(r.Directory, "bundle.json"))
					for _, root := range []string{"head", "merge-base"} {
						_ = filepath.WalkDir(filepath.Join(r.Directory, root), func(path string, e os.DirEntry, err error) error {
							if err == nil && e.Type().IsRegular() {
								rel, _ := filepath.Rel(r.Directory, path)
								content, _ := os.ReadFile(path)
								seen[filepath.ToSlash(rel)] = string(content)
							}
							return nil
						})
					}
					entries, _ := os.ReadDir(filepath.Join(r.Directory, "attachments"))
					imagesSeen = len(entries)
				}
				return replies(cx, r, n)
			}
			before := snapshot(t, d.dir)
			out := t.TempDir()
			settings := replay.Settings{Staged: stagedRun, Backends: fakeBackends(backend), OutputDir: out}
			runner, err := replay.NewRunner(ctx(), settings)
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := runner.Run(ctx(), c)
			if err != nil || !outcome.OK() {
				t.Fatalf("%v %+v", err, outcome)
			}
			if !bytes.Equal(bundleSeen, src.result.JSON) {
				t.Error("the agent did not read the saved bundle bytes")
			}
			if !maps(seen, src.trees) {
				t.Errorf("the agent saw %d files, the case has %d, or their content differs", len(seen), len(src.trees))
			}
			if imagesSeen != len(src.images) {
				t.Errorf("images %d", imagesSeen)
			}
			if outcome.BundleDigest != c.Digest || outcome.Backend != "claude" || outcome.Calls == 0 || !outcome.StoredValid || len(outcome.CallLog) == 0 {
				t.Errorf("outcome %+v", outcome)
			}
			if !stagedRun && len(outcome.CallLog) != 1 {
				t.Errorf("one pass made %d calls", len(outcome.CallLog))
			}
			for _, file := range []string{"document.json", "outcome.json", "progress.log", "diagnostics.json", "transcript.jsonl"} {
				if _, err := os.Stat(filepath.Join(out, file)); err != nil && file != "transcript.jsonl" {
					t.Errorf("missing %s", file)
				}
			}
			var document struct {
				Review struct{ Repository string }
			}
			raw, _ := os.ReadFile(filepath.Join(out, "document.json"))
			if err := json.Unmarshal(raw, &document); err != nil || document.Review.Repository != "acme/demo" || !bytes.Equal(raw, outcome.Document) {
				t.Errorf("document: %v %q", err, document.Review.Repository)
			}
			if after := snapshot(t, d.dir); after != before {
				t.Error("the dataset changed")
			}
		})
	}
}

func maps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestLoadCaseNamesWhatIsBroken(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "good", nil)
	ds, _ := replay.OpenDataset(d.dir)
	caseDir := func(id string) string { return filepath.Join(d.dir, "cases", id) }
	cases := []struct {
		name   string
		id     string
		break_ func(t *testing.T, id string)
		want   string
	}{
		{"unknown", "nothing", nil, "no such case"},
		{"bad id", "../good", nil, "not a case id"},
		{"no bundle", "a", func(t *testing.T, id string) { _ = os.Remove(filepath.Join(caseDir(id), "bundle.json")) }, "reading bundle.json"},
		{"not a bundle", "b", func(t *testing.T, id string) {
			_ = os.WriteFile(filepath.Join(caseDir(id), "bundle.json"), []byte("[1]"), 0o644)
		}, "does not decode"},
		{"digest", "c", func(t *testing.T, id string) {
			_ = os.WriteFile(filepath.Join(caseDir(id), "bundle.json"), append(bytes.Clone(src.result.JSON), '\n'), 0o644)
		}, "digest case.json records"},
		{"image", "d", func(t *testing.T, id string) { _ = os.RemoveAll(filepath.Join(caseDir(id), "attachments")) }, "no such file"},
		{"refs", "e", func(t *testing.T, id string) {
			git(t, filepath.Join(d.dir, "checkouts.git"), "update-ref", "-d", "refs/cases/"+id+"/head")
		}, "checkout refs do not resolve"},
		{"trees", "f", func(t *testing.T, id string) {
			git(t, filepath.Join(d.dir, "checkouts.git"), "update-ref", "refs/cases/"+id+"/head", gitRef(t, d.dir, "refs/cases/"+id+"/merge-base"))
		}, "trees differ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.break_ != nil {
				d.addCase(t, tc.id, nil)
				tc.break_(t, tc.id)
			}
			_, err := ds.LoadCase(ctx(), tc.id)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "case "+tc.id) {
				t.Errorf("error %v, want %q", err, tc.want)
			}
		})
	}
}

func gitRef(t *testing.T, dir, ref string) string {
	return git(t, filepath.Join(dir, "checkouts.git"), "rev-parse", ref)
}

func TestSelection(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", map[string]any{"split": "calibration", "size": "s", "representative": true, "session_vendors": []string{"claude_code"}})
	d.addCase(t, "c2", map[string]any{"split": "held_out", "size": "m", "session_vendors": []string{"codex"}})
	d.addCase(t, "c3", map[string]any{"split": "calibration", "size": "m", "primary": false})
	d.addCase(t, "c4", map[string]any{"split": "calibration", "size": "l", "representative": true, "session_vendors": []string{"claude_code", "codex"}})
	ds, err := replay.OpenDataset(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	seed := uint64(3)
	for _, tc := range []struct {
		name string
		sel  replay.Selection
		want []string
	}{
		{"all, sorted", replay.Selection{}, []string{"c1", "c2", "c3", "c4"}},
		{"split", replay.Selection{Split: "calibration"}, []string{"c1", "c3", "c4"}},
		{"held out", replay.Selection{Split: "held_out"}, []string{"c2"}},
		{"representative", replay.Selection{Representative: true}, []string{"c1", "c4"}},
		{"primary", replay.Selection{Primary: true}, []string{"c1", "c2", "c4"}},
		{"sizes", replay.Selection{Sizes: []string{"m", "l"}}, []string{"c2", "c3", "c4"}},
		{"claude sessions", replay.Selection{Sessions: "claude_code"}, []string{"c1", "c4"}},
		{"codex sessions", replay.Selection{Sessions: "codex"}, []string{"c2", "c4"}},
		{"no sessions", replay.Selection{Sessions: "none"}, []string{"c3"}},
		{"limit", replay.Selection{Limit: 2}, []string{"c1", "c2"}},
		{"cases", replay.Selection{IDs: []string{"c4", "c2", "c4"}}, []string{"c2", "c4"}},
		{"combined", replay.Selection{Split: "calibration", Sizes: []string{"m", "l"}, Sessions: "claude_code"}, []string{"c4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ds.Select(tc.sel)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Errorf("got %v (%v), want %v", got, err, tc.want)
			}
		})
	}
	t.Run("shuffle is deterministic", func(t *testing.T) {
		a, _ := ds.Select(replay.Selection{ShuffleSeed: &seed})
		b, _ := ds.Select(replay.Selection{ShuffleSeed: &seed})
		other := uint64(4)
		c, _ := ds.Select(replay.Selection{ShuffleSeed: &other, Limit: 2})
		if !slices.Equal(a, b) || len(a) != 4 || len(c) != 2 {
			t.Errorf("%v %v %v", a, b, c)
		}
	})
	for name, sel := range map[string]replay.Selection{
		"split":         {Split: "dev"},
		"sessions":      {Sessions: "cursor"},
		"cases+filters": {IDs: []string{"c1"}, Split: "calibration"},
	} {
		if _, err := ds.Select(sel); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func execute(t *testing.T, p replay.Plan) (replay.Summary, string, error) {
	t.Helper()
	var out bytes.Buffer
	p.Out = &out
	if p.OutDir == "" {
		p.OutDir = t.TempDir()
	}
	if p.Name == "" {
		p.Name = "run"
	}
	summary, err := replay.Execute(ctx(), p)
	return summary, out.String(), err
}

func TestExecuteWritesTheRun(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", nil)
	d.addCase(t, "c2", nil)
	ds, _ := replay.OpenDataset(d.dir)
	backend := &generatetest.Backend{Version: "9.9.9", RunFunc: generatetest.StagedReplies(nil)}
	override := "Replace the outline guidance.\n"
	settings := staged(backend)
	settings.PromptOverrides = prompt.Overrides{prompt.IDReviewStagedOutline: override}
	outDir := t.TempDir()
	summary, printed, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: settings, Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Totals.OK != 2 || summary.Totals.Calls == 0 || len(summary.FailuresByReason) != 0 {
		t.Errorf("%+v", summary.Totals)
	}
	for _, want := range []string{"c1", "c2", "TOTAL", "2 ok"} {
		if !strings.Contains(printed, want) {
			t.Errorf("output lacks %q:\n%s", want, printed)
		}
	}
	var manifest replay.Manifest
	raw, err := os.ReadFile(filepath.Join(outDir, "run", "manifest.json"))
	if err != nil || json.Unmarshal(raw, &manifest) != nil {
		t.Fatal(err)
	}
	if manifest.Backend != "claude" || manifest.BackendVersion != "9.9.9" || !manifest.Staged || !strings.HasSuffix(manifest.PromptVersion, prompt.CustomSuffix) ||
		manifest.PromptDigest == "" || len(manifest.PromptOverrides) != 1 || manifest.PromptOverrides[0].ID != "review-staged-outline" || manifest.DatasetIndexSHA256 == "" ||
		!slices.Equal(manifest.CaseIDs, []string{"c1", "c2"}) || manifest.ToolCommit == "" {
		t.Errorf("manifest %+v", manifest)
	}
	for _, id := range []string{"c1", "c2"} {
		out, err := replay.ReadOutcome(filepath.Join(outDir, "run", "cases", id))
		if err != nil || !out.OK() || out.PromptVersion != manifest.PromptVersion {
			t.Errorf("%s: %v %+v", id, err, out)
		}
		if _, err := os.Stat(filepath.Join(outDir, "run", "cases", id, "document.json")); err != nil {
			t.Error(err)
		}
	}
	if _, err := os.Stat(filepath.Join(outDir, "run", "summary.json")); err != nil {
		t.Error(err)
	}
	// The override's text is what the model was sent.
	if !slices.ContainsFunc(backend.Requests, func(r agent.Request) bool { return strings.Contains(r.Prompt, strings.TrimSpace(override)) }) {
		t.Error("no request carried the override")
	}
}

func TestExecuteReportsABrokenCaseAndGoesOn(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", nil)
	d.addCase(t, "c2", nil)
	_ = os.Remove(filepath.Join(d.dir, "cases", "c1", "bundle.json"))
	ds, _ := replay.OpenDataset(d.dir)
	backend := &generatetest.Backend{RunFunc: generatetest.StagedReplies(nil)}
	summary, printed, err := execute(t, replay.Plan{Dataset: ds, Settings: staged(backend)})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Totals.OK != 1 || summary.Totals.Failed != 1 || summary.FailuresByReason["case"] != 1 || !strings.Contains(printed, "failures by reason: case: 1") {
		t.Errorf("%+v\n%s", summary, printed)
	}
}

func TestResumeSkipsCasesThatSucceeded(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", nil)
	d.addCase(t, "c2", nil)
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	// The first run's second case fails: the backend breaks after one staged case.
	failing := true
	backend := &generatetest.Backend{RunFunc: func(cx context.Context, r agent.Request, n int) (agent.Result, error) {
		if failing && n >= 3 {
			return agent.Result{Calls: 1}, os.ErrDeadlineExceeded
		}
		return generatetest.StagedReplies(nil)(cx, r, n)
	}}
	first, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: staged(backend)})
	if err != nil || first.Totals.OK != 1 || first.Totals.Failed != 1 {
		t.Fatalf("%v %+v", err, first.Totals)
	}
	failing = false
	calls := len(backend.Requests)
	second, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Resume: true, Settings: staged(backend)})
	if err != nil {
		t.Fatal(err)
	}
	if second.Totals.Resumed != 1 || second.Totals.OK != 1 || second.Totals.Cases != 2 {
		t.Errorf("%+v", second.Totals)
	}
	if ran := len(backend.Requests) - calls; ran != 3 {
		t.Errorf("the second run made %d model calls, want the 3 of the one staged case that failed", ran)
	}
	// Without --resume the finished case runs again.
	third, _, _ := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, Settings: staged(backend)})
	if third.Totals.Resumed != 0 || third.Totals.OK != 2 {
		t.Errorf("%+v", third.Totals)
	}
}

func TestCostGuardStopsStartingCases(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	for _, id := range []string{"c1", "c2", "c3"} {
		d.addCase(t, id, nil)
	}
	ds, _ := replay.OpenDataset(d.dir)
	backend := &generatetest.Backend{RunFunc: pricedReplies(nil)}
	summary, printed, err := execute(t, replay.Plan{Dataset: ds, Settings: staged(backend), MaxCostUSD: 0.01})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Totals.OK != 1 || summary.Totals.Skipped != 2 || !strings.Contains(summary.Stopped, "cost guard") || summary.Totals.CostUSD <= 0.01 {
		t.Errorf("%+v stopped=%q\n%s", summary.Totals, summary.Stopped, printed)
	}
	if !strings.Contains(printed, "stopped: cost guard") {
		t.Errorf("the table does not say why it stopped:\n%s", printed)
	}
}

func TestMaxCasesRefusesALargeSelection(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	for _, id := range []string{"c1", "c2", "c3"} {
		d.addCase(t, id, nil)
	}
	ds, _ := replay.OpenDataset(d.dir)
	var made atomic.Int32
	_, _, err := execute(t, replay.Plan{Dataset: ds, MaxCases: 2, Settings: replay.Settings{Staged: true},
		NewRunner: func(context.Context, replay.Settings) (*replay.Runner, error) { made.Add(1); return nil, os.ErrInvalid }})
	if err == nil || !strings.Contains(err.Error(), "--max-cases 2") || made.Load() != 0 {
		t.Errorf("error %v, runners %d", err, made.Load())
	}
}

func TestDryRunCallsNoModel(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", nil)
	d.addCase(t, "c2", nil)
	_ = os.Remove(filepath.Join(d.dir, "cases", "c2", "case.json"))
	ds, _ := replay.OpenDataset(d.dir)
	outDir := t.TempDir()
	made := false
	_, printed, err := execute(t, replay.Plan{Dataset: ds, OutDir: outDir, DryRun: true, Settings: replay.Settings{Staged: true},
		NewRunner: func(context.Context, replay.Settings) (*replay.Runner, error) { made = true; return nil, os.ErrInvalid }})
	if err != nil || made {
		t.Fatalf("%v, runner built: %v", err, made)
	}
	if !strings.Contains(printed, "2 case(s) would run") || !strings.Contains(printed, "c1\n") || !strings.Contains(printed, "would fail to load") {
		t.Errorf("output:\n%s", printed)
	}
	if entries, _ := os.ReadDir(outDir); len(entries) != 0 {
		t.Errorf("a dry run wrote %d entries", len(entries))
	}
}

func TestSettingsAreChecked(t *testing.T) {
	backend := &generatetest.Backend{}
	for name, tc := range map[string]struct {
		settings replay.Settings
		want     string
	}{
		"one pass has no prompts": {replay.Settings{Backends: fakeBackends(backend), PromptOverrides: prompt.Overrides{prompt.IDReviewStagedOutline: "x\n"}}, "needs a staged run"},
		"unknown prompt":          {replay.Settings{Staged: true, Backends: fakeBackends(backend), PromptOverrides: prompt.Overrides{"nope": "x\n"}}, "not a staged review prompt"},
		"fix prompt":              {replay.Settings{Staged: true, Backends: fakeBackends(backend), PromptOverrides: prompt.Overrides{prompt.IDFix: "x\n"}}, "not a staged review prompt"},
		"empty prompt":            {replay.Settings{Staged: true, Backends: fakeBackends(backend), PromptOverrides: prompt.Overrides{prompt.IDReviewStagedOutline: " "}}, "must not be empty"},
		"effort":                  {replay.Settings{Backends: fakeBackends(backend), Effort: "huge"}, "effort"},
		"cursor":                  {replay.Settings{Backends: fakeBackends(backend), Backend: config.BackendCursorAgent}, "does not run jobs"},
		"cap":                     {replay.Settings{Backends: fakeBackends(backend), Caps: map[string]int{"nope": 1}}, "unknown cap"},
		"broken backend":          {replay.Settings{Backends: fakeBackends(&generatetest.Backend{CheckErr: os.ErrNotExist})}, "file does not exist"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := replay.NewRunner(ctx(), tc.settings); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAFailedGenerationIsAnOutcome(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	c, err := ds.LoadCase(ctx(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	backend := &generatetest.Backend{RunFunc: generatetest.StagedReplies(func(stage string, _ agent.Request) (agent.Result, error, bool) {
		return agent.Result{Calls: 1}, os.ErrPermission, stage == agent.StageOutline
	})}
	runner, err := replay.NewRunner(ctx(), staged(backend))
	if err != nil {
		t.Fatal(err)
	}
	out, err := runner.Run(ctx(), c)
	if err != nil || out.OK() || out.Reason != string(generate.CategoryBackend) || out.Error == "" || out.Document != nil {
		t.Errorf("%v %+v", err, out)
	}
}

func TestFailedSectionsAreReported(t *testing.T) {
	src := assembleSource(t)
	d := newDataset(t, src)
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	backend := &generatetest.Backend{RunFunc: generatetest.StagedReplies(generatetest.WithoutSection("sec_rest"))}
	summary, printed, err := execute(t, replay.Plan{Dataset: ds, Settings: staged(backend)})
	if err != nil || summary.Totals.OK != 1 || !slices.Equal(summary.Rows[0].FailedSections, []string{"sec_rest"}) || !strings.Contains(printed, "sec_rest") {
		t.Errorf("%v %+v\n%s", err, summary.Rows, printed)
	}
}
