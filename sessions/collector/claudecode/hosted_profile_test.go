package claudecode

import (
	"time"

	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

func hostedFindSessions(dir string, roots []string) ([]model.Session, model.ScanReport, error) {
	return FindSessionsWith(dir, roots, time.Time{}, traceprocessing.Hosted)
}
