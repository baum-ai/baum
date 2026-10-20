//go:build !windows

package baumcli

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/hosted/apiclient"
	"github.com/baum-ai/baum/hosted/traceupload"
	"github.com/baum-ai/baum/sessions/collector"
)

func TestSetupWithAPIKeyRequestsSyncAndRunsUpload(t *testing.T) {
	workingEntire(t)
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	repo := filepath.Join(t.TempDir(), "demo")
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/acme/demo.git")
	t.Chdir(repo)
	rollout := filepath.Join(os.Getenv("HOME"), ".codex", "sessions", "2026", "09", "07", "rollout-setup.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-09-07T12:00:00Z","type":"session_meta","payload":{"id":"codex-setup","session_id":"codex-setup","cwd":` + quoteJSON(repo) + `,"source":"cli"}}` + "\n" +
		`{"timestamp":"2026-09-07T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	var deviceRequests, findRequests, syncRequests int
	var uploaded []byte
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/device", func(w http.ResponseWriter, r *http.Request) {
		deviceRequests++
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rcnt_api_key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": "user-1", "github_login": "octocat"})
	})
	mux.HandleFunc("GET /github/repositories/find", func(w http.ResponseWriter, r *http.Request) {
		findRequests++
		if r.Header.Get("Authorization") != "Bearer rcnt_api_key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "repo-1", "full_name": "acme/demo", "enabled": true})
	})
	mux.HandleFunc("POST /github/repositories/repo-1/sync", func(w http.ResponseWriter, r *http.Request) {
		syncRequests++
		if r.Header.Get("Authorization") != "Bearer rcnt_api_key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /github/repositories/repo-1/trace-uploads", func(w http.ResponseWriter, r *http.Request) {
		uploaded, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv(serverEnvVar, srv.URL)

	stdout, _, err := runCLI(t, "setup")
	if err != nil || uploaded != nil || strings.Contains(stdout, "Upload") {
		t.Fatalf("default setup: stdout=%q uploaded=%v error=%v", stdout, uploaded != nil, err)
	}
	complete, err := repositorySetupComplete(t.Context(), repo)
	if err != nil || !complete {
		t.Fatalf("default setup complete=%v error=%v", complete, err)
	}
	findRequests, syncRequests = 0, 0
	stdout, _, err = runCLI(t, "setup", "--sync", "--upload", "--yes")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if deviceRequests != 0 || findRequests != 1 || syncRequests != 1 {
		t.Errorf("requests: device=%d find=%d sync=%d", deviceRequests, findRequests, syncRequests)
	}
	if !strings.Contains(stdout, "Requested sync for acme/demo.") || !strings.Contains(stdout, "Uploaded 1 session") {
		t.Errorf("stdout = %q", stdout)
	}
	manifest := uploadedManifest(t, uploaded)
	if len(manifest.RepositoryRoots) != 1 || manifest.RepositoryRoots[0].ID != "repo-1" ||
		len(manifest.RepositoryRoots[0].Roots) != 1 || manifest.RepositoryRoots[0].Roots[0] != filepath.ToSlash(repo) {
		t.Fatalf("repository roots = %+v, want verified setup root %q", manifest.RepositoryRoots, repo)
	}
}

func TestSetupConfirmsEachRepositoryFromPipedInput(t *testing.T) {
	workingEntire(t)
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	parent := t.TempDir()
	one := newGitHubRepo(t, parent, "one")
	two := newGitHubRepo(t, parent, "two")
	t.Chdir(t.TempDir())
	for i, repo := range []string{one, two} {
		rollout := filepath.Join(os.Getenv("HOME"), ".codex", "sessions", "2026", "09", "07", fmt.Sprintf("rollout-%d.jsonl", i))
		if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("codex-%d", i)
		content := `{"timestamp":"2026-09-07T12:00:00Z","type":"session_meta","payload":{"id":"` + id + `","session_id":"` + id + `","cwd":` + quoteJSON(repo) + `,"source":"cli"}}` + "\n" +
			`{"timestamp":"2026-09-07T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}` + "\n"
		if err := os.WriteFile(rollout, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var uploads int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"id": "user-1", "github_login": "octocat"})
	})
	mux.HandleFunc("GET /github/repositories/find", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("full_name")
		writeJSON(w, http.StatusOK, map[string]any{"id": "id-" + name, "full_name": name, "enabled": true})
	})
	mux.HandleFunc("POST /github/repositories/{id}/trace-uploads", func(w http.ResponseWriter, r *http.Request) {
		uploads++
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv(serverEnvVar, srv.URL)

	stdout, stderr, err := runCLIWithInput(t, "y\ny\n", "setup", one, two, "--upload")
	if err != nil {
		t.Fatalf("setup: %v (stderr %q)", err, stderr)
	}
	if uploads != 2 || strings.Contains(stdout, "Upload cancelled") {
		t.Errorf("uploads = %d, stdout = %q", uploads, stdout)
	}
}

func TestSetupAllIsolatesRepositoryFailures(t *testing.T) {
	workingEntire(t)
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	repositories := map[string]string{}
	for i, name := range []string{"one", "two", "three"} {
		repo := filepath.Join(t.TempDir(), name)
		runGitTest(t, repo, "init")
		runGitTest(t, repo, "remote", "add", "origin", "https://github.com/acme/"+name+".git")
		repositories[name] = repo
		rollout := filepath.Join(home, ".codex", "sessions", "2026", "08", "14", fmt.Sprintf("rollout-%d.jsonl", i))
		if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"timestamp":"2026-08-14T12:00:00Z","type":"session_meta","payload":{"id":"codex-` + name + `","session_id":"codex-` + name + `","cwd":` + quoteJSON(repo) + `,"source":"cli"}}` + "\n" +
			`{"timestamp":"2026-08-14T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}` + "\n"
		if err := os.WriteFile(rollout, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var uploaded []string
	inventories := map[string][]traceupload.RepositoryRoots{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"id": "user-1", "github_login": "octocat"})
	})
	mux.HandleFunc("GET /github/installations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"installations": []any{map[string]any{"repositories": []any{
			map[string]any{"id": "repo-1", "full_name": "acme/one", "enabled": true},
			map[string]any{"id": "repo-2", "full_name": "acme/two", "enabled": true},
			map[string]any{"id": "repo-3", "full_name": "acme/three", "enabled": true},
		}}}})
	})
	for i, name := range []string{"one", "two", "three"} {
		id, repoName := fmt.Sprintf("repo-%d", i+1), name
		mux.HandleFunc("POST /github/repositories/"+id+"/sync", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		})
		mux.HandleFunc("POST /github/repositories/"+id+"/trace-uploads", func(w http.ResponseWriter, r *http.Request) {
			uploaded = append(uploaded, repoName)
			body, _ := io.ReadAll(r.Body)
			inventories[repoName] = uploadedManifest(t, body).RepositoryRoots
			if repoName == "two" {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "broken upload"})
				return
			}
			w.WriteHeader(http.StatusAccepted)
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)

	stdout, stderr, err := runCLI(t, "setup", "--all")
	if err != nil || len(uploaded) != 0 || !strings.Contains(stdout, "3 succeeded, 0 failed") {
		t.Fatalf("default setup all: uploads=%v stdout=%q stderr=%q error=%v", uploaded, stdout, stderr, err)
	}
	for _, repo := range repositories {
		marker, err := repositorySetupMarker(t.Context(), repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(marker); err != nil {
			t.Fatal(err)
		}
	}
	stdout, stderr, err = runCLI(t, "setup", "--all", "--upload", "--yes")
	if err == nil || !strings.Contains(err.Error(), "1 repository") {
		t.Fatalf("error = %v", err)
	}
	if strings.Join(uploaded, ",") != "one,three,two" {
		t.Fatalf("uploads = %v", uploaded)
	}
	for _, name := range uploaded {
		if len(inventories[name]) != 3 {
			t.Fatalf("%s setup inventory = %+v, want all three repositories", name, inventories[name])
		}
	}
	if !strings.Contains(stderr, "setup failed for acme/two") || !strings.Contains(stdout, "2 succeeded, 1 failed") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	for _, name := range []string{"one", "three"} {
		complete, markerErr := repositorySetupComplete(t.Context(), repositories[name])
		if markerErr != nil || !complete {
			t.Errorf("%s complete=%v error=%v", name, complete, markerErr)
		}
	}
	uploaded = nil
	stdout, _, err = runCLI(t, "setup", "--all", "--upload", "--yes")
	if err == nil || strings.Join(uploaded, ",") != "one,three,two" {
		t.Fatalf("rerun: uploads=%v stdout=%q error=%v", uploaded, stdout, err)
	}
}

