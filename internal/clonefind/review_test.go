package clonefind

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/hookevents"
)

// counting wraps the real probe and records the directories it ran in.
func counting(calls *[]string) Probe {
	return func(ctx context.Context, dir string) (gitlocal.Light, error) {
		*calls = append(*calls, dir)
		return gitlocal.Probe(ctx, dir, 5*time.Second)
	}
}

// On macOS one rule says which paths may make macOS ask the user: the walk
// never enters them, and git never runs in a folder an agent worked in there,
// nor is it even stat'ed. ~/Documents is read only with the opt-in.
func TestMacOSProtectedFoldersOfAgentsAreNotTouched(t *testing.T) {
	h := home(t)
	protected := map[string]string{
		"acme/documents":  filepath.Join(h, "Documents", "a"),
		"acme/desktop":    filepath.Join(h, "Desktop", "b"),
		"acme/downloads":  filepath.Join(h, "Downloads", "c"),
		"acme/pictures":   filepath.Join(h, "Pictures", "d"),
		"acme/movies":     filepath.Join(h, "Movies", "e"),
		"acme/music":      filepath.Join(h, "Music", "f"),
		"acme/library":    filepath.Join(h, "Library", "Developer", "g"),
		"acme/containers": filepath.Join(h, "Library", "Containers", "h"),
		"acme/group":      filepath.Join(h, "Library", "Group Containers", "i"),
		"acme/cloud":      filepath.Join(h, "Library", "CloudStorage", "j"),
		"acme/icloud":     filepath.Join(h, "Library", "Mobile Documents", "k"),
		"acme/dropbox":    filepath.Join(h, "Dropbox (Work)", "l"),
	}
	now := time.Now()
	var events []hookevents.Record
	for name, dir := range protected {
		repo(t, dir, "origin", gh(name))
		events = append(events, agentEvent(dir, now))
	}
	open := repo(t, filepath.Join(h, "elsewhere", "open"), "origin", gh("acme/open"))
	events = append(events, agentEvent(open, now))

	var calls []string
	darwin := options(h, events...)
	darwin.GOOS, darwin.Probe = "darwin", counting(&calls)
	result := find(t, darwin)
	if len(result.Repositories) != 1 || len(result.Repositories["acme/open"]) != 1 {
		t.Fatalf("repositories = %+v", result.Repositories)
	}
	for _, call := range calls {
		for _, dir := range protected {
			if call == dir {
				t.Errorf("git ran in %s", call)
			}
		}
	}
	// Locate, which the service runs for each event, follows the same rule.
	for name, dir := range protected {
		if got := Locate(context.Background(), darwin, dir, now); got != nil {
			t.Errorf("%s: Locate = %+v", name, got)
		}
	}

	// The one-off Documents opt-in lets that folder through, walked and as
	// an agent's folder, and only that one.
	calls = nil
	darwin.IncludeDocuments = true
	result = find(t, darwin)
	if len(result.Repositories) != 2 || result.Repositories["acme/documents"] == nil || result.Repositories["acme/documents"][0].Source != SourceAgent {
		t.Fatalf("with the opt-in: %+v", result.Repositories)
	}
	if got := Locate(context.Background(), darwin, protected["acme/documents"], now); len(got) != 1 {
		t.Fatalf("Locate with the opt-in = %+v", got)
	}

	// Other systems have no such prompt: an agent worked there, so it is
	// read, except in Downloads, which holds nobody's clone.
	linux := options(h, events...)
	if got := find(t, linux); len(got.Repositories) != len(protected) || got.Repositories["acme/downloads"] != nil {
		t.Fatalf("linux repositories = %+v", got.Repositories)
	}
}

// Removable and network volumes ask too; a home directory on a volume of its
// own does not.
func TestMacOSVolumesAreProtected(t *testing.T) {
	// A macOS home directory, spelt so that it reads as no real one.
	mac := "/Us" + "ers/dev"
	f := newFinder(Options{Home: mac, GOOS: "darwin", TempDirs: []string{}})
	for path, want := range map[string]bool{
		"/Volumes":                 true,
		"/Volumes/USB/code/app":    true,
		"/Volumes/share/app":       true,
		mac + "/code/app":          false,
		mac + "/Library/Caches/x":  true,
		mac + "/library/caches/x":  true,
		mac + "/Pictures/x":        true,
		mac + "/Documents/x":       true,
		mac + "/OneDrive - Acme/x": true,
	} {
		if got := f.protected(path); got != want {
			t.Errorf("protected(%s) = %v", path, got)
		}
	}
	onVolume := newFinder(Options{Home: "/Volumes/Home/dev", GOOS: "darwin", TempDirs: []string{}})
	if onVolume.protected("/Volumes/Home/dev/code/app") || !onVolume.protected("/Volumes/Home/dev/Music/x") || !onVolume.protected("/Volumes/USB/app") {
		t.Error("a home on a volume is read like any other")
	}
	if linux := newFinder(Options{Home: "/home/dev", GOOS: "linux", TempDirs: []string{}}); linux.protected("/Volumes/USB/app") {
		t.Error("only macOS asks")
	}
}

