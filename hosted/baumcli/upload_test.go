//go:build !windows

package baumcli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/hosted/traceupload"
	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/vendors"
)

// fakeGitHubToken has the shape of a GitHub token, so redaction detects it;
// it is split so secret scanners do not take the source for a leak.
const fakeGitHubToken = "ghp_" + "JTeKdNnFRIBXuDL7DxtpYlSXpfKtHF4vUCsM"

func TestNormalizeGitHubRemote(t *testing.T) {
	tests := map[string]string{
		"https://github.com/Acme/demo.git":       "Acme/demo",
		"git@github.com:acme/demo.git":           "acme/demo",
		"ssh://git@ssh.github.com/acme/demo.git": "acme/demo",
	}
	for remote, want := range tests {
		got, err := normalizeGitHubRemote(remote)
		if err != nil || got != want {
			t.Errorf("normalizeGitHubRemote(%q) = %q, %v; want %q", remote, got, err, want)
		}
	}
	for _, remote := range []string{"/tmp/repo", "git@example.com:acme/demo.git", "https://github.com/acme"} {
		if _, err := normalizeGitHubRemote(remote); err == nil {
			t.Errorf("normalizeGitHubRemote(%q) succeeded", remote)
		}
	}
}

func TestUploadEndToEnd(t *testing.T) {
	setTempConfigHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(t.TempDir(), "demo")
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/acme/demo.git")
	rollout := filepath.Join(home, ".codex", "sessions", "2026", "07", "22", "rollout-test.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatal(err)
	}
	secret := fakeGitHubToken
	content := `{"timestamp":"2026-07-22T12:00:00Z","type":"session_meta","payload":{"id":"codex-1","session_id":"codex-1","cwd":` + quoteJSON(repo) + `,"source":"cli"}}` + "\n" +
		`{"timestamp":"2026-07-22T12:00:05Z","type":"event_msg","payload":{"type":"user_message","message":"` + secret + `"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	modifiedAt := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(rollout, modifiedAt, modifiedAt); err != nil {
		t.Fatal(err)
	}

	var uploaded []byte
	mux := http.NewServeMux()
	mux.HandleFunc("GET /github/repositories/find", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rcnt_upload" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("full_name") != "acme/demo" {
			t.Errorf("full_name = %q", r.URL.Query().Get("full_name"))
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "repo-1", "full_name": "acme/demo", "enabled": true})
	})
	mux.HandleFunc("POST /github/repositories/repo-1/trace-uploads", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/gzip" || r.ContentLength <= 0 {
			t.Errorf("upload headers: content-type=%q length=%d", r.Header.Get("Content-Type"), r.ContentLength)
		}
		uploaded, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if err := saveCredentials(credentials{ServerURL: srv.URL, Token: "rcnt_upload"}); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCLI(t, "upload", repo)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if !strings.Contains(stdout, "Found 1 agent session:") ||
		!strings.Contains(stdout, "  - Codex: 1") ||
		!strings.Contains(stdout, "Continue? [y/N]") ||
		!strings.Contains(stdout, "Uploading 1 session for acme/demo") ||
		!strings.Contains(stdout, "Uploaded 1 session") {
		t.Errorf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "Preparing 1 session for upload...") ||
		!strings.Contains(stderr, "Prepared 1 session for upload.") {
		t.Errorf("stderr = %q", stderr)
	}
	if len(uploaded) == 0 || strings.Contains(string(uploaded), secret) {
		t.Errorf("uploaded archive missing or contains plaintext secret")
	}

	// There is one archive format, and the CLI emits it: schema version "1",
	// with each session a manifest-indexed set of logical files.
	manifest := uploadedManifest(t, uploaded)
	if manifest.SchemaVersion != traceupload.ManifestVersion {
		t.Errorf("schema_version = %q, want %q", manifest.SchemaVersion, traceupload.ManifestVersion)
	}
	if len(manifest.Sessions) != 1 || len(manifest.Sessions[0].Files) != 1 {
		t.Fatalf("manifest sessions = %+v", manifest.Sessions)
	}
	file := manifest.Sessions[0].Files[0]
	if file.Name != "rollout-test.jsonl" || file.Kind != model.KindTranscript || !file.Root ||
		file.Member != "sessions/000001/files/000001" {
		t.Errorf("manifest file = %+v", file)
	}
	if strings.Contains(string(uploaded), rollout) {
		t.Error("the archive names the uploader's local path")
	}
}

// An archive that unpacks past the server's limit would be accepted and then
// fail in the worker, so it is refused before anything is sent, and the CLI
// never reports it uploaded.
func TestUploadRefusesAnArchiveOverTheServerLimit(t *testing.T) {
	setTempConfigHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(t.TempDir(), "demo")
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/acme/demo.git")
	rollout := filepath.Join(home, ".codex", "sessions", "2026", "07", "22", "rollout-test.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-07-22T12:00:00Z","type":"session_meta","payload":{"id":"codex-1","session_id":"codex-1","cwd":` + quoteJSON(repo) + `,"source":"cli"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := maxUnpackedBytes
	maxUnpackedBytes = 1024
	t.Cleanup(func() { maxUnpackedBytes = previous })

	mux := http.NewServeMux()
	mux.HandleFunc("GET /github/repositories/find", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "repo-1", "full_name": "acme/demo", "enabled": true})
	})
	mux.HandleFunc("POST /github/repositories/repo-1/trace-uploads", func(w http.ResponseWriter, r *http.Request) {
		t.Error("an archive over the limit was uploaded")
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if err := saveCredentials(credentials{ServerURL: srv.URL, Token: "rcnt_upload"}); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runCLI(t, "upload", "--yes", repo)
	if err == nil || !strings.Contains(err.Error(), "over the 1.0 KiB the server accepts in one upload; nothing was uploaded") {
		t.Fatalf("upload error = %v, want the archive refused before upload", err)
	}
	if strings.Contains(stdout, "Uploading") || strings.Contains(stdout, "Uploaded") {
		t.Errorf("stdout = %q, want no upload reported", stdout)
	}
}

// uploadedManifest reads manifest.json out of an uploaded archive. It must be
// the first member: the server decodes it before accepting any content.
func uploadedManifest(t *testing.T, archive []byte) traceupload.Manifest {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	header, err := tar.NewReader(gz).Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "manifest.json" {
		t.Fatalf("first archive member = %q, want manifest.json", header.Name)
	}
	gz2, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz2.Close() }()
	tr := tar.NewReader(gz2)
	if _, err := tr.Next(); err != nil {
		t.Fatal(err)
	}
	var manifest traceupload.Manifest
	if err := json.NewDecoder(tr).Decode(&manifest); err != nil {
		t.Fatalf("decoding manifest: %v", err)
	}
	return manifest
}

func TestCancelledUploadLeavesNoStagingDirectory(t *testing.T) {
	setTempConfigHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	repo := filepath.Join(t.TempDir(), "demo")
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/acme/demo.git")
	rollout := filepath.Join(home, ".codex", "sessions", "2026", "07", "22", "rollout-test.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-07-22T12:00:00Z","type":"session_meta","payload":{"id":"codex-1","session_id":"codex-1","cwd":` + quoteJSON(repo) + `,"source":"cli"}}` + "\n" +
		`{"timestamp":"2026-07-22T12:00:05Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// The interrupt arrives while the archive is being uploaded.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /github/repositories/find", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "repo-1", "full_name": "acme/demo", "enabled": true})
	})
	mux.HandleFunc("POST /github/repositories/repo-1/trace-uploads", func(w http.ResponseWriter, r *http.Request) {
		cancel()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	// The handler never sees the disconnect while the request body is unread,
	// so it holds the request open until the test ends. Cleanups run last
	// first, so this releases it before srv.Close waits for it.
	t.Cleanup(func() { close(release) })
	if err := saveCredentials(credentials{ServerURL: srv.URL, Token: "rcnt_upload"}); err != nil {
		t.Fatal(err)
	}

	var out, errOut strings.Builder
	err := runContext(ctx, []string{"upload", "--yes", repo}, strings.NewReader(""), &out, &errOut)
	if err == nil {
		t.Fatal("cancelled upload succeeded")
	}
	leftovers, _ := filepath.Glob(filepath.Join(tmp, "baum-trace-upload-*"))
	if len(leftovers) != 0 {
		t.Errorf("staging directories left behind: %v", leftovers)
	}
}

func TestUploadDeclinedDoesNotRequireLogin(t *testing.T) {
	setTempConfigHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(t.TempDir(), "demo")
	runGitTest(t, repo, "init")
	rollout := filepath.Join(home, ".codex", "sessions", "2026", "07", "22", "rollout-test.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-07-22T12:00:00Z","type":"session_meta","payload":{"id":"codex-1","session_id":"codex-1","cwd":` + quoteJSON(repo) + `,"source":"cli"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCLIWithInput(t, "no\n", "upload", repo)
	if err != nil {
		t.Fatalf("declining upload: %v", err)
	}
	if !strings.Contains(stdout, "Found 1 agent session:") ||
		!strings.Contains(stdout, "Continue? [y/N] \nUpload cancelled.") {
		t.Errorf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q", stderr)
	}
}

// TestUploadFailsWhenNoSessionCanBeRead: a store whose rollouts this build
// cannot decode is not an empty store, and reporting it as one hides every
// session the user has.
func TestUploadFailsWhenNoSessionCanBeRead(t *testing.T) {
	setTempConfigHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(t.TempDir(), "demo")
	runGitTest(t, repo, "init")
	rollout := filepath.Join(home, ".codex", "sessions", "2026", "07", "22", "rollout-test.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatal(err)
	}
	// A rollout whose first record the decoder rejects: no timestamp.
	content := `{"type":"session_meta","payload":{"id":"codex-1","cwd":` + quoteJSON(repo) + `}}` + "\n"
	if err := os.WriteFile(rollout, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runCLI(t, "upload", repo)
	if err == nil {
		t.Fatalf("upload succeeded with an unreadable store: stdout=%q", stdout)
	}
	if !strings.Contains(err.Error(), "no readable agent sessions") || !strings.Contains(err.Error(), rollout) {
		t.Errorf("error = %v", err)
	}
	if strings.Contains(stdout, "No agent sessions found.") {
		t.Errorf("unreadable store reported as empty: %q", stdout)
	}
}

func TestUploadAllIsolatesRepositoryFailures(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	home := t.TempDir()
	t.Setenv("HOME", home)
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
	mux.HandleFunc("GET /github/repositories/find", func(w http.ResponseWriter, r *http.Request) {
		fullName := r.URL.Query().Get("full_name")
		if fullName == "acme/two" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": "repo-" + fullName[strings.LastIndex(fullName, "/")+1:], "full_name": fullName, "enabled": true})
	})
	for _, name := range []string{"one", "three"} {
		id, repoName := "repo-"+name, name
		mux.HandleFunc("POST /github/repositories/"+id+"/trace-uploads", func(w http.ResponseWriter, r *http.Request) {
			uploaded = append(uploaded, repoName)
			body, _ := io.ReadAll(r.Body)
			inventories[repoName] = uploadedManifest(t, body).RepositoryRoots
			w.WriteHeader(http.StatusAccepted)
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)

	stdout, stderr, err := runCLI(t, "upload", "--all", "--yes")
	if err == nil || !strings.Contains(err.Error(), "1 repository") {
		t.Fatalf("error = %v", err)
	}
	sort.Strings(uploaded)
	if strings.Join(uploaded, ",") != "one,three" {
		t.Fatalf("uploads = %v", uploaded)
	}
	for _, name := range uploaded {
		if inventories[name] != nil {
			t.Fatalf("%s partial upload inventory = %+v, want omitted", name, inventories[name])
		}
	}
	if !strings.Contains(stderr, "upload failed for acme/two") || !strings.Contains(stdout, "2 succeeded, 0 skipped, 1 failed") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	if !strings.Contains(stdout, "REPOSITORY") || !strings.Contains(stdout, "acme/one") ||
		!strings.Contains(stdout, "acme/two") || !strings.Contains(stdout, "acme/three") {
		t.Fatalf("plan missing from stdout: %q", stdout)
	}
}

func TestUploadAllRejectsDirectoryArgument(t *testing.T) {
	setTempConfigHome(t)
	if _, _, err := runCLI(t, "upload", "--all", "some/dir"); err == nil || !strings.Contains(err.Error(), "--all does not take a directory argument") {
		t.Fatalf("error = %v", err)
	}
}

func TestUploadAllMutuallyExclusiveWithPreviousPath(t *testing.T) {
	setTempConfigHome(t)
	if _, _, err := runCLI(t, "upload", "--all", "--previous-path", "/tmp/old"); err == nil || !strings.Contains(err.Error(), "none of the others can be") {
		t.Fatalf("error = %v", err)
	}
}

func TestUploadAllWithNoLocalRepositoriesDoesNotRequireLogin(t *testing.T) {
	setTempConfigHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	stdout, _, err := runCLI(t, "upload", "--all")
	if err != nil || stdout != "No agent sessions found.\n" {
		t.Fatalf("upload --all with no sessions: stdout=%q err=%v", stdout, err)
	}
}

func TestWriteSkippedSessionFiles(t *testing.T) {
	var report collector.ScanReport
	report.Add("/store/rollout-a.jsonl", "line 1 has invalid timestamp")
	var output strings.Builder
	writeSkippedSessionFiles(&output, report)
	want := "Skipped 1 unreadable session file:\n  - /store/rollout-a.jsonl: line 1 has invalid timestamp\n"
	if output.String() != want {
		t.Errorf("output = %q, want %q", output.String(), want)
	}

	output.Reset()
	writeSkippedSessionFiles(&output, collector.ScanReport{})
	if output.String() != "" {
		t.Errorf("empty report wrote %q", output.String())
	}
}

func TestUploadWithNoSessionsDoesNotRequireLogin(t *testing.T) {
	setTempConfigHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runCLI(t, "upload", repo)
	if err != nil || stdout != "No agent sessions found.\n" {
		t.Fatalf("upload with no sessions: stdout=%q err=%v", stdout, err)
	}
}

func TestWriteUploadSessionSummary(t *testing.T) {
	sessions := []collector.Session{
		{Source: vendors.Codex, ModifiedAt: time.Date(2026, time.April, 1, 12, 0, 0, 0, time.UTC)},
		{Source: vendors.ClaudeCode, ModifiedAt: time.Date(2026, time.January, 4, 12, 0, 0, 0, time.UTC)},
		{Source: vendors.Codex, ModifiedAt: time.Date(2026, time.May, 7, 12, 0, 0, 0, time.UTC)},
		{Source: vendors.Cursor, ModifiedAt: time.Date(2026, time.March, 2, 12, 0, 0, 0, time.UTC)},
	}
	var output strings.Builder
	writeUploadSessionSummaryWithLayout(&output, sessions, "02/01/2006")
	want := "Found 4 agent sessions:\n" +
		"  - Codex: 2 (01/04/2026 - 07/05/2026)\n" +
		"  - Claude Code: 1 (04/01/2026)\n" +
		"  - Cursor: 1 (02/03/2026)\n"
	if output.String() != want {
		t.Errorf("summary = %q, want %q", output.String(), want)
	}
}

func TestDateLayoutForLocale(t *testing.T) {
	tests := map[string]string{
		"en_US.UTF-8": "01/02/2006",
		"en_GB.UTF-8": "02/01/2006",
		"de_DE.UTF-8": "02.01.2006",
		"ja_JP.UTF-8": "2006/01/02",
		"C.UTF-8":     time.DateOnly,
	}
	for locale, want := range tests {
		if got := dateLayoutForLocale(locale); got != want {
			t.Errorf("dateLayoutForLocale(%q) = %q, want %q", locale, got, want)
		}
	}
}

func TestUserDateLayout(t *testing.T) {
	originalGetUserLocale := getUserLocale
	t.Cleanup(func() { getUserLocale = originalGetUserLocale })

	getUserLocale = func() (string, error) { return "en-US", nil }
	if got := userDateLayout(); got != "01/02/2006" {
		t.Errorf("US date layout = %q", got)
	}
	getUserLocale = func() (string, error) { return "", errors.New("locale unavailable") }
	if got := userDateLayout(); got != time.DateOnly {
		t.Errorf("fallback date layout = %q", got)
	}
}

func TestConfirmUpload(t *testing.T) {
	var output strings.Builder
	confirmed, err := confirmUpload(strings.NewReader("maybe\nyes\n"), &output)
	if err != nil || !confirmed {
		t.Fatalf("confirmUpload = %v, %v; want true, nil", confirmed, err)
	}
	if !strings.Contains(output.String(), "Please answer yes or no.") {
		t.Errorf("output = %q", output.String())
	}

	output.Reset()
	confirmed, err = confirmUpload(strings.NewReader("\n"), &output)
	if err != nil || confirmed {
		t.Fatalf("default confirmation = %v, %v; want false, nil", confirmed, err)
	}
}

func TestConfirmUploadSharesOneReaderAcrossPrompts(t *testing.T) {
	in := newPromptInput(strings.NewReader("y\ny\n"))
	for i := range 2 {
		confirmed, err := confirmUpload(in, io.Discard)
		if err != nil || !confirmed {
			t.Fatalf("prompt %d = %v, %v; want true, nil", i+1, confirmed, err)
		}
	}
}

func TestPreparationProgressForRedirectedOutput(t *testing.T) {
	var output strings.Builder
	progress := newPreparationProgress(&output, 3)
	progress.Start()
	progress.Update(1, 3)
	progress.Update(3, 3)
	progress.Finish()
	want := "Preparing 3 sessions for upload...\nPrepared 3 sessions for upload.\n"
	if output.String() != want {
		t.Errorf("progress = %q, want %q", output.String(), want)
	}
}

func TestPreparationProgressForTerminal(t *testing.T) {
	var output strings.Builder
	progress := &preparationProgress{writer: &output, total: 4, interactive: true}
	progress.Start()
	progress.Update(2, 4)
	progress.Finish()
	if !strings.Contains(output.String(), "[===========>            ] 2/4") ||
		!strings.Contains(output.String(), "[=======================>] 4/4\n") {
		t.Errorf("progress = %q", output.String())
	}
}

func writeCodexRollout(t *testing.T, home, name, cwd string) {
	t.Helper()
	rollout := filepath.Join(home, ".codex", "sessions", "2026", "08", "14", "rollout-"+name+".jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-08-14T12:00:00Z","type":"session_meta","payload":{"id":"codex-` + name + `","session_id":"codex-` + name + `","cwd":` + quoteJSON(cwd) + `,"source":"cli"}}` + "\n" +
		`{"timestamp":"2026-08-14T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUploadSkipsDisabledRepository(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(t.TempDir(), "demo")
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/acme/demo.git")
	writeCodexRollout(t, home, "demo", repo)

	posted := false
	mux := http.NewServeMux()
	mux.HandleFunc("GET /github/repositories/find", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "repo-1", "full_name": "acme/demo", "enabled": false})
	})
	mux.HandleFunc("POST /github/repositories/repo-1/trace-uploads", func(w http.ResponseWriter, r *http.Request) {
		posted = true
		w.WriteHeader(http.StatusAccepted)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)

	stdout, stderr, err := runCLI(t, "upload", repo, "--yes")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if posted || strings.Contains(stderr, "Preparing") {
		t.Errorf("disabled repository was prepared or uploaded: posted=%v stderr=%q", posted, stderr)
	}
	if !strings.Contains(stdout, "Skipping upload for acme/demo: not enabled in baum") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestUploadAllSkipsDisabledRepositories(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"on", "off"} {
		repo := filepath.Join(t.TempDir(), name)
		runGitTest(t, repo, "init")
		runGitTest(t, repo, "remote", "add", "origin", "https://github.com/acme/"+name+".git")
		writeCodexRollout(t, home, name, repo)
	}

	var uploaded []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"id": "user-1", "github_login": "octocat"})
	})
	mux.HandleFunc("GET /github/repositories/find", func(w http.ResponseWriter, r *http.Request) {
		fullName := r.URL.Query().Get("full_name")
		writeJSON(w, http.StatusOK, map[string]any{"id": "repo-" + strings.TrimPrefix(fullName, "acme/"), "full_name": fullName, "enabled": fullName == "acme/on"})
	})
	for _, name := range []string{"on", "off"} {
		mux.HandleFunc("POST /github/repositories/repo-"+name+"/trace-uploads", func(w http.ResponseWriter, r *http.Request) {
			uploaded = append(uploaded, name)
			w.WriteHeader(http.StatusAccepted)
		})
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)

	stdout, _, err := runCLI(t, "upload", "--all", "--yes")
	if err != nil {
		t.Fatalf("upload --all: %v", err)
	}
	if strings.Join(uploaded, ",") != "on" {
		t.Errorf("uploads = %v, want only on", uploaded)
	}
	if !strings.Contains(stdout, "Skipping upload for acme/off") || !strings.Contains(stdout, "1 succeeded, 1 skipped, 0 failed") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestUploadExcludesSessionsFromNestedClones(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(t.TempDir(), "outer")
	runGitTest(t, repo, "init")
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/acme/outer.git")
	inner := filepath.Join(repo, "third_party", "inner")
	runGitTest(t, inner, "init")
	runGitTest(t, inner, "remote", "add", "origin", "https://github.com/acme/inner.git")
	writeCodexRollout(t, home, "outer", repo)
	writeCodexRollout(t, home, "inner", inner)

	var uploaded []byte
	mux := http.NewServeMux()
	mux.HandleFunc("GET /github/repositories/find", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "repo-1", "full_name": "acme/outer", "enabled": true})
	})
	mux.HandleFunc("POST /github/repositories/repo-1/trace-uploads", func(w http.ResponseWriter, r *http.Request) {
		uploaded, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)

	if _, _, err := runCLI(t, "upload", repo, "--yes"); err != nil {
		t.Fatalf("upload: %v", err)
	}
	manifest := uploadedManifest(t, uploaded)
	if len(manifest.Sessions) != 1 {
		t.Fatalf("manifest sessions = %+v, want only the outer repository's", manifest.Sessions)
	}
}

func quoteJSON(value string) string {
	return `"` + strings.ReplaceAll(value, `\`, `\\`) + `"`
}

func runGitTest(t *testing.T, repo string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", repo}, args...)
	if args[0] == "init" {
		commandArgs = []string{"init", repo}
	}
	if output, err := exec.Command("git", commandArgs...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", commandArgs, err, output)
	}
}
