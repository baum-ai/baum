// Package entirearchive reads bounded checkpoint archives independently of Git transport.
package entirearchive

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/baum-ai/baum/sessions/traceprocessing"
)

const (
	maxCheckpointBytes = 256 << 20
	maxCheckpointFiles = 512
)

var ErrArchiveLimit = errors.New("Entire checkpoint archive exceeds limits")
var ErrArchiveTruncated = errors.New("Entire checkpoint archive is truncated")
var ErrArchiveEntry = errors.New("invalid Entire checkpoint archive entry")

// Open reads a git-archive of one checkpoint into an fs.FS. checkpointDir is
// the checkpoint's directory in the archive (CheckpointTree.Dir): the branch
// path, or "" for a ref's archive, whose root is the checkpoint.
func OpenWith(reader io.Reader, checkpointDir string, profile traceprocessing.MappingProfile) (fs.FS, error) {
	result := map[string][]byte{}
	tarReader := tar.NewReader(io.LimitReader(reader, maxCheckpointBytes+1))
	prefix := ""
	if checkpointDir != "" {
		prefix = checkpointDir + "/"
	}
	var total int64
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if profile == traceprocessing.Hosted && errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, fmt.Errorf("reading Entire checkpoint archive: %w: %w", ErrArchiveTruncated, err)
			}
			return nil, fmt.Errorf("reading Entire checkpoint archive: %w", err)
		}
		// git archive leads with a pax_global_header carrying the commit id and
		// emits an entry per directory; neither contributes checkpoint content.
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name, err := checkpointMemberName(header.Name, prefix, profile)
		if err != nil {
			return nil, err
		}
		if len(result) >= maxCheckpointFiles || header.Size < 0 ||
			header.Size > maxCheckpointBytes-total {
			if profile == traceprocessing.Hosted {
				return nil, ErrArchiveLimit
			}
			return nil, errors.New("the Entire checkpoint archive exceeds limits")
		}
		data, err := readCheckpointMember(tarReader, name, header.Size, profile)
		if err != nil {
			return nil, err
		}
		total += header.Size
		result[name] = data
	}
	if _, ok := result["metadata.json"]; !ok {
		if profile == traceprocessing.Hosted {
			return nil, fmt.Errorf("Entire checkpoint archive has no metadata.json")
		}
		return nil, errors.New("the Entire checkpoint archive has no metadata.json")
	}
	if profile == traceprocessing.Hosted {
		return traceprocessing.HostedBytesFS(result, traceprocessing.TreeFilePolicy), nil
	}
	return traceprocessing.BytesFS(result, traceprocessing.TreeFilePolicy), nil
}

func checkpointMemberName(entry, prefix string, profile traceprocessing.MappingProfile) (string, error) {
	if !strings.HasPrefix(entry, prefix) {
		if profile == traceprocessing.Hosted {
			return "", fmt.Errorf("%w %q", ErrArchiveEntry, entry)
		}
		return "", fmt.Errorf("invalid Entire checkpoint archive entry %q", entry)
	}
	name := strings.TrimPrefix(entry, prefix)
	// fs.ValidPath allows "."; reject it so a directory entry cannot become a
	// file keyed as the checkpoint root.
	if name == "." || !fs.ValidPath(name) {
		if profile == traceprocessing.Hosted {
			return "", fmt.Errorf("%w: invalid path %q", ErrArchiveEntry, entry)
		}
		return "", fmt.Errorf("invalid Entire checkpoint archive path %q", entry)
	}
	return name, nil
}

func readCheckpointMember(r io.Reader, name string, size int64, profile traceprocessing.MappingProfile) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			if profile == traceprocessing.Hosted {
				return nil, fmt.Errorf("reading Entire checkpoint file %q: archive is truncated: %w: %w", name, ErrArchiveTruncated, err)
			}
			return nil, fmt.Errorf("reading Entire checkpoint file %q: archive is truncated: %w", name, err)
		}
		return nil, fmt.Errorf("reading Entire checkpoint file %q: %w", name, err)
	}
	if int64(len(data)) != size {
		if profile != traceprocessing.Hosted {
			return nil, fmt.Errorf("reading Entire checkpoint file %q: archive is truncated (read %d bytes, expected %d)", name, len(data), size)
		}
		return nil, fmt.Errorf(
			"reading Entire checkpoint file %q: archive is truncated (read %d bytes, expected %d): %w",
			name, len(data), size, ErrArchiveTruncated,
		)
	}
	return data, nil
}
