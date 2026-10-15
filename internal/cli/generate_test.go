package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/review"
)

// generateEnv is bundleEnv with a stub backend in place of the local CLIs.
func generateEnv(t *testing.T) (*generatetest.Backend, []string) {
	t.Helper()
	repo, _, stores := bundleEnv(t)
	repo.Dirty(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	stub := &generatetest.Backend{Version: "9.9.9"}
	old := generateBackends
	generateBackends = func() generate.Backends {
		return generate.Backends{
			Claude: func(string) generate.CheckedBackend { return stub },
			Codex:  func(string) generate.CheckedBackend { return &generatetest.Backend{BackendName: "codex"} },
		}
	}
	t.Cleanup(func() { generateBackends = old })
	return stub, stores
}

// writeConfig replaces the config file bundleEnv points XDG_CONFIG_HOME at.
func writeConfig(t *testing.T, content string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "baum")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(dir, "config.toml")) })
}

func TestGenerateWritesOnlyTheDocumentToStdout(t *testing.T) {
	stub, stores := generateEnv(t)
	stdout, stderr, err := run(t, append([]string{"generate", "--pr", "7"}, stores...)...)
	if err != nil {
		t.Fatalf("generate: %v\n%s", err, stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil || decoded["schema"] != "baum.review/1" {
		t.Fatalf("stdout is not the document: %v\n%.300s", err, stdout)
	}
	if strings.Count(stdout, "\n") != 1 || !strings.HasSuffix(stdout, "}\n") {
		t.Error("stdout is not exactly one JSON document and a newline")
	}
	validator, err := review.NewDocumentValidator()
	if err != nil {
		t.Fatal(err)
	}
	if findings := validator.ValidateStored([]byte(stdout), nil); len(findings) > 0 {
		t.Errorf("the stored document is invalid: %v", findings)
	}
	for _, want := range []string{"assembling the bundle", "running claude 9.9.9", "generated in", "baum view"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, `"schema"`) {
		t.Error("the document was written to stderr")
	}
	if len(stub.Requests) != 1 {
		t.Errorf("backend ran %d times", len(stub.Requests))
	}
	entries, _ := os.ReadDir(os.TempDir())
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), agent.RunDirPrefix) || strings.HasPrefix(entry.Name(), "baum-generate-") {
			t.Errorf("left behind: %s", entry.Name())
		}
	}
}

func TestGenerateOutputFileIsAtomicPrivateAndHinted(t *testing.T) {
	_, stores := generateEnv(t)
	path := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := run(t, append([]string{"generate", "--pr", "7", "-o", path}, stores...)...)
	if err != nil {
		t.Fatalf("generate: %v\n%s", err, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %.100q, want nothing", stdout)
	}
	written, err := os.ReadFile(path)
	if err != nil || !json.Valid(written) || strings.Contains(string(written), "stale") {
		t.Fatalf("file: %v %.100s", err, written)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", info.Mode().Perm())
		}
	}
	if !strings.Contains(stderr, "view it with: baum view "+path) {
		t.Errorf("stderr lacks the view hint:\n%s", stderr)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temporary files left beside the output: %v", entries)
	}
}

func TestGenerateFailuresKeepTheirExitCodesAndWriteNothing(t *testing.T) {
	stub, stores := generateEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "review.json")
	cases := []struct {
		name string
		args []string
		edit func()
		code int
		want string
	}{
		{"unknown flag", []string{"--nope"}, nil, ExitInput, "unknown flag"},
		{"negative pr", []string{"--pr", "-3"}, nil, ExitInput, "pull request number"},
		{"missing output directory", []string{"--pr", "7", "-o", filepath.Join(dir, "absent", "r.json")}, nil, ExitInput, "directory does not exist"},
		{"anthropic backend", []string{"--pr", "7", "--backend", "anthropic"}, nil, ExitBackend, "not available in this version"},
		{"cursor-agent backend", []string{"--pr", "7", "--backend", "cursor-agent"}, nil, ExitBackend, "not available in this version"},
		{"unknown backend", []string{"--pr", "7", "--backend", "gpt"}, nil, ExitInput, "unknown backend"},
		{"provider down", []string{"--pr", "7"}, func() {
			stub.RunFunc = func(context.Context, agent.Request, int) (agent.Result, error) {
				return agent.Result{}, errors.New("503")
			}
		}, ExitBackend, agent.ReasonProviderUnavailable},
		{"pass timeout", []string{"--pr", "7"}, func() {
			writeConfig(t, "[budgets]\npass_timeout = \"50ms\"\n")
			stub.RunFunc = func(ctx context.Context, _ agent.Request, _ int) (agent.Result, error) {
				<-ctx.Done()
				return agent.Result{}, ctx.Err()
			}
		}, ExitBudget, agent.GenerationBudgetExceededReason("pass_timeout")},
		{"rejected", []string{"--pr", "7"}, func() {
			stub.RunFunc = func(context.Context, agent.Request, int) (agent.Result, error) {
				return agent.Result{Document: []byte(`{"schema":"baum.review/1"}`), Model: "m", Calls: 1, Tokens: 1}, nil
			}
		}, ExitValidation, "validation rejected the document"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub.RunFunc = nil
			if c.edit != nil {
				c.edit()
			}
			args := append(append([]string{"generate"}, c.args...), stores...)
			if c.name == "rejected" || c.name == "provider down" || c.name == "pass timeout" {
				args = append(args, "-o", path)
			}
			stdout, _, err := run(t, args...)
			if err == nil || ExitCode(err) != c.code {
				t.Fatalf("err = %v, exit %d, want %d", err, ExitCode(err), c.code)
			}
			if c.want != "" && !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
			if stdout != "" {
				t.Errorf("stdout = %.100q", stdout)
			}
			if _, statErr := os.Stat(path); statErr == nil {
				t.Error("a failed run wrote the output file")
			}
		})
	}
}

