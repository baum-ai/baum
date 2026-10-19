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
		generatetest.FakeCursorPass{AttentionRefs: []string{"rsk_absent"}, Fence: true, Split: 3},
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
		// The pull request's own .cursor (hooks.json, cli.json, mcp.json),
		// .claude and AGENTS.md stay inactive only because the checkout is
		// unpacked under head/ and merge-base/, not at the workspace root
		// --trust covers. A change to that layout must revisit SECURITY.md.
		if !slices.Contains(run.Root, "head") || !slices.Contains(run.Root, "merge-base") {
			t.Errorf("run %d: the checkout is not under head/ and merge-base/: root %v", i, run.Root)
		}
		for _, name := range run.Root {
			if strings.HasPrefix(name, ".") || strings.EqualFold(name, "AGENTS.md") || strings.EqualFold(name, "CLAUDE.md") {
				t.Errorf("run %d: the workspace root holds %s, which cursor-agent may load as configuration", i, name)
			}
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

// A turn the CLI retries starts its answer over: the text before the retry is
// not joined to the answer, which is valid on the first pass.
func TestCursorAgentBackendRetriedTurn(t *testing.T) {
	f := newFixture(t)
	f.repo.Dirty(t)
	options, runs := cursorAgentOptions(t, f, generatetest.FakeCursorPass{Retry: true, Split: 2})
	result, err := generate.Run(context.Background(), options)
	if err != nil || len(runs()) != 1 || result.Outcome.ValidationRepairs != 0 {
		t.Fatalf("err = %v, runs = %d, outcome = %+v\n%s", err, len(runs()), result.Outcome, f.progress.String())
	}
}

func TestCursorAgentBackendFailures(t *testing.T) {
	t.Run("a tool that does not read stops the run", func(t *testing.T) {
		f := newFixture(t)
		f.repo.Dirty(t)
		options, runs := cursorAgentOptions(t, f, generatetest.FakeCursorPass{Tool: "shellToolCall"})
		// A pull request that steers the model into a tool gets no second try.
		options.Budgets.MaxProviderAttempts = 3
		_, err := generate.Run(context.Background(), options)
		if err == nil || generate.CategoryOf(err) != generate.CategoryBackend || !strings.Contains(err.Error(), "shellToolCall") || !strings.Contains(f.progress.String(), "attempt 1 failed (disallowed_tool)") {
			t.Fatalf("err = %v\n%s", err, f.progress.String())
		}
		if len(runs()) != 1 {
			t.Errorf("attempted %d times, want once", len(runs()))
		}
		f.requireClean(t)
	})
	// A failure that every attempt would repeat is attempted once, though the
	// budget allows three, and its own message reaches the user.
	for name, tc := range map[string]struct{ stderr, status, want string }{
		"missing option":      {"error: unknown option '--mode'", "unsupported_option", "cursor-agent update"},
		"sandbox unavailable": {"Error: Sandbox mode is enabled but not available on this system.", "sandbox_unavailable", "only with its sandbox enabled"},
		"not signed in":       {"Error: Authentication required. Please run 'agent login' first.", "authentication_error", "run `cursor-agent login`"},
	} {
		t.Run(name+" is not retried", func(t *testing.T) {
			f := newFixture(t)
			f.repo.Dirty(t)
			options, runs := cursorAgentOptions(t, f, generatetest.FakeCursorPass{Stderr: tc.stderr})
			options.Budgets.MaxProviderAttempts = 3
			_, err := generate.Run(context.Background(), options)
			if err == nil || generate.CategoryOf(err) != generate.CategoryBackend || !strings.Contains(err.Error(), "cursor-agent cannot run") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v\n%s", err, f.progress.String())
			}
			if len(runs()) != 1 {
				t.Errorf("attempted %d times, want once", len(runs()))
			}
		})
	}
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
