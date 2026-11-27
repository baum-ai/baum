package locallink

import (
	"slices"
	"testing"
)

func TestCitedFilesReadsBothContextShapes(t *testing.T) {
	document := []byte(`{
		"summary": {"context": {"area": {"refs": [{"path": "v1/area.go"}]}}},
		"context": {
			"area": {"text": "x", "files": ["app/settings.tsx", "internal/repos.go"]},
			"reached_by": null,
			"related": {"text": "y", "files": ["internal/reader.go"]}
		},
		"index": {"hunks": [{"path": "changed.go", "old_path": "old.go"}]},
		"sections": [{"cites": [{"type": "file", "path": "cited.md"}]}]
	}`)
	var got []string
	for path := range CitedFiles(document) {
		got = append(got, path)
	}
	slices.Sort(got)
	want := []string{"app/settings.tsx", "changed.go", "cited.md", "internal/reader.go", "internal/repos.go", "old.go", "v1/area.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("CitedFiles = %v, want %v", got, want)
	}
}
