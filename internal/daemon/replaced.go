package daemon

import (
	"os"
	"time"
)

// replacedCheckInterval is how often the daemon checks whether its own
// executable was replaced on disk.
const replacedCheckInterval = 5 * time.Second

// executableWatch notices the daemon's executable being replaced on disk, by
// an update or a development rebuild. Until the daemon restarts it keeps
// running the old file, and helpers it starts from its own path (the GitHub
// credential helper) run the new one, which refuses a parent that is not the
// same file.
type executableWatch struct {
	path     string
	interval time.Duration
	running  os.FileInfo
	// seen is the replacement observed at the last check; it must be unchanged
	// for one interval, so a file still being written is not started.
	seen os.FileInfo
	// reported is the replacement last reported, so a failed restart is not
	// retried until the file changes again.
	reported os.FileInfo
	next     time.Time
}

func newExecutableWatch(path string, interval time.Duration) *executableWatch {
	if path == "" {
		return nil
	}
	running, err := os.Stat(path)
	if err != nil {
		return nil
	}
	return &executableWatch{path: path, interval: interval, running: running}
}

// replaced reports, once per replacement, an executable at the daemon's path
// that is not the file it runs and has stayed the same for an interval.
func (w *executableWatch) replaced(now time.Time) bool {
	if w == nil || now.Before(w.next) {
		return false
	}
	w.next = now.Add(w.interval)
	info, err := os.Stat(w.path)
	if err != nil || sameExecutable(info, w.running) {
		w.seen = nil
		return false
	}
	stable := w.seen != nil && sameExecutable(info, w.seen)
	w.seen = info
	if !stable || (w.reported != nil && sameExecutable(info, w.reported)) {
		return false
	}
	w.reported = info
	return true
}

func sameExecutable(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
