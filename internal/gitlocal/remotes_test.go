package gitlocal

import (
	"context"
	"slices"
	"testing"
)

func TestRemoteURLs(t *testing.T) {
	f := newFixture(t)
	r := open(t, f.dir, Options{})
	if got, err := r.RemoteURLs(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("no remotes: %v, %v", got, err)
	}
	git(t, f.dir, "remote", "add", "origin", "git@github.com:someone/fork.git")
	git(t, f.dir, "remote", "add", "up.stream", "https://github.com/acme/widgets.git")
	got, err := r.RemoteURLs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Remote{{"origin", "git@github.com:someone/fork.git"}, {"up.stream", "https://github.com/acme/widgets.git"}}
	if !slices.Equal(got, want) {
		t.Errorf("remotes = %v, want %v", got, want)
	}
}
