package entire

import (
	"io"
	"io/fs"

	"github.com/baum-ai/baum/sessions/collector/entirearchive"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

// Open reads a bounded git archive using the local error and filesystem contract.
func Open(reader io.Reader, checkpointPath string) (fs.FS, error) {
	return entirearchive.OpenWith(reader, checkpointPath, traceprocessing.Local)
}
