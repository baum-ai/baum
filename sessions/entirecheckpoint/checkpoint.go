// Package entirecheckpoint reads one Entire checkpoint tree without assigning
// session-model meaning to its records.
package entirecheckpoint

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/baum-ai/baum/sessions/traceprocessing"
)

// maxSessions bounds the session entries one checkpoint may list. Each entry
// is read and kept in memory, so the count multiplies the checkpoint's size.
// SessionList enforces it while decoding metadata.json.
const maxSessions = 64

var ErrEscapingSessionRef = errors.New("Entire session ref escapes checkpoint root")

type Checkpoint struct {
	Metadata CheckpointMetadata `json:"metadata"`
	Sessions []*SessionSnapshot `json:"sessions"`
	// Tasks are the subagents kept at the checkpoint root, shared by every
	// session in it (tasks.go).
	Tasks []*Task `json:"tasks,omitempty"`
}

type SessionSnapshot struct {
	Index          int              `json:"index"`
	Directory      string           `json:"directory"`
	Refs           SessionRefs      `json:"refs"`
	Metadata       SessionMetadata  `json:"metadata"`
	Full           []byte           `json:"full,omitempty"`
	FullPresent    bool             `json:"full_present"`
	CompactRaw     []byte           `json:"compact_raw,omitempty"`
	CompactPresent bool             `json:"compact_present"`
	Compact        []CompactRecord  `json:"compact,omitempty"`
	Prompt         []byte           `json:"prompt,omitempty"`
	PromptPresent  bool             `json:"prompt_present"`
	AssetsManifest []byte           `json:"assets_manifest,omitempty"`
	AssetsPresent  bool             `json:"assets_present"`
	ContentHash    string           `json:"content_hash,omitempty"`
	Hash           HashVerification `json:"hash"`
}

// ParseCheckpoint reads one checkpoint directory from fsys. The filesystem
// root is the checkpoint directory itself, matching tar exports and the hosted
// resolver's checkpoint filesystem.
func ParseCheckpoint(fsys fs.FS) (*Checkpoint, error) {
	return ParseCheckpointWith(fsys, traceprocessing.Local)
}

// ParseCheckpointWith retains the hosted resource and duplicate-source checks,
// task discovery and Pi compact interpretation, while local defaults stay intact.
func ParseCheckpointWith(fsys fs.FS, profile traceprocessing.MappingProfile) (*Checkpoint, error) {
	raw, err := fs.ReadFile(fsys, "metadata.json")
	if err != nil {
		return nil, fmt.Errorf("read checkpoint metadata.json: %w", err)
	}
	metadata, err := decodeCheckpointMetadataWith(raw, profile)
	if err != nil {
		return nil, fmt.Errorf("decode checkpoint metadata.json: %w", err)
	}
	if metadata.CheckpointID == "" {
		return nil, fmt.Errorf("checkpoint metadata.json has no checkpoint_id")
	}
	if len(metadata.Sessions) == 0 {
		return nil, fmt.Errorf("checkpoint %s lists no sessions", metadata.CheckpointID)
	}

	checkpoint := &Checkpoint{Metadata: metadata}
	seenSessionIDs := map[string]int{}
	owners := map[string]int{}
	for index, refs := range metadata.Sessions {
		source := fsys
		if profile == traceprocessing.Hosted {
			source = claimingFS{FS: fsys, index: index, owners: owners}
		}
		snapshot, err := parseSession(source, index, refs, profile)
		if err != nil {
			return nil, fmt.Errorf("session %d: %w", index, err)
		}
		if id := snapshot.Metadata.SessionID; profile == traceprocessing.Hosted && id != "" {
			if first, repeated := seenSessionIDs[id]; repeated {
				return nil, fmt.Errorf("checkpoint %s: sessions %d and %d share session_id %q",
					metadata.CheckpointID, first, index, id)
			}
			seenSessionIDs[id] = index
		}
		checkpoint.Sessions = append(checkpoint.Sessions, snapshot)
	}
	if profile == traceprocessing.Hosted {
		if checkpoint.Tasks, err = parseTasks(fsys); err != nil {
			return nil, err
		}
	}
	return checkpoint, nil
}

// claimingFS records which session entry first opened each file and fails when
// any ref opens it again, from another entry or the same one. Refs fall back to
// a session directory when the named file is missing, so only the files
// actually read identify sharing.
type claimingFS struct {
	fs.FS
	index  int
	owners map[string]int
}

func (c claimingFS) Open(name string) (fs.File, error) {
	file, err := c.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if first, claimed := c.owners[name]; claimed {
		_ = file.Close()
		if first == c.index {
			return nil, fmt.Errorf("session %d reads %q more than once", c.index, name)
		}
		return nil, fmt.Errorf("sessions %d and %d both read %q", first, c.index, name)
	}
	c.owners[name] = c.index
	return file, nil
}