func TestSetupAllUploadSkipsCheckoutsWithoutSessionsAsSucceeded(t *testing.T) {
	workingEntire(t)
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	repo := filepath.Join(t.TempDir(), "demo")
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/acme/demo.git")

	rollout := filepath.Join(home, ".codex", "sessions", "2026", "08", "14", "rollout-demo.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-08-14T12:00:00Z","type":"session_meta","payload":{"id":"codex-demo","session_id":"codex-demo","cwd":` + quoteJSON(repo) + `,"source":"cli"}}` + "\n" +
		`{"timestamp":"2026-08-14T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var uploadRequests int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"id": "user-1", "github_login": "octocat"})
	})
	mux.HandleFunc("GET /github/installations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"installations": []any{map[string]any{"repositories": []any{
			map[string]any{"id": "repo-1", "full_name": "acme/demo", "enabled": true},
		}}}})
	})
	mux.HandleFunc("POST /github/repositories/repo-1/sync", func(w http.ResponseWriter, r *http.Request) {
		if err := os.Remove(rollout); err != nil {
			t.Fatalf("remove rollout before upload: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /github/repositories/repo-1/trace-uploads", func(w http.ResponseWriter, r *http.Request) {
		uploadRequests++
		w.WriteHeader(http.StatusAccepted)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)

	stdout, stderr, err := runCLI(t, "setup", "--all", "--sync", "--upload", "--yes")
	if err != nil {
		t.Fatalf("error = %v (stdout=%q stderr=%q)", err, stdout, stderr)
	}
	if uploadRequests != 0 {
		t.Fatalf("uploads = %d, want 0", uploadRequests)
	}
	if !strings.Contains(stdout, "No agent sessions found.") || !strings.Contains(stdout, "1 succeeded, 0 failed") {
		t.Fatalf("stdout=%q", stdout)
	}
	if strings.Contains(stderr, "setup failed") {
		t.Fatalf("stderr=%q", stderr)
	}
	if complete, _ := repositorySetupComplete(t.Context(), repo); !complete {
		t.Error("hooks setup not recorded")
	}
}

func TestWriteSetupPlanAlignsColumnsWithoutPaddingAfterPath(t *testing.T) {
	rows := []setupPlanRow{
		{repository: apiclient.GitHubRepository{FullName: "acme/short"}, local: collector.Repository{Root: "/a/very/long/path/to/a/repository", SessionCount: 12}, state: "ready"},
		{repository: apiclient.GitHubRepository{FullName: "acme/much-longer-name"}, state: "already set up"},
	}
	var output strings.Builder
	if err := writeSetupPlan(&output, rows, false); err != nil {
		t.Fatal(err)
	}
	want := "REPOSITORY             STATE           LOCAL PATH\n" +
		"acme/short             ready           /a/very/long/path/to/a/repository\n" +
		"acme/much-longer-name  already set up  -\n"
	if output.String() != want {
		t.Fatalf("table:\n%s\nwant:\n%s", output.String(), want)
	}
}

func TestWriteSetupPlanShowsSessionsWhenUploading(t *testing.T) {
	rows := []setupPlanRow{
		{repository: apiclient.GitHubRepository{FullName: "acme/short"}, local: collector.Repository{Root: "/a", SessionCount: 12}, state: "ready"},
	}
	var output strings.Builder
	if err := writeSetupPlan(&output, rows, true); err != nil {
		t.Fatal(err)
	}
	want := "REPOSITORY  STATE  SESSIONS  LOCAL PATH\n" +
		"acme/short  ready  12        /a\n"
	if output.String() != want {
		t.Fatalf("table:\n%s\nwant:\n%s", output.String(), want)
	}
}

func TestSetupSyncFailureLeavesHooksInstalled(t *testing.T) {
	stub := workingEntire(t)
	addStubAgent(t, stub.dir, "claude")
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	repo := filepath.Join(t.TempDir(), "demo")
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/acme/missing.git")
	t.Chdir(repo)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /github/repositories/find", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "repository not found"})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)

	_, _, err := runCLI(t, "setup", "--sync", "--yes")
	if err == nil || !strings.Contains(err.Error(), "hooks are set up") {
		t.Fatalf("error = %v, want sync failure that mentions installed hooks", err)
	}
	if _, statErr := os.Stat(stub.calls); statErr != nil {
		t.Fatalf("Entire did not run before the server lookup: %v", statErr)
	}
	if complete, _ := repositorySetupComplete(t.Context(), repo); !complete {
		t.Fatal("hooks were not recorded before the server lookup failed")
	}
}

// failingServer fails the test on any request and returns its URL.
func failingServer(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func newGitHubRepo(t *testing.T, parent, name string) string {
	t.Helper()
	repo := filepath.Join(parent, name)
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/acme/"+name+".git")
	return repo
}

// workingEntire stubs an Entire that succeeds and remembers it was enabled, so
// a second setup finds every detected agent already configured.
func workingEntire(t *testing.T) entireStub {
	t.Helper()
	stub := stubEntire(t, `
state="$ENTIRE_TEST_CALLS.enabled"
case "$1" in
version) echo "entire version 0.9.0" ;;
status)
	if [ -f "$state" ]; then
		printf '%s\n' '{"enabled":true,"agents":["Claude Code","Codex","Cursor","Gemini CLI","OpenCode","Pi","Copilot CLI","Factory AI Droid"]}'
	else
		echo "not enabled" >&2
		exit 1
	fi ;;
enable) touch "$state" ;;
esac
`)
	addStubAgent(t, stub.dir, "claude")
	return stub
}

// snapshotTree maps every file below dir (skipping .git) to its contents.
func snapshotTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		files[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestSetupWithoutCredentialsNeverContactsTheServer(t *testing.T) {
	stub := workingEntire(t)
	addStubAgent(t, stub.dir, "claude")
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "")
	t.Setenv(serverEnvVar, failingServer(t))
	repo := newGitHubRepo(t, t.TempDir(), "demo")
	t.Chdir(repo)

	stdout, _, err := runCLI(t, "setup")
	if err != nil {
		t.Fatalf("setup: %v (stdout=%q)", err, stdout)
	}
	complete, err := repositorySetupComplete(t.Context(), repo)
	if err != nil || !complete {
		t.Fatalf("complete=%v error=%v", complete, err)
	}
	callsAfterFirst := readCalls(t, stub.calls)
	if len(callsAfterFirst) == 0 {
		t.Fatal("Entire was not configured")
	}
	marker, err := repositorySetupMarker(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	markerBefore, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	repoBefore := snapshotTree(t, repo)
	homeBefore := snapshotTree(t, os.Getenv("HOME"))

	if _, _, err := runCLI(t, "setup"); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	markerAfter, err := os.ReadFile(marker)
	if err != nil || string(markerAfter) != string(markerBefore) {
		t.Fatalf("marker changed: %q -> %q (%v)", markerBefore, markerAfter, err)
	}
	if !maps.Equal(repoBefore, snapshotTree(t, repo)) {
		t.Fatal("rerun changed files in the repository")
	}
	if !maps.Equal(homeBefore, snapshotTree(t, os.Getenv("HOME"))) {
		t.Fatal("rerun changed files in HOME")
	}
	for _, call := range readCalls(t, stub.calls)[len(callsAfterFirst):] {
		args := call[strings.Index(call, "|")+1:]
		if strings.HasPrefix(args, "enable") || strings.HasPrefix(args, "configure") || strings.Contains(args, "add") {
			t.Errorf("rerun added Entire hooks again: %q", call)
		}
	}
}

func TestSetupMarkerRecordsOnlySuccessfulEntireSetup(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "")
	t.Setenv(serverEnvVar, failingServer(t))
	workspace := t.TempDir()
	repo := newGitHubRepo(t, workspace, "demo")
	t.Chdir(workspace)

	// Entire setup fails: warn, no marker, and --all offers the repository again.
	stubEntire(t, "exit 1\n")
	stdout, stderr, err := runCLI(t, "setup", "--all", "--yes")
	if err != nil || !strings.Contains(stderr, "entire setup failed") {
		t.Fatalf("failing entire: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if complete, _ := repositorySetupComplete(t.Context(), repo); complete {
		t.Fatal("marker written although Entire setup failed")
	}
	stdout, _, err = runCLI(t, "setup", "--all", "--yes")
	if err != nil || strings.Contains(stdout, "already set up") || strings.Contains(stdout, "No repositories need setup.") {
		t.Fatalf("rerun after failure: stdout=%q err=%v", stdout, err)
	}

	// --skip-entire never writes the marker either.
	if _, _, err := runCLI(t, "setup", "--skip-entire", repo); err != nil {
		t.Fatal(err)
	}
	if complete, _ := repositorySetupComplete(t.Context(), repo); complete {
		t.Fatal("marker written with --skip-entire")
	}

	// A working Entire writes it, and --all then reports the repository done.
	workingEntire(t)
	if _, _, err := runCLI(t, "setup", "--all", "--yes"); err != nil {
		t.Fatal(err)
	}
	if complete, _ := repositorySetupComplete(t.Context(), repo); !complete {
		t.Fatal("marker missing after a successful Entire setup")
	}
	stdout, _, err = runCLI(t, "setup", "--all", "--yes")
	if err != nil || !strings.Contains(stdout, "No repositories need setup.") {
		t.Fatalf("rerun after success: stdout=%q err=%v", stdout, err)
	}
}

func TestSetupWithoutACodingAgentDoesNotMarkTheRepositoryComplete(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "")
	t.Setenv(serverEnvVar, failingServer(t))
	workspace := t.TempDir()
	repo := newGitHubRepo(t, workspace, "demo")
	t.Chdir(workspace)

	// An Entire that works but no agent binary on PATH or in HOME.
	stubEntire(t, `
case "$1" in
version) echo "entire version 0.9.0" ;;
esac
`)
	stdout, _, err := runCLI(t, "setup", repo)
	if err != nil || !strings.Contains(stdout, "No supported coding agents detected") {
		t.Fatalf("setup: stdout=%q err=%v", stdout, err)
	}
	if complete, _ := repositorySetupComplete(t.Context(), repo); complete {
		t.Fatal("marker written although no hooks were configured")
	}
	stdout, _, err = runCLI(t, "setup", "--all", "--yes")
	if err != nil || strings.Contains(stdout, "No repositories need setup.") || strings.Contains(stdout, "already set up") {
		t.Fatalf("--all after setup without agents: stdout=%q err=%v", stdout, err)
	}
}

func TestSetupRequiresGitHubOrigin(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(serverEnvVar, failingServer(t))
	repo := filepath.Join(t.TempDir(), "plain")
	runGitTest(t, repo, "init")
	t.Chdir(repo)
	if _, _, err := runCLI(t, "setup", "--skip-entire"); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("error = %v, want origin error", err)
	}
	if complete, _ := repositorySetupComplete(t.Context(), repo); complete {
		t.Fatal("setup marked a repository without a GitHub origin complete")
	}
}

func TestSetupPathsNeverContactTheServer(t *testing.T) {
	workingEntire(t)
	setTempConfigHome(t)
	t.Setenv(serverEnvVar, failingServer(t))
	parent := t.TempDir()
	one := newGitHubRepo(t, parent, "one")
	two := newGitHubRepo(t, parent, "two")
	t.Chdir(t.TempDir())

	_, _, err := runCLI(t, "setup", one)
	if err != nil {
		t.Fatalf("setup PATH: %v", err)
	}
	if complete, _ := repositorySetupComplete(t.Context(), one); !complete {
		t.Fatal("one is not complete")
	}
	stdout, _, err := runCLI(t, "setup", one, two, filepath.Join(parent, "missing"))
	if err == nil {
		t.Fatalf("missing path accepted: stdout=%q", stdout)
	}
	stdout, stderr, err := runCLI(t, "setup", one, two)
	if err != nil || !strings.Contains(stdout, "2 succeeded, 0 failed") {
		t.Fatalf("setup PATH PATH: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if complete, _ := repositorySetupComplete(t.Context(), two); !complete {
		t.Fatal("two is not complete")
	}
}

func TestSetupAllDiscoversCheckoutsLocallyWithoutTheServer(t *testing.T) {
	workingEntire(t)
	setTempConfigHome(t)
	t.Setenv(serverEnvVar, failingServer(t))
	workspace := t.TempDir()
	one := newGitHubRepo(t, workspace, "one")
	nested := newGitHubRepo(t, filepath.Join(workspace, "group", "deeper"), "two")
	newGitHubRepo(t, filepath.Join(workspace, "node_modules", "pkg"), "ignored")
	newGitHubRepo(t, filepath.Join(workspace, ".hidden"), "ignored2")
	newGitHubRepo(t, filepath.Join(workspace, "a", "b", "c", "d"), "toodeep")
	noOrigin := filepath.Join(workspace, "plain")
	runGitTest(t, noOrigin, "init")
	// A repository nested inside a repository is not scanned separately.
	newGitHubRepo(t, one, "inner")
	t.Chdir(workspace)

	stdout, stderr, err := runCLI(t, "setup", "--all")
	if err != nil || !strings.Contains(stdout, "2 succeeded, 0 failed") {
		t.Fatalf("setup --all: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if !strings.Contains(stdout, "REPOSITORY  STATE") || strings.Contains(stdout, "ignored") ||
		strings.Contains(stdout, "toodeep") || strings.Contains(stdout, "inner") {
		t.Fatalf("plan: %q", stdout)
	}
	for _, repo := range []string{one, nested} {
		if complete, _ := repositorySetupComplete(t.Context(), repo); !complete {
			t.Errorf("%s not complete", repo)
		}
	}
	if complete, _ := repositorySetupComplete(t.Context(), noOrigin); complete {
		t.Error("checkout without GitHub origin was set up")
	}

	stdout, _, err = runCLI(t, "setup", "--all")
	if err != nil || !strings.Contains(stdout, "No repositories need setup.") || !strings.Contains(stdout, "already set up") {
		t.Fatalf("rerun: stdout=%q err=%v", stdout, err)
	}
}

func TestSetupAllCancelledChangesNothing(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(serverEnvVar, failingServer(t))
	workspace := t.TempDir()
	repo := newGitHubRepo(t, workspace, "one")
	t.Chdir(workspace)
	stdout, _, err := runCLIWithInput(t, "n\n", "setup", "--all", "--skip-entire")
	if err != nil || !strings.Contains(stdout, "Setup cancelled.") {
		t.Fatalf("stdout=%q err=%v", stdout, err)
	}
	if complete, _ := repositorySetupComplete(t.Context(), repo); complete {
		t.Fatal("cancelled setup changed the repository")
	}
}

func TestSetupRejectsPathsWithAll(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(serverEnvVar, failingServer(t))
	repo := newGitHubRepo(t, t.TempDir(), "one")
	if _, _, err := runCLI(t, "setup", "--all", repo); err == nil || !strings.Contains(err.Error(), "--all") {
		t.Fatalf("error = %v", err)
	}
}

func TestSetupSyncAndUploadRequireLogin(t *testing.T) {
	stub := workingEntire(t)
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "")
	var deviceRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/auth/device") {
			deviceRequests++
		} else {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)
	repo := newGitHubRepo(t, t.TempDir(), "demo")
	t.Chdir(repo)

	for _, flag := range []string{"--sync", "--upload"} {
		_, _, err := runCLI(t, "setup", flag, "--yes")
		if err == nil || !strings.Contains(err.Error(), "baum login") {
			t.Fatalf("%s error = %v, want login hint", flag, err)
		}
	}
	t.Chdir(t.TempDir())
	if _, _, err := runCLI(t, "setup", "--all", "--sync", "--yes"); err == nil || !strings.Contains(err.Error(), "baum login") {
		t.Fatalf("--all --sync error = %v", err)
	}
	if deviceRequests != 0 {
		t.Fatalf("device flow started %d times", deviceRequests)
	}
	if _, statErr := os.Stat(stub.calls); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Entire ran without credentials: %v", statErr)
	}
	if complete, _ := repositorySetupComplete(t.Context(), repo); complete {
		t.Fatal("setup changed the repository without credentials")
	}
}

func TestSetupSyncIgnoresTokenIssuedForAnotherServer(t *testing.T) {
	setTempConfigHome(t)
	repo := newGitHubRepo(t, t.TempDir(), "demo")
	t.Chdir(repo)
	if err := saveCredentials(credentials{ServerURL: "http://localhost:5173/api", Token: "rcnt_local", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, "setup", "--sync", "--server", failingServer(t))
	if err == nil || !strings.Contains(err.Error(), "baum login") {
		t.Fatalf("error = %v", err)
	}
}

func TestSetupSyncWithExpiredTokenAsksForLogin(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_invalid")
	repo := newGitHubRepo(t, t.TempDir(), "demo")
	t.Chdir(repo)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
	}))
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)
	_, _, err := runCLI(t, "setup", "--sync", "--skip-entire")
	if err == nil || !strings.Contains(err.Error(), "baum login") {
		t.Fatalf("error = %v", err)
	}
}

