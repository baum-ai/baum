package generate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lastpersonlabs/baum/agent"
	"github.com/lastpersonlabs/baum/internal/assemble"
	"github.com/lastpersonlabs/baum/internal/assemble/assembletest"
	"github.com/lastpersonlabs/baum/internal/generate"
	"github.com/lastpersonlabs/baum/internal/generate/generatetest"
	"github.com/lastpersonlabs/baum/internal/gitlocal"
	"github.com/lastpersonlabs/baum/sessions"
)

// fixture is a pipeline over the fixture clone with its run directories under
// a private temporary directory, so a test can see what is left behind.
type fixture struct {
	repo     assembletest.Repo
	tmp      string
	backend  *generatetest.Backend
	progress bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{repo: assembletest.NewRepo(t, assembletest.Options{}), tmp: t.TempDir(), backend: &generatetest.Backend{Version: "9.9.9"}}
	t.Setenv("TMPDIR", f.tmp)
	return f
}

func (f *fixture) options(t *testing.T) generate.Options {
	t.Helper()
	clone, err := gitlocal.Open(context.Background(), f.repo.Dir, gitlocal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clone.Close() })
	return generate.Options{
		Assembler: &assemble.Assembler{
			GitHub: &assembletest.GitHub{Evidence: f.repo.Evidence()}, Git: clone,
			Sessions: &assembletest.Sessions{Found: assembletest.SessionTree()},
			Images:   func(context.Context, string, int64) ([]byte, error) { return assembletest.PNG, nil },
		},
		Request: assemble.Request{Repository: assembletest.Repository, Number: assembletest.Number, Limits: assemble.DefaultLimits()},
		Backend: f.backend, BackendVersion: "9.9.9",
		Budgets:  agent.Budgets{WallClock: time.Minute, MaxCalls: 4, MaxTokens: 10000, MaxProviderAttempts: 1, MaxRepairAttempts: 2},
		Progress: &f.progress,
	}
}

// leftovers lists what the pipeline left in the temporary directory.
func (f *fixture) leftovers(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(f.tmp)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		// The clone's private repository belongs to the test, which closes it.
		if !strings.HasPrefix(entry.Name(), "baum-git-") {
			names = append(names, entry.Name())
		}
	}
	return names
}

func (f *fixture) requireClean(t *testing.T) {
	t.Helper()
	if left := f.leftovers(t); len(left) != 0 {
		t.Errorf("left in the temporary directory: %v", left)
	}
}

func TestValidFirstPassIsCompleteStrictAndClean(t *testing.T) {
	f := newFixture(t)
	f.repo.Dirty(t)
	before := f.repo.State(t)
	result, err := generate.Run(context.Background(), f.options(t))
	if err != nil {
		t.Fatalf("run: %v\n%s", err, f.progress.String())
	}
	var document struct {
		Review struct {
			Repository string `json:"repository"`
			Head       string `json:"head"`
			Generator  struct {
				Model          string `json:"model"`
				PromptVersion  string `json:"prompt_version"`
				SchemaRevision string `json:"schema_revision"`
			} `json:"generator"`
		} `json:"review"`
	}
	if err := json.Unmarshal(result.Document, &document); err != nil {
		t.Fatal(err)
	}
	generator := document.Review.Generator
	if document.Review.Repository != "acme/demo" || document.Review.Head != f.repo.Head ||
		generator.Model != "stub-model" || generator.PromptVersion != "review-public-v1" || generator.SchemaRevision == "" {
		t.Errorf("document review block = %+v", document.Review)
	}
	if strings.Contains(string(result.Document), assembletest.PlantedSecret) {
		t.Error("the planted secret is in the document")
	}
	if prompt := f.backend.Requests[0].Prompt; !strings.HasPrefix(prompt, sessions.GenerationRunMarker) {
		t.Errorf("the prompt does not start with the generation-run marker: %.80q", prompt)
	}
	if len(f.backend.Requests) != 1 || !f.backend.Existed[0] {
		t.Fatalf("requests=%d existed=%v", len(f.backend.Requests), f.backend.Existed)
	}
	dir := f.backend.Requests[0].Directory
	if !strings.HasPrefix(filepath.Base(dir), agent.RunDirPrefix) {
		t.Errorf("run directory %s lacks the prefix %s", dir, agent.RunDirPrefix)
	}
	if info, err := os.Stat(dir); err == nil {
		t.Errorf("run directory %s still exists: %v", dir, info.Mode())
	}
	f.requireClean(t)
	if after := f.repo.State(t); after != before {
		t.Errorf("the clone changed:\n%s\n--- after ---\n%s", before, after)
	}
	for _, want := range []string{"assembling the bundle for acme/demo#7", "running claude 9.9.9", "pass 1: the document is valid", "generated in"} {
		if !strings.Contains(f.progress.String(), want) {
			t.Errorf("progress lacks %q:\n%s", want, f.progress.String())
		}
	}
	if !strings.Contains(f.progress.String(), "100 token(s)") {
		t.Errorf("progress lacks the usage summary:\n%s", f.progress.String())
	}
	if strings.Contains(f.progress.String(), `"schema"`) {
		t.Error("the document leaked into progress")
	}
}

