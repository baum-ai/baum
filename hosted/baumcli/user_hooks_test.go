//go:build !windows

package baumcli

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
			want := []string{"hooks agent --check --user " + spec.Slug + " " + event, "hooks agent --user " + spec.Slug + " " + event}
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
			if tc.spec.Slug == "codex" {
				// Codex runs a project hook only in a trusted project, once the
				// user trusted the hook.
				trustCodex(t, world.repo, filepath.Join(world.repo, filepath.FromSlash(tc.file))+":stop:0:0")
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

// trustCodex writes the user's Codex configuration: project trusted, and
// each hook key ("<file>:<event>:<group>:<handler>") trusted. An empty
// project trusts none.
func trustCodex(t *testing.T, project string, hooks ...string) {
	t.Helper()
	var config strings.Builder
	if project != "" {
		config.WriteString("[projects." + strconv.Quote(project) + "]\ntrust_level = \"trusted\"\n\n")
	}
	for _, key := range hooks {
		config.WriteString("[hooks.state." + strconv.Quote(key) + "]\ntrusted_hash = \"sha256:test\"\n\n")
	}
	writeAgentSettings(t, os.Getenv("HOME"), ".codex/config.toml", config.String())
}

// expectUserHook runs the user-wide stop hook and checks whether it handed the
// event to Entire through baum.
func (w hookWorld) expectUserHook(t *testing.T, spec agenthooks.Spec, runs bool) {
	t.Helper()
	w.runUserDispatcher(t, w.repo, spec, "stop")
	baum, entire := w.logs(t)
	ran := len(baum) == 2 && baum[1] == "hooks agent --user "+spec.Slug+" stop"
	if ran != runs || len(entire) != map[bool]int{true: 1, false: 0}[runs] {
		t.Errorf("user-wide %s stop hook: baum calls = %q, entire calls = %q, want it to run: %v", spec.Slug, baum, entire, runs)
	}
}

// Codex loads a project's hooks only in a trusted project and runs each only
// once the user trusted it, so the user-wide hook yields to a project hook
// only then; otherwise the event would reach nobody.
func TestUserHooksYieldToCodexProjectHooksOnlyWhenCodexRunsThem(t *testing.T) {
	codex, _ := agenthooks.Lookup("codex")
	for name, tc := range map[string]struct {
		project bool
		hook    string
		runs    bool
	}{
		"no Codex configuration":        {runs: true},
		"project not trusted":           {hook: ".codex/hooks.json:stop:0:0", runs: true},
		"hook not trusted":              {project: true, runs: true},
		"another hook trusted":          {project: true, hook: ".codex/hooks.json:stop:0:1", runs: true},
		"project and hook both trusted": {project: true, hook: ".codex/hooks.json:stop:0:0", runs: false},
	} {
		t.Run(name, func(t *testing.T) {
			world := newHookWorld(t)
			commitEntireSettings(t, world.repo)
			t.Setenv(tokenEnvVar, "provisioned")
			settings := strings.Replace(claudeSettings(t, agenthooks.Dispatcher(codex, "stop")), "SessionStart", "Stop", 1)
			commitFile(t, world.repo, ".codex/hooks.json", settings)
			project, hooks := "", []string{}
			if tc.project {
				project = world.repo
			}
			if tc.hook != "" {
				hooks = append(hooks, filepath.Join(world.repo, tc.hook))
			}
			trustCodex(t, project, hooks...)
			world.expectUserHook(t, codex, tc.runs)
		})
	}
}

// Hooks outside the repository that hand the event to Entire count too: the
// user's own, managed policy and Codex's system configuration.
func TestUserHooksYieldToEntireHooksOutsideTheRepository(t *testing.T) {
	claude, _ := agenthooks.Lookup("claude-code")
	codex, _ := agenthooks.Lookup("codex")
	claudeStop := strings.Replace(claudeSettings(t, agenthooks.EntireCommand(claude, "stop")), "SessionStart", "Stop", 1)
	codexStop := strings.Replace(claudeSettings(t, agenthooks.EntireCommand(codex, "stop")), "SessionStart", "Stop", 1)
	codexInline := "[[hooks.Stop]]\n[[hooks.Stop.hooks]]\ntype = \"command\"\ncommand = '''" + agenthooks.EntireCommand(codex, "stop") + "'''\n"
	for name, tc := range map[string]struct {
		spec       agenthooks.Spec
		place      func(t *testing.T, home, managed, system string)
		yieldsStop bool
	}{
		"~/.claude/settings.json": {spec: claude, place: func(t *testing.T, home, _, _ string) {
			writeAgentSettings(t, home, ".claude/settings.json", claudeStop)
		}},
		"managed-settings.json": {spec: claude, place: func(t *testing.T, _, managed, _ string) {
			writeAgentSettings(t, managed, "managed-settings.json", claudeStop)
		}},
		"managed-settings.d": {spec: claude, place: func(t *testing.T, _, managed, _ string) {
			writeAgentSettings(t, managed, "managed-settings.d/50-entire.json", claudeStop)
		}},
		"~/.codex/config.toml inline hooks": {spec: codex, place: func(t *testing.T, home, _, _ string) {
			writeAgentSettings(t, home, ".codex/config.toml", codexInline)
		}},
		"~/.codex/hooks.json": {spec: codex, place: func(t *testing.T, home, _, _ string) {
			writeAgentSettings(t, home, ".codex/hooks.json", codexStop)
		}},
		"/etc/codex/requirements.toml": {spec: codex, place: func(t *testing.T, _, _, system string) {
			writeAgentSettings(t, system, "requirements.toml", codexInline)
		}},
		"/etc/codex/hooks.json": {spec: codex, place: func(t *testing.T, _, _, system string) {
			writeAgentSettings(t, system, "hooks.json", codexStop)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			world := newHookWorld(t)
			commitEntireSettings(t, world.repo)
			t.Setenv(tokenEnvVar, "provisioned")
			managed, system := t.TempDir(), t.TempDir()
			t.Setenv(testClaudeManagedDirEnv, managed)
			t.Setenv(testCodexSystemDirEnv, system)
			tc.place(t, os.Getenv("HOME"), managed, system)
			world.expectUserHook(t, tc.spec, false)
			// Another event still arrives.
			world.runUserDispatcher(t, world.repo, tc.spec, "session-end")
			if _, entire := world.logs(t); len(entire) != 1 {
				t.Errorf("session-end: entire calls = %q", entire)
			}
		})
	}
}

// The gate reads Entire's settings as Entire does: "enabled": false turns it
// off, the local file's key wins, and an unreadable file turns it off.
func TestUserHooksFollowEntiresEnabledSetting(t *testing.T) {
	claude, _ := agenthooks.Lookup("claude-code")
	for name, tc := range map[string]struct {
		settings, local string
		runs            bool
	}{
		"no enabled key":          {settings: `{}`, runs: true},
		"enabled false":           {settings: `{"enabled": false}`},
		"local turns it on":       {settings: `{"enabled": false}`, local: `{"enabled": true}`, runs: true},
		"local turns it off":      {settings: `{"enabled": true}`, local: `{"enabled": false}`},
		"local without the key":   {settings: `{"enabled": false}`, local: `{}`},
		"only local settings":     {local: `{"enabled": true}`, runs: true},
		"settings not JSON":       {settings: `{"enabled":`},
		"local settings not JSON": {settings: `{"enabled": true}`, local: `{`},
	} {
		t.Run(name, func(t *testing.T) {
			world := newHookWorld(t)
			t.Setenv(tokenEnvVar, "provisioned")
			if tc.settings != "" {
				writeAgentSettings(t, world.repo, ".entire/settings.json", tc.settings)
			}
			if tc.local != "" {
				writeAgentSettings(t, world.repo, ".entire/settings.local.json", tc.local)
			}
			world.expectUserHook(t, claude, tc.runs)
		})
	}
}

func TestHooksUninstallWarnsAboutCodexHooksItMoves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codex, _ := agenthooks.Lookup("codex")
	ours := strings.Replace(claudeSettings(t, agenthooks.UserDispatcher(codex, "stop")), "SessionStart", "Stop", 1)
	ours = strings.Replace(ours, `]}]}}`, `]},{"hooks":[{"type":"command","command":"./mine.sh"}]}]}}`, 1)
	writeAgentSettings(t, home, ".codex/hooks.json", ours)
	_, stderr, err := runCLI(t, "hooks", "uninstall", "--user", "--agent", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "/hooks") || !strings.Contains(stderr, "  Stop: ./mine.sh\n") {
		t.Errorf("stderr = %q, want a warning naming the moved hook", stderr)
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
		"Previous file saved as ~/.local/state/baum/backups/claude-code-settings.json.",
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
		!strings.Contains(stdout, "Removed baum's Codex hooks and ~/.codex/hooks.json, which baum had created.") {
		t.Errorf("uninstall output:\n%s", stdout)
	}
	if got := readHookTestFile(t, settingsPath); got != original {
		t.Errorf("uninstall did not restore the settings:\n%s", got)
	}

	if _, _, err := runCLI(t, "hooks", "install", "--user", "--agent", "cursor"); err == nil {
		t.Error("install accepted cursor")
	}
}

// The desktop app reads the status as JSON: which agents were found, where
// their settings live and whether baum's hooks are there and up to date.
func TestHooksStatusUser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	status := func() map[string]userHooksStatus {
		t.Helper()
		stdout, _, err := runCLI(t, "hooks", "status", "--user", "--json")
		if err != nil {
			t.Fatal(err)
		}
		var body struct{ Agents []userHooksStatus }
		if err := json.Unmarshal([]byte(stdout), &body); err != nil {
			t.Fatalf("%v: %s", err, stdout)
		}
		byAgent := map[string]userHooksStatus{}
		for _, s := range body.Agents {
			byAgent[s.Agent] = s
		}
		return byAgent
	}
	before := status()
	claude, codex := before["claude-code"], before["codex"]
	if !claude.Detected || claude.Installed || claude.Settings != filepath.Join(home, ".claude", "settings.json") || claude.Name != "Claude Code" {
		t.Errorf("claude-code before install = %+v", claude)
	}
	if codex.Detected || codex.Installed {
		t.Errorf("codex before install = %+v", codex)
	}
	if _, _, err := runCLI(t, "hooks", "install", "--user"); err != nil {
		t.Fatal(err)
	}
	after := status()
	if !after["claude-code"].Installed || after["codex"].Installed {
		t.Errorf("after installing for the detected agents = %+v", after)
	}
	writeAgentSettings(t, home, ".codex/hooks.json", `{"hooks": `)
	if got := status()["codex"]; got.Installed || !strings.Contains(got.Error, "not valid JSON") {
		t.Errorf("codex with a malformed file = %+v", got)
	}
	if _, _, err := runCLI(t, "hooks", "status"); err == nil || !strings.Contains(err.Error(), "--user") {
		t.Errorf("status without --user: err = %v", err)
	}
	stdout, _, err := runCLI(t, "hooks", "status", "--user")
	if err != nil || !strings.Contains(stdout, "Claude Code (found): installed in ~/.claude/settings.json") {
		t.Errorf("text status: %v\n%s", err, stdout)
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

// Setup installs the user-wide hooks where they add something today, for a
// hosted install or a logged-in user, and names the files in the --all plan;
// an open-source user without a login gets none.
func TestSetupInstallsUserHooksOnlyForHostedUsers(t *testing.T) {
	for name, tc := range map[string]struct {
		hosted, login, skip bool
		installs            bool
	}{
		"open source":            {},
		"open source, logged in": {login: true, installs: true},
		"hosted install":         {hosted: true, installs: true},
		"hosted, skipped":        {hosted: true, skip: true},
	} {
		t.Run(name, func(t *testing.T) {
			stub := workingEntire(t)
			addStubAgent(t, stub.dir, "codex")
			setTempConfigHome(t)
			t.Setenv(serverEnvVar, failingServer(t))
			if tc.login {
				t.Setenv(tokenEnvVar, "provisioned")
			}
			if tc.hosted {
				original := distribution
				distribution = hostedDistribution
				t.Cleanup(func() { distribution = original })
			}
			parent := t.TempDir()
			newGitHubRepo(t, parent, "demo")
			t.Chdir(parent)
			args := []string{"setup", "--all", "--yes"}
			if tc.skip {
				args = append(args, "--skip-user-hooks")
			}
			stdout, _, err := runCLI(t, args...)
			if err != nil {
				t.Fatal(err)
			}
			planned := strings.Contains(stdout, "Setup also adds baum's user-wide hooks to ~/.claude/settings.json and ~/.codex/hooks.json")
			if planned != tc.installs {
				t.Errorf("plan names the user-wide files: %v, want %v\n%s", planned, tc.installs, stdout)
			}
			home := os.Getenv("HOME")
			for _, file := range []string{".claude/settings.json", ".codex/hooks.json"} {
				contents, err := os.ReadFile(filepath.Join(home, file))
				installed := err == nil && strings.Contains(string(contents), "--check --user")
				if installed != tc.installs {
					t.Errorf("%s installed: %v, want %v (%v)", file, installed, tc.installs, err)
				}
			}
		})
	}
}

// With every repository already set up, setup still installs user-wide hooks
// that are missing, after the usual confirmation.
func TestSetupAllInstallsPendingUserHooksWhenRepositoriesAreDone(t *testing.T) {
	setup := func(t *testing.T) string {
		stub := workingEntire(t)
		addStubAgent(t, stub.dir, "codex")
		setTempConfigHome(t)
		t.Setenv(serverEnvVar, failingServer(t))
		t.Setenv(tokenEnvVar, "provisioned")
		parent := t.TempDir()
		newGitHubRepo(t, parent, "demo")
		t.Chdir(parent)
		if _, _, err := runCLI(t, "setup", "--all", "--yes"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := runCLI(t, "hooks", "uninstall", "--user"); err != nil {
			t.Fatal(err)
		}
		return os.Getenv("HOME")
	}
	installed := func(home string) bool {
		for _, file := range []string{".claude/settings.json", ".codex/hooks.json"} {
			contents, err := os.ReadFile(filepath.Join(home, file))
			if err == nil && strings.Contains(string(contents), "--check --user") {
				return true
			}
		}
		return false
	}

	t.Run("missing hooks are installed after confirmation", func(t *testing.T) {
		home := setup(t)
		stdout, _, err := runCLIWithInput(t, "yes\n", "setup", "--all")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Setup also adds baum's user-wide hooks", "Continue?", "Installed baum's Claude Code hooks"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("output misses %q:\n%s", want, stdout)
			}
		}
		if !installed(home) {
			t.Error("user-wide hooks were not installed")
		}
		stdout, _, err = runCLIWithInput(t, "", "setup", "--all")
		if err != nil || !strings.Contains(stdout, "No repositories need setup.") || strings.Contains(stdout, "Continue?") {
			t.Errorf("rerun with hooks present: %v\n%s", err, stdout)
		}
	})
	t.Run("declined confirmation writes nothing", func(t *testing.T) {
		home := setup(t)
		stdout, _, err := runCLIWithInput(t, "no\n", "setup", "--all")
		if err != nil || !strings.Contains(stdout, "Setup cancelled.") {
			t.Fatalf("err = %v\n%s", err, stdout)
		}
		if installed(home) {
			t.Error("hooks were installed after declining")
		}
	})
}

func TestPluginHooksYieldToLegacyAndKeepExistingOptIns(t *testing.T) {
	for _, agent := range []string{"claude-code", "codex"} {
		t.Run(agent, func(t *testing.T) {
			world := newHookWorld(t)
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			commitEntireSettings(t, world.repo)
			spec, _ := agenthooks.Lookup(agent)
			data, err := os.ReadFile(filepath.Join("..", "..", "plugins", "baum-"+agent, "hooks", "hooks.json"))
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Hooks map[string][]struct{ Hooks []struct{ Command string } }
			}
			if err := json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			command := config.Hooks["Stop"][0].Hooks[0].Command
			run := func() []string {
				t.Helper()
				cmd := exec.Command("sh", "-c", command)
				cmd.Dir = world.repo
				cmd.Stdin = strings.NewReader(`{"session_id":"s"}`)
				if output, err := cmd.CombinedOutput(); err != nil || len(output) != 0 {
					t.Fatalf("plugin: %v %s", err, output)
				}
				_, entire := world.logs(t)
				return entire
			}
			if got := run(); len(got) != 0 {
				t.Fatalf("no opt-in: Entire=%v", got)
			}
			t.Setenv(tokenEnvVar, "provisioned")
			if got := run(); len(got) != 1 {
				t.Fatalf("fresh plugin: Entire=%v", got)
			}
			home := os.Getenv("HOME")
			if _, err := userhooks.Install(home, agent); err != nil {
				t.Fatal(err)
			}
			if got := run(); len(got) != 0 {
				t.Fatalf("plugin duplicated legacy hook: %v", got)
			}
			world.runUserDispatcher(t, world.repo, spec, "stop")
			_, entire := world.logs(t)
			if len(entire) != 1 {
				t.Fatalf("legacy hook: %v", entire)
			}
			if _, _, err := runCLI(t, "hooks", "migrate", "--user", "--agent", agent); err != nil {
				t.Fatal(err)
			}
			if got := run(); len(got) != 1 {
				t.Fatalf("migrated plugin: Entire=%v", got)
			}
			installUserHooksDuringSetup(io.Discard, io.Discard)
			if legacy, err := userhooks.HasLegacyEvent(home, agent, "stop"); err != nil || legacy {
				t.Fatalf("setup restored legacy=%v %v", legacy, err)
			}
			statuses, err := userHooksStatuses()
			if err != nil {
				t.Fatal(err)
			}
			for _, status := range statuses {
				if status.Agent == agent && (!status.PluginManaged || status.Installed || status.Error != "") {
					t.Fatalf("status=%+v", status)
				}
			}
			if _, _, err := runCLI(t, "hooks", "install", "--user", "--agent", agent); err == nil {
				t.Fatal("install recreated legacy hooks")
			}
			if _, _, err := runCLI(t, "hooks", "install", "--user", "--legacy", "--agent", agent); err != nil {
				t.Fatal(err)
			}
			if got := run(); len(got) != 0 {
				t.Fatalf("rollback duplicated legacy: %v", got)
			}
			if world.requests.Load() != 0 {
				t.Fatal("plugin contacted hosted server")
			}
		})
	}
}

