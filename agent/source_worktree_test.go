package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/bundle"
)

func TestSourceContextWorktreeRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &bundle.Bundle{Diff: []bundle.Hunk{{ID: "h_1", Path: "a.go", StartLine: 1, LineCount: 2}}}
	if got := sourceContext(dir, b); !strings.Contains(got, "func A()") {
		t.Fatalf("no excerpt from the worktree root: %q", got)
	}
	if p := focusedPrefix(b, dir, []string{"h_1"}); strings.Contains(p, "merge-base/") {
		t.Fatalf("worktree prefix names run trees: %s", p)
	}
}
