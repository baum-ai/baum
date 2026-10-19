//go:build !windows

package baumcli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/hosted/agenthooks"
	"github.com/baum-ai/baum/hosted/userhooks"
)

// runUserDispatcher runs the user-wide command for one agent event the way
// the agent does, in dir.
func (w hookWorld) runUserDispatcher(t *testing.T, dir string, spec agenthooks.Spec, event string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", agenthooks.UserDispatcher(spec, event))
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(`{"session_id":"s"}`)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s user-wide hook: %v: %s", spec.Slug, event, err, output)
	}
}

// commitEntireSettings commits Entire's settings, as `entire enable` and the
// onboarding pull request leave them.
func commitEntireSettings(t *testing.T, repo string) {
	t.Helper()
	commitFile(t, repo, ".entire/settings.json", `{"enabled": true}`+"\n")
}

func userWideSpecs(t *testing.T) []agenthooks.Spec {
	t.Helper()
	var specs []agenthooks.Spec
	for _, agent := range userhooks.Agents() {
		spec, _ := agenthooks.Lookup(agent)
		specs = append(specs, spec)
	}
	return specs
}

// Without a login or a set-up clone, the user-wide hooks do nothing at all,
// even in a repository that uses Entire: the repository's own hooks are what
// runs Entire there.
func TestUserHooksDoNothingForOpenSourceUsers(t *testing.T) {
	world := newHookWorld(t)
	commitEntireSettings(t, world.repo)
	for _, spec := range userWideSpecs(t) {
		for _, event := range spec.Events {
			world.runUserDispatcher(t, world.repo, spec, event)
			baum, entire := world.logs(t)
			if want := "hooks agent --check --user " + spec.Slug + " " + event; len(baum) != 1 || baum[0] != want {
				t.Errorf("%s %s: baum calls = %q, want only %q", spec.Slug, event, baum, want)
			}
			if len(entire) != 0 {
				t.Errorf("%s %s: entire calls = %q, want none", spec.Slug, event, entire)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(world.repo, ".git", "baum")); !os.IsNotExist(err) {
		t.Errorf("baum left state in the clone: %v", err)
	}
	if n := world.requests.Load(); n != 0 {
		t.Errorf("%d requests reached the baum server", n)
	}
}

// For a logged-in user, the user-wide hooks bring a repository that uses
// Entire but commits no agent hooks into the hosted flow: session start
// bootstraps the clone and every event reaches Entire once.
func TestUserHooksRunThroughBaumWhenLoggedIn(t *testing.T) {
	world := newHookWorld(t)
	commitEntireSettings(t, world.repo)
	t.Setenv(tokenEnvVar, "provisioned")
	for _, spec := range userWideSpecs(t) {
		for _, event := range spec.Events {
			world.runUserDispatcher(t, world.repo, spec, event)
			baum, entire := world.logs(t)
			want := []string{"hooks agent --check --user " + spec.Slug + " " + event, "hooks agent " + spec.Slug + " " + event}
			if strings.Join(baum, "|") != strings.Join(want, "|") {
				t.Errorf("%s %s: baum calls = %q, want %q", spec.Slug, event, baum, want)
			}
			if want := "hooks " + spec.Slug + " " + event; len(entire) != 1 || entire[0] != want {
				t.Errorf("%s %s: entire calls = %q, want %q", spec.Slug, event, entire, want)
			}
		}
	}
	if contents, err := os.ReadFile(filepath.Join(world.repo, ".git", "hooks", "commit-msg")); err != nil || !strings.Contains(string(contents), baumHookMarker) {
		t.Errorf("session start did not bootstrap the clone: %v", err)
	}
	if n := world.requests.Load(); n != 0 {
		t.Errorf("%d requests reached the baum server", n)
	}
}

// Where Entire is not enabled, or outside any repository, the user-wide hooks
// do nothing even for a logged-in user, so they install no Git hooks in
// repositories that never asked for them.
func TestUserHooksSkipRepositoriesWithoutEntire(t *testing.T) {
	world := newHookWorld(t)
	t.Setenv(tokenEnvVar, "provisioned")
	claude, _ := agenthooks.Lookup("claude-code")
	for name, dir := range map[string]string{"repository without Entire": world.repo, "no repository": t.TempDir()} {
		world.runUserDispatcher(t, dir, claude, "session-start")
		baum, entire := world.logs(t)
		if len(baum) != 1 || len(entire) != 0 {
			t.Errorf("%s: baum calls = %q, entire calls = %q, want only the check", name, baum, entire)
		}
	}
	if _, err := os.Stat(filepath.Join(world.repo, ".git", "baum")); !os.IsNotExist(err) {
		t.Errorf("baum left state in the clone: %v", err)
	}
}

// TestUserHooksDoNotReportAnEventTwice runs the hooks an agent would run for
// one event when both the repository and the user-wide settings carry one:
// the repository's command, whatever its form, handles the event, and the
// user-wide one yields, so Entire hears it once. An event the repository's
// settings lack still reaches Entire through the user-wide hook.
func TestUserHooksDoNotReportAnEventTwice(t *testing.T) {
	claude, _ := agenthooks.Lookup("claude-code")
	codex, _ := agenthooks.Lookup("codex")
	preRename := strings.ReplaceAll(agenthooks.Dispatcher(claude, "stop"), "baum", "recount"+"able")
	cases := []struct {
		name    string
		spec    agenthooks.Spec
		file    string
		command string
		commit  bool
		subdir  bool
	}{
		{name: "committed dispatcher", spec: claude, file: ".claude/settings.json", command: agenthooks.Dispatcher(claude, "stop"), commit: true},
		{name: "Entire's own command", spec: claude, file: ".claude/settings.json", command: agenthooks.EntireCommand(claude, "stop"), commit: true},
		{name: "pre-rename dispatcher", spec: claude, file: ".claude/settings.json", command: preRename, commit: true},
		{name: "local settings", spec: claude, file: ".claude/settings.local.json", command: agenthooks.EntireCommand(claude, "stop")},
		{name: "from a subdirectory", spec: claude, file: ".claude/settings.json", command: agenthooks.Dispatcher(claude, "stop"), commit: true, subdir: true},
		{name: "Codex hooks.json", spec: codex, file: ".codex/hooks.json", command: agenthooks.Dispatcher(codex, "stop"), commit: true},
		{name: "Codex config.toml", spec: codex, file: ".codex/config.toml", command: agenthooks.EntireCommand(codex, "stop"), commit: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			world := newHookWorld(t)
			commitEntireSettings(t, world.repo)
			t.Setenv(tokenEnvVar, "provisioned")
			settings := claudeSettings(t, tc.command)
			settings = strings.Replace(settings, "SessionStart", "Stop", 1)
			if strings.HasSuffix(tc.file, ".toml") {
				settings = "[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = '''" + tc.command + "'''\n"
			}
			if tc.commit {
				commitFile(t, world.repo, tc.file, settings)
			} else {
				writeAgentSettings(t, world.repo, tc.file, settings)
			}
			dir := world.repo
			if tc.subdir {
				dir = filepath.Join(world.repo, "src", "pkg")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			// The agent runs the repository's hook and the user-wide one.
			cmd := exec.Command("sh", "-c", tc.command)
			cmd.Dir = dir
			cmd.Stdin = strings.NewReader(`{"session_id":"s"}`)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("repository hook: %v: %s", err, output)
			}
			world.runUserDispatcher(t, dir, tc.spec, "stop")
			_, entire := world.logs(t)
			if want := "hooks " + tc.spec.Slug + " stop"; len(entire) != 1 || entire[0] != want {
				t.Errorf("entire calls = %q, want %q once", entire, want)
			}

			world.runUserDispatcher(t, dir, tc.spec, "session-end")
			_, entire = world.logs(t)
			if want := "hooks " + tc.spec.Slug + " session-end"; len(entire) != 1 || entire[0] != want {
				t.Errorf("an event the repository lacks: entire calls = %q, want %q", entire, want)
			}
		})
	}
}

func TestHooksInstallAndUninstallUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	writeAgentSettings(t, home, ".claude/settings.json", claudeSettings(t, "./mine.sh"))
	original := readHookTestFile(t, settingsPath)

	if _, _, err := runCLI(t, "hooks", "install"); err == nil || !strings.Contains(err.Error(), "--user") {
		t.Errorf("install without --user: err = %v", err)
	}
	stdout, _, err := runCLI(t, "hooks", "install", "--user", "--agent", "claude-code", "--agent", "codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Installed baum's Claude Code hooks in ~/.claude/settings.json.",
		"Previous file saved as ~/.claude/settings.json.baum-backup.",
		"Installed baum's Codex hooks in ~/.codex/hooks.json.",
		"/hooks",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("install output misses %q:\n%s", want, stdout)
		}
	}
	installed := readHookTestFile(t, settingsPath)
	if !strings.Contains(installed, "./mine.sh") || !strings.Contains(installed, "--check --user claude-code stop") {
		t.Errorf("installed settings:\n%s", installed)
	}

	stdout, _, err = runCLI(t, "hooks", "install", "--user", "--agent", "claude-code")
	if err != nil || !strings.Contains(stdout, "are up to date") {
		t.Errorf("re-install: %v\n%s", err, stdout)
	}
	if readHookTestFile(t, settingsPath) != installed {
		t.Error("re-install changed the settings")
	}

	stdout, _, err = runCLI(t, "hooks", "uninstall", "--user")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Removed baum's Claude Code hooks from ~/.claude/settings.json.") ||
		!strings.Contains(stdout, "Removed baum's Codex hooks from ~/.codex/hooks.json.") {
		t.Errorf("uninstall output:\n%s", stdout)
	}
	if got := readHookTestFile(t, settingsPath); got != original {
		t.Errorf("uninstall did not restore the settings:\n%s", got)
	}

	if _, _, err := runCLI(t, "hooks", "install", "--user", "--agent", "cursor"); err == nil {
		t.Error("install accepted cursor")
	}
}

func TestHooksInstallRefusesMalformedSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeAgentSettings(t, home, ".claude/settings.json", `{"hooks": `)
	_, stderr, err := runCLI(t, "hooks", "install", "--user", "--agent", "claude-code", "--agent", "codex")
	if err == nil {
		t.Fatal("install succeeded over a malformed settings file")
	}
	if !strings.Contains(stderr, "not valid JSON") {
		t.Errorf("stderr = %q", stderr)
	}
	if got := readHookTestFile(t, filepath.Join(home, ".claude", "settings.json")); got != `{"hooks": ` {
		t.Errorf("the malformed file was changed: %q", got)
	}
	// The other agent is still installed.
	if _, err := os.Stat(filepath.Join(home, ".codex", "hooks.json")); err != nil {
		t.Errorf("codex hooks were not installed: %v", err)
	}
}

func TestSetupInstallsUserHooksUnlessSkipped(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "skipped"}[skip], func(t *testing.T) {
			stub := workingEntire(t)
			addStubAgent(t, stub.dir, "codex")
			setTempConfigHome(t)
			t.Setenv(serverEnvVar, failingServer(t))
			repo := newGitHubRepo(t, t.TempDir(), "demo")
			t.Chdir(repo)
			args := []string{"setup"}
			if skip {
				args = append(args, "--skip-user-hooks")
			}
			if _, _, err := runCLI(t, args...); err != nil {
				t.Fatal(err)
			}
			home := os.Getenv("HOME")
			for _, file := range []string{".claude/settings.json", ".codex/hooks.json"} {
				contents, err := os.ReadFile(filepath.Join(home, file))
				if skip {
					if !os.IsNotExist(err) {
						t.Errorf("%s was written with --skip-user-hooks", file)
					}
					continue
				}
				if err != nil || !strings.Contains(string(contents), "--check --user") {
					t.Errorf("%s: %v\n%s", file, err, contents)
				}
			}
		})
	}
}
