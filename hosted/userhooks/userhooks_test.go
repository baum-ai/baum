package userhooks_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/hosted/agenthooks"
	"github.com/baum-ai/baum/hosted/userhooks"
)

// A Claude Code user settings file as people keep it: other settings, hooks of
// their own on events baum also uses, and keys baum knows nothing about.
const claudeUserSettings = `{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "permissions": {
    "allow": ["Bash(git status)"],
    "defaultMode": "acceptEdits"
  },
  "env": {"FOO": "bar"},
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "notify-send 'Claude stopped' && echo done > /tmp/x"
          }
        ]
      }
    ],
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [{"type": "command", "command": "./guard.sh", "timeout": 5}]
      }
    ],
    "Notification": []
  },
  "statusLine": {"type": "command", "command": "~/.claude/statusline.sh"}
}
`

// A Codex user hooks file of the shape Codex users have today.
const codexUserHooks = `{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "command": "bash /opt/sidebar/hook.sh codex session-start",
            "type": "command"
          }
        ],
        "matcher": "startup|resume"
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "command": "bash /opt/sidebar/hook.sh codex stop",
            "type": "command"
          }
        ],
        "matcher": ""
      }
    ]
  }
}
`

type hookEntry struct {
	Matcher *string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	} `json:"hooks"`
}

func decodeHooks(t *testing.T, contents []byte) map[string][]hookEntry {
	t.Helper()
	var settings struct {
		Hooks map[string][]hookEntry `json:"hooks"`
	}
	if err := json.Unmarshal(contents, &settings); err != nil {
		t.Fatalf("decode settings: %v\n%s", err, contents)
	}
	return settings.Hooks
}

