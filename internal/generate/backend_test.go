package generate_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent/anthropic"
	"github.com/baum-ai/baum/agent/claude"
	"github.com/baum-ai/baum/agent/codex"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
)

func fakes(claude, codex *generatetest.Backend) generate.Backends {
	return generate.Backends{
		Claude: func(string) generate.CheckedBackend { return claude },
		Codex:  func(string) generate.CheckedBackend { return codex },
	}
}

func TestAutoPicksClaudeThenCodex(t *testing.T) {
	claude := &generatetest.Backend{BackendName: "claude", Version: "2.1.9"}
	codex := &generatetest.Backend{BackendName: "codex", Version: "0.200.0"}
	selected, err := generate.Select(context.Background(), config.BackendAuto, "", fakes(claude, codex), nil)
	if err != nil || selected.Backend.Name() != "claude" || selected.Version != "2.1.9" {
		t.Fatalf("both installed: %v %v", selected, err)
	}
	claude.CheckErr = errors.New("claude is too old")
	selected, err = generate.Select(context.Background(), config.BackendAuto, "", fakes(claude, codex), nil)
	if err != nil || selected.Backend.Name() != "codex" {
		t.Fatalf("claude failing: %v %v", selected, err)
	}
	codex.CheckErr = errors.New("sandbox cannot start")
	_, err = generate.Select(context.Background(), config.BackendAuto, "", fakes(claude, codex), nil)
	if generate.CategoryOf(err) != generate.CategoryBackend || !strings.Contains(err.Error(), "too old") || !strings.Contains(err.Error(), "sandbox cannot start") {
		t.Fatalf("neither usable: %v", err)
	}
}

func TestExplicitBackendsAndTheUnavailableOnes(t *testing.T) {
	claude := &generatetest.Backend{BackendName: "claude"}
	codex := &generatetest.Backend{BackendName: "codex"}
	if selected, err := generate.Select(context.Background(), config.BackendCodex, "", fakes(claude, codex), nil); err != nil || selected.Backend.Name() != "codex" {
		t.Fatalf("codex: %v %v", selected, err)
	}
	claude.CheckErr = errors.New("not installed")
	if _, err := generate.Select(context.Background(), config.BackendClaude, "", fakes(claude, codex), nil); err == nil || generate.CategoryOf(err) != generate.CategoryBackend {
		t.Fatalf("claude explicit with a failing check should not fall back: %v", err)
	}
	backends := fakes(claude, codex)
	cursor := &generatetest.Backend{BackendName: "cursor-agent", Version: "2026.10.16"}
	var models []string
	backends.CursorAgent = func(model string) generate.CheckedBackend {
		models = append(models, model)
		return cursor
	}
	selected, err := generate.Select(context.Background(), config.BackendCursorAgent, "gpt-5", backends, nil)
	if err != nil || selected.Backend.Name() != "cursor-agent" || selected.Version != "2026.10.16" || selected.Model != "gpt-5" || models[0] != "gpt-5" {
		t.Fatalf("cursor-agent: %+v %v", selected, err)
	}
	cursor.CheckErr = errors.New("not signed in")
	if _, err := generate.Select(context.Background(), config.BackendCursorAgent, "", backends, nil); generate.CategoryOf(err) != generate.CategoryBackend || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("cursor-agent explicit with a failing check: %v", err)
	}
}

