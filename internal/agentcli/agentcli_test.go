//go:build unix

package agentcli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeAgent(t *testing.T, dir, name, version string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\n[ \"$1\" = --version ] && echo '" + version + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSupported(t *testing.T) {
	for _, tc := range []struct {
		version, minimum string
		want             bool
	}{
		{"0.166.0", "0.156.1", true},
		{"0.156.1", "0.156.1", true},
		{"0.128.0", "0.156.1", false},
		{"2.1.1000", "2.1.248", true},
		{"", "0.156.1", false},
		{"devel", "0.156.1", false},
	} {
		if got := Supported(tc.version, tc.minimum); got != tc.want {
			t.Errorf("Supported(%q, %q) = %v", tc.version, tc.minimum, got)
		}
	}
}

// Find reads the version an agent prints, whatever surrounds it.
func TestFindReadsTheVersion(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "codex", "codex-cli 0.128.0")
	t.Setenv("PATH", dir)
	codex, _ := Lookup("codex")
	found := Find(context.Background(), codex)
	if found.Path != filepath.Join(dir, "codex") || found.Version != "0.128.0" || found.Supported || found.Minimum != codex.Minimum {
		t.Fatalf("%+v", found)
	}
	claude, _ := Lookup("claude-code")
	if found := Find(context.Background(), claude); found.Path != "" || found.Supported {
		t.Fatalf("%+v", found)
	}
}

// A version manager's per-shell directory is a link that vanishes with the
// shell; the record keeps the directory it points to.
func TestRecordingResolvesLinkedDirectories(t *testing.T) {
	real := filepath.Join(t.TempDir(), "node-versions", "v22", "bin")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	shell := filepath.Join(t.TempDir(), "fnm_multishells_1")
	if err := os.Symlink(filepath.Dir(real), shell); err != nil {
		t.Fatal(err)
	}
	writeAgent(t, real, "codex", "0.166.0")
	recorded, ok := Recording(Found{Agent: "codex", Path: filepath.Join(shell, "bin", "codex"), Version: "0.166.0"})
	realDir, _ := filepath.EvalSymlinks(real)
	if !ok || recorded.Dir != realDir || recorded.Path != filepath.Join(realDir, "codex") {
		t.Fatalf("%+v", recorded)
	}
	if !(Record{Agents: []Recorded{recorded}}).Holds(filepath.Join(shell, "bin", "codex")) {
		t.Fatal("the linked path is not held")
	}
	if _, ok := Recording(Found{Agent: "codex"}); ok {
		t.Fatal("recorded an agent that was not found")
	}
}

// The record keeps the terminal's PATH order: Claude Code's directory, which
// also holds an old Codex, must not come before the newer Codex's.
func TestNewRecordKeepsTheTerminalsOrder(t *testing.T) {
	newer, older := t.TempDir(), t.TempDir()
	writeAgent(t, newer, "codex", "codex-cli 0.166.0")
	writeAgent(t, older, "codex", "codex-cli 0.128.0")
	writeAgent(t, older, "claude", "2.1.300 (Claude Code)")
	terminal := newer + ":" + older
	t.Setenv("PATH", terminal)
	var found []Found
	for _, agent := range Agents {
		found = append(found, Find(context.Background(), agent))
	}
	record := NewRecord(found, terminal)
	if len(record.Agents) != 2 || record.Agents[0].Agent != "codex" {
		t.Fatalf("%+v", record.Agents)
	}
	t.Setenv("PATH", WithDirs("/usr/bin", record.Dirs()))
	codex, _ := Lookup("codex")
	if got := Find(context.Background(), codex); got.Version != "0.166.0" {
		t.Fatalf("baum would run %+v", got)
	}
}

func TestRecordRoundTripAndPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	file, err := RecordFile()
	if err != nil {
		t.Fatal(err)
	}
	if record, err := Load(file); err != nil || len(record.Agents) != 0 {
		t.Fatalf("missing record: %+v, %v", record, err)
	}
	present, gone := t.TempDir(), filepath.Join(t.TempDir(), "gone")
	record := Record{Agents: []Recorded{
		{Agent: "claude-code", Path: filepath.Join(present, "claude"), Dir: present},
		{Agent: "codex", Path: filepath.Join(present, "codex"), Dir: present},
		{Agent: "codex", Path: filepath.Join(gone, "codex"), Dir: gone},
	}}
	if err := Save(file, record); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record file: %v %v", info, err)
	}
	loaded, err := Load(file)
	if err != nil || !slices.Equal(loaded.Agents, record.Agents) {
		t.Fatalf("%+v, %v", loaded, err)
	}
	// Recorded directories come first, once; one that is gone is skipped.
	got := WithRecorded("/usr/bin:" + present + ":/bin")
	if want := present + ":/usr/bin:/bin"; got != want {
		t.Fatalf("PATH %q, want %q", got, want)
	}
}

// Keep applies the record now and again once it changes.
func TestKeepFollowsTheRecord(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", "/inherited")
	file, _ := RecordFile()
	first, second := t.TempDir(), t.TempDir()
	if err := Save(file, Record{Agents: []Recorded{{Agent: "codex", Dir: first}}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed := make(chan string, 1)
	Keep(ctx, "/usr/bin", 10*time.Millisecond, func(path string) { changed <- path })
	if got := os.Getenv("PATH"); got != first+":/usr/bin" {
		t.Fatalf("PATH %q", got)
	}
	// A later modification time, whatever the file system's resolution.
	if err := Save(file, Record{Agents: []Recorded{{Agent: "codex", Dir: second}}}); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(file, later, later); err != nil {
		t.Fatal(err)
	}
	select {
	case path := <-changed:
		if path != second+":/usr/bin" || !strings.HasPrefix(os.Getenv("PATH"), second) {
			t.Fatalf("PATH %q", path)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the new record was not applied")
	}
}
