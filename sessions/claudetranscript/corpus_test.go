package claudetranscript

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions/collector/claudecode"
)

// Opt-in: set BAUM_CLAUDE_CORPUS to a ~/.claude/projects directory.
// Skipped in the normal containerized suite (no home directory).
const corpusEnv = "BAUM_CLAUDE_CORPUS"

func corpusDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv(corpusEnv)
	if dir == "" {
		t.Skipf("set %s to a Claude projects directory to run this", corpusEnv)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Skipf("%s=%q is not a directory", corpusEnv, dir)
	}
	return dir
}

func mainTranscripts(t *testing.T, root string) []string {
	t.Helper()
	projects, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}
	var found []string
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		dir := filepath.Join(root, project.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				continue
			}
			found = append(found, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(found)
	return found
}

func TestCorpusDecodes(t *testing.T) {
	root := corpusDir(t)
	paths := mainTranscripts(t, root)
	if len(paths) == 0 {
		t.Fatalf("no transcripts under %s", root)
	}

	classes := map[Class]int{}
	types := map[string]int{}
	userKinds := map[UserKind]int{}
	unknown := map[string]int{}
	blocks := map[string]int{}
	var files []*File
	trees, subagents, torn, spawnLinks, metas := 0, 0, 0, 0, 0

	for _, path := range paths {
		tree, err := DecodeLocalFiles(claudecode.SessionFiles(path))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		trees++
		subagents += len(tree.Subagents)
		spawnLinks += len(tree.byToolUse)
		files = append(files, tree.Files()...)
		for _, file := range tree.Files() {
			if file.Meta != nil {
				metas++
			}
			if file.TornFinalLine {
				torn++
			}
			for _, line := range file.Lines {
				classes[line.Class]++
				types[line.Type]++
				if line.Class == ClassUnknownControl {
					unknown[line.Type]++
				}
				if line.Class == ClassUser {
					userKinds[line.UserKind]++
				}
				if line.Class == ClassUser || line.Class == ClassAssistant {
					for _, block := range line.Message.Blocks() {
						blocks[block.Type]++
					}
				}
			}
		}
	}

	t.Logf("trees=%d subagents=%d spawn-links=%d meta-sidecars=%d torn-final-lines=%d",
		trees, subagents, spawnLinks, metas, torn)
	t.Logf("classes=%v", classes)
	t.Logf("types=%v", types)
	t.Logf("user kinds=%v", userKinds)
	t.Logf("blocks=%v", blocks)
	if len(unknown) > 0 {
		t.Logf("unrecognised control types (kept, joined to nothing): %v", unknown)
	}

	shape := Survey(files)
	t.Logf("versions=%v (%s..%s)", shape.Versions.Seen, shape.Versions.Min, shape.Versions.Max)
	t.Logf("content shapes=%v", shape.Content)
	t.Logf("top-level keys we do not read: %v", shape.UnreadKeys)

	// Every subagent transcript in the corpus has a sidecar naming its spawn. If
	// that stops being true the mapping loses nested sessions, so it is asserted
	// rather than merely logged.
	if subagents > 0 && metas != subagents {
		t.Errorf("%d subagent transcripts but %d sidecars; spawn links would be lost", subagents, metas)
	}
}
