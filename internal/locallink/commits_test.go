package locallink

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/sessions"
)

// LinkCommits links a pull request's commits exactly as Resolve does, with
// checkpoint associations and the matcher's own steps.
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

// Compact linkage facts outlive whole-graph eviction, even for transcripts
// older than the tail cache's former admission cutoff.
func TestLinkCommitsReusesOldSessionFacts(t *testing.T) {
	useSessionDigests(t)
	sessions.SetTailCacheLimit(0)
	t.Cleanup(func() { sessions.SetTailCacheLimit(sessions.DefaultTailCacheBytes) })
	f := linktest.New(t)
	req := evidenceRequest(t, f)
	settle(t, req)
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
	link := func() []CommitLink {
		t.Helper()
		result, err := LinkCommits(ctx, f.Repo, commits, LinkOptions{Repository: linktest.Repository, Sessions: f.Store})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := decodes()
	first := link()
	if len(first) == 0 || decodes() == before {
		t.Fatal("cold link did not map/link sessions")
	}
	before = decodes()
	again := link()
	if !reflect.DeepEqual(first, again) || decodes() != before {
		t.Fatal("unchanged old sessions mapped again or links changed")
	}
	// An append changes the key. Only that transcript needs decoding.
	path := transcriptOf(f, linktest.SessionID)
	retitle(t, path, linktest.SessionID, "updated title")
	settle(t, req)
	before = decodes()
	changed := link()
	if decodes()-before != 1 || !reflect.DeepEqual(first, changed) {
		t.Fatal("changed file was not mapped once with identical links")
	}
	// A different file at the same path must not reuse its predecessor.
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(filepath.Dir(path), "replacement")
	if err := os.WriteFile(replacement, body, 0600); err != nil {
		t.Fatal(err)
	}
	// The supported session key uses settled size+mtime, so move the mtime too.
	at := settledAt.Add(time.Second)
	if err := os.Chtimes(replacement, at, at); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	before = decodes()
	replaced := link()
	if decodes()-before != 1 || !reflect.DeepEqual(first, replaced) {
		t.Fatal("replacement did not invalidate session facts")
	}
	if sessionDigests.held > sessionDigests.limit {
		t.Fatal("digest memory bound exceeded")
	}
}
