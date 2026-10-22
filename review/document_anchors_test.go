package review

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileAnchorsRequireExactContainedRegularFiles(t *testing.T) {
	directory := t.TempDir()
	for _, file := range []string{"head/api/Handler.go", "merge-base/queue/Legacy.go"} {
		path := filepath.Join(directory, file)
		checkNoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		checkNoError(t, os.WriteFile(path, nil, 0o644))
	}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"api/Handler.go", true}, {"queue/Legacy.go", true},
		{"API/Handler.go", false}, {"api/handler.go", false},
		{"queue/legacy.go", false}, {"api/Handler.go.", false},
		{"api./Handler.go", false}, {"api/Handler.go ", false},
		{"api", false}, {"missing.go", false}, {"../secret.go", false},
	} {
		t.Run(tc.path, func(t *testing.T) {
			anchor := documentAnchor{Type: "file", Path: &tc.path}
			if got := resolveDocumentAnchor(anchor, contextBundle(), directory); got != tc.want {
				t.Fatalf("resolved = %v, want %v", got, tc.want)
			}
			granularity := "job"
			items := []documentEvidence{{Status: "verified", Granularity: &granularity, Anchors: []documentAnchor{anchor}}}
			normalizeEvidence(items, contextBundle(), directory)
			if tc.want {
				checkEqual(t, "verified", items[0].Status)
				checkEqual(t, &granularity, items[0].Granularity)
			} else {
				checkEqual(t, "asserted", items[0].Status)
				if items[0].Granularity != nil {
					t.Fatal("unavailable file kept evidence granularity")
				}
			}
		})
	}
	path := "api/Handler.go"
	if resolveDocumentAnchor(documentAnchor{Type: "file", Path: &path}, contextBundle(), "") {
		t.Fatal("file anchor resolved without a checkout")
	}

	checkNoError(t, os.WriteFile(filepath.Join(directory, "secret.go"), nil, 0o644))
	for link, target := range map[string]string{"inside.go": "api/Handler.go", "escape.go": "../secret.go"} {
		if err := os.Symlink(target, filepath.Join(directory, "head", link)); err != nil {
			t.Skipf("symbolic links are unavailable: %v", err)
		}
		if got, want := resolveDocumentAnchor(documentAnchor{Type: "file", Path: &link}, contextBundle(), directory), link == "inside.go"; got != want {
			t.Errorf("symlink %q resolved = %v, want %v", link, got, want)
		}
	}
}
