package generate_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/sessions"
)

// TestFakeCursorAgentProcess is the fake cursor-agent: the scripts
// InstallFakeCursorAgent writes run this test binary with only this test.
func TestFakeCursorAgentProcess(t *testing.T) { generatetest.RunFakeCursorAgent(t) }

// cursorAgentOptions runs the real cursor-agent backend, as DefaultBackends
// builds it, against a fake cursor-agent on PATH that follows passes.
func cursorAgentOptions(t *testing.T, f *fixture, passes ...generatetest.FakeCursorPass) (generate.Options, func() []generatetest.FakeCursorRun) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shell binaries")
	}
	dir, runs := generatetest.InstallFakeCursorAgent(t, "TestFakeCursorAgentProcess", passes...)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	backend := generate.DefaultBackendsFor(config.Config{}).CursorAgent("")
	version, err := backend.Check(context.Background())
	if err != nil || version != "2026.10.16" {
		t.Fatalf("check: %q %v", version, err)
	}
	options := f.options(t)
	options.Backend, options.BackendVersion, options.Model = backend, version, ""
	options.Budgets = agent.Budgets{WallClock: time.Minute, MaxCalls: 10, MaxTokens: 100000, MaxProviderAttempts: 1, MaxRepairAttempts: 2}
	return options, runs
}

func TestCursorAgentBackendGeneratesARepairedRedactedDocument(t *testing.T) {
	f := newFixture(t)
	f.repo.Dirty(t)
	t.Setenv("CURSOR_API_KEY", "key_cursor_must_not_pass")
	t.Setenv("GITHUB_TOKEN", "ghp_must_not_pass")
	options, runs := cursorAgentOptions(t, f,
		// A fenced answer with a ref to an item the section does not have: the
		// validator rejects it.
		generatetest.FakeCursorPass{AttentionRefs: []string{"rsk_absent"}, Fence: true},
		// The repair is valid, with a secret in free text.
		generatetest.FakeCursorPass{SectionSummary: "The change reads " + assembletest.PlantedSecret + " from the settings."})
	options.DiagnosticsOut = filepath.Join(t.TempDir(), "diagnostics.json")
	versions, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	result, err := generate.Publish(context.Background(), versions, key, options, nil)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, f.progress.String())
	}

	got := runs()
	if len(got) != 2 {
		t.Fatalf("%d cursor-agent runs", len(got))
	}
	for i, run := range got {
		// Self-run exclusion: every prompt, the repair's too, starts with the
		// marker, and the run directory carries the prefix session discovery
		// skips.
		if !strings.HasPrefix(run.Prompt, sessions.GenerationRunMarker) {
			t.Errorf("run %d: the prompt does not start with the generation-run marker: %.80q", i, run.Prompt)
		}
		if !strings.HasPrefix(filepath.Base(run.Directory), agent.RunDirPrefix) {
			t.Errorf("run %d: directory %s lacks the prefix %s", i, run.Directory, agent.RunDirPrefix)
		}
		if !strings.Contains(run.Prompt, "Output schema (JSON Schema).") || !strings.Contains(run.Prompt, `"$schema"`) && !strings.Contains(run.Prompt, `"properties"`) {
			t.Errorf("run %d: the prompt does not carry the output schema", i)
		}
		if !slices.Contains(run.Args, "--mode") || !slices.Contains(run.Args, "ask") || !slices.Contains(run.Args, "--sandbox") || !slices.Contains(run.Args, "enabled") {
			t.Errorf("run %d: args %v", i, run.Args)
		}
		for _, entry := range run.Env {
			if strings.Contains(entry, "must_not_pass") {
				t.Errorf("run %d: the child environment holds %s", i, entry)
			}
		}
	}
	if !strings.Contains(got[1].Prompt, "rsk_absent") || !strings.Contains(got[1].Prompt, "The validator found") || !strings.Contains(got[1].Prompt, "final message must be the entire corrected document") {
		t.Errorf("the repair prompt lacks the rejected document, its findings or the final-message wording")
	}

	validator, err := review.NewDocumentValidator()
	if err != nil {
		t.Fatal(err)
	}
	if findings := validator.ValidateStored(result.Document, nil); len(findings) > 0 {
		t.Errorf("the stored document is invalid: %v", findings)
	}
	if strings.Contains(string(result.Document), assembletest.PlantedSecret) || !strings.Contains(string(result.Document), "The change reads") {
		t.Errorf("the free text was not masked in place")
	}
	var document struct {
		Review struct {
			Generator struct{ Model, PromptVersion string } `json:"generator"`
		} `json:"review"`
	}
	if err := json.Unmarshal(result.Document, &document); err != nil || document.Review.Generator.Model != "Fake Sonnet" {
		t.Errorf("generator = %+v %v", document.Review.Generator, err)
	}
	if result.Metadata.Backend != agent.BackendCursorAgent {
		t.Errorf("metadata = %+v", result.Metadata)
	}

	outcome := result.Outcome
	// Each run: one identified model call and the final answer, and
	// 1000 input + 300 output + 200 cache write charged tokens.
	if outcome.ValidationRepairs != 1 || outcome.Calls != 4 || outcome.Tokens != 3000 {
		t.Errorf("repairs %d calls %d tokens %d", outcome.ValidationRepairs, outcome.Calls, outcome.Tokens)
	}
	a := outcome.Accounting
	if a == nil || len(a.Attempts) != 2 || a.Usage.Unreported || a.Usage.Input != 2000 || a.Usage.CacheRead != 4000 || a.Usage.CacheCreate != 400 {
		t.Fatalf("accounting = %+v", a)
	}
	// Cursor bills through the user's subscription and reports neither a
	// provider model ID nor the speed: the cost stays unknown.
	if cost, reason := pricing.Embedded().AccountingCost(a); cost != nil || reason == "" {
		t.Errorf("cost = %v (%s), want unknown", cost, reason)
	}
	if result.Metadata.RecordedCostUSD != nil || result.Metadata.CostReason != pricing.ReasonBilledByCursor {
		t.Errorf("recorded cost = %v (%s), want unknown, billed by Cursor", result.Metadata.RecordedCostUSD, result.Metadata.CostReason)
	}

	diagnostics, err := os.ReadFile(options.DiagnosticsOut)
	if err != nil || strings.Contains(string(diagnostics), assembletest.PlantedSecret) {
		t.Fatalf("diagnostics: %v", err)
	}
	for _, want := range []string{`"tool": "readToolCall"`, `"path": "bundle.json"`, `"kind": "document"`} {
		if !strings.Contains(string(diagnostics), want) {
			t.Errorf("diagnostics lack %s", want)
		}
	}
	f.requireClean(t)
}

