package traceupload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/baum-ai/goredact"

	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/model"
)

// stageSession redacts every file of one session tree into the staging
// directory and returns the manifest entry describing it. Files are staged and
// stored under archive members this function chooses — `sessions/<n>/files/<m>`
// — so no vendor directory layout leaves the machine. The recorded roots are
// the exception and travel verbatim: they are the answer to where the
// repository was, which redacting them would destroy.
func stageSession(ctx context.Context, engine *goredact.Engine, stagedDir string, index int, session collector.Session) (SessionManifest, error) {
	entry := SessionManifest{
		Source:         session.Source,
		SessionID:      session.SessionID,
		RepoRoot:       session.RepoRoot,
		WorktreeRoot:   session.WorktreeRoot,
		ModifiedAt:     session.ModifiedAt,
		FindingsByType: map[string]int{},
	}
	files := session.Files
	if len(files) == 0 {
		files = []collector.SessionFile{{
			Path: session.Path, Name: filepath.Base(session.Path), Kind: model.KindTranscript,
		}}
	}
	roots := 0
	names := make(map[string]bool, len(files))
	for i, file := range files {
		// The name is how a reader tells the files of a tree apart once the
		// layout is gone, so two files may not share one.
		if file.Name == "" || names[file.Name] {
			return SessionManifest{}, fmt.Errorf("session %s names file %q twice or not at all", session.SessionID, file.Name)
		}
		names[file.Name] = true
		if file.Kind == model.KindTranscript {
			roots++
		}
		fileEntry, err := stageSessionFile(ctx, engine, stagedDir, index, i, file)
		if err != nil {
			return SessionManifest{}, fmt.Errorf("session %s: %w", session.SessionID, err)
		}
		entry.Files = append(entry.Files, fileEntry)
		entry.SizeBytes += fileEntry.SizeBytes
		mergeCounts(entry.FindingsByType, fileEntry.FindingsByType)
	}
	if roots != 1 {
		return SessionManifest{}, fmt.Errorf("session %s names %d root transcripts, want exactly 1", session.SessionID, roots)
	}
	return entry, nil
}

func stageSessionFile(
	ctx context.Context,
	engine *goredact.Engine,
	stagedDir string,
	sessionIndex, fileIndex int,
	file collector.SessionFile,
) (_ SessionFileManifest, err error) {
	id := fmt.Sprintf("%06d", fileIndex+1)
	member := sessionMember(sessionIndex, id)

	var in io.Reader
	var size int64
	switch {
	case file.Data != nil && file.Path != "":
		return SessionFileManifest{}, fmt.Errorf("file %s has both a path and generated data", file.Name)
	case file.Data != nil:
		in = bytes.NewReader(file.Data)
		size = int64(len(file.Data))
	case file.Path == "":
		return SessionFileManifest{}, fmt.Errorf("file %s has neither a path nor generated data", file.Name)
	default:
		handle, err := os.Open(file.Path)
		if err != nil {
			return SessionFileManifest{}, fmt.Errorf("reading file %s: %w", file.Name, err)
		}
		defer handle.Close()
		info, err := handle.Stat()
		if err != nil {
			return SessionFileManifest{}, fmt.Errorf("stating file %s: %w", file.Name, err)
		}
		if !info.Mode().IsRegular() {
			return SessionFileManifest{}, fmt.Errorf("file %s is not regular", file.Name)
		}
		in, size = handle, info.Size()
	}

	outputPath := stagedPath(stagedDir, sessionIndex, id)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return SessionFileManifest{}, fmt.Errorf("creating staging directory for %s: %w", file.Name, err)
	}
	out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return SessionFileManifest{}, fmt.Errorf("staging file %s: %w", file.Name, err)
	}
	defer func() { err = errors.Join(err, out.Close()) }()

	// The staged bytes are the source bytes with detected spans masked in
	// place — nothing else is rewritten. The BOM and UTF-8 normalization the
	// previous engine performed was its own input requirement, not an archive
	// contract, and it ended here.
	hash := sha256.New()
	writer := io.MultiWriter(out, hash)
	stats, err := engine.Redact(ctx, writer, nulFreeReader{io.LimitReader(in, size)})
	if err != nil {
		return SessionFileManifest{}, fmt.Errorf("redacting file %s: %w", file.Name, err)
	}
	counts := make(map[string]int, len(stats.ByRule))
	for rule, findings := range stats.ByRule {
		counts[rule] = int(findings)
	}
	return SessionFileManifest{
		ID:             id,
		Name:           file.Name,
		Kind:           file.Kind,
		Member:         member,
		Root:           file.Kind == model.KindTranscript,
		SizeBytes:      stats.BytesWritten,
		SHA256:         hex.EncodeToString(hash.Sum(nil)),
		FindingsByType: counts,
	}, nil
}

// errBinaryContent rejects sessions that are not text. Sessions are JSONL
// records; a NUL byte means the file is binary and the upload must fail
// rather than archive it.
var errBinaryContent = errors.New("session contains NUL bytes (binary content not supported)")

// nulFreeReader fails the stream at the first NUL byte. The redaction engine
// itself accepts arbitrary bytes, so the text-only rule has to be enforced on
// the way in. Bytes read alongside the NUL are dropped with the error; the
// whole build fails, so nothing staged from them survives.
type nulFreeReader struct{ r io.Reader }

func (r nulFreeReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if bytes.IndexByte(p[:n], 0) >= 0 {
		return 0, errBinaryContent
	}
	return n, err
}

func sessionMember(sessionIndex int, fileID string) string {
	return fmt.Sprintf("sessions/%06d/files/%s", sessionIndex+1, fileID)
}

func stagedPath(stagedDir string, sessionIndex int, fileID string) string {
	return filepath.Join(stagedDir, fmt.Sprintf("%06d", sessionIndex+1), fileID)
}