func TestShippedPluginHooksFailQuietlyWithoutCompatibleCLI(t *testing.T) {
	for _, agent := range []string{"claude-code", "codex"} {
		t.Run(agent, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "plugins", "baum-"+agent, "hooks", "hooks.json"))
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Hooks map[string][]struct{ Hooks []struct{ Command string } }
			}
			if err := json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			for _, old := range []bool{false, true} {
				bin := t.TempDir()
				if err := os.Symlink("/bin/sh", filepath.Join(bin, "sh")); err != nil {
					t.Fatal(err)
				}
				if old {
					writeExecutable(t, filepath.Join(bin, "baum"), "#!/bin/sh\necho unsupported CLI >&2\nexit 2\n")
				}
				cmd := exec.Command("/bin/sh", "-c", config.Hooks["Stop"][0].Hooks[0].Command)
				cmd.Env = append(os.Environ(), "PATH="+bin)
				if output, err := cmd.CombinedOutput(); err != nil || len(output) != 0 {
					t.Fatalf("old=%v: %v %q", old, err, output)
				}
			}
		})
	}
}

func TestDefaultLegacyInstallSkipsPluginManagedAgents(t *testing.T) {
	stub := workingEntire(t)
	addStubAgent(t, stub.dir, "codex")
	setTempConfigHome(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := os.Getenv("HOME")
	if _, err := userhooks.Migrate(home, "claude-code"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "hooks", "install", "--user"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("recreated migrated Claude settings: %v", err)
	}
	if legacy, err := userhooks.HasLegacyEvent(home, "codex", "stop"); err != nil || !legacy {
		t.Fatalf("unmigrated Codex legacy=%v %v", legacy, err)
	}
}
