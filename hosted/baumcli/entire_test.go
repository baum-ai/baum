//go:build !windows

package baumcli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/baum-ai/baum/hosted/agenthooks"
)

func TestDetectEntireAgentsFromPATHAndConfigDirectory(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "codex"), "#!/bin/sh\nexit 0\n")
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir)

	agents := detectEntireAgents()
	if got := agentSlugs(agents); strings.Join(got, ",") != "claude-code,codex" {
		t.Fatalf("detected agents = %v", got)
	}
}

func TestEntireAgentDisplayNamesMatchStatusOutput(t *testing.T) {
	names := make(map[string]string, len(entireAgents))
	for _, agent := range entireAgents {
		names[agent.Slug] = agent.DisplayName
	}
	// These are the display names `entire status --json` reports, not the slugs
	// passed to `entire agent add`; a mismatch re-adds the agent on every setup.
	if names["copilot-cli"] != "Copilot CLI" || names["factoryai-droid"] != "Factory AI Droid" {
		t.Fatalf("Entire status names: copilot=%q droid=%q", names["copilot-cli"], names["factoryai-droid"])
	}
}

func TestSetupEntireReportsMalformedStatus(t *testing.T) {
	stub := stubEntire(t, `
case "$1" in
version) echo "entire version 0.9.0" ;;
status) echo 'not json' ;;
esac
`)
	addStubAgent(t, stub.dir, "claude")
	repo := t.TempDir()
	runGitTest(t, repo, "init")
	var stdout, stderr bytes.Buffer
	_, err := setupEntire(t.Context(), repo, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "decode `entire status --json`") {
		t.Fatalf("setup Entire error = %v", err)
	}
}

func TestSetupEntireFreshEnable(t *testing.T) {
	stub := stubEntire(t, `
case "$1" in
version) echo "entire version 0.9.0" ;;
status) echo "not enabled" >&2; exit 1 ;;
enable) ;;
agent) ;;
esac
`)
	addStubAgent(t, stub.dir, "claude")
	addStubAgent(t, stub.dir, "codex")
	repo := t.TempDir()
	runGitTest(t, repo, "init")
	var stdout, stderr bytes.Buffer
	if _, err := setupEntire(t.Context(), repo, &stdout, &stderr); err != nil {
		t.Fatalf("setup Entire: %v (stderr %q)", err, stderr.String())
	}
	calls := readCalls(t, stub.calls)
	want := []string{
		"version",
		"status --json",
		"enable --agent claude-code --checkpoint-backend branch --no-init-repo --yes",
		"agent add codex",
	}
	assertCallArgs(t, calls, repo, want)
}

