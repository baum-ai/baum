package claudesession

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions/claudetranscript"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

const corpusEnv = "BAUM_CLAUDE_CORPUS"

func hostedBuild(tree *claudetranscript.Tree) (*session.Graph, *Report, error) {
	return BuildWith(tree, BuildOptions{Profile: traceprocessing.Hosted, Survey: true})
}
func hostedClassifyInput(line claudetranscript.Line, text string) session.InputKind {
	return classifyInputWith(line, text, traceprocessing.Hosted)
}
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