// assertInstalled checks that every placement of agent has exactly one baum
// handler, at the right event and matcher, with the right command.
func assertInstalled(t *testing.T, agent string, contents []byte) {
	t.Helper()
	spec, _ := agenthooks.Lookup(agent)
	settings, _ := agenthooks.LookupUserWide(agent)
	hooks := decodeHooks(t, contents)
	owned := 0
	for _, groups := range hooks {
		for _, group := range groups {
			for _, handler := range group.Hooks {
				if _, _, ok := agenthooks.UserDispatcherEvent(handler.Command); ok {
					owned++
				}
			}
		}
	}
	if owned != len(settings.Placements) {
		t.Errorf("%s: %d baum handlers, want %d", agent, owned, len(settings.Placements))
	}
	for _, placement := range settings.Placements {
		found := false
		for _, group := range hooks[placement.HookEvent] {
			matcher := ""
			if group.Matcher != nil {
				matcher = *group.Matcher
			}
			want := ""
			if placement.Matcher != nil {
				want = *placement.Matcher
			}
			for _, handler := range group.Hooks {
				if handler.Command == agenthooks.UserDispatcher(spec, placement.Event) && matcher == want && handler.Timeout == placement.Timeout {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%s: no handler for %s under %s", agent, placement.Event, placement.HookEvent)
		}
	}
}

func TestFreshInstallCreatesTheSettings(t *testing.T) {
	for _, agent := range userhooks.Agents() {
		t.Run(agent, func(t *testing.T) {
			home := t.TempDir()
			result, err := userhooks.Install(home, agent)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Changed || result.Backup != "" {
				t.Errorf("result = %+v, want a change without a backup", result)
			}
			contents, err := os.ReadFile(result.Path)
			if err != nil {
				t.Fatal(err)
			}
			assertInstalled(t, agent, contents)
			var compact, indented bytes.Buffer
			if err := json.Compact(&compact, contents); err != nil {
				t.Fatal(err)
			}
			if err := json.Indent(&indented, compact.Bytes(), "", "  "); err != nil {
				t.Fatal(err)
			}
			if string(contents) != indented.String()+"\n" {
				t.Errorf("a new file is not indented by two spaces:\n%s", contents)
			}
			if !strings.Contains(string(contents), ">/dev/null 2>&1") {
				t.Error("the shell redirections were HTML-escaped")
			}
			info, err := os.Stat(result.Path)
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Errorf("mode = %v, want 0600", perm)
			}
		})
	}
}

func TestInstallKeepsUnrelatedSettingsAndUninstallRestoresThem(t *testing.T) {
	for agent, original := range map[string]string{"claude-code": claudeUserSettings, "codex": codexUserHooks} {
		t.Run(agent, func(t *testing.T) {
			installed, err := userhooks.InstallSettings([]byte(original), agent)
			if err != nil {
				t.Fatal(err)
			}
			assertInstalled(t, agent, installed)
			// Every original byte is still there, in order: baum only inserted.
			if !isSubsequence([]byte(original), installed) {
				t.Errorf("install changed text it does not own:\n%s", installed)
			}
			// The user's handlers keep their positions, which Codex keys its
			// trust in a hook to.
			hooks := decodeHooks(t, installed)
			if first := hooks["Stop"][0].Hooks[0].Command; strings.Contains(first, "baum") {
				t.Errorf("baum's Stop handler went before the user's: %q", first)
			}
			again, err := userhooks.InstallSettings(installed, agent)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(again, installed) {
				t.Errorf("re-install changed the settings:\n%s", again)
			}
			removed, err := userhooks.UninstallSettings(installed, agent)
			if err != nil {
				t.Fatal(err)
			}
			if string(removed) != original {
				t.Errorf("uninstall did not restore the original:\n%s", removed)
			}
		})
	}
}

func isSubsequence(short, long []byte) bool {
	i := 0
	for _, c := range long {
		if i < len(short) && short[i] == c {
			i++
		}
	}
	return i == len(short)
}

func TestReinstallIsByteIdentical(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, path, claudeUserSettings)
	first, err := userhooks.Install(home, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || first.Backup != path+userhooks.BackupSuffix {
		t.Fatalf("first install = %+v", first)
	}
	if backup := readFile(t, first.Backup); backup != claudeUserSettings {
		t.Errorf("backup does not hold the previous file:\n%s", backup)
	}
	installed := readFile(t, path)
	info, _ := os.Stat(path)
	second, err := userhooks.Install(home, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed || second.Backup != "" {
		t.Errorf("second install = %+v, want no change", second)
	}
	if readFile(t, path) != installed {
		t.Error("second install rewrote the file")
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(info.ModTime()) {
		t.Error("second install touched the file")
	}
	if backup := readFile(t, first.Backup); backup != claudeUserSettings {
		t.Error("second install replaced the backup")
	}
}

func TestUninstallRemovesOnlyBaumHandlers(t *testing.T) {
	spec, _ := agenthooks.Lookup("claude-code")
	ours, _ := json.Marshal(agenthooks.UserDispatcher(spec, "stop"))
	// A user who moved baum's handler into a group of their own keeps the
	// group and their handler.
	settings := `{"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"./mine.sh"},{"type":"command","command":` + string(ours) + `}]}]}}`
	removed, err := userhooks.UninstallSettings([]byte(settings), "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"./mine.sh"}]}]}}`; string(removed) != want {
		t.Errorf("uninstall =\n%s\nwant\n%s", removed, want)
	}
	// The committed dispatcher and Entire's command are a repository's, not
	// the user-wide install's, and stay.
	committed, _ := json.Marshal(agenthooks.Dispatcher(spec, "stop"))
	entire, _ := json.Marshal(agenthooks.EntireCommand(spec, "stop"))
	settings = `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":` + string(committed) + `},{"type":"command","command":` + string(entire) + `}]}]}}`
	removed, err = userhooks.UninstallSettings([]byte(settings), "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if string(removed) != settings {
		t.Errorf("uninstall removed hooks baum does not own:\n%s", removed)
	}
}

func TestUninstallWithoutSettingsCreatesNothing(t *testing.T) {
	home := t.TempDir()
	for _, agent := range userhooks.Agents() {
		result, err := userhooks.Uninstall(home, agent)
		if err != nil {
			t.Fatal(err)
		}
		if result.Changed {
			t.Errorf("%s: uninstall reported a change", agent)
		}
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Errorf("uninstall created %v", entries)
	}
}

func TestFreshInstallThenUninstallLeavesAnEmptyObject(t *testing.T) {
	home := t.TempDir()
	result, err := userhooks.Install(home, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := userhooks.Uninstall(home, "codex"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, result.Path); got != "{}\n" {
		t.Errorf("after uninstall = %q", got)
	}
}

// An install from an older baum, or one a user edited, is brought up to date:
// a stale handler is rewritten where it stands, a misplaced one moves and a
// repeat goes.
func TestInstallRepairsEarlierHandlers(t *testing.T) {
	spec, _ := agenthooks.Lookup("codex")
	older := `sh -c 'if command -v baum >/dev/null 2>&1 && baum hooks agent --check --user codex stop >/dev/null 2>&1; then exec baum hooks agent codex stop --old; fi'`
	misplaced, _ := json.Marshal(agenthooks.UserDispatcher(spec, "session-start"))
	olderJSON, _ := json.Marshal(older)
	gone, _ := json.Marshal(strings.ReplaceAll(older, "codex stop", "codex retired-event"))
	settings := `{
  "hooks": {
    "Stop": [
      {"hooks": [{"type": "command", "command": ` + string(olderJSON) + `, "timeout": 5}]},
      {"hooks": [{"type": "command", "command": ` + string(olderJSON) + `}]}
    ],
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": ` + string(misplaced) + `}]},
      {"hooks": [{"type": "command", "command": ` + string(gone) + `}]}
    ]
  }
}
`
	installed, err := userhooks.InstallSettings([]byte(settings), "codex")
	if err != nil {
		t.Fatal(err)
	}
	assertInstalled(t, "codex", installed)
	if strings.Contains(string(installed), "--old") || strings.Contains(string(installed), "retired-event") {
		t.Errorf("stale handlers survived:\n%s", installed)
	}
}

func TestMalformedSettingsAreRefusedAndKept(t *testing.T) {
	for name, contents := range map[string]string{
		"invalid JSON":          `{"hooks": {`,
		"comments":              "{\n  // mine\n  \"hooks\": {}\n}\n",
		"not an object":         `[]`,
		"hooks not an object":   `{"hooks": []}`,
		"event not a list":      `{"hooks": {"Stop": {}}}`,
		"group not an object":   `{"hooks": {"Stop": ["x"]}}`,
		"handlers not a list":   `{"hooks": {"Stop": [{"hooks": {}}]}}`,
		"duplicate hooks key":   `{"hooks": {}, "hooks": {}}`,
		"duplicate event key":   `{"hooks": {"Stop": [], "Stop": []}}`,
		"handler not object":    `{"hooks": {"Stop": [{"hooks": [1]}]}}`,
		"duplicate command key": `{"hooks": {"Stop": [{"hooks": [{"command": "a", "command": "b"}]}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, ".claude", "settings.json")
			writeFile(t, path, contents)
			for _, run := range []func(string, string) (userhooks.Result, error){userhooks.Install, userhooks.Uninstall} {
				if _, err := run(home, "claude-code"); err == nil {
					t.Fatalf("%q was accepted", contents)
				}
			}
			if readFile(t, path) != contents {
				t.Error("the malformed file was changed")
			}
			entries, _ := os.ReadDir(filepath.Dir(path))
			if len(entries) != 1 {
				t.Errorf("left files behind: %v", entries)
			}
		})
	}
}

func TestOneLineSettingsStayOnOneLine(t *testing.T) {
	installed, err := userhooks.InstallSettings([]byte(`{"model":"opus","hooks":{"Stop":[]}}`), "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.ContainsRune(installed, '\n') {
		t.Errorf("a one-line file was spread over lines:\n%s", installed)
	}
	assertInstalled(t, "claude-code", installed)
}

func TestBlankSettingsFileIsTreatedAsEmpty(t *testing.T) {
	installed, err := userhooks.InstallSettings([]byte("\n"), "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	assertInstalled(t, "claude-code", installed)
}

// A settings file kept in a dotfiles repository and linked into place stays
// linked: the target is updated, and backed up beside it.
func TestSymlinkedSettingsStayLinked(t *testing.T) {
	home, dotfiles := t.TempDir(), t.TempDir()
	target := filepath.Join(dotfiles, "claude-settings.json")
	writeFile(t, target, claudeUserSettings)
	link := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := userhooks.Install(home, "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v", err)
	}
	assertInstalled(t, "claude-code", []byte(readFile(t, target)))
	if result.Backup != target+userhooks.BackupSuffix || readFile(t, result.Backup) != claudeUserSettings {
		t.Errorf("backup = %q", result.Backup)
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want the original 0640", info.Mode().Perm())
	}
}

func TestUnsupportedAgentIsRefused(t *testing.T) {
	if _, err := userhooks.Install(t.TempDir(), "cursor"); err == nil {
		t.Error("cursor has no user-wide hooks, but install succeeded")
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