func TestSetupUploadWithoutSessionsStillInstallsHooks(t *testing.T) {
	workingEntire(t)
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	repo := newGitHubRepo(t, t.TempDir(), "demo")
	t.Chdir(repo)
	t.Setenv("HOME", t.TempDir())

	var findRequests, syncRequests int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /github/repositories/find", func(w http.ResponseWriter, r *http.Request) {
		findRequests++
		writeJSON(w, http.StatusOK, map[string]any{"id": "repo-1", "full_name": "acme/demo", "enabled": true})
	})
	mux.HandleFunc("POST /github/repositories/repo-1/sync", func(w http.ResponseWriter, r *http.Request) {
		syncRequests++
		w.WriteHeader(http.StatusAccepted)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)

	stdout, _, err := runCLI(t, "setup", "--upload", "--yes")
	if err != nil || !strings.Contains(stdout, "No agent sessions found.") || strings.Contains(stdout, "Requested sync") {
		t.Fatalf("upload: stdout=%q err=%v", stdout, err)
	}
	if findRequests != 0 || syncRequests != 0 {
		t.Errorf("remote operations: find=%d sync=%d", findRequests, syncRequests)
	}
	if complete, _ := repositorySetupComplete(t.Context(), repo); !complete {
		t.Error("hooks setup not recorded")
	}
	stdout, _, err = runCLI(t, "setup", "--sync")
	if err != nil || findRequests != 1 || syncRequests != 1 || !strings.Contains(stdout, "Requested sync for acme/demo.") {
		t.Fatalf("sync: find=%d sync=%d stdout=%q err=%v", findRequests, syncRequests, stdout, err)
	}
}

