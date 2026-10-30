package entirecheckpoint

import (
	"io"
	"io/fs"

	"github.com/baum-ai/baum/sessions/traceprocessing"
)

func ParseHostedCheckpoint(fsys fs.FS) (*Checkpoint, error) {
	return ParseCheckpointWith(fsys, traceprocessing.Hosted)
}
func DecodeHostedCompact(r io.Reader) ([]CompactRecord, error) {
	return DecodeCompactWith(r, traceprocessing.Hosted)
}
