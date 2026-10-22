package gitlocal

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHTTPSCredentialsAreScopedAndNeverPersisted(t *testing.T) {
	f := newFixture(t)
	git(t, f.dir, "remote", "add", "origin", "https://github.com/org/repo.git")
	r := open(t, f.dir, Options{})
	t.Setenv("GITHUB_TOKEN", "PRIVATE_GITHUB_CREDENTIAL")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	before, err := os.ReadFile(filepath.Join(f.dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	env, used, err := r.fetchCredentials(context.Background(), []string{"PATH=/usr/bin", "GIT_TRACE_CURL=1", "GIT_CURL_VERBOSE=1", "GIT_CONFIG_KEY_0=evil", "GIT_CONFIG_COUNT=1"})
	if err != nil || !used {
		t.Fatalf("used=%v err=%v", used, err)
	}
	joined := strings.Join(env, "\n")
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:PRIVATE_GITHUB_CREDENTIAL"))
	for _, want := range []string{"http.https://github.com/org/repo.git.extraHeader", "Authorization: Basic " + encoded, "credential.helper", "http.followRedirects", "false"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, unwanted := range []string{"GIT_TRACE_CURL=", "GIT_CURL_VERBOSE=", "evil"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("retained %q", unwanted)
		}
	}
	after, _ := os.ReadFile(filepath.Join(f.dir, ".git", "config"))
	if string(before) != string(after) {
		t.Fatal("credentials changed git config")
	}
	git(t, f.dir, "remote", "set-url", "origin", "https://attacker.example/org/repo.git")
	env, used, err = r.fetchCredentials(context.Background(), []string{"PATH=/usr/bin"})
	if err != nil || used || strings.Contains(strings.Join(env, "\n"), encoded) {
		t.Fatalf("credential escaped host: %v %v", used, err)
	}
	git(t, f.dir, "remote", "set-url", "origin", "git@github.com:org/repo.git")
	env, used, err = r.fetchCredentials(context.Background(), []string{"SSH_AUTH_SOCK=/working/agent"})
	if err != nil || used || len(env) != 1 || env[0] != "SSH_AUTH_SOCK=/working/agent" {
		t.Fatalf("SSH authentication changed: %v %v", env, err)
	}
}

func TestAuthenticatedFetchDoesNotExposeGitErrorSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell test double")
	}
	f := newFixture(t)
	git(t, f.dir, "remote", "add", "origin", "https://github.com/org/repo.git")
	r := open(t, f.dir, Options{})
	t.Setenv("GITHUB_TOKEN", "PRIVATE_GITHUB_CREDENTIAL")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \" $* \" in\n*' fetch '*) echo \"$GIT_CONFIG_VALUE_2 PRIVATE_GITHUB_CREDENTIAL\" >&2; exit 1;;\n*' remote get-url '*) echo https://github.com/org/repo.git;;\n*) exit 1;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	err := r.fetch(context.Background(), []string{"refs/pull/1/head:refs/baum/1/head"})
	if err == nil || strings.Contains(err.Error(), "PRIVATE_GITHUB_CREDENTIAL") || strings.Contains(err.Error(), "Authorization:") {
		t.Fatalf("unsafe error %v", err)
	}
}