func TestSetupAllWithSyncSkipsUnlinkedRepositoriesButSetsUpHooks(t *testing.T) {
	workingEntire(t)
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	linked := newGitHubRepo(t, workspace, "linked")
	unlinked := newGitHubRepo(t, workspace, "unlinked")
	t.Chdir(workspace)

	var syncs []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /github/installations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"installations": []any{map[string]any{"repositories": []any{
			map[string]any{"id": "repo-1", "full_name": "acme/linked", "enabled": true},
		}}}})
	})
	mux.HandleFunc("POST /github/repositories/repo-1/sync", func(w http.ResponseWriter, r *http.Request) {
		syncs = append(syncs, "repo-1")
		w.WriteHeader(http.StatusAccepted)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)

	stdout, stderr, err := runCLI(t, "setup", "--all", "--sync", "--yes")
	if err != nil || !strings.Contains(stdout, "2 succeeded, 0 failed") || len(syncs) != 1 {
		t.Fatalf("stdout=%q stderr=%q err=%v syncs=%v", stdout, stderr, err, syncs)
	}
	for _, repo := range []string{linked, unlinked} {
		if complete, _ := repositorySetupComplete(t.Context(), repo); !complete {
			t.Errorf("%s not complete", repo)
		}
	}
}

func TestSetupPreviousPathRequiresUpload(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(serverEnvVar, failingServer(t))
	repo := newGitHubRepo(t, t.TempDir(), "demo")
	t.Chdir(repo)
	_, _, err := runCLI(t, "setup", "--skip-entire", "--previous-path", filepath.Join(t.TempDir(), "old"))
	if err == nil || !strings.Contains(err.Error(), "--upload") {
		t.Fatalf("error = %v", err)
	}
}

