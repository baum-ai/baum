package reviewruns

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/sessions/collector/codex"
)

// datedDirs lists Codex's day folders (sessions/YYYY/MM/DD) newest first,
// leaving out days that end before since minus two days of slack for time
// zones. Only real directories with numeric names count; a symlink is never
// followed.
func datedDirs(sessions string, since time.Time) []string {
	numeric := func(dir string) []int {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil
		}
		var out []int
		for _, e := range entries {
			if n, err := strconv.Atoi(e.Name()); err == nil && n > 0 && e.Type().IsDir() {
				out = append(out, n)
			}
		}
		sort.Sort(sort.Reverse(sort.IntSlice(out)))
		return out
	}
	floor := since.UTC().AddDate(0, 0, -2)
	var days []string
	for _, y := range numeric(sessions) {
		ydir := filepath.Join(sessions, fmt.Sprintf("%04d", y))
		for _, m := range numeric(ydir) {
			mdir := filepath.Join(ydir, fmt.Sprintf("%02d", m))
			for _, d := range numeric(mdir) {
				if time.Date(y, time.Month(m), d, 23, 59, 59, 0, time.UTC).Before(floor) {
					continue
				}
				days = append(days, filepath.Join(mdir, fmt.Sprintf("%02d", d)))
			}
		}
	}
	return days
}

// removeCodex deletes the rollouts of dir's sessions from Codex's store. It
// lists only the day folders from since on, newest first. A rollout whose
// name ends in -<id>.jsonl for one of ids is removed when its recorded cwd is
// dir. With walk it also reads the first record of the other rollouts (at
// most maxRollouts) and removes those recording dir as their cwd; that is the
// fallback for runs with no complete record of their sessions. Nothing else
// is removed: the cwd of every candidate is checked first.
func removeCodex(dir string, ids []string, since time.Time, walk bool) error {
	if len(ids) == 0 && !walk {
		return nil
	}
	sessions, ok := codexSessions()
	if !ok {
		return nil
	}
	var errs []error
	scanned := 0
	for _, day := range datedDirs(sessions, since) {
		entries, err := os.ReadDir(day)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			// A link is neither a directory nor a regular file here.
			if !e.Type().IsRegular() || !strings.HasPrefix(name, "rollout-") || filepath.Ext(name) != ".jsonl" {
				continue
			}
			byID := slices.ContainsFunc(ids, func(id string) bool { return strings.HasSuffix(name, "-"+id+".jsonl") })
			if !byID {
				if !walk {
					continue
				}
				if scanned++; scanned > maxRollouts {
					return errors.Join(errs...)
				}
			}
			path := filepath.Join(day, name)
			if cwd, root, err := codex.SessionWorkingDir(path); err == nil && root && cwd == dir {
				if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					errs = append(errs, err)
				}
			}
		}
	}
	return errors.Join(errs...)
}
