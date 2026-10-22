//go:build unix

package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecutablePolicyRefusesFilesOthersCanReplace(t *testing.T) {
	for _, c := range []struct {
		name   string
		chmod  string // relative to the temporary directory
		mode   os.FileMode
		reason string
	}{
		{"group-writable file", "d/baum", 0o775, "writable"},
		{"world-writable file", "d/baum", 0o757, "writable"},
		{"not executable", "d/baum", 0o644, "not executable"},
		{"group-writable directory", "d", 0o775, "writable"},
		{"world-writable directory", "d", 0o757, "writable"},
		{"sticky world-writable directory", "d", 0o777 | os.ModeSticky, "writable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := safeDir(t)
			baum := executableIn(t, dir, "d/baum")
			policy := policyFor(dir, baum)
			if _, err := policy.trusted(baum); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(dir, c.chmod), c.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := policy.trusted(baum); err == nil || !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("allowed, or not because %s: %v", c.reason, err)
			}
		})
	}
}

// The executable is checked again just before it runs: one replaced by a
// file others can write after the restart was scheduled is refused, and the
// daemon keeps running.
func TestRestartRechecksTheExecutableBeforeRunningIt(t *testing.T) {
	dir := safeDir(t)
	baum := executableIn(t, dir, "bin/baum")
	paths, idle, respawned := busyDaemon(t, policyFor(dir, baum).trusted)
	if status := askRestart(t, paths, "v0.2.0", baum); !status.RestartPending {
		t.Fatal("refused the baum on the PATH")
	}
	if err := os.Chmod(baum, 0o777); err != nil {
		t.Fatal(err)
	}
	idle()
	for deadline := time.Now().Add(10 * time.Second); ; {
		hello := rawCall(t, paths, `{"protocol":1,"method":"hello"}`)
		if hello.Status != nil && !hello.Status.RestartPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restart still pending: %+v", hello)
		}
		time.Sleep(50 * time.Millisecond)
	}
	keptRunning(t, paths, respawned)
}

// On macOS a directory group-writable only by the admin group, such as
// /Applications, is accepted; the staff group, files, world-writable and
// sticky directories are not.
func TestAdminGroupDirectories(t *testing.T) {
	for _, c := range []struct {
		name string
		dir  bool
		gid  uint32
		mode os.FileMode
		ok   bool
	}{
		{"admin directory", true, 80, os.ModeDir | 0o775, true},
		{"staff directory", true, 20, os.ModeDir | 0o775, false},
		{"other group", true, 81, os.ModeDir | 0o775, false},
		{"world-writable", true, 80, os.ModeDir | 0o777, false},
		{"sticky", true, 80, os.ModeDir | os.ModeSticky | 0o775, false},
		{"file", false, 80, 0o775, false},
	} {
		if got := adminWritableOnly(c.dir, c.gid, c.mode); got != c.ok {
			t.Errorf("%s: accepted = %v, want %v", c.name, got, c.ok)
		}
	}
	// Even if the admin group were the staff group, it is refused.
	old := adminGID
	adminGID = staffGID
	defer func() { adminGID = old }()
	if adminWritableOnly(true, staffGID, os.ModeDir|0o775) {
		t.Error("accepted the staff group")
	}
}

// Through the policy: with the allowance on and the directory's group taken
// as the admin group, a group-writable directory above the executable passes,
// and without the allowance (other platforms) it does not.
func TestAdminGroupDirectoryAboveTheExecutable(t *testing.T) {
	var gid uint32
	for _, g := range mustGroups(t) {
		if g != staffGID {
			gid = uint32(g)
			break
		}
	}
	if gid == 0 {
		t.Skip("no group other than staff to test with")
	}
	dir := safeDir(t)
	baum := executableIn(t, dir, "Applications/baum")
	apps := filepath.Join(dir, "Applications")
	if err := os.Chown(apps, -1, int(gid)); err != nil {
		t.Skipf("cannot change the group: %v", err)
	}
	if err := os.Chmod(apps, 0o775); err != nil {
		t.Fatal(err)
	}
	oldOn, oldGID := adminGroupDirectories, adminGID
	defer func() { adminGroupDirectories, adminGID = oldOn, oldGID }()
	adminGroupDirectories, adminGID = false, gid
	if _, err := policyFor(dir, baum).trusted(baum); err == nil {
		t.Fatal("accepted a group-writable directory without the allowance")
	}
	adminGroupDirectories = true
	if _, err := policyFor(dir, baum).trusted(baum); err != nil {
		t.Fatalf("refused the admin group's directory: %v", err)
	}
	// The executable itself never gets the allowance.
	if err := os.Chown(baum, -1, int(gid)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(baum, 0o775); err != nil {
		t.Fatal(err)
	}
	if _, err := policyFor(dir, baum).trusted(baum); err == nil {
		t.Fatal("accepted a group-writable executable")
	}
}

func mustGroups(t *testing.T) []int {
	t.Helper()
	groups, err := os.Getgroups()
	if err != nil {
		t.Skipf("no groups: %v", err)
	}
	return append(groups, os.Getgid())
}
