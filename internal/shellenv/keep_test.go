package shellenv

import (
	"slices"
	"testing"
)

func TestMergeTakesKeptVariablesFromTheLoginShell(t *testing.T) {
	inherited := []string{"HOME=/home/agent-view", "PATH=/agent/bin", "GITHUB_TOKEN=agent", "GH_TOKEN=agent-only", "AGENT_SECRET=x", "XDG_STATE_HOME=/client/state", "BAUM_MODEL=inherited"}
	resolved := map[string]string{"HOME": "/home/ada", "PATH": "/usr/bin", "GITHUB_TOKEN": "shell", "BAUM_BACKEND": "codex", "XDG_STATE_HOME": "/shell/state", "EDITOR": "vim", ResolvingVariable: "1", "LC_ALL": "C"}
	got := merge(false, inherited, resolved)
	want := []string{"BAUM_BACKEND=codex", "GITHUB_TOKEN=shell", "HOME=/home/agent-view", "LC_ALL=C", "PATH=/usr/bin", "XDG_STATE_HOME=/client/state"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestMergeFallsBackToInheritedKeptVariables(t *testing.T) {
	got := merge(false, []string{"PATH=/bin", "GH_TOKEN=t", "AGENT_SECRET=x", "BAUM_MODEL=m", "=C:=C:\\"}, nil)
	want := []string{"BAUM_MODEL=m", "GH_TOKEN=t", "PATH=/bin"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestMergeFoldsWindowsNames(t *testing.T) {
	got := merge(true, []string{"Path=C:\\bin", "SystemRoot=C:\\Windows", "UserProfile=C:\\Users\\ada", "Secret=x"}, nil)
	want := []string{"Path=C:\\bin", "SystemRoot=C:\\Windows", "UserProfile=C:\\Users\\ada"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	if names := Names(got); !slices.Equal(names, []string{"Path", "SystemRoot", "UserProfile"}) {
		t.Fatal(names)
	}
}