func TestRepositorySetupCompletionIsScopedToWorktree(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "demo")
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "config", "user.name", "Test User")
	runGitTest(t, repo, "config", "user.email", "test@example.com")
	runGitTest(t, repo, "config", "commit.gpgsign", "false")
	runGitTest(t, repo, "commit", "--allow-empty", "-m", "initial")
	worktree := filepath.Join(t.TempDir(), "worktree")
	runGitTest(t, repo, "worktree", "add", "-b", "other", worktree)

	if err := markRepositorySetupComplete(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	mainComplete, err := repositorySetupComplete(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	worktreeComplete, err := repositorySetupComplete(t.Context(), worktree)
	if err != nil {
		t.Fatal(err)
	}
	if !mainComplete || worktreeComplete {
		t.Fatalf("setup completion: main=%v worktree=%v", mainComplete, worktreeComplete)
	}
}

func TestSetupAllRefusesTheFilesystemRoot(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(serverEnvVar, failingServer(t))
	t.Chdir("/")
	_, _, err := runCLI(t, "setup", "--all", "--yes")
	if err == nil || !strings.Contains(err.Error(), "filesystem root") {
		t.Fatalf("error = %v, want filesystem root refusal", err)
	}
}

func TestSetupAllFromHomeSkipsUserDataFolders(t *testing.T) {
	workingEntire(t)
	setTempConfigHome(t)
	t.Setenv(serverEnvVar, failingServer(t))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	kept := newGitHubRepo(t, filepath.Join(home, "code"), "kept")
	skipped := []string{
		newGitHubRepo(t, filepath.Join(home, "Documents"), "docs"),
		newGitHubRepo(t, filepath.Join(home, "Downloads"), "downloads"),
		newGitHubRepo(t, filepath.Join(home, "Google Drive"), "drive"),
		newGitHubRepo(t, filepath.Join(home, "Dropbox"), "dropbox"),
		newGitHubRepo(t, filepath.Join(home, "OneDrive - Acme"), "onedrive"),
	}

	stdout, stderr, err := runCLI(t, "setup", "--all", "--yes")
	if err != nil || !strings.Contains(stdout, "1 succeeded, 0 failed") {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if !strings.Contains(stdout, "Scanned "+canonicalPath(home)) || !strings.Contains(stdout, "(depth 3) and recorded sessions.") {
		t.Fatalf("plan does not name the scanned root: %q", stdout)
	}
	if complete, _ := repositorySetupComplete(t.Context(), kept); !complete {
		t.Error("project directory under HOME was not scanned")
	}
	for _, repo := range skipped {
		if complete, _ := repositorySetupComplete(t.Context(), repo); complete {
			t.Errorf("%s was scanned although it is a user-data folder", repo)
		}
	}
}

func TestSetupAllScansUserDataFoldersOutsideHome(t *testing.T) {
	workingEntire(t)
	setTempConfigHome(t)
	t.Setenv(serverEnvVar, failingServer(t))
	workspace := t.TempDir()
	repo := newGitHubRepo(t, filepath.Join(workspace, "Documents"), "docs")
	t.Chdir(workspace)
	if _, _, err := runCLI(t, "setup", "--all", "--yes"); err != nil {
		t.Fatal(err)
	}
	if complete, _ := repositorySetupComplete(t.Context(), repo); !complete {
		t.Error("a folder named Documents below a non-home root must be scanned")
	}
}

func TestSetupDedupesCheckoutsReachedThroughSymlinks(t *testing.T) {
	stub := workingEntire(t)
	addStubAgent(t, stub.dir, "claude")
	setTempConfigHome(t)
	t.Setenv(serverEnvVar, failingServer(t))
	workspace := t.TempDir()
	repo := newGitHubRepo(t, workspace, "demo")
	link := filepath.Join(workspace, "alias")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	// Session metadata names the checkout through the symlink.
	rollout := filepath.Join(os.Getenv("HOME"), ".codex", "sessions", "2026", "08", "14", "rollout-alias.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-08-14T12:00:00Z","type":"session_meta","payload":{"id":"codex-alias","session_id":"codex-alias","cwd":` + quoteJSON(link) + `,"source":"cli"}}` + "\n" +
		`{"timestamp":"2026-08-14T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(workspace)
	stdout, stderr, err := runCLI(t, "setup", "--all", "--yes")
	if err != nil || !strings.Contains(stdout, "1 succeeded, 0 failed") {
		t.Fatalf("--all:stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if got := strings.Count(stdout, "acme/demo"); got != 2 { // plan row and "Setting up"
		t.Errorf("--all listed the repository %d times:\n%s", got, stdout)
	}

	t.Chdir(t.TempDir())
	stdout, stderr, err = runCLI(t, "setup", repo, link)
	if err != nil || strings.Contains(stdout, "Setup complete") || strings.Count(stdout, "Setting up") != 0 {
		t.Fatalf("PATH PATH: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
}

func TestSetupAllDuplicateClonesSyncAndInventoryOncePerRepository(t *testing.T) {
	workingEntire(t)
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	first := newGitHubRepo(t, filepath.Join(workspace, "a"), "demo")
	second := newGitHubRepo(t, filepath.Join(workspace, "b"), "demo")
	t.Chdir(workspace)
	for i, repo := range []string{first, second} {
		rollout := filepath.Join(home, ".codex", "sessions", "2026", "08", "14", fmt.Sprintf("rollout-%d.jsonl", i))
		if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
			t.Fatal(err)
		}
		content := `{"timestamp":"2026-08-14T12:00:00Z","type":"session_meta","payload":{"id":"codex-` + fmt.Sprint(i) + `","session_id":"codex-` + fmt.Sprint(i) + `","cwd":` + quoteJSON(repo) + `,"source":"cli"}}` + "\n" +
			`{"timestamp":"2026-08-14T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}` + "\n"
		if err := os.WriteFile(rollout, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var syncs int
	var inventories [][]traceupload.RepositoryRoots
	mux := http.NewServeMux()
	mux.HandleFunc("GET /github/installations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"installations": []any{map[string]any{"repositories": []any{
			map[string]any{"id": "repo-1", "full_name": "acme/demo", "enabled": true},
		}}}})
	})
	mux.HandleFunc("POST /github/repositories/repo-1/sync", func(w http.ResponseWriter, r *http.Request) {
		syncs++
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /github/repositories/repo-1/trace-uploads", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		inventories = append(inventories, uploadedManifest(t, body).RepositoryRoots)
		w.WriteHeader(http.StatusAccepted)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)

	stdout, stderr, err := runCLI(t, "setup", "--all", "--sync", "--upload", "--yes")
	if err != nil {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if syncs != 1 {
		t.Errorf("syncs = %d, want 1", syncs)
	}
	for _, repo := range []string{first, second} {
		if complete, _ := repositorySetupComplete(t.Context(), repo); !complete {
			t.Errorf("%s hooks not set up", repo)
		}
	}
	if len(inventories) == 0 {
		t.Fatal("nothing uploaded")
	}
	for _, inventory := range inventories {
		if len(inventory) != 1 || inventory[0].ID != "repo-1" {
			t.Fatalf("inventory = %+v, want one entry for repo-1", inventory)
		}
	}
	if !strings.Contains(stdout, "SESSIONS") || !strings.Contains(stdout, "Sessions will be uploaded for 1 repository") {
		t.Errorf("plan lacks session counts: %q", stdout)
	}
}
