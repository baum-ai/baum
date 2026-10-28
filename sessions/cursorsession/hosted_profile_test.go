package cursorsession

import (
	"github.com/baum-ai/baum/sessions/cursortranscript"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/traceprocessing"
	"time"
)

func hostedBuild(tree *cursortranscript.Tree) (*session.Graph, *Report, error) {
	return BuildWith(tree, traceprocessing.Hosted)
}
func hostedBuildPortable(sessionID, path string, records []cursortranscript.PortableRecord, validFrom time.Time) (*session.Graph, *PortableReport, error) {
	return BuildPortableWith(sessionID, path, records, validFrom, traceprocessing.Hosted)
}

const corpusEnv = "BAUM_CURSOR_STORE"