func TestSetupEntireAlreadyEnabledIsNoOp(t *testing.T) {
	stub := stubEntire(t, `
case "$1" in
version) echo "entire version 0.9.0" ;;
status) printf '%s\n' '{"enabled":true,"agents":["Claude Code"]}' ;;
*) exit 22 ;;
esac
`)
	addStubAgent(t, stub.dir, "claude")
	repo := t.TempDir()
	runGitTest(t, repo, "init")
	var stdout, stderr bytes.Buffer
	if _, err := setupEntire(t.Context(), repo, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	assertCallArgs(t, readCalls(t, stub.calls), repo, []string{"version", "status --json"})
	if !strings.Contains(stdout.String(), "already enabled") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestSetupEntireAddsNewlyDetectedAgent(t *testing.T) {
	stub := stubEntire(t, `
case "$1" in
version) echo "entire version 0.9.0" ;;
status) printf '%s\n' '{"enabled":true,"agents":["Claude Code"]}' ;;
agent) ;;
*) exit 22 ;;
esac
`)
	addStubAgent(t, stub.dir, "claude")
	addStubAgent(t, stub.dir, "codex")
	repo := t.TempDir()
	runGitTest(t, repo, "init")
	var stdout, stderr bytes.Buffer
	if _, err := setupEntire(t.Context(), repo, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	assertCallArgs(t, readCalls(t, stub.calls), repo, []string{"version", "status --json", "agent add codex"})
}

func TestSetupEntirePassesCheckpointRemoteWhenProvided(t *testing.T) {
	stub := stubEntire(t, "exit 0\n")
	var stdout, stderr bytes.Buffer
	repo := t.TempDir()
	err := enableEntire(t.Context(), filepath.Join(stub.dir, "entire"), repo, entireEnableOptions{
		Agents:           entireAgents[:1],
		CheckpointRemote: "checkpoint.example/repo",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	assertCallArgs(t, readCalls(t, stub.calls), repo, []string{"enable --agent claude-code --checkpoint-backend branch --no-init-repo --yes --checkpoint-remote checkpoint.example/repo"})
}

func TestRunEntireCommandSuppressesExpectedReplacementWarningsOnSuccess(t *testing.T) {
	stub := stubEntire(t, `
echo '[entire] Warning: replacing commit-msg (backup commit-msg.pre-entire already exists from a previous install)' >&2
echo 'useful diagnostic' >&2
`)
	var stdout, stderr bytes.Buffer
	if err := runEntireCommand(t.Context(), filepath.Join(stub.dir, "entire"), t.TempDir(), []string{"enable"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "replacing commit-msg") || !strings.Contains(stderr.String(), "useful diagnostic") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunEntireCommandKeepsReplacementWarningsOnFailure(t *testing.T) {
	stub := stubEntire(t, `
echo '[entire] Warning: replacing commit-msg (backup commit-msg.pre-entire already exists from a previous install)' >&2
exit 9
`)
	var stdout, stderr bytes.Buffer
	if err := runEntireCommand(t.Context(), filepath.Join(stub.dir, "entire"), t.TempDir(), []string{"enable"}, &stdout, &stderr); err == nil {
		t.Fatal("run Entire succeeded")
	}
	if !strings.Contains(stderr.String(), "replacing commit-msg") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestEnsureEntireInstalledExtractsReleaseAndVerifiesVersion(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release installer assertion is Linux-specific")
	}
	archive := entireArchive(t, "#!/bin/sh\necho 'entire version 0.9.0'\n")
	asset := "entire_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/" + asset; r.URL.Path != want {
			t.Errorf("download path = %q, want %q", r.URL.Path, want)
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)

	oldLookPath, oldURL, oldDir := lookPath, entireReleaseBaseURL, entireInstallDir
	oldChecksum := entireReleaseSHA256[asset]
	t.Cleanup(func() {
		lookPath, entireReleaseBaseURL, entireInstallDir = oldLookPath, oldURL, oldDir
		entireReleaseSHA256[asset] = oldChecksum
	})
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	entireReleaseBaseURL = server.URL
	entireInstallDir = filepath.Join(t.TempDir(), "bin")
	entireReleaseSHA256[asset] = fmt.Sprintf("%x", sha256.Sum256(archive))
	var stdout, stderr bytes.Buffer
	bin, err := ensureEntireInstalled(t.Context(), &stdout, &stderr)
	if err != nil {
		t.Fatalf("install Entire: %v", err)
	}
	if bin != filepath.Join(entireInstallDir, "entire") {
		t.Errorf("binary = %q", bin)
	}
	if info, err := os.Stat(bin); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed binary: info=%v err=%v", info, err)
	}
	if !strings.Contains(stdout.String(), "entire version 0.9.0") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Error("Entire was downloaded although it is already installed")
	return nil, fmt.Errorf("unexpected download")
}

func TestEnsureEntireInstalledReusesTheInstalledBinaryOffPath(t *testing.T) {
	installDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(installDir, "entire"), "#!/bin/sh\necho 'entire version 0.9.0'\n")
	oldLookPath, oldDir, oldClient := lookPath, entireInstallDir, entireHTTPClient
	t.Cleanup(func() { lookPath, entireInstallDir, entireHTTPClient = oldLookPath, oldDir, oldClient })
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	entireInstallDir = installDir
	entireHTTPClient = &http.Client{Transport: failingTransport{t}}
	var stdout, stderr bytes.Buffer
	bin, err := ensureEntireInstalled(t.Context(), &stdout, &stderr)
	if err != nil {
		t.Fatalf("ensure Entire: %v", err)
	}
	if bin != filepath.Join(installDir, "entire") {
		t.Errorf("binary = %q", bin)
	}
}

func TestInstallEntireRejectsChecksumMismatch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release installer assertion is Linux-specific")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(entireArchive(t, "untrusted"))
	}))
	t.Cleanup(server.Close)
	oldURL, oldDir := entireReleaseBaseURL, entireInstallDir
	t.Cleanup(func() { entireReleaseBaseURL, entireInstallDir = oldURL, oldDir })
	entireReleaseBaseURL = server.URL
	entireInstallDir = filepath.Join(t.TempDir(), "bin")
	var stdout, stderr bytes.Buffer
	if _, err := installEntire(t.Context(), &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("install error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(entireInstallDir, "entire")); !os.IsNotExist(err) {
		t.Fatalf("untrusted binary was installed: %v", err)
	}
}

func TestInstallEntireOnDarwinUsesPinnedArchive(t *testing.T) {
	archive := entireArchive(t, "darwin binary")
	asset := "entire_darwin_arm64.tar.gz"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+asset {
			t.Errorf("download path = %q", r.URL.Path)
		}
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)

	oldURL, oldDir := entireReleaseBaseURL, entireInstallDir
	oldGOOS, oldGOARCH := entireRuntimeGOOS, entireRuntimeGOARCH
	oldChecksum := entireReleaseSHA256[asset]
	t.Cleanup(func() {
		entireReleaseBaseURL, entireInstallDir = oldURL, oldDir
		entireRuntimeGOOS, entireRuntimeGOARCH = oldGOOS, oldGOARCH
		entireReleaseSHA256[asset] = oldChecksum
	})
	entireReleaseBaseURL = server.URL
	entireInstallDir = filepath.Join(t.TempDir(), "bin")
	entireRuntimeGOOS, entireRuntimeGOARCH = "darwin", "arm64"
	entireReleaseSHA256[asset] = fmt.Sprintf("%x", sha256.Sum256(archive))

	var stdout, stderr bytes.Buffer
	bin, err := installEntire(t.Context(), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if bin != filepath.Join(entireInstallDir, "entire") {
		t.Errorf("binary = %q", bin)
	}
	if !strings.Contains(stdout.String(), "/"+asset) {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestSetupSkipEntireDoesNotAttemptInstallation(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	repo := filepath.Join(t.TempDir(), "demo")
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/acme/demo.git")
	t.Chdir(repo)

	mux := setupAPIMux(t, "rcnt_api_key")
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)
	oldLookPath := lookPath
	t.Cleanup(func() { lookPath = oldLookPath })
	lookPath = func(string) (string, error) { t.Error("Entire lookup attempted"); return "", os.ErrNotExist }

	if _, _, err := runCLI(t, "setup", "--yes", "--skip-entire"); err != nil {
		t.Fatal(err)
	}
}

func TestSetupEntireFailureWarnsAndContinues(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	repo := filepath.Join(t.TempDir(), "demo")
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/acme/demo.git")
	t.Chdir(repo)
	stub := stubEntire(t, `
case "$1" in
version) echo "entire version 0.9.0" ;;
status) exit 1 ;;
enable) echo "enable exploded" >&2; exit 9 ;;
esac
`)
	addStubAgent(t, stub.dir, "claude")
	mux := setupAPIMux(t, "rcnt_api_key")
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)

	stdout, stderr, err := runCLI(t, "setup", "--sync", "--upload", "--yes")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if !strings.Contains(stderr, "enable exploded") || !strings.Contains(stderr, "warning: entire setup failed") {
		t.Errorf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, "Requested sync for acme/demo.") || !strings.Contains(stdout, "No agent sessions found.") {
		t.Errorf("setup did not continue: stdout = %q", stdout)
	}
}

type entireStub struct{ dir, calls string }

func stubEntire(t *testing.T, body string) entireStub {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s|%s\\n' \"$PWD\" \"$*\" >> \"$ENTIRE_TEST_CALLS\"\n" + body
	writeExecutable(t, filepath.Join(dir, "entire"), script)
	t.Setenv("ENTIRE_TEST_CALLS", calls)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	oldLookPath := lookPath
	t.Cleanup(func() { lookPath = oldLookPath })
	lookPath = func(name string) (string, error) {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
		return "", os.ErrNotExist
	}
	return entireStub{dir: dir, calls: calls}
}

func addStubAgent(t *testing.T, dir, name string) {
	t.Helper()
	writeExecutable(t, filepath.Join(dir, name), "#!/bin/sh\nexit 0\n")
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readCalls(t *testing.T, path string) []string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(contents)), "\n")
}