func TestInvalidThenRepaired(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = func(_ context.Context, request agent.Request, n int) (agent.Result, error) {
		document, err := generatetest.Document(request.Directory, func(d map[string]any) {
			if n == 0 {
				d["sections"] = []any{}
				d["summary"] = "not an object"
			}
		})
		return agent.Result{Document: document, Model: "stub-model", Calls: 1, Tokens: 50}, err
	}
	result, err := generate.Run(context.Background(), f.options(t))
	if err != nil {
		t.Fatalf("run: %v\n%s", err, f.progress.String())
	}
	if repair := f.backend.Requests[1].Prompt; !strings.HasPrefix(repair, sessions.GenerationRunMarker) {
		t.Errorf("the repair prompt does not start with the marker: %.80q", repair)
	}
	if result.Outcome.ValidationRepairs != 1 || len(f.backend.Requests) != 2 || len(result.Document) == 0 {
		t.Errorf("repairs=%d requests=%d", result.Outcome.ValidationRepairs, len(f.backend.Requests))
	}
	if !strings.Contains(f.progress.String(), "pass 1: the validator found problems") || !strings.Contains(f.progress.String(), "pass 2: the document is valid") {
		t.Errorf("progress:\n%s", f.progress.String())
	}
	f.requireClean(t)
}

func TestRejectedAfterRepairsIsValidation(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = func(_ context.Context, request agent.Request, _ int) (agent.Result, error) {
		return agent.Result{Document: []byte(`{"schema":"baum.review/1","summary":"nope"}`), Model: "stub-model", Calls: 1, Tokens: 10}, nil
	}
	options := f.options(t)
	options.DiagnosticsOut = filepath.Join(t.TempDir(), "diagnostics.json")
	result, err := generate.Run(context.Background(), options)
	if generate.CategoryOf(err) != generate.CategoryValidation {
		t.Fatalf("err = %v (%q)", err, generate.CategoryOf(err))
	}
	if result.Document != nil {
		t.Error("a rejected run returned a document")
	}
	if len(f.backend.Requests) != 3 {
		t.Errorf("requests = %d, want the first pass and two repairs", len(f.backend.Requests))
	}
	raw, readErr := os.ReadFile(options.DiagnosticsOut)
	if readErr != nil || !strings.Contains(string(raw), `"reason": "validation rejected the document"`) {
		t.Errorf("diagnostics: %v\n%s", readErr, raw)
	}
	f.requireClean(t)
}

func TestSecretInAnIdentifierIsRejected(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = func(_ context.Context, request agent.Request, _ int) (agent.Result, error) {
		document, err := generatetest.Document(request.Directory, func(d map[string]any) {
			section := d["sections"].([]any)[0].(map[string]any)
			section["id"] = "sec_" + assembletest.PlantedSecret
		})
		return agent.Result{Document: document, Model: "stub-model", Calls: 1, Tokens: 10}, err
	}
	options := f.options(t)
	options.DiagnosticsOut = filepath.Join(t.TempDir(), "diagnostics.json")
	result, err := generate.Run(context.Background(), options)
	if generate.CategoryOf(err) != generate.CategoryValidation {
		t.Fatalf("err = %v (%q)", err, generate.CategoryOf(err))
	}
	if result.Document != nil {
		t.Error("a document with a secret was returned")
	}
	if !strings.Contains(result.Outcome.Findings, agent.SecretFinding) || result.Outcome.IdentifierRedaction.MaskedClosed == 0 {
		t.Errorf("rejected for another reason: %+v", result.Outcome)
	}
	raw, _ := os.ReadFile(options.DiagnosticsOut)
	for name, text := range map[string]string{"error": err.Error(), "progress": f.progress.String(), "diagnostics": string(raw)} {
		if strings.Contains(text, assembletest.PlantedSecret) {
			t.Errorf("the secret is in the %s", name)
		}
	}
	f.requireClean(t)
}

