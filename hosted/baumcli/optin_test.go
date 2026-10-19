//go:build !windows

package baumcli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/baum-ai/baum/hosted/agenthooks"
)

// hookWorld is a machine on which the dispatchers committed to customer
// repositories run for real: `baum` on PATH is this test binary acting as the
// baum command, `entire` is a stub, and both log how they were called.
type hookWorld struct {
	repo, baumLog, entireLog string
	requests                 *atomic.Int64
}

func newHookWorld(t *testing.T) hookWorld {
	t.Helper()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, logs := t.TempDir(), t.TempDir()
	world := hookWorld{
		repo:      initHookTestRepo(t),
		baumLog:   filepath.Join(logs, "baum.log"),
		entireLog: filepath.Join(logs, "entire.log"),
		requests:  &atomic.Int64{},
	}
	writeExecutable(t, filepath.Join(bin, "baum"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+world.baumLog+"'\n"+
		runAsBaumEnv+"=1 exec '"+testBinary+"' \"$@\"\n")
	writeExecutable(t, filepath.Join(bin, "entire"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+world.entireLog+"'\ncat >/dev/null\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+systemBinaryPath)
	original := entireInstallDir
	t.Cleanup(func() { entireInstallDir = original })
	entireInstallDir = t.TempDir()
	t.Setenv("HOME", t.TempDir())

	// Every baum server request fails the test's expectations by being counted.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		world.requests.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)
	return world
}

func (w hookWorld) logs(t *testing.T) (baum, entire []string) {
	t.Helper()
	read := func(path string) []string {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	baum, entire = read(w.baumLog), read(w.entireLog)
	_ = os.Remove(w.baumLog)
	_ = os.Remove(w.entireLog)
	return baum, entire
}

// runAgentDispatcher runs the frozen command a customer repository commits
// for one agent event, the way the agent does: as a shell command line, in the
// clone. TestAgentHookDispatcherIsFrozen pins the strings Dispatcher renders.
func (w hookWorld) runAgentDispatcher(t *testing.T, spec agenthooks.Spec, event string) {
	t.Helper()
	cmd := exec.Command("sh", "-c", agenthooks.Dispatcher(spec, event))
	cmd.Dir = w.repo
	cmd.Stdin = strings.NewReader(`{"session_id":"s"}`)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s dispatcher: %v: %s", spec.Slug, event, err, output)
	}
}

func (w hookWorld) runGitDispatcher(t *testing.T, hook string, args ...string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), hook)
	writeExecutable(t, path, gitHookDispatcher(hook))
	cmd := exec.Command(path, args...)
	cmd.Dir = w.repo
	cmd.Stdin = strings.NewReader("")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s dispatcher: %v: %s", hook, err, output)
	}
}

// TestDispatchersFallThroughToEntireForOpenSourceUsers replays every frozen
// agent dispatcher, and the Git dispatcher, on a machine that never ran
// `baum login` or `baum setup`. baum answers `--check` with a refusal, so the
// command runs Entire itself, exactly as if baum were not installed, and no
// baum server hears anything.
func TestDispatchersFallThroughToEntireForOpenSourceUsers(t *testing.T) {
	world := newHookWorld(t)
	for _, spec := range agenthooks.All() {
		for _, event := range spec.Events {
			world.runAgentDispatcher(t, spec, event)
			baum, entire := world.logs(t)
			wantCheck := "hooks agent --check " + spec.Slug + " " + event
			if len(baum) != 1 || baum[0] != wantCheck {
				t.Errorf("%s %s: baum calls = %q, want only %q", spec.Slug, event, baum, wantCheck)
			}
			if want := "hooks " + spec.Slug + " " + event; len(entire) != 1 || entire[0] != want {
				t.Errorf("%s %s: entire calls = %q, want %q", spec.Slug, event, entire, want)
			}
		}
	}
	world.runGitDispatcher(t, "commit-msg", "message-file")
	baum, entire := world.logs(t)
	if len(baum) != 1 || baum[0] != "hooks git --check commit-msg" {
		t.Errorf("git hook: baum calls = %q", baum)
	}
	if len(entire) != 1 || entire[0] != "hooks git commit-msg message-file" {
		t.Errorf("git hook: entire calls = %q", entire)
	}
	if _, err := os.Stat(filepath.Join(world.repo, ".git", "baum")); !os.IsNotExist(err) {
		t.Errorf("baum left state in an open-source user's clone: %v", err)
	}
	if n := world.requests.Load(); n != 0 {
		t.Errorf("%d requests reached the baum server", n)
	}
}

