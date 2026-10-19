package hookevents

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/privatefs"
)

// Spool bounds: the spool never holds more than MaxSpoolEvents events or
// MaxSpoolBytes bytes. A hook that would pass either drops the oldest events
// first and logs each one it drops.
const (
	MaxSpoolEvents       = 1000
	MaxSpoolBytes  int64 = 8 << 20
)

// staleTemporary is how old a temporary spool file must be before a drain
// removes it: a hook writes and renames one within milliseconds, so an older
// one was left by a hook that was killed.
const staleTemporary = 10 * time.Minute

// spoolName is a spooled event's file name: the nanosecond it was spooled,
// zero-padded so names sort in spool order, and a random suffix.
var spoolName = regexp.MustCompile(`^[0-9]{20}-[0-9a-f]{32}\.json$`)

// Spool is the directory events wait in while the daemon cannot take them:
// one file per event, mode 0600, in a 0700 directory.
type Spool struct {
	Dir string
	// MaxEvents and MaxBytes bound the spool; zero is MaxSpoolEvents and
	// MaxSpoolBytes.
	MaxEvents int
	MaxBytes  int64
	Log       *slog.Logger
	// newer names the spooled events of a newer Version already logged, so
	// each is logged once rather than on every drain.
	newer map[string]bool
}

// SpoolDir is the spool directory of the daemon whose state directory paths
// name.
func SpoolDir(paths daemon.Paths) string { return filepath.Join(paths.State, "spool") }

func (s *Spool) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s.Log
}

// Put writes e to the spool, first dropping the oldest events when it would
// pass a bound.
func (s *Spool) Put(e Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(data) > maxEventSize {
		return fmt.Errorf("the event is %d bytes, over the %d a spooled event may have", len(data), maxEventSize)
	}
	if err = privatefs.MkdirAll(s.Dir); err != nil {
		return err
	}
	s.makeRoom(int64(len(data)))
	// CreateTemp makes the file 0600 before anything is written to it, and a
	// drain ignores it until the rename.
	f, err := os.CreateTemp(s.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	err = errors.Join(err, f.Close())
	if err == nil {
		name := fmt.Sprintf("%020d-%s.json", time.Now().UnixNano(), NewID())
		err = os.Rename(f.Name(), filepath.Join(s.Dir, name))
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}

type spooled struct {
	name string
	size int64
}

// entries lists the spooled events, oldest first.
func (s *Spool) entries() []spooled {
	dir, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil
	}
	var entries []spooled
	for _, entry := range dir {
		if !spoolName.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		entries = append(entries, spooled{name: entry.Name(), size: info.Size()})
	}
	slices.SortFunc(entries, func(a, b spooled) int { return strings.Compare(a.name, b.name) })
	return entries
}

// makeRoom drops the oldest events until one more of size bytes fits. Hooks
// running at once may each drop one; the bound still holds.
func (s *Spool) makeRoom(size int64) {
	maxEvents, maxBytes := s.MaxEvents, s.MaxBytes
	if maxEvents <= 0 {
		maxEvents = MaxSpoolEvents
	}
	if maxBytes <= 0 {
		maxBytes = MaxSpoolBytes
	}
	entries := s.entries()
	var total int64
	for _, entry := range entries {
		total += entry.size
	}
	for len(entries) > 0 && (len(entries)+1 > maxEvents || total+size > maxBytes) {
		oldest := entries[0]
		entries = entries[1:]
		total -= oldest.size
		if err := os.Remove(filepath.Join(s.Dir, oldest.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		s.log().Warn("hook event spool full; dropped the oldest event", "spooled_at", spooledAt(oldest.name), "max_events", maxEvents, "max_bytes", maxBytes)
	}
}

// spooledAt is when the event in a spool file was spooled.
func spooledAt(name string) time.Time {
	nanos, err := strconv.ParseInt(name[:20], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(0, nanos).UTC()
}

// Drain hands every spooled event to record, oldest first, and removes it
// once record succeeds. It stops at the first record error, leaving that
// event and later ones for the next drain. A file that is not a small private
// file, or not an event, is removed and logged; an event of a newer Version
// is left for a newer daemon.
func (s *Spool) Drain(record func(Event) error) error {
	s.removeStaleTemporaries()
	for _, entry := range s.entries() {
		path := filepath.Join(s.Dir, entry.name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !privatefs.PrivateFile(path, info) || info.Size() > maxEventSize {
			s.discard(path, "not a small private file")
			continue
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		var e Event
		if err = json.Unmarshal(data, &e); err != nil {
			s.discard(path, "not a hook event")
			continue
		}
		switch err = e.check(); {
		case errors.Is(err, errNewer):
			if s.newer == nil {
				s.newer = map[string]bool{}
			}
			if !s.newer[entry.name] {
				s.newer[entry.name] = true
				s.log().Info("left a spooled hook event of a newer format for a newer baum daemon", "version", e.V)
			}
			continue
		case err != nil:
			s.discard(path, "not a valid hook event")
			continue
		}
		if err = record(e); err != nil {
			return err
		}
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Spool) discard(path, reason string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
	s.log().Warn("dropped a spooled hook event", "reason", reason, "spooled_at", spooledAt(filepath.Base(path)))
}

func (s *Spool) removeStaleTemporaries() {
	dir, err := os.ReadDir(s.Dir)
	if err != nil {
		return
	}
	for _, entry := range dir {
		if !strings.HasPrefix(entry.Name(), ".tmp-") {
			continue
		}
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > staleTemporary {
			_ = os.Remove(filepath.Join(s.Dir, entry.Name()))
		}
	}
}