func parseSession(fsys fs.FS, index int, refs SessionRefs, profile traceprocessing.MappingProfile) (*SessionSnapshot, error) {
	dir, err := sessionDir(refs, index)
	if err != nil {
		return nil, err
	}
	snapshot := &SessionSnapshot{Index: index, Directory: dir, Refs: refs}

	metadataRaw, err := readRequired(fsys, refs.Metadata, path.Join(dir, "metadata.json"), "session metadata.json")
	if err != nil {
		return nil, err
	}
	if snapshot.Metadata, err = decodeSessionMetadata(metadataRaw); err != nil {
		return nil, fmt.Errorf("decode session metadata.json: %w", err)
	}

	if snapshot.Full, snapshot.FullPresent, err = readOptional(
		fsys, refs.Transcript, path.Join(dir, "full.jsonl"), "full.jsonl",
	); err != nil {
		return nil, err
	}
	if snapshot.CompactRaw, snapshot.CompactPresent, err = readOptional(
		fsys, refs.CompactTranscript, path.Join(dir, "transcript.jsonl"), "transcript.jsonl",
	); err != nil {
		return nil, err
	}
	if snapshot.CompactPresent {
		if snapshot.Compact, err = DecodeCompactWith(bytes.NewReader(snapshot.CompactRaw), profile); err != nil {
			return nil, fmt.Errorf("decode transcript.jsonl: %w", err)
		}
	}
	if snapshot.Prompt, snapshot.PromptPresent, err = readOptional(
		fsys, refs.Prompt, path.Join(dir, "prompt.txt"), "prompt.txt",
	); err != nil {
		return nil, err
	}
	if snapshot.AssetsManifest, snapshot.AssetsPresent, err = readOptional(
		fsys, refs.AssetsManifest, path.Join(dir, "assets/manifest.json"), "assets/manifest.json",
	); err != nil {
		return nil, err
	}

	hashRaw, hashPresent, err := readOptional(
		fsys, refs.ContentHash, path.Join(dir, "content_hash.txt"), "content_hash.txt",
	)
	if err != nil {
		return nil, err
	}
	if hashPresent {
		snapshot.ContentHash = strings.TrimSpace(string(hashRaw))
	}
	snapshot.Hash = VerifyContentHash(snapshot.Full, snapshot.FullPresent, snapshot.ContentHash)
	return snapshot, nil
}

func readRequired(fsys fs.FS, ref, fallback, label string) ([]byte, error) {
	data, _, err := readRef(fsys, ref, fallback, true)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	return data, nil
}

func readOptional(fsys fs.FS, ref, fallback, label string) ([]byte, bool, error) {
	data, present, err := readRef(fsys, ref, fallback, false)
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", label, err)
	}
	return data, present, nil
}

func sessionDir(refs SessionRefs, index int) (string, error) {
	for _, ref := range []string{refs.Metadata, refs.Transcript, refs.CompactTranscript, refs.ContentHash, refs.Prompt, refs.AssetsManifest} {
		if ref == "" {
			continue
		}
		clean, err := cleanRef(ref)
		if err != nil {
			return "", err
		}
		if dir := path.Base(path.Dir(clean)); dir != "." && dir != "/" {
			return dir, nil
		}
	}
	return strconv.Itoa(index), nil
}

func readRef(fsys fs.FS, ref, fallback string, required bool) ([]byte, bool, error) {
	if ref != "" {
		clean, err := cleanRef(ref)
		if err != nil {
			return nil, false, err
		}
		data, readErr := fs.ReadFile(fsys, clean)
		if readErr == nil {
			return data, true, nil
		}
		if !errors.Is(readErr, fs.ErrNotExist) {
			return nil, false, readErr
		}
		if canUseCheckpointRootFallback(clean, fallback) {
			data, fallbackErr := fs.ReadFile(fsys, fallback)
			if fallbackErr == nil {
				return data, true, nil
			}
			if !errors.Is(fallbackErr, fs.ErrNotExist) {
				return nil, false, fallbackErr
			}
		}
		if required {
			return nil, false, readErr
		}
		return nil, false, nil
	}
	data, err := fs.ReadFile(fsys, fallback)
	if err == nil {
		return data, true, nil
	}
	if errors.Is(err, fs.ErrNotExist) && !required {
		return nil, false, nil
	}
	return nil, false, err
}

func cleanRef(ref string) (string, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(ref), "/")
	for _, component := range strings.Split(trimmed, "/") {
		if component == ".." {
			return "", fmt.Errorf("%w: %q", ErrEscapingSessionRef, ref)
		}
	}
	clean := path.Clean(trimmed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q", ErrEscapingSessionRef, ref)
	}
	return clean, nil
}

func canUseCheckpointRootFallback(ref, fallback string) bool {
	return path.Base(ref) == path.Base(fallback) &&
		path.Base(path.Dir(ref)) == path.Base(path.Dir(fallback))
}
