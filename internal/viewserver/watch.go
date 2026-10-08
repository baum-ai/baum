package viewserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"
)

// LoadDocument reads the document at path. It must be valid JSON and no larger
// than MaxDocumentBytes.
func LoadDocument(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read document: %w", err)
	}
	defer file.Close()
	document, err := io.ReadAll(io.LimitReader(file, MaxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read document: %w", err)
	}
	if len(document) > MaxDocumentBytes {
		return nil, fmt.Errorf("%s is larger than the %d MiB limit", path, MaxDocumentBytes>>20)
	}
	if !isJSON(document) {
		return nil, fmt.Errorf("%s is not valid JSON", path)
	}
	return document, nil
}

func isJSON(document []byte) bool {
	return json.Valid(bytes.TrimSpace(document))
}

// watch polls the document file until ctx ends. It stats the path every time
// rather than holding the file open, so a save that replaces the file with a
// rename is seen. A change is read once the file has stayed the same for a
// whole poll interval, which skips a half-written save. A document that is not
// valid JSON, or is too large, is not served: the previous one stays and a
// warning is printed. A missing file is waited for.
func (s *Server) watch(ctx context.Context) {
	handled := s.baseline
	var pending fs.FileInfo
	hash := s.hash
	missing := false

	ticker := time.NewTicker(s.opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		info, err := os.Stat(s.opts.Path)
		if err != nil {
			if !missing {
				_, _ = fmt.Fprintf(s.opts.Warn, "warning: cannot read %s, still serving the previous document: %v\n", s.opts.Path, err)
			}
			missing = true
			pending = nil
			continue
		}
		if missing {
			missing = false
			handled = nil
		}
		if sameFile(info, handled) {
			pending = nil
			continue
		}
		if !sameFile(info, pending) {
			pending = info
			continue
		}
		handled, pending = info, nil

		document, err := LoadDocument(s.opts.Path)
		if err != nil {
			_, _ = fmt.Fprintf(s.opts.Warn, "warning: %v; still serving the previous document\n", err)
			continue
		}
		if next := sha256.Sum256(document); next != hash {
			hash = next
			s.setDocument(document)
			s.warnFindings(document)
		}
	}
}

func sameFile(a, b fs.FileInfo) bool {
	if a == nil || b == nil {
		return false
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