func TestCursorAgentBackendFailures(t *testing.T) {
	t.Run("a tool that does not read stops the run", func(t *testing.T) {
		f := newFixture(t)
		f.repo.Dirty(t)
		options, runs := cursorAgentOptions(t, f, generatetest.FakeCursorPass{Tool: "shellToolCall"})
		_, err := generate.Run(context.Background(), options)
		// The loop reports a failed attempt as the provider's; the progress
		// says why.
		if err == nil || generate.CategoryOf(err) != generate.CategoryBackend || !strings.Contains(f.progress.String(), "attempt 1 failed (disallowed_tool)") {
			t.Fatalf("err = %v\n%s", err, f.progress.String())
		}
		if len(runs()) != 1 {
			t.Errorf("%d runs", len(runs()))
		}
		f.requireClean(t)
	})
	t.Run("output that is not JSON is repaired within the budget, then rejected", func(t *testing.T) {
		f := newFixture(t)
		f.repo.Dirty(t)
		options, runs := cursorAgentOptions(t, f, generatetest.FakeCursorPass{Text: "I could not finish the review.", NoUsage: true})
		result, err := generate.Run(context.Background(), options)
		if err == nil || generate.CategoryOf(err) != generate.CategoryValidation {
			t.Fatalf("err = %v", err)
		}
		if len(runs()) != 3 || result.Outcome.ValidationRepairs != 2 || !result.Outcome.Usage.Unreported {
			t.Errorf("runs %d outcome %+v", len(runs()), result.Outcome)
		}
	})
}
