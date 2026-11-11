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

// Revision fingerprints the composer's own record: its update time, status and
// the list of bubbles it holds. It changes when the conversation does and not
// when Cursor writes some other conversation to the same database file.
func (s *Store) Revision(composerID string) (string, error) {
	raw, err := s.rawComposer(composerID)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:12]), nil
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