// TestDispatchersRunThroughBaumOnceOptedIn replays the same commands for a
// logged-in user and for a clone set up with `baum setup`: baum runs the hook
// and delegates to Entire, as it always has for hosted customers.
func TestDispatchersRunThroughBaumOnceOptedIn(t *testing.T) {
	optIns := map[string]func(t *testing.T, world hookWorld){
		"logged in": func(t *testing.T, _ hookWorld) {
			if err := saveCredentials(credentials{ServerURL: "https://baum.example/api", Token: "stored"}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = removeCredentials() })
		},
		"BAUM_TOKEN": func(t *testing.T, _ hookWorld) { t.Setenv(tokenEnvVar, "provisioned") },
		"clone set up": func(t *testing.T, world hookWorld) {
			if err := markRepositorySetupComplete(t.Context(), world.repo); err != nil {
				t.Fatal(err)
			}
		},
		"clone bootstrapped": func(t *testing.T, world hookWorld) {
			marker, err := hooksBootstrapMarker(t.Context(), world.repo)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeHooksBootstrapMarker(marker, "older"); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, optIn := range optIns {
		t.Run(name, func(t *testing.T) {
			world := newHookWorld(t)
			optIn(t, world)
			spec, _ := agenthooks.Lookup("claude-code")
			world.runAgentDispatcher(t, spec, "stop")
			baum, entire := world.logs(t)
			want := []string{"hooks agent --check claude-code stop", "hooks agent claude-code stop"}
			if strings.Join(baum, "|") != strings.Join(want, "|") {
				t.Errorf("baum calls = %q, want %q", baum, want)
			}
			if len(entire) != 1 || entire[0] != "hooks claude-code stop" {
				t.Errorf("entire calls = %q", entire)
			}
			world.runGitDispatcher(t, "commit-msg", "message-file")
			baum, entire = world.logs(t)
			if want := []string{"hooks git --check commit-msg", "hooks git commit-msg message-file"}; strings.Join(baum, "|") != strings.Join(want, "|") {
				t.Errorf("git hook: baum calls = %q, want %q", baum, want)
			}
			if len(entire) != 1 || entire[0] != "hooks git commit-msg message-file" {
				t.Errorf("git hook: entire calls = %q", entire)
			}
			if n := world.requests.Load(); n != 0 {
				t.Errorf("hooks sent %d requests to the baum server", n)
			}
		})
	}
}

// TestHostedCommandsSendNothingBeforeLogin runs the hosted commands an
// open-source user might try without logging in. Each one either works
// locally or stops with a login hint before contacting the server.
func TestHostedCommandsSendNothingBeforeLogin(t *testing.T) {
	world := newHookWorld(t)
	t.Chdir(world.repo)
	runGitTest(t, world.repo, "remote", "add", "origin", "https://github.com/acme/demo.git")
	for _, args := range [][]string{
		{"whoami"},
		{"logout"},
		{"upload", "--yes"},
		{"setup", "--skip-entire", "--sync"},
		{"setup", "--skip-entire", "--upload", "--yes"},
		{"hooks", "agent", "--check", "claude-code", "stop"},
		{"hooks", "git", "--check", "pre-push"},
	} {
		_, _, _ = runCLI(t, args...)
	}
	if n := world.requests.Load(); n != 0 {
		t.Errorf("%d requests reached the baum server before login", n)
	}
}

// commitFile commits one file to the clone, as merging the onboarding pull
// request and pulling it would.
func commitFile(t *testing.T, repo, path, contents string) {
	t.Helper()
	writeAgentSettings(t, repo, path, contents)
	runGitTest(t, repo, "add", path)
	runGitTest(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "add "+path)
}

// claudeSettings is a Claude Code settings file whose session-start hook runs
// command.
func claudeSettings(t *testing.T, command string) string {
	t.Helper()
	encoded, err := agenthooks.EncodeJSON(command)
	if err != nil {
		t.Fatal(err)
	}
	return `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":` + string(encoded) + `}]}]}}`
}

// TestOnboardedRepositoryBootstrapsWithoutLogin keeps the onboarding promise
// for a teammate who has baum but never logged in: in a repository whose
// committed hook settings dispatch through baum, the first agent session
// installs the Git hook dispatchers, and no baum server hears anything.
// Uploading still needs a login.
func TestOnboardedRepositoryBootstrapsWithoutLogin(t *testing.T) {
	world := newHookWorld(t)
	claude, _ := agenthooks.Lookup("claude-code")
	commitFile(t, world.repo, claude.SettingsFile, claudeSettings(t, agenthooks.Dispatcher(claude, "session-start")))

	world.runAgentDispatcher(t, claude, "session-start")
	baum, entire := world.logs(t)
	want := []string{"hooks agent --check claude-code session-start", "hooks agent claude-code session-start"}
	if strings.Join(baum, "|") != strings.Join(want, "|") {
		t.Errorf("baum calls = %q, want %q", baum, want)
	}
	if len(entire) != 1 || entire[0] != "hooks claude-code session-start" {
		t.Errorf("entire calls = %q", entire)
	}
	for _, name := range entireGitHookNames() {
		if contents := readHookTestFile(t, filepath.Join(world.repo, ".git", "hooks", name)); !strings.Contains(contents, baumHookMarker) {
			t.Errorf("session start did not install the %s dispatcher: %q", name, contents)
		}
	}

	// A session recorded in the clone, which only a logged-in upload sends.
	runGitTest(t, world.repo, "remote", "add", "origin", "https://github.com/acme/demo.git")
	home, _ := os.UserHomeDir()
	writeAgentSettings(t, home, ".codex/sessions/2026/07/22/rollout-test.jsonl",
		`{"timestamp":"2026-07-22T12:00:00Z","type":"session_meta","payload":{"id":"codex-1","session_id":"codex-1","cwd":`+quoteJSON(world.repo)+`,"source":"cli"}}`+"\n")
	t.Chdir(world.repo)
	if _, _, err := runCLI(t, "upload", "--yes"); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("upload without login: err = %v, want a login hint", err)
	}
	if n := world.requests.Load(); n != 0 {
		t.Fatalf("%d requests reached the baum server without a login", n)
	}
	t.Setenv(tokenEnvVar, "provisioned")
	_, _, _ = runCLI(t, "upload", "--yes")
	if world.requests.Load() == 0 {
		t.Error("a logged-in upload never reached the server")
	}
}

