package gitlocal

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestParseConfigRemoteURLs(t *testing.T) {
	got := parseConfigRemoteURLs("[core]\n\turl = nope\n[remote \"origin\"]\n\turl = git@github.com:a/b.git # c\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n[REMOTE \"up\"]\n\tURL = \"https://github.com/c/d\"\n[branch \"x\"]\n\turl = nope\n[remote \"x\"] url = https://github.com/e/f\n")
	want := []string{"git@github.com:a/b.git", "https://github.com/c/d", "https://github.com/e/f"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

func TestRemoteEarlierNamesReadsTheConfigAgain(t *testing.T) {
	AddRenamed("config-old/app", "config-new/app")
	dir := t.TempDir()
	write := func(url string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "config"), []byte("[remote \"origin\"]\n\turl = "+url+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("git@github.com:Config-Old/app.git")
	if got := RemoteEarlierNames(dir); !slices.Equal(got, []string{"config-old/app"}) {
		t.Fatalf("got %q", got)
	}
	// A different size is noticed even when the modification time is not.
	write("https://github.com/config-new/app.git")
	if got := RemoteEarlierNames(dir); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
	if got := RemoteEarlierNames(filepath.Join(dir, "missing")); len(got) != 0 {
		t.Fatalf("got %q", got)
	}
}
