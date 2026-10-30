package entirecheckpoint

import (
	"io"
	"io/fs"

	"github.com/baum-ai/baum/sessions/traceprocessing"
)

func hostedParseCheckpoint(fsys fs.FS) (*Checkpoint, error) {
	return ParseCheckpointWith(fsys, traceprocessing.Hosted)
}
func hostedDecodeCompact(r io.Reader) ([]CompactRecord, error) {
	return DecodeCompactWith(r, traceprocessing.Hosted)
}
func hostedDecodeCheckpointMetadata(raw []byte) (CheckpointMetadata, error) {
	return decodeCheckpointMetadataWith(raw, traceprocessing.Hosted)
}
