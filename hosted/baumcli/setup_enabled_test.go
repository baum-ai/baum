//go:build !windows

package baumcli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetupSkipsSyncAndUploadForRepositoriesNotEnabledInBaum(t *testing.T) {
	workingEntire(t)
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "rcnt_api_key")
	repo := newGitHubRepo(t, t.TempDir(), "demo")
	t.Chdir(repo)
	t.Setenv("HOME", t.TempDir())

	var syncRequests, uploadRequests int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /github/repositories/find", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"id": "repo-1", "full_name": "acme/demo", "enabled": false})
	})
	mux.HandleFunc("POST /github/repositories/repo-1/sync", func(w http.ResponseWriter, r *http.Request) {
		syncRequests++
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("POST /github/repositories/repo-1/trace-uploads", func(w http.ResponseWriter, r *http.Request) {
		uploadRequests++
		w.WriteHeader(http.StatusAccepted)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	t.Setenv(serverEnvVar, server.URL)

	stdout, _, err := runCLI(t, "setup", "--sync", "--upload", "--yes")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if syncRequests != 0 || uploadRequests != 0 {
		t.Errorf("a disabled repository got sync=%d upload=%d requests", syncRequests, uploadRequests)
	}
	for _, want := range []string{
		"Skipping sync for acme/demo: not enabled in baum",
		"Skipping upload for acme/demo: not enabled in baum",
		"turn it on in the repositories page",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q: %q", want, stdout)
		}
	}
	if complete, _ := repositorySetupComplete(t.Context(), repo); !complete {
		t.Error("hooks must still be installed for a disabled repository")
	}
}
