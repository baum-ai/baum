// Package traceupload builds locally redacted archives of agent sessions.
package traceupload

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/goredact"
	"github.com/klauspost/pgzip"
	"golang.org/x/sync/errgroup"

	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/vendors"
)

// ManifestVersion is the archive schema the CLI emits and the server's ingest
// worker reads. There is exactly one, and the worker rejects anything else.
// CLIs and servers of different releases meet, so a new version is an API
// change that raises apiclient.Protocol.
const ManifestVersion = "1"

// MaxUnpackedBytes bounds an archive's decompressed tar stream: the manifest,
// every member and the tar headers between them. The ingest worker holds every
// member in memory and counts every byte it decompresses against this bound,
// so it is what one upload can cost the shared worker. Bundle.UnpackedSize is
// the same stream measured as it is written, so the CLI can refuse an archive
// the worker would reject before uploading it.
const MaxUnpackedBytes int64 = 512 << 20

// redactionConfig is how every uploaded byte is redacted: goredact's balanced
// profile, format-preserving masking so records keep the shape readers parse,
// and record-aligned emission so the staged stream is written in whole JSONL
// records.
//
// goredact streams with bounded memory and no input size limit, so a session
// file is one Redact call — no chunking. It also does not read agent-generated
// identifiers (toolu_, msg_, req_, call_, ...) as secrets, which is what
// retired the structural-ID exemption pass the previous engine (ferret-scan)
// required; those identifiers now survive because they are never detected,
// not because findings on them are dropped.
var redactionConfig = goredact.Config{
	Profile:       goredact.ProfileBalanced,
	MaskStrategy:  goredact.MaskFormatPreserving,
	RecordAligned: true,
}

// Repository identifies the connected repository receiving the sessions.
type Repository struct {
	ID            string            `json:"id"`
	FullName      string            `json:"full_name"`
	RootInventory []RepositoryRoots `json:"-"`
}

// RepositoryRoots binds Git-verified roots from this upload invocation to one
// connected repository.
type RepositoryRoots struct {
	ID       string   `json:"id"`
	FullName string   `json:"full_name"`
	Roots    []string `json:"roots"`
}

// RedactionSummary describes the local redaction applied to the archive. Tool,
// Profile, RuleSet, and Strategy pin exactly which detector produced the
// counts, so an archive records what it was scanned with rather than leaving
// it to be inferred from its upload date.
type RedactionSummary struct {
	Tool           string         `json:"tool"`
	Profile        string         `json:"profile"`
	RuleSet        string         `json:"rule_set"`
	Strategy       string         `json:"strategy"`
	FindingsByType map[string]int `json:"findings_by_type"`
}

// SessionFileManifest describes one redacted file of a session tree.
//
// The archive stores it under an opaque member the builder chose, not under the
// path it had on the uploader's machine: reproducing a vendor's directory
// layout only exists to let a path-based reader run unchanged, and both readers
// take a file set instead. What survives is what a reader actually needs — the
// name the source gave the file, which carries its session and agent ids, and
// what kind of file it is. A source needing a relationship its own files do not
// record should add a source-owned relation record to the manifest rather than
// encode it in a directory name.
type SessionFileManifest struct {
	// ID identifies the file within its session.
	ID string `json:"id"`
	// Name is the file name the source gave it.
	Name string `json:"name"`
	// Kind is what the file is to its source.
	Kind model.FileKind `json:"kind"`
	// Member is the tar member holding the redacted bytes.
	Member string `json:"member"`
	// Root marks the session's own transcript. Exactly one file has it.
	Root           bool           `json:"root,omitempty"`
	SizeBytes      int64          `json:"size_bytes"`
	SHA256         string         `json:"sha256"`
	FindingsByType map[string]int `json:"findings_by_type"`
}

// SessionManifest describes one redacted session tree in the archive. It is the
// file index: nothing outside it is ingestable, and nothing in it may be absent.
type SessionManifest struct {
	Source    vendors.Slug `json:"source"`
	SessionID string       `json:"session_id"`
	// RepoRoot and WorktreeRoot are the roots the collector resolved on the
	// uploading machine. Optional: an older CLI copy sends neither, and such a
	// manifest stays valid at schema_version "1".
	RepoRoot       string                `json:"repo_root,omitempty"`
	WorktreeRoot   string                `json:"worktree_root,omitempty"`
	ModifiedAt     time.Time             `json:"modified_at"`
	Files          []SessionFileManifest `json:"files"`
	SizeBytes      int64                 `json:"size_bytes"`
	FindingsByType map[string]int        `json:"findings_by_type"`
}

