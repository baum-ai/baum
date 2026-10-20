package userhooks

import (
	"os"
	"testing"
)

// SetGeteuid replaces the effective user for a test and returns the restore.
func SetGeteuid(f func() int) func() {
	original := geteuid
	geteuid = f
	return func() { geteuid = original }
}

// TestMain keeps backups below each test's home: a developer's
// XDG_STATE_HOME would otherwise collect them.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("XDG_STATE_HOME")
	os.Exit(m.Run())
}
