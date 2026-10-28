package codexsession

import (
	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/traceprocessing"
	"os"
	"path/filepath"
	"testing"
)

func hostedBuild(tree *codextranscript.Tree) (*session.Graph, *Report, error) {
	return BuildWith(tree, traceprocessing.Hosted)
}
func corpusRoot(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("BAUM_CODEX_CORPUS")
	if dir == "" {
		t.Skip("set BAUM_CODEX_CORPUS to a Codex home")
	}
	if info, err := os.Stat(filepath.Join(dir, "sessions")); err != nil || !info.IsDir() {
		t.Skip("corpus has no sessions directory")
	}
	return dir
}
