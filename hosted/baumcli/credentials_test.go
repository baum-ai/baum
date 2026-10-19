//go:build !windows

package baumcli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setTempConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func TestCredentialsRoundTrip(t *testing.T) {
	dir := setTempConfigHome(t)

	if _, ok, err := loadCredentials(); err != nil || ok {
		t.Fatalf("load before save: ok = %v, err = %v", ok, err)
	}

	want := credentials{
		ServerURL: "https://api.baum.example",
		Token:     "rcnt_secret",
		ExpiresAt: time.Now().Add(time.Hour).UTC().Truncate(time.Second),
	}
	if err := saveCredentials(want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, ok, err := loadCredentials()
	if err != nil || !ok {
		t.Fatalf("load: ok = %v, err = %v", ok, err)
	}
	if got.ServerURL != want.ServerURL || got.Token != want.Token || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	info, err := os.Stat(filepath.Join(dir, "baum", "credentials.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}

	if err := removeCredentials(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok, _ := loadCredentials(); ok {
		t.Error("credentials still present after remove")
	}
	if err := removeCredentials(); err != nil {
		t.Errorf("second remove: %v", err)
	}
}

func TestSaveCredentialsTightensExistingFileAndLeavesNoTemp(t *testing.T) {
	dir := setTempConfigHome(t)
	path := filepath.Join(dir, "baum", "credentials.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := saveCredentials(credentials{ServerURL: "https://api.baum.example", Token: "rcnt_new"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
	got, ok, err := loadCredentials()
	if err != nil || !ok || got.Token != "rcnt_new" {
		t.Errorf("load: %+v, ok = %v, err = %v", got, ok, err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".credentials-*"))
	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestResolveServerURL(t *testing.T) {
	setTempConfigHome(t)
	creds := credentials{ServerURL: "https://from-file"}

	if got := resolveServerURL("https://from-flag", creds); got != "https://from-flag" {
		t.Errorf("flag: %q", got)
	}
	t.Setenv(serverEnvVar, "https://from-env")
	if got := resolveServerURL("", creds); got != "https://from-env" {
		t.Errorf("env: %q", got)
	}
	t.Setenv(serverEnvVar, "")
	if got := resolveServerURL("", creds); got != "https://from-file" {
		t.Errorf("file: %q", got)
	}
	if got := resolveServerURL("", credentials{}); got != defaultServerURL {
		t.Errorf("default: %q", got)
	}
	if defaultServerURL != "https://usebaum.com/api" {
		t.Errorf("public default: %q", defaultServerURL)
	}
}

func TestResolveTokenPrefersEnvironment(t *testing.T) {
	setTempConfigHome(t)
	creds := credentials{ServerURL: "https://one.example/api", Token: "rcnt_from_file"}

	t.Setenv(tokenEnvVar, "rcnt_from_env")
	token, fromFile := resolveToken(creds, creds.ServerURL)
	if token != "rcnt_from_env" || fromFile {
		t.Errorf("env token: %q fromFile=%v", token, fromFile)
	}

	t.Setenv(tokenEnvVar, "")
	token, fromFile = resolveToken(creds, creds.ServerURL)
	if token != "rcnt_from_file" || !fromFile {
		t.Errorf("file token: %q fromFile=%v", token, fromFile)
	}
}

func TestResolveTokenStaysWithTheServerThatIssuedIt(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "")
	creds := credentials{ServerURL: "https://one.example/api", Token: "rcnt_from_file"}

	if token, fromFile := resolveToken(creds, "https://one.example/api/"); token != "rcnt_from_file" || !fromFile {
		t.Errorf("trailing slash: %q fromFile=%v", token, fromFile)
	}
	if token, fromFile := resolveToken(creds, "https://two.example/api"); token != "" || fromFile {
		t.Errorf("other server: %q fromFile=%v, want no token", token, fromFile)
	}

	// A file from before server URLs were recorded is trusted anywhere.
	if token, fromFile := resolveToken(credentials{Token: "rcnt_legacy"}, "https://two.example/api"); token != "rcnt_legacy" || !fromFile {
		t.Errorf("legacy file: %q fromFile=%v", token, fromFile)
	}

	// The environment override is not tied to a server.
	t.Setenv(tokenEnvVar, "rcnt_from_env")
	if token, _ := resolveToken(creds, "https://two.example/api"); token != "rcnt_from_env" {
		t.Errorf("env token for other server: %q", token)
	}
}

func TestAuthenticatedClientNamesTheServerTheTokenBelongsTo(t *testing.T) {
	setTempConfigHome(t)
	t.Setenv(tokenEnvVar, "")
	t.Setenv(serverEnvVar, "")
	if err := saveCredentials(credentials{ServerURL: "https://one.example/api", Token: "rcnt_one"}); err != nil {
		t.Fatal(err)
	}

	_, _, err := authenticatedClient("https://two.example/api")
	want := "logged in to https://one.example/api, not https://two.example/api; run `baum login --server https://two.example/api`"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}

	client, _, err := authenticatedClient("")
	if err != nil || client.Token != "rcnt_one" || client.BaseURL != "https://one.example/api" {
		t.Fatalf("stored server: client = %+v, err = %v", client, err)
	}
}
