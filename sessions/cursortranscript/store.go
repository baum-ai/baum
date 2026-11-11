// Package cursortranscript reads Cursor's global SQLite conversation store as
// typed records. It classifies source records but does not decide what they mean
// in the shared session model; the cursorsession package owns that mapping.
package cursortranscript

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/baum-ai/baum/sessions/sqliteread"
)

const (
	cursorTable       = "cursorDiskKV"
	headerTable       = "composerHeaders"
	composerKeyPrefix = "composerData:"
	bubbleKeyPrefix   = "bubbleId:"
	diffKeyPrefix     = "codeBlockDiff:"
)

// Store is an open, read-only Cursor global store.
type Store struct {
	db                   *sqliteread.DB
	discovery            *Discovery
	diffJunkByComposerID map[string]int
}

// DefaultStorePath returns Cursor's global conversation database path.
func DefaultStorePath() string {
	config, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(config, "Cursor", "User", "globalStorage", "state.vscdb")
}

// Open opens path read-only. SQLite applies the live WAL through sqliteread;
// this package never opens Cursor's stale .backup file.
func Open(path string) (*Store, error) {
	if path == "" {
		path = DefaultStorePath()
	}
	if filepath.Ext(path) == ".backup" {
		return nil, fmt.Errorf("Cursor backup stores are not conversation sources: %s", path)
	}
	db, err := sqliteread.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening Cursor store %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// rawComposer returns the stored composerData row of one composer without
// reading its bubbles, diffs or children. It is a single indexed lookup, so it
// stays cheap in a store holding many large conversations.
func (s *Store) rawComposer(composerID string) ([]byte, error) {
	key := composerKeyPrefix + composerID
	var raw []byte
	err := s.db.ScanPrefix(cursorTable, "key", key, func(row sqliteread.Row) error {
		if rowKey, ok := row.Text("key"); !ok || rowKey != key {
			return nil
		}
		if value, ok := rowBytes(row, "value"); ok && !isNull(value) {
			raw = value
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("composer %s has no readable composerData record", composerID)
	}
	return raw, nil
}

// Name is the conversation name stored in the composer's own record, read
// without building its tree. It is what Tree(composerID).Composer.Data.Name
// holds.
func (s *Store) Name(composerID string) (string, error) {
	raw, err := s.rawComposer(composerID)
	if err != nil {
		return "", err
	}
	data, err := DecodeComposerData(raw)
	if err != nil {
		return "", fmt.Errorf("%s%s: %w", composerKeyPrefix, composerID, err)
	}
	return data.Name, nil
}

// RecordRevision fingerprints the composer's own record: its update time,
// status, name and the list of bubbles it holds.
func (s *Store) RecordRevision(composerID string) (string, error) {
	raw, err := s.rawComposer(composerID)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:12]), nil
}

// Revision fingerprints what Tree(composerID) reads: the record of the
// composer and of every composer listed beneath it, and each of their bubble
// and code block diff rows. It changes when the conversation does, however
// Cursor edits it, and not when Cursor writes some other conversation to the
// same database file. Unlike Tree it decodes nothing and indexes nothing.
func (s *Store) Revision(composerID string) (string, error) {
	hash := sha256.New()
	seen := map[string]bool{}
	var walk func(id string) error
	walk = func(id string) error {
		if seen[id] {
			return nil
		}
		seen[id] = true
		raw, err := s.rawComposer(id)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "composer\x00%s\x00%d\x00", id, len(raw))
		hash.Write(raw)
		// The index row names the workspace the conversation is placed by.
		if err := s.db.ScanPrefix(headerTable, "composerId", id, func(row sqliteread.Row) error {
			if headerID, _ := row.Text("composerId"); headerID != id {
				return nil
			}
			_, _ = fmt.Fprintf(hash, "header\x00%s", id)
			for i, column := range row.Columns {
				if i < len(row.Values) {
					_, _ = fmt.Fprintf(hash, "\x00%s=%v", column, row.Values[i])
				}
			}
			_, _ = fmt.Fprintln(hash)
			return nil
		}); err != nil {
			return err
		}
		for _, prefix := range []string{bubbleKeyPrefix + id + ":", diffKeyPrefix + id + ":"} {
			if err := s.db.ScanPrefix(cursorTable, "key", prefix, func(row sqliteread.Row) error {
				key, _ := row.Text("key")
				value, _ := rowBytes(row, "value")
				sum := sha256.Sum256(value)
				_, _ = fmt.Fprintf(hash, "row\x00%s\x00%x\n", key, sum)
				return nil
			}); err != nil {
				return err
			}
		}
		data, err := DecodeComposerData(raw)
		if err != nil {
			return nil
		}
		for _, child := range data.SubagentComposerIDs {
			if child == "" {
				continue
			}
			// A child with no record of its own is a dangling reference, as in Tree.
			if _, err := s.rawComposer(child); err != nil {
				continue
			}
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(composerID); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)[:12]), nil
}

// Close releases the read-only database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}