func TestExitCodes(t *testing.T) {
	if ExitCode(nil) != 0 || ExitCode(os.ErrNotExist) != ExitFailure {
		t.Error("plain errors exit 1 and nil exits 0")
	}
	for category, code := range map[generate.Category]int{
		generate.CategoryInput: ExitInput, generate.CategoryBackend: ExitBackend, generate.CategoryBudget: ExitBudget,
		generate.CategoryValidation: ExitValidation, generate.CategoryInterrupted: ExitInterrupted,
	} {
		if got := ExitCode(&generate.Error{Category: category, Err: os.ErrClosed}); got != code {
			t.Errorf("%s exits %d, want %d", category, got, code)
		}
	}
}

func TestGenerateInterruptedExitsWithItsCode(t *testing.T) {
	stub, stores := generateEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	old := bundleContext
	bundleContext = func(context.Context) (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { bundleContext = old })
	stub.RunFunc = func(ctx context.Context, _ agent.Request, _ int) (agent.Result, error) {
		cancel()
		<-ctx.Done()
		return agent.Result{}, ctx.Err()
	}
	_, _, err := run(t, append([]string{"generate", "--pr", "7"}, stores...)...)
	if ExitCode(err) != ExitInterrupted {
		t.Fatalf("err = %v, exit %d", err, ExitCode(err))
	}
}