func (m SessionManifest) Len() int { return len(m.Files) }
func (m SessionManifest) At(i int) (string, string, string) {
	return m.Files[i].Name, m.Files[i].Name, string(m.Files[i].Kind)
}

// RootFile returns the session's own transcript.
func (m SessionManifest) RootFile() (SessionFileManifest, bool) {
	for _, file := range m.Files {
		if file.Root {
			return file, true
		}
	}
	return SessionFileManifest{}, false
}

// Manifest is the metadata stored alongside the redacted sessions.
type Manifest struct {
	SchemaVersion   string            `json:"schema_version"`
	CreatedAt       time.Time         `json:"created_at"`
	CLIVersion      string            `json:"cli_version"`
	Repository      Repository        `json:"repository"`
	RepositoryRoots []RepositoryRoots `json:"repository_roots,omitempty"`
	Redaction       RedactionSummary  `json:"redaction"`
	Sessions        []SessionManifest `json:"sessions"`
}

// Bundle owns a temporary tar.gz archive. Close removes all temporary data.
type Bundle struct {
	File     *os.File
	Manifest Manifest
	Size     int64
	// UnpackedSize is the length of the decompressed tar stream, which the
	// worker bounds by MaxUnpackedBytes.
	UnpackedSize int64
	tempDir      string
}

// ProgressFunc reports how many sessions have been prepared.
type ProgressFunc func(completed, total int)

// Close closes the archive and removes its private temporary directory.
func (b *Bundle) Close() error {
	if b == nil {
		return nil
	}
	var closeErr error
	if b.File != nil {
		closeErr = b.File.Close()
	}
	removeErr := os.RemoveAll(b.tempDir)
	return errors.Join(closeErr, removeErr)
}