func assertCallArgs(t *testing.T, calls []string, repo string, args []string) {
	t.Helper()
	if len(calls) != len(args) {
		t.Fatalf("calls = %q, want %q", calls, args)
	}
	for i := range calls {
		wantDir := repo
		if args[i] == "version" {
			wantDir, _ = os.Getwd()
		}
		if want := wantDir + "|" + args[i]; calls[i] != want {
			t.Errorf("call %d = %q, want %q", i, calls[i], want)
		}
	}
}

func agentSlugs(agents []entireAgent) []string {
	slugs := make([]string, len(agents))
	for i := range agents {
		slugs[i] = agents[i].Slug
	}
	return slugs
}

func entireArchive(t *testing.T, binary string) []byte {
	t.Helper()
	var result bytes.Buffer
	gz := gzip.NewWriter(&result)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "entire", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(binary)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func setupAPIMux(t *testing.T, token string) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": "user-1", "github_login": "octocat"})
	})
	mux.HandleFunc("GET /github/repositories/find", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "repo-1", "full_name": "acme/demo", "enabled": true})
	})
	mux.HandleFunc("POST /github/repositories/repo-1/sync", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	return mux
}

func Example_entireEnableCommand() {
	args := []string{"enable", "--agent", "claude-code", "--checkpoint-backend", "branch", "--no-init-repo", "--yes"}
	fmt.Println(strings.Join(args, " "))
	// Output: enable --agent claude-code --checkpoint-backend branch --no-init-repo --yes
}

