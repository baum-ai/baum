package repos

import (
	"context"
	"errors"
	"testing"
)

func TestAddForFilesAForkCloneUnderTheRepositoryItsUpstreamNames(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	p := clone(t, "https://github.com/me/app.git")
	git(t, p, "remote", "add", "upstream", "git@github.com:Acme/App.git")

	c, e := r.AddFor(ctx, p, "Acme/App", nil)
	if e != nil {
		t.Fatal(e)
	}
	if c.Repository != "acme/app" || !c.Available {
		t.Fatalf("clone = %+v", c)
	}
	// The fork's own repository is neither registered nor picked, and the
	// clone stays available when listed.
	picked, e := r.Repositories(ctx)
	if e != nil || len(picked) != 1 || picked[0].Repository != "acme/app" || len(picked[0].Clones) != 1 || !picked[0].Clones[0].Available {
		t.Fatalf("picked = %+v, %v", picked, e)
	}

	// Add keeps filing the same working tree under origin.
	plain, e := r.Add(ctx, p, nil)
	if e != nil || plain.Repository != "me/app" {
		t.Fatalf("Add = %+v, %v", plain, e)
	}
}

func TestAddForRejectsAWorkingTreeWithoutAMatchingRemote(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	p := clone(t, "https://github.com/me/app.git")
	git(t, p, "remote", "add", "other", "https://gitlab.com/acme/app.git")
	if _, e := r.AddFor(ctx, p, "acme/app", nil); !errors.Is(e, ErrNoMatchingRemote) {
		t.Fatalf("error = %v", e)
	}
	if _, e := r.AddFor(ctx, p, "not a repository", nil); !errors.Is(e, ErrInvalidRepository) {
		t.Fatalf("error = %v", e)
	}
	if list, _ := r.Repositories(ctx); len(list) != 0 {
		t.Fatalf("a failed AddFor registered %+v", list)
	}
}
