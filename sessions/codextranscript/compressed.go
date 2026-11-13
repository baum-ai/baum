package codextranscript

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const (
	rolloutPrefix     = "rollout-"
	plainSuffix       = ".jsonl"
	compressedSuffix  = ".jsonl.zst"
	maxDecoderWindow  = 64 << 20
	unknownSizeFactor = 8
)

// ErrDecompress marks a read error of a compressed rollout that came from the
// zstd stream (corrupt or truncated), not from the rollout's records.
var ErrDecompress = errors.New("decompressing rollout")

// IsRolloutName reports whether name is a Codex rollout file, plain or
// compressed.
func IsRolloutName(name string) bool {
	return strings.HasPrefix(name, rolloutPrefix) &&
		(strings.HasSuffix(name, plainSuffix) || strings.HasSuffix(name, compressedSuffix))
}

// PlainName is name without the .zst suffix Codex adds when it compresses a
// rollout. Sessions keep the plain name so that their identity does not change
// when their rollout is compressed.
func PlainName(name string) string {
	return strings.TrimSuffix(name, ".zst")
}

// PlainPath is path without the .zst suffix: where the plain rollout of a
// compressed one would be.
func PlainPath(path string) string {
	return strings.TrimSuffix(path, ".zst")
}

// Compressed reports whether path is a compressed rollout.
func Compressed(path string) bool {
	return strings.HasSuffix(path, compressedSuffix)
}

// OpenRollout opens a rollout for reading its decoded bytes. Closing the
// result closes the file.
func OpenRollout(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !Compressed(path) {
		return f, nil
	}
	rc, err := decompress(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &closeBoth{ReadCloser: rc, file: f}, nil
}

// Decompress is the decoded stream of the compressed rollout r holds, for a
// caller that has the file open already. Errors from it wrap ErrDecompress.
// Closing the result does not close r.
func Decompress(r io.Reader) (io.ReadCloser, error) {
	return decompress(r)
}

type closeBoth struct {
	io.ReadCloser
	file *os.File
}

func (c *closeBoth) Close() error {
	return errors.Join(c.ReadCloser.Close(), c.file.Close())
}

// decompress streams the zstd data in r. Errors from the stream wrap
// ErrDecompress. Closing the result does not close r.
func decompress(r io.Reader) (io.ReadCloser, error) {
	d, err := zstd.NewReader(r,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxWindow(maxDecoderWindow))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDecompress, err)
	}
	return &decoded{d: d}, nil
}

type decoded struct{ d *zstd.Decoder }

func (r *decoded) Read(p []byte) (int, error) {
	n, err := r.d.Read(p)
	if err != nil && err != io.EOF {
		err = fmt.Errorf("%w: %w", ErrDecompress, err)
	}
	return n, err
}

func (r *decoded) Close() error {
	r.d.Close()
	return nil
}

// DecodedSize is the size in bytes of the rollout at path once decoded; info
// is its stat. A compressed rollout's size is the content size in its zstd
// frame header. A frame without one (or an unreadable header) is estimated at
// eight times the compressed size, which errs high: a budget that
// over-counts only defers decoding.
func DecodedSize(path string, info fs.FileInfo) int64 {
	if !Compressed(path) {
		return info.Size()
	}
	estimate := info.Size() * unknownSizeFactor
	f, err := os.Open(path)
	if err != nil {
		return estimate
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 32)
	n, _ := io.ReadFull(f, head)
	var h zstd.Header
	if err := h.Decode(head[:n]); err != nil || !h.HasFCS || h.FrameContentSize > 1<<40 {
		return estimate
	}
	return int64(h.FrameContentSize)
}