// BuildWithProgress redacts sessions, writes a manifest, and calls progress
// after each session is staged. Calls to progress are serialized.
func BuildWithProgress(
	ctx context.Context,
	sessions []collector.Session,
	repository Repository,
	cliVersion string,
	progress ProgressFunc,
) (_ *Bundle, err error) {
	// One engine serves every session: it is immutable after construction and
	// safe for concurrent Redact calls.
	engine, err := goredact.New(redactionConfig)
	if err != nil {
		return nil, fmt.Errorf("constructing redaction engine: %w", err)
	}

	tempDir, err := os.MkdirTemp("", "baum-trace-upload-")
	if err != nil {
		return nil, fmt.Errorf("creating temporary upload directory: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(tempDir)
		}
	}()

	rootInventory := make([]RepositoryRoots, len(repository.RootInventory))
	for i, entry := range repository.RootInventory {
		rootInventory[i] = entry
		rootInventory[i].Roots = append([]string(nil), entry.Roots...)
		sort.Strings(rootInventory[i].Roots)
	}
	sort.Slice(rootInventory, func(i, j int) bool {
		left, right := strings.ToLower(rootInventory[i].FullName), strings.ToLower(rootInventory[j].FullName)
		if left == right {
			return rootInventory[i].ID < rootInventory[j].ID
		}
		return left < right
	})

	manifest := Manifest{
		SchemaVersion:   ManifestVersion,
		CreatedAt:       time.Now().UTC(),
		CLIVersion:      cliVersion,
		Repository:      Repository{ID: repository.ID, FullName: repository.FullName},
		RepositoryRoots: rootInventory,
		Redaction: RedactionSummary{
			Tool:           "goredact",
			Profile:        redactionConfig.Profile.String(),
			RuleSet:        engine.RuleSetVersion(),
			Strategy:       redactionConfig.MaskStrategy.String(),
			FindingsByType: map[string]int{},
		},
		Sessions: make([]SessionManifest, 0, len(sessions)),
	}

	stagedDir := filepath.Join(tempDir, "sessions")
	if err := os.Mkdir(stagedDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating session staging directory: %w", err)
	}
	entries := make([]SessionManifest, len(sessions))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(runtime.GOMAXPROCS(0))
	var progressMu sync.Mutex
	completed := 0
	for i, session := range sessions {
		group.Go(func() error {
			entry, err := stageSession(groupCtx, engine, stagedDir, i, session)
			if err != nil {
				return err
			}
			entries[i] = entry
			if progress != nil {
				progressMu.Lock()
				completed++
				progress(completed, len(sessions))
				progressMu.Unlock()
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	for _, entry := range entries {
		manifest.Sessions = append(manifest.Sessions, entry)
		mergeCounts(manifest.Redaction.FindingsByType, entry.FindingsByType)
	}

	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding upload manifest: %w", err)
	}
	manifestData = append(manifestData, '\n')
	manifestPath := filepath.Join(tempDir, "manifest.json")
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		return nil, fmt.Errorf("writing upload manifest: %w", err)
	}

	archivePath := filepath.Join(tempDir, "baum-traces.tar.gz")
	unpackedSize, err := writeArchive(archivePath, manifestPath, stagedDir, manifest)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("opening upload archive: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stating upload archive: %w", err)
	}
	return &Bundle{File: file, Manifest: manifest, Size: info.Size(), UnpackedSize: unpackedSize, tempDir: tempDir}, nil
}

// archiveCompression is the deflate level the archive is written at. Level 8
// with parallel gzip is smaller *and* faster than the serial default (6) was:
// on 238 MiB of real transcripts, 2.42s/77.9 MiB became 0.19s/77.8 MiB. Raising
// the level is what spends the parallelism on a smaller upload instead of
// banking all of it as local time.
const archiveCompression = 8

// archiveBlockBytes is how much of the tar stream each compression worker takes.
// Larger blocks give the deflate window more history, so the archive is smaller;
// 1 MiB costs archiveBlockBytes x GOMAXPROCS of buffering and recovered 1.0 MiB
// over pgzip's 256 KiB default.
const archiveBlockBytes = 1 << 20

// writeArchive writes the gzip+tar archive and reports the length of its tar
// stream.
func writeArchive(archivePath, manifestPath, stagedDir string, manifest Manifest) (_ int64, err error) {
	archive, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("creating upload archive: %w", err)
	}
	defer func() { err = errors.Join(err, archive.Close()) }()
	// Compression is the whole cost of this phase — deflate ran at ~98 MiB/s on
	// one core while every other upload phase had already been parallelized — so
	// it is spread over the machine. The output is an ordinary gzip stream, which
	// is what the server's ingest worker unpacks.
	gz, err := pgzip.NewWriterLevel(archive, archiveCompression)
	if err != nil {
		return 0, fmt.Errorf("creating archive compressor: %w", err)
	}
	if err := gz.SetConcurrency(archiveBlockBytes, runtime.GOMAXPROCS(0)); err != nil {
		return 0, fmt.Errorf("configuring archive compressor: %w", err)
	}
	defer func() { err = errors.Join(err, gz.Close()) }()
	stream := &countingWriter{w: gz}
	tw := tar.NewWriter(stream)

	if err := addFile(tw, manifestPath, "manifest.json", manifest.CreatedAt); err != nil {
		return 0, errors.Join(err, tw.Close())
	}
	for i, session := range manifest.Sessions {
		for _, file := range session.Files {
			if err := addFile(tw, stagedPath(stagedDir, i, file.ID), file.Member, session.ModifiedAt); err != nil {
				return 0, errors.Join(err, tw.Close())
			}
		}
	}
	// Closing writes the end-of-archive blocks, which the stream includes.
	if err := tw.Close(); err != nil {
		return 0, fmt.Errorf("closing upload archive: %w", err)
	}
	return stream.n, nil
}

// countingWriter counts the bytes written through it.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func addFile(tw *tar.Writer, path, archivePath string, modifiedAt time.Time) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening staged file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stating staged file: %w", err)
	}
	header := &tar.Header{Name: archivePath, Mode: 0o600, Size: info.Size(), ModTime: modifiedAt.UTC()}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("writing archive header: %w", err)
	}
	if _, err := io.Copy(tw, file); err != nil {
		return fmt.Errorf("writing archive file: %w", err)
	}
	return nil
}

func mergeCounts(dst, src map[string]int) {
	keys := make([]string, 0, len(src))
	for key := range src {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		dst[key] += src[key]
	}
}
