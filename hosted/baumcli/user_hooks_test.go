//go:build !windows

package baumcli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/baum-ai/baum/hosted/agenthooks"
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
	for _, agent := range []string{"claude-code", "codex"} {
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
