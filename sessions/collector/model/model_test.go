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
		{"/", "/anything", false, true},
		// Windows: case is ignored, the boundary still holds.
		{"/c/Projects/Demo", "/C/projects/demo/sub", true, true},
		{"/c/Projects/Demo", "/C/projects/demo-other", true, false},
	} {
		if got := pathWithin(p(tc.root), p(tc.path), tc.fold); got != tc.want {
			t.Errorf("pathWithin(%q, %q, fold=%v) = %v, want %v", tc.root, tc.path, tc.fold, got, tc.want)
		}
	}
}
