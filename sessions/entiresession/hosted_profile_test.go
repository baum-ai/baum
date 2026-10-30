package entiresession

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baum-ai/baum/sessions/entirecheckpoint"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

const entireCorpusEnv = "BAUM_ENTIRE_CORPUS"

func hostedBuild(checkpoint *entirecheckpoint.Checkpoint) ([]Result, *Report, error) {
	return BuildWith(checkpoint, traceprocessing.Hosted)
}
func hostedEnvelope(graph *session.Graph, snapshot *entirecheckpoint.SessionSnapshot, id string, at time.Time) (*EnvelopeData, error) {
	return EnvelopeWith(graph, snapshot, id, at, traceprocessing.Hosted)
}
func hostedBuildCompact(snapshot *entirecheckpoint.SessionSnapshot) (*session.Graph, error) {
	return buildCompactWith(snapshot, traceprocessing.Hosted)
}
func hostedCompactVerb(name string) session.ToolVerb {
	return compactVerbWith(name, traceprocessing.Hosted)
}
func walkCheckpointDirs(root string, visit func(path, rel string) error) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if len(strings.Split(filepath.ToSlash(rel), "/")) != 2 {
			return nil
		}
		if _, err := os.Stat(filepath.Join(path, "metadata.json")); err != nil {
			return nil
		}
		return visit(path, rel)
	})
}
