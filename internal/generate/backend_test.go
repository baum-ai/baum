package generate_test

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/lastpersonlabs/baum/internal/config"
	"github.com/lastpersonlabs/baum/internal/generate"
	"github.com/lastpersonlabs/baum/internal/generate/generatetest"
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
	selected, err := generate.Select(context.Background(), config.BackendAuto, "", fakes(claude, codex))
	if err != nil || selected.Backend.Name() != "claude" || selected.Version != "2.1.9" {
		t.Fatalf("both installed: %v %v", selected, err)
	}
	claude.CheckErr = errors.New("claude is too old")
	selected, err = generate.Select(context.Background(), config.BackendAuto, "", fakes(claude, codex))
	if err != nil || selected.Backend.Name() != "codex" {
		t.Fatalf("claude failing: %v %v", selected, err)
	}
	codex.CheckErr = errors.New("sandbox cannot start")
	_, err = generate.Select(context.Background(), config.BackendAuto, "", fakes(claude, codex))
	if generate.CategoryOf(err) != generate.CategoryBackend || !strings.Contains(err.Error(), "too old") || !strings.Contains(err.Error(), "sandbox cannot start") {
		t.Fatalf("neither usable: %v", err)
	}
}

func TestExplicitBackendsAndTheUnavailableOnes(t *testing.T) {
	claude := &generatetest.Backend{BackendName: "claude"}
	codex := &generatetest.Backend{BackendName: "codex"}
	if selected, err := generate.Select(context.Background(), config.BackendCodex, "", fakes(claude, codex)); err != nil || selected.Backend.Name() != "codex" {
		t.Fatalf("codex: %v %v", selected, err)
	}
	claude.CheckErr = errors.New("not installed")
	if _, err := generate.Select(context.Background(), config.BackendClaude, "", fakes(claude, codex)); err == nil || generate.CategoryOf(err) != generate.CategoryBackend {
		t.Fatalf("claude explicit with a failing check should not fall back: %v", err)
	}
	for _, name := range []config.Backend{config.BackendAnthropic, config.BackendCursorAgent} {
		_, err := generate.Select(context.Background(), name, "", fakes(claude, codex))
		if generate.CategoryOf(err) != generate.CategoryBackend || !strings.Contains(err.Error(), "not available in this version") {
			t.Errorf("%s: %v", name, err)
		}
	}
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
		_, err := generate.Select(context.Background(), config.BackendAuto, "", generate.DefaultBackends())
		if generate.CategoryOf(err) != generate.CategoryBackend || !strings.Contains(err.Error(), "finding Claude Code") || !strings.Contains(err.Error(), "finding Codex") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("claude found", func(t *testing.T) {
		fakeBinary(t, "claude", `echo "2.1.7 (Claude Code)"`)
		selected, err := generate.Select(context.Background(), config.BackendAuto, "", generate.DefaultBackends())
		if err != nil || selected.Backend.Name() != "claude" || selected.Version != "2.1.7" {
			t.Fatalf("%v %v", selected, err)
		}
	})
	t.Run("claude too old falls back to codex failing its checks", func(t *testing.T) {
		fakeBinary(t, "claude", `echo "1.0.0"`)
		_, err := generate.Select(context.Background(), config.BackendAuto, "", generate.DefaultBackends())
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
	for _, want := range []string{"claude.User", "codex.Local"} {
		if !strings.Contains(string(source), want) {
			t.Errorf("DefaultBackends does not build %s", want)
		}
	}
}
