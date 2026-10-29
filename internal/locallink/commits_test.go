package locallink

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

// LinkCommits links a pull request's commits exactly as Resolve does, with
// checkpoint truth and the matcher's own steps.
func TestLinkCommitsEqualsResolve(t *testing.T) {
	for name, build := range map[string]func(t *testing.T) linktest.Fixture{
		"matcher": linktest.New,
		"checkpoint": func(t *testing.T) linktest.Fixture {
			f := linktest.NewWithTrailer(t, linktest.CheckpointTrailer)
			f.AddCheckpointEditing(t, "b.txt")
			return f
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := build(t)
			ctx := context.Background()
			repo, err := gitlocal.Open(ctx, f.Repo, gitlocal.Options{})
			if err != nil {
				t.Fatal(err)
			}
			commits, err := repo.Commits(ctx, f.Base, f.Head)
			_ = repo.Close()
			if err != nil {
				t.Fatal(err)
			}
			links, err := LinkCommits(ctx, f.Repo, commits, LinkOptions{Repository: linktest.Repository, Sessions: f.Store})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, link := range links {
				if link.Vendor == "" {
					t.Errorf("vendor = %q", link.Vendor)
				}
				got = append(got, strings.Join([]string{link.Commit, link.Session, link.Method}, " "))
			}
			var want []string
			for _, c := range resolve(t, request(f)).Commits {
				for _, link := range c.Links {
					want = append(want, strings.Join([]string{c.OID, link.Session, link.Method}, " "))
				}
			}
			slices.Sort(got)
			slices.Sort(want)
			if len(want) == 0 || !slices.Equal(got, want) {
				t.Fatalf("links = %v, want %v", got, want)
			}
		})
	}
}
