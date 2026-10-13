package sessioncorrelate

import (
	"testing"

	"github.com/lastpersonlabs/baum/sessions/session"
)

// Each touch resolves to the path Correlate reports for it, and one nothing
// relativizes does not resolve.
func TestPathResolverAgreesWithCorrelate(t *testing.T) {
	graph := newTree("/home/dev/repo", "").
		nested("s-child", "").
		touch("s-root", "/home/dev/repo/web/app.ts", session.TouchEdit).
		touch("s-child", "/home/dev/repo/README.md", session.TouchRead).
		touch("s-root", "docs/guide.md", session.TouchRead).
		touch("s-root", "/tmp/plan.md", session.TouchEdit).
		touch("s-root", "../outside.txt", session.TouchRead).
		build()

	resolver := NewPathResolver(graph, "claude_code")
	want := map[string]string{
		"/home/dev/repo/web/app.ts@s-root": "web/app.ts",
		"/home/dev/repo/README.md@s-child": "README.md",
		"docs/guide.md@s-root":             "docs/guide.md",
		"/tmp/plan.md@s-root":              "",
		"../outside.txt@s-root":            "",
	}
	for _, touch := range graph.FileTouches {
		got, ok := resolver.Touch(touch)
		if ok != (want[touch.ID] != "") || got != want[touch.ID] {
			t.Errorf("%s: Touch = %q, %v; want %q", touch.ID, got, ok, want[touch.ID])
		}
	}
	correlated := map[string]bool{}
	for _, file := range Correlate(graph, "claude_code").Files {
		correlated[file.Path] = true
	}
	for id, path := range want {
		if path != "" && !correlated[path] {
			t.Errorf("%s: Correlate does not report %q", id, path)
		}
	}
	if len(correlated) != 3 {
		t.Errorf("Correlate files = %v", correlated)
	}
}

func TestPathResolverOfNilGraph(t *testing.T) {
	if _, ok := NewPathResolver(nil, "").Touch(session.FileTouch{Path: "/abs/x.go"}); ok {
		t.Error("an absolute path with no working directory must not resolve")
	}
}