// TestAutoOrder is the documented order: claude, codex, then the Anthropic
// API. cursor-agent is never picked by auto, even when it is the only usable
// backend.
func TestAutoOrder(t *testing.T) {
	if fmt.Sprint(generate.AutoOrder) != "[claude codex anthropic]" {
		t.Fatalf("AutoOrder = %v", generate.AutoOrder)
	}
	failing := &generatetest.Backend{BackendName: "claude", CheckErr: errors.New("not installed")}
	backends := fakes(failing, &generatetest.Backend{BackendName: "codex", CheckErr: errors.New("sandbox cannot start")})
	backends.CursorAgent = func(string) generate.CheckedBackend { return &generatetest.Backend{BackendName: "cursor-agent"} }
	backends.Anthropic = func(string) generate.CheckedBackend { return &generatetest.Backend{BackendName: "anthropic"} }
	var skipped bytes.Buffer
	selected, err := generate.Select(context.Background(), config.BackendAuto, "opus", backends, &skipped)
	if err != nil || selected.Backend.Name() != "anthropic" || selected.Model != anthropic.DefaultModel {
		t.Fatalf("%+v %v", selected, err)
	}
	for _, want := range []string{"claude skipped: not installed", "codex skipped: sandbox cannot start", "anthropic uses its own default model"} {
		if !strings.Contains(skipped.String(), want) {
			t.Errorf("skipped output lacks %q:\n%s", want, skipped.String())
		}
	}
	if strings.Contains(skipped.String(), "cursor-agent") {
		t.Errorf("auto tried cursor-agent:\n%s", skipped.String())
	}
	backends.Anthropic = nil
	if _, err := generate.Select(context.Background(), config.BackendAuto, "", backends, nil); err == nil || strings.Contains(err.Error(), "cursor-agent") {
		t.Fatalf("auto with only cursor-agent usable: %v", err)
	}
}

func TestAnthropicBackend(t *testing.T) {
	failing := &generatetest.Backend{BackendName: "claude", CheckErr: errors.New("not installed")}
	t.Run("explicit, with its default model resolved", func(t *testing.T) {
		var built []string
		backends := fakes(failing, failing)
		backends.Anthropic = func(model string) generate.CheckedBackend {
			built = append(built, model)
			return &generatetest.Backend{BackendName: "anthropic"}
		}
		selected, err := generate.Select(context.Background(), config.BackendAnthropic, "", backends, nil)
		if err != nil || selected.Backend.Name() != "anthropic" || selected.Model != anthropic.DefaultModel || built[0] != anthropic.DefaultModel {
			t.Fatalf("%+v %v %v", selected, err, built)
		}
		if selected, err = generate.Select(context.Background(), config.BackendAnthropic, "claude-sonnet-5-5", backends, nil); err != nil || selected.Model != "claude-sonnet-5-5" {
			t.Fatalf("named model: %+v %v", selected, err)
		}
	})
	t.Run("without a key", func(t *testing.T) {
		_, err := generate.Select(context.Background(), config.BackendAnthropic, "", generate.DefaultBackendsFor(config.Config{}), nil)
		if generate.CategoryOf(err) != generate.CategoryBackend || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY is not set") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("with a key", func(t *testing.T) {
		selected, err := generate.Select(context.Background(), config.BackendAnthropic, "", generate.DefaultBackendsFor(config.Config{AnthropicAPIKey: "sk-ant-test"}), nil)
		if err != nil || selected.Backend.Name() != "anthropic" || selected.Model != anthropic.DefaultModel {
			t.Fatalf("%+v %v", selected, err)
		}
		if strings.Contains(fmt.Sprintf("%+v", selected), "sk-ant-test") {
			t.Error("the selection prints the key")
		}
	})
	t.Run("auto falls back to it last, with its own default model", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		var skipped bytes.Buffer
		selected, err := generate.Select(context.Background(), config.BackendAuto, "opus", generate.DefaultBackendsFor(config.Config{AnthropicAPIKey: "sk-ant-test"}), &skipped)
		if err != nil || selected.Backend.Name() != "anthropic" || selected.Model != anthropic.DefaultModel {
			t.Fatalf("%+v %v", selected, err)
		}
		for _, want := range []string{"claude skipped: finding Claude Code", "codex skipped: finding Codex", `the model "opus" was meant for claude; anthropic uses its own default model`} {
			if !strings.Contains(skipped.String(), want) {
				t.Errorf("skipped output lacks %q:\n%s", want, skipped.String())
			}
		}
		if strings.Contains(skipped.String(), "sk-ant-test") {
			t.Error("the skipped output prints the key")
		}
	})
	t.Run("auto without a key names all three", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := generate.Select(context.Background(), config.BackendAuto, "", generate.DefaultBackendsFor(config.Config{}), nil)
		for _, want := range []string{"claude: finding Claude Code", "codex: finding Codex", "anthropic: ANTHROPIC_API_KEY is not set"} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("err %v lacks %q", err, want)
			}
		}
	})
	t.Run("auto prefers the CLIs", func(t *testing.T) {
		backends := fakes(failing, &generatetest.Backend{BackendName: "codex"})
		backends.Anthropic = func(string) generate.CheckedBackend { return &generatetest.Backend{BackendName: "anthropic"} }
		if selected, err := generate.Select(context.Background(), config.BackendAuto, "", backends, nil); err != nil || selected.Backend.Name() != "codex" {
			t.Fatalf("%+v %v", selected, err)
		}
	})
}

