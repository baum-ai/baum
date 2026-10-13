package model

import (
	"path/filepath"
	"testing"
)

func TestPathWithin(t *testing.T) {
	p := filepath.FromSlash
	for _, tc := range []struct {
		root, path string
		fold, want bool
	}{
		{"/repo", "/repo", false, true},
		{"/repo", "/repo/sub/dir", false, true},
		{"/repo", "/repo-other", false, false},
		{"/repo", "/repo-other/sub", false, false},
		{"/repo", "/repos", false, false},
		{"/repo", "/rep", false, false},
		{"/repo", "/Repo/sub", false, false},
		{"/", "/anything", false, false},
		{"/", "/", false, true},
		// Windows: case is ignored, the boundary still holds.
		{"/c/Projects/Demo", "/C/projects/demo/sub", true, true},
		{"/c/Projects/Demo", "/C/projects/demo-other", true, false},
	} {
		if got := pathWithin(p(tc.root), p(tc.path), tc.fold); got != tc.want {
			t.Errorf("pathWithin(%q, %q, fold=%v) = %v, want %v", tc.root, tc.path, tc.fold, got, tc.want)
		}
	}
}

func TestIsAgentRun(t *testing.T) {
	for path, want := range map[string]bool{
		"/tmp/baum-agent-run-123":              true,
		"/tmp/baum-agent-run-123/checkout/sub": true,
		`C:\Temp\baum-agent-run-9\checkout`:    true,
		"/repo":                                false,
		"/tmp/not-baum-agent-run-1":            false,
		"/repo/pr-review-run-1":                false,
		"":                                     false,
	} {
		if got := IsAgentRun(path); got != want {
			t.Errorf("IsAgentRun(%q) = %v, want %v", path, got, want)
		}
	}
}
