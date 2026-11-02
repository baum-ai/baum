package hookevents

import (
	"slices"
	"strings"
	"testing"
)

func TestPushes(t *testing.T) {
	old, pushed, zero := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("0", 40)
	line := func(branch, local, remote string) string {
		return "refs/heads/" + branch + " " + local + " refs/heads/" + branch + " " + remote
	}
	got := Pushes([]string{
		line("feature", pushed, old),
		line("new-branch", pushed, zero),
		"refs/heads/gone " + zero + " refs/heads/gone " + old,
		"refs/tags/v1 " + pushed + " refs/tags/v1 " + zero,
		"refs/heads/x " + pushed + " refs/heads/x",
		"refs/heads/x NOTANOID refs/heads/x " + old,
	})
	want := []Push{{Branch: "feature", Old: old, New: pushed}, {Branch: "new-branch", New: pushed}}
	if !slices.Equal(got, want) {
		t.Fatalf("pushes %+v", got)
	}
}

func TestRemoteName(t *testing.T) {
	for name, want := range map[string]bool{
		"origin": true, "upstream/team": true, "": false,
		"https://github.com/acme/app.git": false, "git@github.com:acme/app.git": false,
		"github.com:acme/app": false, "/srv/git/app.git": false, "../app": false, `C:\repos\app`: false, "-x": false,
	} {
		if got := RemoteName(name); got != want {
			t.Errorf("RemoteName(%q) = %t", name, got)
		}
	}
}
