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
	// baum never ran, so no event was sent, spooled or logged either.
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".local", "state", "baum")); !os.IsNotExist(err) {
		t.Errorf("baum left state for an open-source user: %v", err)
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

// onboardedWorld is a hook world whose repository committed baum's
// dispatcher, as merging the onboarding pull request leaves it.
func onboardedWorld(t *testing.T) hookWorld {
	t.Helper()
	world := newHookWorld(t)
	claude, _ := agenthooks.Lookup("claude-code")
	commitFile(t, world.repo, claude.SettingsFile, claudeSettings(t, agenthooks.Dispatcher(claude, "session-start")))
	return world
}

// asHostedInstall makes the baum on PATH one installed for the hosted
// service, by build or by the install script's marker.
func asHostedInstall(t *testing.T, how string) {
	t.Helper()
	switch how {
	case "build":
		t.Setenv(testDistributionEnv, hostedDistribution)
	case "install marker":
		path, err := credentialsPath()
		if err != nil {
			t.Fatal(err)
		}
		writeAgentSettings(t, filepath.Dir(path), hostedInstallMarkerName, "installed by the hosted install script\n")
		t.Cleanup(func() { _ = os.Remove(filepath.Join(filepath.Dir(path), hostedInstallMarkerName)) })
	default:
		t.Fatalf("unknown hosted install %q", how)
	}
}

// sessionStartBootstraps runs the committed session-start command and reports
// whether baum ran it and installed the Git hook dispatchers.
func (w hookWorld) sessionStartBootstraps(t *testing.T) bool {
	t.Helper()
	claude, _ := agenthooks.Lookup("claude-code")
	w.runAgentDispatcher(t, claude, "session-start")
	baum, entire := w.logs(t)
	if len(entire) != 1 || entire[0] != "hooks claude-code session-start" {
		t.Errorf("entire calls = %q", entire)
	}
	ran := strings.Join(baum, "|") == "hooks agent --check claude-code session-start|hooks agent claude-code session-start"
	if !ran && (len(baum) != 1 || baum[0] != "hooks agent --check claude-code session-start") {
		t.Errorf("baum calls = %q", baum)
	}
	installed := 0
	for _, name := range entireGitHookNames() {
		if contents, err := os.ReadFile(filepath.Join(w.repo, ".git", "hooks", name)); err == nil && strings.Contains(string(contents), baumHookMarker) {
			installed++
		}
	}
	if ran != (installed == len(entireGitHookNames())) {
		t.Errorf("baum ran the hook: %v, but %d of %d Git hook dispatchers are installed", ran, installed, len(entireGitHookNames()))
	}
	return ran
}

// An open-source baum never sets up Entire, whose hooks push transcripts, in
// a repository just because its committed files ask for it: without a login
// the hooks fall through to Entire, as before baum carried the hosted
// commands.
func TestOpenSourceBuildDoesNotBootstrapAnOnboardedRepositoryWithoutLogin(t *testing.T) {
	world := onboardedWorld(t)
	if world.sessionStartBootstraps(t) {
		t.Error("an open-source build bootstrapped an onboarded repository without a login")
	}
	if _, err := os.Stat(filepath.Join(world.repo, ".git", "baum")); !os.IsNotExist(err) {
		t.Errorf("baum left state in the clone: %v", err)
	}
}

// TestHostedInstallBootstrapsAnOnboardedRepositoryWithoutLogin keeps the
// onboarding promise for a teammate who installed the hosted baum but never
// logged in: the first agent session installs the Git hook dispatchers, and no
// baum server hears anything. Uploading still needs a login.
func TestHostedInstallBootstrapsAnOnboardedRepositoryWithoutLogin(t *testing.T) {
	for _, how := range []string{"build", "install marker"} {
		t.Run(how, func(t *testing.T) {
			world := onboardedWorld(t)
			asHostedInstall(t, how)
			if !world.sessionStartBootstraps(t) {
				t.Fatal("a hosted install did not bootstrap the onboarded repository")
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
		})
	}
}

// A login opts in with either build.
func TestLoggedInUserBootstrapsWithEitherBuild(t *testing.T) {
	for _, build := range []string{"", hostedDistribution} {
		t.Run("distribution="+build, func(t *testing.T) {
			world := onboardedWorld(t)
			t.Setenv(testDistributionEnv, build)
			t.Setenv(tokenEnvVar, "provisioned")
			if !world.sessionStartBootstraps(t) {
				t.Error("a logged-in user's hook did not run through baum")
			}
			if n := world.requests.Load(); n != 0 {
				t.Errorf("the hook sent %d requests to the baum server", n)
			}
		})
	}
}

// Only what the repository commits makes it onboarded, even for a hosted
// install: Entire's own hook command does not run
// baum, and settings that are not committed may come from anywhere, such as
// a user-wide configuration.
func TestRepositoryIsOnboardedOnlyByCommittedDispatchers(t *testing.T) {
	claude, _ := agenthooks.Lookup("claude-code")
	for name, setUp := range map[string]func(t *testing.T, repo string){
		"Entire's own command committed": func(t *testing.T, repo string) {
			commitFile(t, repo, claude.SettingsFile, claudeSettings(t, agenthooks.EntireCommand(claude, "session-start")))
		},
		"dispatcher not committed": func(t *testing.T, repo string) {
			commitFile(t, repo, "README.md", "demo\n")
			writeAgentSettings(t, repo, claude.SettingsFile, claudeSettings(t, agenthooks.Dispatcher(claude, "session-start")))
		},
	} {
		t.Run(name, func(t *testing.T) {
			world := newHookWorld(t)
			asHostedInstall(t, "build")
			setUp(t, world.repo)
			if world.sessionStartBootstraps(t) {
				t.Error("the hook ran through baum in a repository that was never onboarded")
			}
			if _, err := os.Stat(filepath.Join(world.repo, ".git", "baum")); !os.IsNotExist(err) {
				t.Errorf("baum left state in the clone: %v", err)
			}
		})
	}
}

// A linked worktree of a clone that was set up or bootstrapped runs its hooks
// through baum too: the opt-in reads the markers from the common Git
// directory.
func TestCloneMarkersCoverLinkedWorktrees(t *testing.T) {
	for name, mark := range map[string]func(t *testing.T, repo string){
		"setup": func(t *testing.T, repo string) {
			if err := markRepositorySetupComplete(t.Context(), repo); err != nil {
				t.Fatal(err)
			}
		},
		"bootstrap": func(t *testing.T, repo string) {
			marker, err := hooksBootstrapMarker(t.Context(), repo)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeHooksBootstrapMarker(marker, "older"); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			world := newHookWorld(t)
			commitFile(t, world.repo, "README.md", "demo\n")
			mark(t, world.repo)
			worktree := filepath.Join(t.TempDir(), "feature")
			runGitTest(t, world.repo, "worktree", "add", "-q", "-b", "feature", worktree)

			world.repo = worktree
			world.runGitDispatcher(t, "commit-msg", "message-file")
			baum, _ := world.logs(t)
			if want := []string{"hooks git --check commit-msg", "hooks git commit-msg message-file"}; strings.Join(baum, "|") != strings.Join(want, "|") {
				t.Errorf("baum calls in the linked worktree = %q, want %q", baum, want)
			}
		})
	}
}