// TestSetupEntireDoesNotReaddAnAgentItAlreadyOwns is the failure the owned
// dispatcher causes: `entire status` lists only agents whose hooks are Entire's
// own, so a set-up repository looks unconfigured and every run of setup asked
// Entire to add the agent again, appending a second copy of each hook.
func TestSetupEntireDoesNotReaddAnAgentItAlreadyOwns(t *testing.T) {
	stub := stubEntire(t, `
case "$1" in
version) echo "entire version 0.9.0" ;;
status) printf '%s\n' '{"enabled":true,"agents":[]}' ;;
agent) ;;
*) exit 22 ;;
esac
`)
	addStubAgent(t, stub.dir, "claude")
	addStubAgent(t, stub.dir, "codex")
	repo := t.TempDir()
	runGitTest(t, repo, "init")
	writeAgentSettings(t, repo, ".claude/settings.json", ownedClaudeSettings(t, "session-start"))

	var stdout, stderr bytes.Buffer
	if _, err := setupEntire(t.Context(), repo, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	assertCallArgs(t, readCalls(t, stub.calls), repo, []string{"version", "status --json", "agent add codex"})
	if got := countHookCommands(t, repo, ".claude/settings.json"); got != 1 {
		t.Errorf("Claude Code hook commands = %d, want 1", got)
	}
}

// TestSetupEntireCollapsesTheCopyEnableAppends covers a fresh `entire enable`
// over hooks that already dispatch through baum: Entire appends its own
// command beside the dispatcher, and taking ownership afterwards leaves one hook
// for the event rather than two.
func TestSetupEntireCollapsesTheCopyEnableAppends(t *testing.T) {
	spec, _ := lookupAgentHookSpec("claude-code")
	dispatcher, err := agenthooks.EncodeJSON(agenthooks.Dispatcher(spec, "session-start"))
	if err != nil {
		t.Fatal(err)
	}
	entire, err := agenthooks.EncodeJSON(agenthooks.EntireCommand(spec, "session-start"))
	if err != nil {
		t.Fatal(err)
	}
	afterEnable := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(afterEnable, []byte(`{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":`+string(dispatcher)+`},{"type":"command","command":`+string(entire)+`}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENTIRE_TEST_AFTER_ENABLE", afterEnable)
	stub := stubEntire(t, `
case "$1" in
version) echo "entire version 0.9.0" ;;
status) echo "not enabled" >&2; exit 1 ;;
enable) cp "$ENTIRE_TEST_AFTER_ENABLE" .claude/settings.json ;;
*) exit 22 ;;
esac
`)
	addStubAgent(t, stub.dir, "claude")
	repo := t.TempDir()
	runGitTest(t, repo, "init")
	writeAgentSettings(t, repo, ".claude/settings.json", ownedClaudeSettings(t, "session-start"))

	var stdout, stderr bytes.Buffer
	if _, err := setupEntire(t.Context(), repo, &stdout, &stderr); err != nil {
		t.Fatalf("setup Entire: %v (stderr %q)", err, stderr.String())
	}
	got := readHookTestFile(t, filepath.Join(repo, ".claude", "settings.json"))
	if strings.Count(got, `"command":`) != 1 || !strings.Contains(got, string(dispatcher)) {
		t.Errorf("settings after setup:\n%s", got)
	}
}

// ownedClaudeSettings is a Claude Code settings file whose one hook already
// dispatches through baum, as a set-up or onboarded repository has.
func ownedClaudeSettings(t *testing.T, event string) string {
	t.Helper()
	spec, _ := lookupAgentHookSpec("claude-code")
	dispatcher, err := agenthooks.EncodeJSON(agenthooks.Dispatcher(spec, event))
	if err != nil {
		t.Fatal(err)
	}
	return `{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":` + string(dispatcher) + `}]}]}}`
}

func countHookCommands(t *testing.T, root, relative string) int {
	t.Helper()
	return strings.Count(readHookTestFile(t, filepath.Join(root, filepath.FromSlash(relative))), `"command":`)
}
