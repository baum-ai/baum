package shellenv

import (
	"slices"
	"testing"
)

func TestWithPathReplacesPathInAnyCase(t *testing.T) {
	got := withPath([]string{`PATH=C:\agent\bin`, `path=C:\other`, `=C:=C:\work`, `GH_TOKEN=x`, `noequals`}, `C:\Windows\system32;C:\Users\ada\AppData\Local\Microsoft\WinGet\Links`)
	want := map[string]string{
		"Path":     `C:\Windows\system32;C:\Users\ada\AppData\Local\Microsoft\WinGet\Links`,
		"GH_TOKEN": "x",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("%s = %q, got %v", name, got[name], got)
		}
	}
}

// On Windows the daemon keeps what started it, except that tools are found
// on the PATH a new logon would get.
func TestWindowsDaemonEnvironmentTakesOnlyThePathFromTheRegistry(t *testing.T) {
	inherited := []string{`Path=C:\agent\bin`, `SystemRoot=C:\Windows`, `UserProfile=C:\Users\ada`, `GH_TOKEN=x`, `Secret=y`, `=C:=C:\work`}
	registry := `C:\Windows\system32;C:\Program Files\GitHub CLI\;C:\Users\ada\.local\bin`
	got := merge(true, inherited, withPath(inherited, registry))
	want := []string{"GH_TOKEN=x", "Path=" + registry, `SystemRoot=C:\Windows`, `UserProfile=C:\Users\ada`}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}