// Only what the repository commits makes it onboarded: Entire's own hook
// command does not run baum, and settings that are not committed may come
// from anywhere, such as a user-wide configuration.
func TestRepositoryIsOnboardedOnlyByCommittedDispatchers(t *testing.T) {
	claude, _ := agenthooks.Lookup("claude-code")
	t.Run("Entire's own command committed", func(t *testing.T) {
		world := newHookWorld(t)
		commitFile(t, world.repo, claude.SettingsFile, claudeSettings(t, agenthooks.EntireCommand(claude, "session-start")))
		world.runAgentDispatcher(t, claude, "stop")
		if baum, _ := world.logs(t); len(baum) != 1 {
			t.Errorf("baum calls = %q, want only the check", baum)
		}
	})
	t.Run("dispatcher not committed", func(t *testing.T) {
		world := newHookWorld(t)
		commitFile(t, world.repo, "README.md", "demo\n")
		writeAgentSettings(t, world.repo, claude.SettingsFile, claudeSettings(t, agenthooks.Dispatcher(claude, "session-start")))
		world.runAgentDispatcher(t, claude, "session-start")
		baum, entire := world.logs(t)
		if len(baum) != 1 || len(entire) != 1 || entire[0] != "hooks claude-code session-start" {
			t.Errorf("baum calls = %q, entire calls = %q, want a fall-through to Entire", baum, entire)
		}
		if _, err := os.Stat(filepath.Join(world.repo, ".git", "baum")); !os.IsNotExist(err) {
			t.Errorf("baum left state in a clone that was never onboarded: %v", err)
		}
	})
}

// A linked worktree of a clone set up with `baum setup` runs its hooks through
// baum too: the opt-in reads the marker from the common Git directory.
func TestSetupMarkerCoversLinkedWorktrees(t *testing.T) {
	world := newHookWorld(t)
	commitFile(t, world.repo, "README.md", "demo\n")
	if err := markRepositorySetupComplete(t.Context(), world.repo); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "feature")
	runGitTest(t, world.repo, "worktree", "add", "-q", "-b", "feature", worktree)

	world.repo = worktree
	world.runGitDispatcher(t, "commit-msg", "message-file")
	baum, _ := world.logs(t)
	if want := []string{"hooks git --check commit-msg", "hooks git commit-msg message-file"}; strings.Join(baum, "|") != strings.Join(want, "|") {
		t.Errorf("baum calls in the linked worktree = %q, want %q", baum, want)
	}
}
