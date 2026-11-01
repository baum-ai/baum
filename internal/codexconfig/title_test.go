package codexconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestTerminalTitlePreservesUserConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, input, keep string
		items             []string
	}{
		{"defaults", "# user comment\nmodel = 'custom'\n", "# user comment\nmodel = 'custom'\n", []string{"spinner", "project", "thread-id"}},
		{"existing table", "[tui] # title\ntheme = 'dark'\n[tui.keymap.global]\nquit = ['ctrl-q']\n", "theme = 'dark'\n[tui.keymap.global]\nquit = ['ctrl-q']\n", []string{"spinner", "project", "thread-id"}},
		{"table at EOF", "[tui]", "[tui]\n", []string{"spinner", "project", "thread-id"}},
		{"custom items", "[tui]\nterminal_title = ['model', 'project'] # keep\nnotifications = false\n", "# keep\nnotifications = false\n", []string{"model", "project", "thread-id"}},
		{"ID in wrong position", "[tui]\nterminal_title = ['thread-id', 'model', 'project']\n", "[tui]\n", []string{"model", "project", "thread-id"}},
		{"empty title", "[tui]\nterminal_title = []\n", "[tui]\n", []string{"thread-id"}},
		{"multiline title", "[tui]\nterminal_title = [\n 'model', # note\n 'project',\n]\nnotifications = false\n", "notifications = false\n", []string{"model", "project", "thread-id"}},
		{"dotted title", "tui.terminal_title = ['project']\n[plugins.test]\nenabled = true\n", "[plugins.test]\nenabled = true\n", []string{"project", "thread-id"}},
		{"quoted key", "'tui'.'terminal_title' = ['model']\n", "'tui'.'terminal_title' =", []string{"model", "thread-id"}},
		{"dotted sibling", "tui.theme = 'dark'\n[plugins.test]\nenabled = true\n", "tui.theme = 'dark'\n[plugins.test]\nenabled = true\n", []string{"spinner", "project", "thread-id"}},
		{"implicit parent", "[tui.keymap.global]\nquit = ['ctrl-q']\n", "[tui.keymap.global]\nquit = ['ctrl-q']\n", []string{"spinner", "project", "thread-id"}},
		{"inline table", "tui = { theme = 'dark' } # keep\n", "theme = 'dark'", []string{"spinner", "project", "thread-id"}},
		{"empty inline table", "tui = {} # keep\n", "# keep", []string{"spinner", "project", "thread-id"}},
		{"inline title", "tui = { theme = 'dark', terminal_title = ['model'] } # keep\n", "theme = 'dark'", []string{"model", "thread-id"}},
		{"CRLF", "[tui]\r\ntheme = 'dark'\r\n", "theme = 'dark'\r\n", []string{"spinner", "project", "thread-id"}},
		{"profile untouched", "[profiles.work.tui]\nsession_picker_view = 'list'\n", "[profiles.work.tui]\nsession_picker_view = 'list'\n", []string{"spinner", "project", "thread-id"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := terminalTitle([]byte(tc.input))
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Tui struct {
					TerminalTitle []string `toml:"terminal_title"`
				}
			}
			if err := toml.Unmarshal(got, &config); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(config.Tui.TerminalTitle, tc.items) || !strings.Contains(string(got), tc.keep) {
				t.Fatalf("title=%v, config:\n%s", config.Tui.TerminalTitle, got)
			}
			again, err := terminalTitle(got)
			if err != nil || !bytes.Equal(again, got) {
				t.Fatalf("not idempotent: %s %v", again, err)
			}
		})
	}
}

func TestEnsureTerminalTitleUsesCodexHomeAndPreservesFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "custom-codex"))
	if changed, err := EnsureTerminalTitle(); err != nil || !changed {
		t.Fatalf("first setup=%v %v", changed, err)
	}
	path := filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("new file mode=%o", info.Mode().Perm())
	}
	if changed, err := EnsureTerminalTitle(); err != nil || changed {
		t.Fatalf("second setup=%v %v", changed, err)
	}
	again, _ := os.Stat(path)
	if !again.ModTime().Equal(info.ModTime()) {
		t.Fatal("rewrote a configured file")
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Fatalf("touched default home: %v", err)
	}
	t.Setenv("CODEX_HOME", "")
	if changed, err := EnsureTerminalTitle(); err != nil || !changed {
		t.Fatalf("default home=%v %v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureTerminalTitleLeavesInvalidConfigUnchanged(t *testing.T) {
	for _, input := range []string{
		"api_key = 'private-value\n",
		"[tui]\nterminal_title = false\n",
		"[tui]\nterminal_title = [123]\n",
		"tui = 'invalid'\n",
	} {
		t.Run(input, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
				t.Fatal(err)
			}
			changed, err := ensureTerminalTitle(path)
			if changed || err == nil || strings.Contains(err.Error(), "private-value") {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != input {
				t.Fatalf("changed invalid config: %s", got)
			}
		})
	}
}

func TestEnsureTerminalTitlePreservesSymlinkAndMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "shared.toml")
	link := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(target, []byte("# shared\n[tui]\ntheme = 'dark'\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if changed, err := ensureTerminalTitle(link); err != nil || !changed {
		t.Fatalf("setup=%v %v", changed, err)
	}
	if _, err := os.Readlink(link); err != nil {
		t.Fatal("replaced symlink:", err)
	}
	info, _ := os.Stat(target)
	got, _ := os.ReadFile(target)
	if info.Mode().Perm() != 0o640 || !bytes.Contains(got, []byte("thread-id")) {
		t.Fatalf("target mode=%o config=%s", info.Mode().Perm(), got)
	}
}

func TestEnsureTerminalTitleReportsUnreadablePath(t *testing.T) {
	if changed, err := ensureTerminalTitle(t.TempDir()); changed || err == nil {
		t.Fatalf("directory setup=%v %v", changed, err)
	}
}

func TestEnsureTerminalTitleDoesNotReplaceDanglingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require privileges on Windows")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Symlink("missing.toml", path); err != nil {
		t.Fatal(err)
	}
	if changed, err := ensureTerminalTitle(path); changed || err == nil {
		t.Fatalf("dangling symlink setup=%v %v", changed, err)
	}
	if target, err := os.Readlink(path); err != nil || target != "missing.toml" {
		t.Fatalf("changed symlink: %q %v", target, err)
	}
}
