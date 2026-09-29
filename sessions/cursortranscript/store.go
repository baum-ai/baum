// Package cursortranscript reads Cursor's global SQLite conversation store as
// typed records. It classifies source records but does not decide what they mean
// in the shared session model; internal/cursorsession owns that mapping.
package cursortranscript

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/lastpersonlabs/baum/sessions/sqliteread"
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

// Close releases the read-only database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}