// fakeBinary puts a script named name on PATH, and nothing else.
func fakeBinary(t *testing.T, name, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shell binaries")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestDefaultBackendsWithBinariesOnPath(t *testing.T) {
	t.Run("none installed", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := generate.Select(context.Background(), config.BackendAuto, "", generate.DefaultBackends(), nil)
		if generate.CategoryOf(err) != generate.CategoryBackend || !strings.Contains(err.Error(), "finding Claude Code") || !strings.Contains(err.Error(), "finding Codex") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("claude found", func(t *testing.T) {
		fakeBinary(t, "claude", `echo "2.1.7 (Claude Code)"`)
		selected, err := generate.Select(context.Background(), config.BackendAuto, "", generate.DefaultBackends(), nil)
		if err != nil || selected.Backend.Name() != "claude" || selected.Version != "2.1.7" {
			t.Fatalf("%v %v", selected, err)
		}
	})
	t.Run("claude too old falls back to codex failing its checks", func(t *testing.T) {
		fakeBinary(t, "claude", `echo "1.0.0"`)
		_, err := generate.Select(context.Background(), config.BackendAuto, "", generate.DefaultBackends(), nil)
		if generate.CategoryOf(err) != generate.CategoryBackend || !strings.Contains(err.Error(), "older than") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestGenerateOnlyBuildsLocalProfiles is a guard: the generate command runs on
// a person's machine, so its source must never name the Isolated or Container
// profiles, or the unsandboxed codex mode.
func TestGenerateOnlyBuildsLocalProfiles(t *testing.T) {
	files := []string{"backend.go", "generate.go", filepath.Join("..", "cli", "generate.go")}
	for _, name := range files {
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, source, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.SelectorExpr:
				if n.Sel.Name == "Isolated" || n.Sel.Name == "Container" {
					t.Errorf("%s names the %s profile", name, n.Sel.Name)
				}
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					if value, _ := strconv.Unquote(n.Value); strings.Contains(value, "danger-full-"+"access") {
						t.Errorf("%s names the unsandboxed mode", name)
					}
				}
			}
			return true
		})
	}
	source, _ := os.ReadFile("backend.go")
	for _, want := range []string{"claude.User", "codex.Local", "cursor.Backend"} {
		if !strings.Contains(string(source), want) {
			t.Errorf("DefaultBackends does not build %s", want)
		}
	}
}

