package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/github"
)

func diskBytes(t testing.TB, s *Service, key string) []byte {
	t.Helper()
	path, err := s.databasePath()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := appdb.Read(path, "cache", "discovery/"+key)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// setModified moves app.sqlite's modification time, so the service trusts
// (in the past) or never trusts (in the future) what it kept.
func setModified(t testing.TB, s *Service, at time.Time) {
	t.Helper()
	path, err := s.databasePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// Readers share one decoded view until the document changes, and Cached
// hands each caller slices of its own.
func TestReadersShareOneDecodeAndCannotChangeIt(t *testing.T) {
	h := newHub()
	h.add("acme/app", &hubPR{number: 1, updated: minutes(-10)})
	h.add("acme/app", &hubPR{number: 2, updated: minutes(-9)})
	s, _ := hubService(t, h)
	ctx := context.Background()
	if _, err := s.PollNow(ctx, appRepo); err != nil {
		t.Fatal(err)
	}
	key := repositoryKey(t, s, appRepo)
	// Within racyWindow of the write the document is read again; its bytes
	// are the same, so it is not decoded again.
	first, err := s.view(key)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.view(key); err != nil || again != first {
		t.Fatalf("unchanged document decoded again: %v", err)
	}
	setModified(t, s, time.Now().Add(-time.Hour))
	if again, err := s.view(key); err != nil || again != first {
		t.Fatalf("trusted document decoded again: %v", err)
	}
	got, err := s.Cached(ctx, appRepo)
	if err != nil || len(got.PullRequests) != 2 {
		t.Fatalf("cached %+v %v", got, err)
	}
	got.PullRequests[0].Title = "changed by a caller"
	got.PullRequests = append(got.PullRequests[:1], PullRequest{Number: 99})
	got.Observations = append(got.Observations, Observation{Number: 98})
	again, err := s.Cached(ctx, appRepo)
	if err != nil || len(again.PullRequests) != 2 || again.PullRequests[0].Title != "PR 1" || again.PullRequests[1].Number != 2 || len(again.Observations) != 0 {
		t.Fatalf("a caller changed the shared snapshot: %+v %v", again, err)
	}
}

// A write by another process or service is seen at the next read, whether
// app.sqlite's modification time could vouch for the kept copy or not.
func TestAnotherWriterIsSeen(t *testing.T) {
	h := newHub()
	h.add("acme/app", &hubPR{number: 1, updated: minutes(-10)})
	s, _ := hubService(t, h)
	ctx := context.Background()
	if _, err := s.PollNow(ctx, appRepo); err != nil {
		t.Fatal(err)
	}
	key := repositoryKey(t, s, appRepo)
	for _, title := range []string{"Written elsewhere", "Written again"} {
		// The kept copy is trusted until app.sqlite changes.
		setModified(t, s, time.Now().Add(-time.Hour))
		if _, err := s.Cached(ctx, appRepo); err != nil {
			t.Fatal(err)
		}
		other := New(s.client, s.directory)
		other.now = s.now
		cache, err := other.load(key)
		if err != nil {
			t.Fatal(err)
		}
		cache.Snapshot.PullRequests[0].Title = title
		if err := other.save(key, cache); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Cached(ctx, appRepo); err != nil || got.PullRequests[0].Title != title {
			t.Fatalf("other writer not seen: %+v %v", got, err)
		}
	}
	setModified(t, s, time.Now().Add(-time.Hour))
	if _, err := s.Cached(ctx, appRepo); err != nil {
		t.Fatal(err)
	}
	path, _ := s.databasePath()
	if err := appdb.Write(path, "cache", "discovery/"+key, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cached(ctx, appRepo); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("deleted cache still read: %v", err)
	}
}

// A poll that changed nothing but its times keeps them in memory: Cached and
// Revision follow them, the disk copy stays as it was until something else
// changes or unsavedLimit passes, and a restarted service reads the disk copy.
func TestUnchangedPollIsNotWritten(t *testing.T) {
	h := newHub()
	h.add("acme/app", &hubPR{number: 1, updated: minutes(-10)})
	s, now := hubService(t, h)
	ctx := context.Background()
	first, err := s.PollNow(ctx, appRepo)
	if err != nil {
		t.Fatal(err)
	}
	key := repositoryKey(t, s, appRepo)
	written := diskBytes(t, s, key)
	revision, err := s.Revision(ctx, appRepo)
	if err != nil {
		t.Fatal(err)
	}

	*now = first.NextPoll
	second, err := s.Poll(ctx, appRepo)
	if err != nil || !second.FetchedAt.Equal(first.NextPoll) || !second.NextPoll.After(first.NextPoll) {
		t.Fatalf("second poll %+v %v", second, err)
	}
	if !bytes.Equal(diskBytes(t, s, key), written) {
		t.Fatal("unchanged poll rewrote the cache")
	}
	if got, err := s.Cached(ctx, appRepo); err != nil || got.Stale || !got.FetchedAt.Equal(second.FetchedAt) || !got.NextPoll.Equal(second.NextPoll) {
		t.Fatalf("cached after unchanged poll %+v %v", got, err)
	}
	if again, err := s.Revision(ctx, appRepo); err != nil || again == revision {
		t.Fatalf("revision kept across new times: %+v %v", again, err)
	}
	if cache := loadCache(t, s, appRepo); !cache.Snapshot.NextPoll.Equal(second.NextPoll) || cache.Interval != 2*first.NextPoll.Sub(first.FetchedAt) {
		t.Fatalf("next poll did not start from the kept times: %+v", cache)
	}
	restarted := New(s.client, s.directory)
	restarted.now = s.now
	if got, err := restarted.Cached(ctx, appRepo); err != nil || !got.FetchedAt.Equal(first.FetchedAt) {
		t.Fatalf("restart read %+v %v", got, err)
	}

	// Anything else that changes writes the kept times with it.
	if err := s.Observe(ctx, appRepo, Observation{Number: 1, HeadSHA: "head1", State: "MERGED", FetchedAt: *now}); err != nil {
		t.Fatal(err)
	}
	var saved diskCache
	if err := json.Unmarshal(diskBytes(t, s, key), &saved); err != nil || !saved.Snapshot.FetchedAt.Equal(second.FetchedAt) || len(saved.Snapshot.Observations) != 1 {
		t.Fatalf("observation saved without the kept times: %+v %v", saved.Snapshot, err)
	}
	written = diskBytes(t, s, key)

	// A change GitHub reports is written at once.
	*now = second.NextPoll
	h.change("acme/app", 1, func(pr *hubPR) { pr.title, pr.updated = "Edited", *now })
	third, err := s.PollNow(ctx, appRepo)
	if err != nil || titleOf(third, 1) != "Edited" {
		t.Fatalf("third poll %+v %v", third, err)
	}
	if bytes.Equal(diskBytes(t, s, key), written) {
		t.Fatal("changed poll was not written")
	}
	written = diskBytes(t, s, key)

	// New times alone are kept unsaved while the disk copy is less than
	// unsavedLimit old, and written once it is.
	for _, step := range []struct {
		after time.Duration
		write bool
	}{{unsavedLimit - time.Second, false}, {unsavedLimit, true}} {
		*now = third.FetchedAt.Add(step.after)
		cache, err := s.load(key)
		if err != nil {
			t.Fatal(err)
		}
		cache.Snapshot.FetchedAt, cache.Snapshot.NextPoll = *now, now.Add(time.Minute)
		if err := s.store(key, cache, true); err != nil {
			t.Fatal(err)
		}
		if changed := !bytes.Equal(diskBytes(t, s, key), written); changed != step.write {
			t.Fatalf("%v after the last write: written %t, want %t", step.after, changed, step.write)
		}
	}
	if err := json.Unmarshal(diskBytes(t, s, key), &saved); err != nil || !saved.Snapshot.FetchedAt.Equal(*now) {
		t.Fatalf("times written %v: %v", saved.Snapshot.FetchedAt, err)
	}
}

// Another writer replacing the disk copy wins over times a poll kept unsaved.
func TestAnotherWriterReplacesUnsavedTimes(t *testing.T) {
	h := newHub()
	h.add("acme/app", &hubPR{number: 1, updated: minutes(-10)})
	s, now := hubService(t, h)
	ctx := context.Background()
	first, err := s.PollNow(ctx, appRepo)
	if err != nil {
		t.Fatal(err)
	}
	*now = first.NextPoll
	if _, err := s.Poll(ctx, appRepo); err != nil {
		t.Fatal(err)
	}
	key := repositoryKey(t, s, appRepo)
	other := New(s.client, s.directory)
	other.now = s.now
	cache, err := other.load(key)
	if err != nil {
		t.Fatal(err)
	}
	cache.Snapshot.PullRequests[0].Title = "Written elsewhere"
	if err := other.save(key, cache); err != nil {
		t.Fatal(err)
	}
	got, err := s.Cached(ctx, appRepo)
	if err != nil || got.PullRequests[0].Title != "Written elsewhere" || !got.FetchedAt.Equal(first.FetchedAt) {
		t.Fatalf("cached %+v %v", got, err)
	}
}

func repositoryKey(t testing.TB, s *Service, repository github.Repository) string {
	t.Helper()
	reader, err := s.client.REST(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.path(reader.CacheIdentity(), repository.String())
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// syntheticCache is a repository's cache with prs open pull requests, each
// with its GitHub row, snapshot entry and author permission response.
func syntheticCache(repository github.Repository, prs int, now time.Time) diskCache {
	cache := diskCache{Format: cacheFormat, Interval: time.Minute, Complete: true, Cursor: now.Add(-time.Minute), Swept: now.Add(-time.Minute),
		Responses: map[string]cachedResponse{}, Authors: map[string]authorPermission{},
		Snapshot:    Snapshot{Repository: repository.String(), Viewer: "viewer", PullRequests: []PullRequest{}, FetchedAt: now, NextPoll: now.Add(time.Minute), Rate: RateLimit{Remaining: new(4000), Reset: now.Add(time.Hour)}},
		Fingerprint: fingerprint{Newest: prs, Open: prs, Updated: now.Add(-time.Minute)}}
	for n := 1; n <= prs; n++ {
		author := "author" + strconv.Itoa(n%200)
		head := fmt.Sprintf("%040x", n)
		var row gqlPR
		row.Number, row.Title, row.HeadRefOid, row.HeadRefName, row.BaseRefName = n, "Change number "+strconv.Itoa(n)+" to the synthetic repository", head, "branch-"+strconv.Itoa(n), "main"
		row.Additions, row.Deletions, row.ChangedFiles, row.UpdatedAt = n%400, n%90, n%30, now.Add(-time.Duration(n)*time.Minute)
		row.Author = actor{Login: author, Typename: "User"}
		row.HeadRepository = &gqlRepository{DatabaseID: 1, NameWithOwner: repository.Owner + "/" + repository.Name}
		row.BaseRepository = gqlRepository{DatabaseID: 1, NameWithOwner: repository.Owner + "/" + repository.Name}
		row.Mergeable, row.MergeStateStatus = "MERGEABLE", "CLEAN"
		row.ReviewDecision = new("REVIEW_REQUIRED")
		cache.Pulls = append(cache.Pulls, row)
		cache.Snapshot.PullRequests = append(cache.Snapshot.PullRequests, PullRequest{Repository: repository.String(), Number: n, HeadSHA: head, HeadRef: row.HeadRefName, BaseRef: "main",
			Additions: row.Additions, Deletions: row.Deletions, ChangedFiles: row.ChangedFiles, Title: row.Title, URL: "https://github.com/" + repository.Owner + "/" + repository.Name + "/pull/" + strconv.Itoa(n),
			Author: author, State: "OPEN", AuthorHasWrite: new(true), ViewerCanPush: new(true), TeamMembershipKnown: true, Review: "required", Checks: "passing", Merge: "clean",
			ReviewRequired: new(true), Unresolved: new(0), UpdatedAt: row.UpdatedAt})
		path := "/repos/" + repository.Owner + "/" + repository.Name + "/collaborators/" + author + "/permission"
		cache.Responses[path] = cachedResponse{ETag: `W/"` + head[:32] + `"`, Body: json.RawMessage(`{"permission":"write","user":{"permissions":{"push":true}}}`)}
		cache.Authors[author] = authorPermission{Push: new(true), Read: now}
	}
	return cache
}

// benchService seeds repositories caches of prs pull requests each and
// returns their keys.
func benchService(b *testing.B, repositories, prs int) (*Service, []github.Repository, []string) {
	b.Helper()
	client := github.New(func(context.Context, string) (string, error) { return "synthetic", nil }, github.Options{})
	s := New(client, appdbtest.TempDir(b))
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	var repos []github.Repository
	var keys []string
	size := 0
	for i := range repositories {
		repository := github.Repository{Host: "github.com", Owner: "acme", Name: "repo" + strconv.Itoa(i)}
		key := repositoryKey(b, s, repository)
		raw, err := json.Marshal(syntheticCache(repository, prs, now))
		if err != nil {
			b.Fatal(err)
		}
		size += len(raw)
		path, _ := s.databasePath()
		if err := appdb.Write(path, "cache", "discovery/"+key, raw); err != nil {
			b.Fatal(err)
		}
		repos, keys = append(repos, repository), append(keys, key)
	}
	b.Logf("%d documents of %d bytes", repositories, size/repositories)
	return s, repos, keys
}

// legacyLoad is the cache read before documents were kept: read and decode
// the whole document every time.
func legacyLoad(s *Service, key string) (diskCache, error) {
	path, err := s.databasePath()
	if err != nil {
		return diskCache{}, err
	}
	raw, err := appdb.Read(path, "cache", "discovery/"+key)
	if err != nil {
		return diskCache{}, err
	}
	var cache diskCache
	if json.Unmarshal(raw, &cache) != nil {
		return diskCache{}, errInvalidDocument
	}
	return cache, nil
}

func legacySave(s *Service, key string, cache diskCache) error {
	raw, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	path, err := s.databasePath()
	if err != nil {
		return err
	}
	return appdb.Write(path, "cache", "discovery/"+key, raw)
}

// BenchmarkCachedRead reads every repository's snapshot once per iteration,
// as /api/prs does: before (decode every read), and shared, with app.sqlite
// unchanged for a while (trusted) or just written (verified: read again, not
// decoded).
func BenchmarkCachedRead(b *testing.B) {
	for _, shape := range []struct{ repositories, prs int }{{1, 5000}, {500, 10}} {
		name := fmt.Sprintf("repos=%d,prs=%d", shape.repositories, shape.prs)
		b.Run(name+"/before", func(b *testing.B) {
			s, _, keys := benchService(b, shape.repositories, shape.prs)
			b.ResetTimer()
			for b.Loop() {
				for _, key := range keys {
					if _, err := legacyLoad(s, key); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
		for _, mode := range []struct {
			name string
			at   time.Duration
		}{{"trusted", -time.Hour}, {"verified", time.Hour}} {
			b.Run(name+"/"+mode.name, func(b *testing.B) {
				s, repos, _ := benchService(b, shape.repositories, shape.prs)
				setModified(b, s, time.Now().Add(mode.at))
				ctx := context.Background()
				b.ResetTimer()
				for b.Loop() {
					for _, repository := range repos {
						if _, err := s.Cached(ctx, repository); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}

// BenchmarkUnchangedPoll is a poll's cache work when GitHub reported no
// change: load the cache, move its times, save it.
func BenchmarkUnchangedPoll(b *testing.B) {
	for _, shape := range []struct{ repositories, prs int }{{1, 5000}, {1, 10}} {
		name := fmt.Sprintf("prs=%d", shape.prs)
		b.Run(name+"/before", func(b *testing.B) {
			s, _, keys := benchService(b, shape.repositories, shape.prs)
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				cache, err := legacyLoad(s, keys[0])
				if err != nil {
					b.Fatal(err)
				}
				cache.Snapshot.FetchedAt = s.now().Add(time.Duration(i))
				if err := legacySave(s, keys[0], cache); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(name+"/after", func(b *testing.B) {
			s, _, keys := benchService(b, shape.repositories, shape.prs)
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				cache, err := s.load(keys[0])
				if err != nil {
					b.Fatal(err)
				}
				cache.Snapshot.FetchedAt = s.now().Add(time.Duration(i))
				if err := s.store(keys[0], cache, true); err != nil {
					b.Fatal(err)
				}
			}
			if !bytes.Equal(diskBytes(b, s, keys[0]), func() []byte {
				raw, _ := json.Marshal(syntheticCache(github.Repository{Host: "github.com", Owner: "acme", Name: "repo0"}, shape.prs, s.now()))
				return raw
			}()) {
				b.Fatal("unchanged poll was written")
			}
		})
	}
}
