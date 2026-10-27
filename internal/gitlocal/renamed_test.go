package gitlocal

import (
	"strings"
	"testing"
)

func TestARecordedEarlierNameReadsAsTheCurrentOne(t *testing.T) {
	AddRenamed("Renamed-Old/App", "renamed-new/app")
	if _, got, ok := RemoteRepository("git@github.com:renamed-old/app.git"); !ok || got != "renamed-new/app" {
		t.Fatalf("RemoteRepository = %q, %v", got, ok)
	}
	if _, got, _ := RemoteRepository("https://github.com/renamed-other/app"); got != "renamed-other/app" {
		t.Fatalf("an unrecorded name became %q", got)
	}
	if got := strings.Join(EarlierNames("Renamed-New/App"), ","); got != "renamed-old/app" {
		t.Fatalf("EarlierNames = %q", got)
	}
	if _, got, _ := RemoteRepository("https://gitlab.com/renamed-old/app"); got != "renamed-old/app" {
		t.Fatalf("another host's repository became %q", got)
	}
}

func TestRenamesChainAndRenamingBackUndoesThem(t *testing.T) {
	AddRenamed("chain-a/app", "chain-b/app")
	AddRenamed("chain-b/app", "chain-c/app")
	if got := RenamedTo("chain-a/app"); got != "chain-c/app" {
		t.Fatalf("a = %q", got)
	}
	AddRenamed("chain-c/app", "chain-a/app")
	if RenamedTo("chain-a/app") != "chain-a/app" || RenamedTo("chain-b/app") != "chain-a/app" || RenamedTo("chain-c/app") != "chain-a/app" {
		t.Fatalf("after renaming back: %q %q %q", RenamedTo("chain-a/app"), RenamedTo("chain-b/app"), RenamedTo("chain-c/app"))
	}
}