func TestAutoAppliesTheModelToClaudeOnly(t *testing.T) {
	var models []string
	backends := generate.Backends{
		Claude: func(model string) generate.CheckedBackend {
			models = append(models, "claude:"+model)
			return &generatetest.Backend{BackendName: "claude", CheckErr: errors.New("not installed")}
		},
		Codex: func(model string) generate.CheckedBackend {
			models = append(models, "codex:"+model)
			return &generatetest.Backend{BackendName: "codex"}
		},
	}
	var skipped bytes.Buffer
	selected, err := generate.Select(context.Background(), config.BackendAuto, "opus", backends, &skipped)
	if err != nil || selected.Backend.Name() != "codex" || selected.Model != "" {
		t.Fatalf("%+v %v", selected, err)
	}
	if strings.Join(models, ",") != "claude:opus,codex:" {
		t.Errorf("models = %v", models)
	}
	for _, want := range []string{"claude skipped: not installed", `"opus"`, "codex uses its own default model"} {
		if !strings.Contains(skipped.String(), want) {
			t.Errorf("skipped output lacks %q:\n%s", want, skipped.String())
		}
	}
}

func TestBackendPathsReplaceThePATHLookup(t *testing.T) {
	t.Run("explicit path found without PATH", func(t *testing.T) {
		fakeBinary(t, "other", `true`) // PATH holds no claude
		custom := filepath.Join(t.TempDir(), "claude-custom")
		if err := os.WriteFile(custom, []byte("#!/bin/sh\necho \"2.3.4 (Claude Code)\"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := config.Config{BackendPaths: map[config.Backend]string{config.BackendClaude: custom}}
		selected, err := generate.Select(context.Background(), config.BackendAuto, "", generate.DefaultBackendsFor(cfg), nil)
		if err != nil || selected.Backend.Name() != "claude" || selected.Version != "2.3.4" {
			t.Fatalf("%v %v", selected, err)
		}
	})
	t.Run("missing explicit path does not fall back to PATH", func(t *testing.T) {
		fakeBinary(t, "claude", `echo "2.1.7 (Claude Code)"`)
		missing := filepath.Join(t.TempDir(), "missing-claude")
		cfg := config.Config{BackendPaths: map[config.Backend]string{config.BackendClaude: missing}}
		var skipped strings.Builder
		_, err := generate.Select(context.Background(), config.BackendClaude, "", generate.DefaultBackendsFor(cfg), &skipped)
		var pathErr *generate.ExplicitPathError
		if !errors.As(err, &pathErr) || generate.CategoryOf(err) != generate.CategoryBackend {
			t.Fatalf("err = %v, want an ExplicitPathError of the backend category", err)
		}
		for _, want := range []string{"backend_paths.claude", missing} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
		if strings.Contains(err.Error(), "install it") {
			t.Errorf("error %q suggests installing a tool that is installed", err)
		}

		// auto skips claude with the same reason and, with no codex, fails naming it.
		_, err = generate.Select(context.Background(), config.BackendAuto, "", generate.DefaultBackendsFor(cfg), &skipped)
		if err == nil || !strings.Contains(skipped.String(), "backend_paths.claude") || !strings.Contains(err.Error(), "backend_paths.claude") {
			t.Fatalf("auto: err = %v, skipped = %q", err, skipped.String())
		}
	})
	t.Run("an explicit codex path that is a directory", func(t *testing.T) {
		cfg := config.Config{BackendPaths: map[config.Backend]string{config.BackendCodex: t.TempDir()}}
		_, err := generate.Select(context.Background(), config.BackendCodex, "", generate.DefaultBackendsFor(cfg), nil)
		if err == nil || !strings.Contains(err.Error(), "backend_paths.codex") {
			t.Fatalf("err = %v", err)
		}
	})
}

// The chosen reasoning effort reaches every harness that takes one.
func TestDefaultBackendsCarryTheEffort(t *testing.T) {
	backends := generate.DefaultBackendsFor(config.Config{Effort: "high"})
	if c := backends.Claude("m").(*claude.Backend); c.Effort != "high" {
		t.Errorf("claude effort %q", c.Effort)
	}
	if c := backends.Codex("m").(*codex.Backend); c.Effort != "high" {
		t.Errorf("codex effort %q", c.Effort)
	}
	if a := backends.Anthropic("m").(*anthropic.Backend); a.Effort != "high" {
		t.Errorf("anthropic effort %q", a.Effort)
	}
}
