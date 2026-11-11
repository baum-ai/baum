package reviewruns

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// manifestPrefix names the file next to a run directory (in the root, never
// inside the agent's working directory) that records which agent sessions the
// run created. A name with this prefix is not a run directory name.
const manifestPrefix = ".sessions-"

// manifest is what a run records about its sessions: when the directory was
// created (no session of it is older), the session ids known so far, and
// whether Complete says the list holds every session the run created. A
// directory without a manifest, or with an incomplete one, predates this
// record or crashed before writing it; removing it falls back to a bounded
// scan of the Codex store.
type manifest struct {
	Created  time.Time `json:"created"`
	Complete bool      `json:"complete"`
	IDs      []string  `json:"ids"`
}

var manifestMu sync.Mutex

func manifestPath(root, dir string) string {
	return filepath.Join(resolvedRoot(root), manifestPrefix+filepath.Base(dir))
}

func readManifest(root, dir string) (manifest, bool) {
	data, err := os.ReadFile(manifestPath(root, dir))
	if err != nil {
		return manifest{}, false
	}
	var m manifest
	if json.Unmarshal(data, &m) != nil || m.Created.IsZero() {
		return manifest{}, false
	}
	for _, id := range m.IDs {
		if !sessionName.MatchString(id) {
			return manifest{}, false
		}
	}
	return m, true
}

func writeManifest(root, dir string, m manifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	path := manifestPath(root, dir)
	tmp, err := os.CreateTemp(filepath.Dir(path), manifestPrefix+"tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Record adds the agent session ids a call in the run directory dir created,
// so removing the run deletes exactly those sessions instead of scanning the
// agent's store. complete says ids are all the run's sessions so far; a later
// call never lowers it. A directory that is not a run directory under root
// is ignored.
func Record(root, dir string, complete bool, ids ...string) error {
	dir = filepath.Clean(dir)
	if !underRoot(root, dir) {
		return nil
	}
	for _, id := range ids {
		if !sessionName.MatchString(id) {
			return errors.New("invalid session id")
		}
	}
	manifestMu.Lock()
	defer manifestMu.Unlock()
	m, ok := readManifest(root, dir)
	if !ok {
		// A directory with no record of its start: its sessions are not all
		// known, whatever the caller says, and only a scan can find the
		// rest, from any date (the epoch means an unknown start).
		m = manifest{Created: time.Unix(1, 0)}
		complete = false
	}
	for _, id := range ids {
		if !slices.Contains(m.IDs, id) {
			m.IDs = append(m.IDs, id)
		}
	}
	m.Complete = m.Complete || complete
	return writeManifest(root, dir, m)
}

// MarkIncomplete notes that a call is about to run in dir whose session id is
// not known yet (a chat resuming or forking in a kept directory): if it
// fails before Record, removing the directory scans for the session. A
// directory with no record needs nothing, it is scanned anyway.
func MarkIncomplete(root, dir string) error {
	dir = filepath.Clean(dir)
	if !underRoot(root, dir) {
		return nil
	}
	manifestMu.Lock()
	defer manifestMu.Unlock()
	m, ok := readManifest(root, dir)
	if !ok || !m.Complete {
		return nil
	}
	m.Complete = false
	return writeManifest(root, dir, m)
}

// Forget drops the record of a run directory the caller removed itself, whose
// sessions it knows there are none of.
func Forget(root, dir string) {
	if underRoot(root, filepath.Clean(dir)) {
		removeManifest(root, filepath.Clean(dir))
	}
}

func removeManifest(root, dir string) {
	manifestMu.Lock()
	defer manifestMu.Unlock()
	_ = os.Remove(manifestPath(root, dir))
}
