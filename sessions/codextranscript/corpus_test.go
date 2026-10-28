package codextranscript_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/sessions/codextranscript"
	collectcodex "github.com/baum-ai/baum/sessions/collector/codex"
)

// Opt-in: set BAUM_CODEX_CORPUS to a Codex home (~/.codex).
// Skipped in the normal containerized suite (no home directory).
const corpusEnv = "BAUM_CODEX_CORPUS"

func corpusRoot(t *testing.T) string {
	t.Helper()
	dir := os.Getenv(corpusEnv)
	if dir == "" {
		t.Skipf("set %s to a Codex home", corpusEnv)
	}
	if info, err := os.Stat(filepath.Join(dir, "sessions")); err != nil || !info.IsDir() {
		t.Skipf("%s=%q has no sessions directory", corpusEnv, dir)
	}
	return dir
}

func scanCorpus(t *testing.T) []collectcodex.Entry {
	t.Helper()
	entries, scan, err := collectcodex.ScanStore(corpusRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if scan.SkippedFiles > 0 {
		t.Logf("skipped %d incomplete rollouts: %v", scan.SkippedFiles, scan.SkippedPaths)
	}
	return entries
}

func TestCorpusDecodes(t *testing.T) {
	for _, entry := range scanCorpus(t) {
		t.Run(entry.Meta.ID, func(t *testing.T) {
			if _, err := codextranscript.ReadFile(entry.Path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCorpusFindsAndDecodesEveryRoot(t *testing.T) {
	entries := scanCorpus(t)
	claimed := map[string]int{}
	for _, entry := range entries {
		if entry.Class != codextranscript.ThreadRoot {
			continue
		}
		files, err := collectcodex.Find(entries, entry)
		if err != nil {
			t.Fatalf("finding %s: %v", entry.Path, err)
		}
		tree, err := codextranscript.DecodeLocalFiles(files)
		if err != nil {
			t.Fatalf("decoding %s: %v", entry.Path, err)
		}
		for _, child := range tree.Children {
			claimed[child.Meta.ID]++
		}
	}
	for _, entry := range entries {
		if entry.Class == codextranscript.ThreadChild && claimed[entry.Meta.ID] != 1 {
			t.Errorf("child %s was claimed %d times", entry.Path, claimed[entry.Meta.ID])
		}
	}
}