func TestAgentWorktreeInAHiddenFolderStandsForTheMainClone(t *testing.T) {
	h := home(t)
	main := repo(t, filepath.Join(h, "elsewhere", "app"), "origin", gh("acme/app"))
	git(t, main, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	linked := filepath.Join(main, ".claude", "worktrees", "x")
	git(t, main, "worktree", "add", "-q", "-b", "x", linked)
	at := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)

	got := find(t, options(h, agentEvent(linked, at))).Repositories["acme/app"]
	if len(got) != 1 || got[0].Path != main || got[0].Source != SourceAgent || !got[0].LastActivity.Equal(at) {
		t.Fatalf("find = %+v", got)
	}
	matches := Locate(context.Background(), options(h), linked, at)
	if len(matches) != 1 || matches[0].Repository != "acme/app" || matches[0].Candidate.Path != main || !matches[0].Candidate.LastActivity.Equal(at) {
		t.Fatalf("locate = %+v", matches)
	}
}

func TestWalkBudgetGoesToShallowFoldersFirst(t *testing.T) {
	h := home(t)
	for _, name := range []string{"r1", "r2", "r3"} {
		repo(t, filepath.Join(h, "code", name), "origin", gh("acme/code-"+name))
		repo(t, filepath.Join(h, "src", name), "origin", gh("acme/src-"+name))
	}
	repo(t, filepath.Join(h, "shallow"), "origin", gh("acme/shallow"))
	o := options(h)
	o.MaxWalked = 5 // the home, its three folders, then code/r1
	result := find(t, o)
	if len(result.Repositories) != 2 || result.Repositories["acme/shallow"] == nil || result.Repositories["acme/code-r1"] == nil {
		t.Fatalf("repositories = %+v", result.Repositories)
	}
}

func TestFolderInsideAWalkedFolderIsNotWalkedAgain(t *testing.T) {
	h := home(t)
	// Both clones are deeper than the walk of the home reaches. The agent's
	// clone is found; the folder around it is not walked for its sibling.
	worked := repo(t, filepath.Join(h, "code", "a", "b", "c", "d", "e"), "origin", gh("acme/worked"))
	repo(t, filepath.Join(h, "code", "a", "b", "c", "d", "f"), "origin", gh("acme/sibling"))
	result := find(t, options(h, agentEvent(worked, time.Now())))
	if result.Repositories["acme/worked"] == nil || result.Repositories["acme/sibling"] != nil {
		t.Fatalf("repositories = %+v", result.Repositories)
	}
}

func TestWorkTreeSetElsewhereIsNotTheCheckedDirectory(t *testing.T) {
	h := home(t)
	clone := repo(t, filepath.Join(h, "code", "app"), "origin", gh("acme/app"))
	elsewhere := filepath.Join(h, "unrelated")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	// What git reports for a repository whose core.worktree names another place.
	probe := func(ctx context.Context, dir string) (gitlocal.Light, error) {
		return gitlocal.Light{Top: elsewhere, CommonDir: filepath.Join(clone, ".git"),
			Remotes: []gitlocal.Remote{{Name: "origin", URL: gh("acme/app")}}}, nil
	}
	o := options(h, agentEvent(clone, time.Now()))
	o.Probe = probe
	if result := find(t, o); len(result.Repositories) != 0 {
		t.Fatalf("repositories = %+v", result.Repositories)
	}
	// A subdirectory of the reported top-level is fine.
	probe = func(ctx context.Context, dir string) (gitlocal.Light, error) {
		return gitlocal.Light{Top: filepath.Dir(clone), CommonDir: filepath.Join(clone, ".git"),
			Remotes: []gitlocal.Remote{{Name: "origin", URL: gh("acme/app")}}}, nil
	}
	o.Probe = probe
	if result := find(t, o); result.Repositories["acme/app"] == nil {
		t.Fatalf("a directory inside the top-level was dropped: %+v", result.Repositories)
	}
}