func TestBudgetsAreTheBudgetCategory(t *testing.T) {
	t.Run("calls", func(t *testing.T) {
		f := newFixture(t)
		options := f.options(t)
		options.Budgets.MaxCalls = 1
		f.backend.RunFunc = func(_ context.Context, _ agent.Request, _ int) (agent.Result, error) {
			return agent.Result{Document: []byte(`{"schema":"baum.review/1"}`), Model: "m", Calls: 1, Tokens: 1}, nil
		}
		_, err := generate.Run(context.Background(), options)
		if generate.CategoryOf(err) != generate.CategoryBudget || !strings.Contains(err.Error(), "model calls") {
			t.Fatalf("err = %v", err)
		}
		f.requireClean(t)
	})
	t.Run("tokens", func(t *testing.T) {
		f := newFixture(t)
		options := f.options(t)
		f.backend.RunFunc = func(_ context.Context, _ agent.Request, _ int) (agent.Result, error) {
			return agent.Result{Model: "m", Calls: 1, Tokens: 20000}, errors.New("budget")
		}
		_, err := generate.Run(context.Background(), options)
		if generate.CategoryOf(err) != generate.CategoryBudget || !strings.Contains(err.Error(), "tokens") {
			t.Fatalf("err = %v", err)
		}
		f.requireClean(t)
	})
	t.Run("wall clock", func(t *testing.T) {
		f := newFixture(t)
		options := f.options(t)
		options.Budgets.WallClock = 50 * time.Millisecond
		f.backend.RunFunc = func(ctx context.Context, _ agent.Request, _ int) (agent.Result, error) {
			<-ctx.Done()
			return agent.Result{}, ctx.Err()
		}
		_, err := generate.Run(context.Background(), options)
		if generate.CategoryOf(err) != generate.CategoryBudget {
			t.Fatalf("err = %v (%q)", err, generate.CategoryOf(err))
		}
		f.requireClean(t)
	})
}

func TestProviderUnavailableIsBackend(t *testing.T) {
	f := newFixture(t)
	f.backend.RunFunc = func(context.Context, agent.Request, int) (agent.Result, error) {
		return agent.Result{}, errors.New("503")
	}
	_, err := generate.Run(context.Background(), f.options(t))
	if generate.CategoryOf(err) != generate.CategoryBackend || err.Error() != agent.ReasonProviderUnavailable {
		t.Fatalf("err = %v (%q)", err, generate.CategoryOf(err))
	}
	f.requireClean(t)
}

func TestCancellationRemovesTheRunDirectory(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	var directory string
	f.backend.RunFunc = func(ctx context.Context, request agent.Request, _ int) (agent.Result, error) {
		directory = request.Directory
		cancel()
		<-ctx.Done()
		return agent.Result{}, ctx.Err()
	}
	result, err := generate.Run(ctx, f.options(t))
	if generate.CategoryOf(err) != generate.CategoryInterrupted {
		t.Fatalf("err = %v (%q)", err, generate.CategoryOf(err))
	}
	if result.Document != nil {
		t.Error("an interrupted run returned a document")
	}
	if _, statErr := os.Stat(directory); !os.IsNotExist(statErr) {
		t.Errorf("run directory %s: %v", directory, statErr)
	}
	f.requireClean(t)
}

func TestKeepRunDirKeepsItAndBundleOutIsTheBundle(t *testing.T) {
	f := newFixture(t)
	options := f.options(t)
	out := t.TempDir()
	options.KeepRunDir, options.BundleOut = true, filepath.Join(out, "bundle.json")
	result, err := generate.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The unpacked trees are read-only.
		_ = filepath.WalkDir(result.RunDir, func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
	})
	if !strings.HasPrefix(filepath.Base(result.RunDir), agent.RunDirPrefix) || !strings.Contains(f.progress.String(), result.RunDir) {
		t.Errorf("run dir %q, progress:\n%s", result.RunDir, f.progress.String())
	}
	given, err := os.ReadFile(filepath.Join(result.RunDir, "bundle.json"))
	if err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(options.BundleOut)
	if err != nil || !bytes.Equal(bytes.TrimSpace(written), bytes.TrimSpace(given)) {
		t.Errorf("--bundle-out is not the bundle the model was given: %v", err)
	}
	// The archives are gone either way.
	for _, name := range f.leftovers(t) {
		if !strings.HasPrefix(name, agent.RunDirPrefix) {
			t.Errorf("left %s", name)
		}
	}
}

func TestInputFailuresAreTheInputCategory(t *testing.T) {
	f := newFixture(t)
	options := f.options(t)
	options.Assembler = &assemble.Assembler{GitHub: &assembletest.GitHub{Err: errors.New("boom")}, Git: options.Assembler.(*assemble.Assembler).Git}
	_, err := generate.Run(context.Background(), options)
	if generate.CategoryOf(err) != generate.CategoryInput {
		t.Fatalf("err = %v", err)
	}
	if len(f.backend.Requests) != 0 {
		t.Error("the backend ran without a bundle")
	}
	f.requireClean(t)
}