func TestGenerateDiagnosticsForEarlyFailures(t *testing.T) {
	_, stores := generateEnv(t)
	path := filepath.Join(t.TempDir(), "diagnostics.json")
	_, _, err := run(t, append([]string{"generate", "--pr", "7", "--backend", "anthropic", "--diagnostics", path}, stores...)...)
	if ExitCode(err) != ExitBackend {
		t.Fatalf("err = %v", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil || !strings.Contains(string(raw), `"category": "backend"`) {
		t.Errorf("diagnostics: %v\n%s", readErr, raw)
	}
}

func TestGenerationStoresHistoryAndFeedsPreviousToTheNextRun(t *testing.T) {
	stub, stores := generateEnv(t)
	root := filepath.Join(t.TempDir(), "custom versions")
	args := append([]string{"generate", "--pr", "7", "--store", root}, stores...)
	var inputs []bundle.Bundle
	stub.RunFunc = func(ctx context.Context, req agent.Request, n int) (agent.Result, error) {
		raw, err := os.ReadFile(filepath.Join(req.Directory, "bundle.json"))
		if err != nil {
			return agent.Result{}, err
		}
		var b bundle.Bundle
		if err := json.Unmarshal(raw, &b); err != nil {
			return agent.Result{}, err
		}
		inputs = append(inputs, b)
		doc, err := generatetest.Document(req.Directory, nil)
		return agent.Result{Document: doc, Model: "stub-model", Calls: 1, Tokens: 100}, err
	}
	first, stderr, err := run(t, args...)
	if err != nil {
		t.Fatalf("first: %v %s", err, stderr)
	}
	second, stderr, err := run(t, args...)
	if err != nil {
		t.Fatalf("second: %v %s", err, stderr)
	}
	if !strings.Contains(stderr, "--store ") {
		t.Fatal("store override missing from reopen hint")
	}
	if len(inputs) != 2 || inputs[0].Previous != nil || inputs[1].Previous == nil || inputs[1].Previous.Version != 1 {
		t.Fatalf("previous not supplied: %+v", inputs)
	}
	previous := inputs[1].Previous.Sections[0]
	if previous.ID != "sec_changes" || strings.Contains(string(previous.Content), `"derived"`) || !strings.Contains(string(previous.Content), `"introduced_version":1`) {
		t.Fatalf("previous lost history or kept derived fields: %s", previous.Content)
	}
	var completed struct {
		Review struct {
			Version         int
			PreviousVersion *int `json:"previous_version"`
		}
		Sections []struct {
			ID    string
			Risks []struct {
				ID         string
				Introduced int `json:"introduced_version"`
			}
		}
	}
	if err := json.Unmarshal([]byte(second), &completed); err != nil {
		t.Fatal(err)
	}
	if completed.Review.Version != 2 || completed.Review.PreviousVersion == nil || *completed.Review.PreviousVersion != 1 || completed.Sections[0].ID != "sec_changes" || completed.Sections[0].Risks[0].Introduced != 1 {
		t.Fatalf("unstable section/risk history: %+v", completed)
	}
	versions, err := store.Open("", root, false)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: "acme/widgets", Number: 7}
	// Repository is derived from the actual generated document, never the path.
	var identity struct{ Review struct{ Repository string } }
	if err := json.Unmarshal([]byte(first), &identity); err != nil {
		t.Fatal(err)
	}
	key.Repository = identity.Review.Repository
	stored, err := versions.Get(context.Background(), key, 1)
	if err != nil || string(stored.Document)+"\n" != first {
		t.Fatalf("version one replaced: %v", err)
	}
	metadata, err := versions.Versions(context.Background(), key)
	if err != nil || len(metadata) != 2 {
		t.Fatalf("versions %+v %v", metadata, err)
	}
	m := metadata[0]
	if m.Head == "" || m.Base == "" || m.MergeBase == "" || len(m.BundleDigest) != 64 || m.Backend != "claude" || m.BackendVersion != "9.9.9" || m.Model != "stub-model" || m.Schema != "baum.review/1" || m.SchemaRevision == "" || m.PromptVersion == "" || len(m.PromptDigest) != 64 || m.GeneratedAt == "" || m.Calls != 1 || m.Tokens != 100 {
		t.Fatalf("incomplete metadata: %+v", m)
	}
	for _, version := range []string{"1", "2"} {
		_, stderr, err := run(t, "validate", "--pr", "7", "--repo", key.Repository, "--store", root, "--version", version)
		if err != nil {
			t.Fatalf("validate stored %s: %v %s", version, err, stderr)
		}
	}
	// Serving an immutable selected version needs no live GitHub lookup.
	_, stop := stubViewSeams(t)
	stop()
	if _, stderr, err := run(t, "view", "--pr", "7", "--repo", key.Repository, "--store", root, "--version", "1", "--no-open"); err != nil {
		t.Fatalf("view stored: %v %s", err, stderr)
	}
	stub.RunFunc = func(context.Context, agent.Request, int) (agent.Result, error) {
		return agent.Result{}, errors.New("provider unavailable")
	}
	if _, _, err := run(t, args...); err == nil {
		t.Fatal("failed run succeeded")
	}
	after, err := versions.Versions(context.Background(), key)
	if err != nil || len(after) != 2 {
		t.Fatalf("failed run published: %+v %v", after, err)
	}
}

func TestGenerateEnterpriseDocumentStoresAndReopensWithHostKey(t *testing.T) {
	_, sessions := generateEnv(t)
	root := filepath.Join(t.TempDir(), "reviews")
	repository := "acme.ghe.com/acme/demo"
	stdout, stderr, err := run(t, append([]string{"generate", "--pr", "7", "--repo", repository, "--store", root}, sessions...)...)
	if err != nil {
		t.Fatalf("generate Enterprise review: %v\n%s", err, stderr)
	}
	var doc struct{ Review struct{ Repository string } }
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil || doc.Review.Repository != "acme/demo" {
		t.Fatalf("document repository = %q, error %v", doc.Review.Repository, err)
	}
	versions, err := store.Open("", root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := versions.Latest(context.Background(), store.Key{Repository: repository, Number: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := versions.Latest(context.Background(), store.Key{Repository: "acme/demo", Number: 7}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Enterprise review crossed host: %v", err)
	}
	if _, stderr, err := run(t, "validate", "--pr", "7", "--repo", repository, "--store", root); err != nil {
		t.Fatalf("reopen Enterprise review: %v\n%s", err, stderr)
	}
}
