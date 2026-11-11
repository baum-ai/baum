package locallink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
)

// useSessionDigests gives the test its own digests, and turns the mapping
// cache off so that every transcript mapped is decoded and counted.
func useSessionDigests(t *testing.T) {
	t.Helper()
	previous := sessionDigests
	sessionDigests = newDigestCache(DefaultDigestBytes)
	sessions.SetMapCacheLimit(0)
	t.Cleanup(func() {
		sessionDigests = previous
		sessions.SetMapCacheLimit(sessions.DefaultMapCacheBytes)
	})
}

func decodes() int64 { return counter("sessions.map.decodes.claude_code") }

// settledAt is when settle dates session files: after the fixture's
// commits, and long enough ago that no session is too recent to have a key.
var settledAt = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

// settle dates every file in the request's session stores settledAt, as a
// store nothing wrote to in the last few seconds is. A file that grew still
// differs by its size.
func settle(t *testing.T, req EvidenceRequest) {
	t.Helper()
	for _, dir := range []string{req.Sessions.ClaudeProjectsDir, req.Sessions.CodexSessionsDir} {
		if dir == "" {
			continue
		}
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			return os.Chtimes(path, settledAt, settledAt)
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
}

// rebuild builds evidence past the whole-answer memo, so it goes through
// the per-session digests, with the session stores settled.
func rebuild(t *testing.T, req EvidenceRequest) []bundle.Session {
	t.Helper()
	settle(t, req)
	evidenceMemo = newEvidenceCache(EvidenceMaxAge)
	return buildEvidence(t, req)
}

// fullBuild builds evidence as a process that never read a session would.
func fullBuild(t *testing.T, req EvidenceRequest) []bundle.Session {
	t.Helper()
	digests, memo, frozen := sessionDigests, evidenceMemo, frozenMemo
	sessionDigests, evidenceMemo, frozenMemo = newDigestCache(DefaultDigestBytes), newEvidenceCache(EvidenceMaxAge), newEvidenceCache(0)
	defer func() { sessionDigests, evidenceMemo, frozenMemo = digests, memo, frozen }()
	return buildEvidence(t, req)
}

// sameEvidence fails unless got is what a full build gives, field for field
// and byte for byte.
func sameEvidence(t *testing.T, step string, got, want []bundle.Session) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: reused evidence differs from a full build:\n%+v\n%+v", step, got, want)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("%s: reused evidence encodes differently:\n%s\n%s", step, gotJSON, wantJSON)
	}
}

// transcriptOf is the path of a Claude Code session the fixture stores.
func transcriptOf(f linktest.Fixture, id string) string {
	return filepath.Join(f.Store.ClaudeProjectsDir, claudecode.EncodeProjectDir(f.Repo), id+".jsonl")
}

// retitle appends a title record to a session's transcript, as Claude Code
// does while a session runs.
func retitle(t *testing.T, path, id, title string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if _, err := fmt.Fprintf(file, `{"type":"ai-title","aiTitle":%q,"sessionId":%q}`+"\n", title, id); err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceMapsOnlyTheSessionsThatChanged(t *testing.T) {
	useEvidenceMemo(t)
	useSessionDigests(t)
	f := linktest.New(t)
	editors := []string{
		"bbbbbbbb-0000-4000-8000-000000000001",
		"bbbbbbbb-0000-4000-8000-000000000002",
		"bbbbbbbb-0000-4000-8000-000000000003",
	}
	for i, id := range editors {
		f.AddEditor(t, id, "08", fmt.Sprintf("unrelated-%d.txt", i))
	}
	req := evidenceRequest(t, f)
	step := func(name string, req EvidenceRequest, wantDecodes int64) []bundle.Session {
		t.Helper()
		before := decodes()
		got := rebuild(t, req)
		if n := decodes() - before; n != wantDecodes {
			t.Errorf("%s: %d transcripts decoded, want %d", name, n, wantDecodes)
		}
		sameEvidence(t, name, got, fullBuild(t, req))
		return got
	}

	first := step("first build", req, 4)
	if len(first) != 1 || first[0].RootSessionID != linktest.SessionID {
		t.Fatalf("evidence = %+v, want the session that committed", first)
	}
	step("nothing changed", req, 0)

	retitle(t, transcriptOf(f, editors[0]), editors[0], "Still editing")
	step("an unlinked session grew", req, 1)

	retitle(t, transcriptOf(f, linktest.SessionID), linktest.SessionID, "Renamed while running")
	if got := step("the linked session grew", req, 1); got[0].Title != "Renamed while running" {
		t.Errorf("title = %q, want the new one", got[0].Title)
	}

	f.AddEditor(t, "bbbbbbbb-0000-4000-8000-000000000004", "09", "unrelated-4.txt")
	step("a session appeared", req, 1)

	if err := os.Remove(transcriptOf(f, editors[1])); err != nil {
		t.Fatal(err)
	}
	step("a session was deleted", req, 0)

	fewer := req
	fewer.Commits = req.Commits[:1]
	step("the pull request's commits changed", fewer, 0)
	step("back to both commits", req, 0)
}

// A linked session whose evidence depends on something outside its
// transcript that changed, here a path its command names appearing in the
// checkout, is mapped again, and only it.
func TestEvidenceRemapsALinkedSessionWhoseSurroundingsChanged(t *testing.T) {
	useEvidenceMemo(t)
	useSessionDigests(t)
	f := linktest.New(t)
	const runner = "cccccccc-0000-4000-8000-000000000001"
	f.AddSessionRunning(t, runner, f.Repo, "read the notes", "cat notes/todo.md")
	f.AddEditor(t, "bbbbbbbb-0000-4000-8000-000000000001", "08", "unrelated.txt")
	req := evidenceRequest(t, f)
	before := rebuild(t, req)
	sameEvidence(t, "first build", before, fullBuild(t, req))

	if err := os.MkdirAll(filepath.Join(f.Repo, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.Repo, "notes", "todo.md"), []byte("todo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loads, decoded := counter("sessions.load.calls"), decodes()
	after := rebuild(t, req)
	if got := counter("sessions.load.calls") - loads; got != 2 {
		t.Errorf("loads = %d, want 2: one reusing every session, one mapping the linked one", got)
	}
	if got := decodes() - decoded; got != 1 {
		t.Errorf("%d transcripts decoded, want 1", got)
	}
	sameEvidence(t, "a named path appeared", after, fullBuild(t, req))
	if reflect.DeepEqual(before, after) {
		t.Fatalf("the evidence did not change; the test does not exercise a path the command names:\n%+v", after)
	}
	encoded, _ := json.Marshal(after)
	if !strings.Contains(string(encoded), "notes/todo.md") {
		t.Errorf("evidence does not name the path that appeared:\n%s", encoded)
	}
}

// A worktree added beside the clone changes the roots that bound touched
// paths. Evidence that placed no path differently is reused.
func TestEvidenceSurvivesAnUnrelatedWorktree(t *testing.T) {
	useEvidenceMemo(t)
	useSessionDigests(t)
	f := linktest.New(t)
	req := evidenceRequest(t, f)
	rebuild(t, req)
	linktest.Git(t, f.Repo, "worktree", "add", "-q", "--detach", filepath.Join(t.TempDir(), "elsewhere"))
	decoded := decodes()
	got := rebuild(t, req)
	if n := decodes() - decoded; n != 0 {
		t.Errorf("%d transcripts decoded after a worktree was added, want 0", n)
	}
	sameEvidence(t, "a worktree was added", got, fullBuild(t, req))
}

// A worktree added where a linked session touched a file places that file
// in the clone: the session's evidence is built again.
func TestEvidenceRemapsWhenAWorktreePlacesATouchedPath(t *testing.T) {
	useEvidenceMemo(t)
	useSessionDigests(t)
	f := linktest.New(t)
	const editor = "dddddddd-0000-4000-8000-000000000001"
	// It edits the second commit's file, which links it, and one beside the
	// clone, where the worktree will be.
	f.AddEditor(t, editor, "10", "c.txt", "../wt/x.txt")
	req := evidenceRequest(t, f)
	before := rebuild(t, req)
	if !slices.ContainsFunc(before, func(s bundle.Session) bool { return s.RootSessionID == editor }) {
		t.Fatalf("evidence = %+v, want the editor linked", before)
	}
	linktest.Git(t, f.Repo, "worktree", "add", "-q", "--detach", filepath.Join(filepath.Dir(f.Repo), "wt"))
	decoded := decodes()
	after := rebuild(t, req)
	if n := decodes() - decoded; n != 1 {
		t.Errorf("%d transcripts decoded, want 1", n)
	}
	sameEvidence(t, "a worktree placed a touched path", after, fullBuild(t, req))
	encoded, _ := json.Marshal(after)
	if !strings.Contains(string(encoded), `"x.txt"`) {
		t.Errorf("evidence does not name the path the worktree placed:\n%s", encoded)
	}
}

// An Entire checkpoint's sessions are read once: its tree names its
// content, so a later build neither reads nor maps it.
func TestEvidenceReadsAnUnchangedCheckpointOnce(t *testing.T) {
	useEvidenceMemo(t)
	useSessionDigests(t)
	f := linktest.New(t)
	f.AddCheckpointEditing(t, "b.txt")
	req := evidenceRequest(t, f)
	archives := counter("git.archive")
	first := rebuild(t, req)
	if counter("git.archive") == archives {
		t.Fatal("the first build read no checkpoint")
	}
	if !slices.ContainsFunc(first, func(s bundle.Session) bool { return s.RootSessionID == linktest.CheckpointSessions[0] }) {
		t.Fatalf("evidence = %+v, want the checkpoint's editing session linked", first)
	}
	sameEvidence(t, "first build", first, fullBuild(t, req))
	archives = counter("git.archive")
	got := rebuild(t, req)
	if n := counter("git.archive") - archives; n != 0 {
		t.Errorf("%d checkpoints read again, want 0", n)
	}
	sameEvidence(t, "nothing changed", got, fullBuild(t, req))
	if !reflect.DeepEqual(first, got) {
		t.Errorf("evidence changed:\n%+v\n%+v", first, got)
	}
}

// Builds for several pull requests at once share the digests.
func TestEvidenceBuildsShareDigestsConcurrently(t *testing.T) {
	useEvidenceMemo(t)
	useSessionDigests(t)
	f := linktest.New(t)
	f.AddEditor(t, "bbbbbbbb-0000-4000-8000-000000000001", "08", "unrelated.txt")
	req := evidenceRequest(t, f)
	rebuild(t, req)
	var wg sync.WaitGroup
	results := make([][]bundle.Session, 6)
	for i := range results {
		wg.Go(func() {
			// Other caps are other answers, so none comes from the memo.
			other := req
			other.MaxCommandEventsPerSession = i + 1
			got, err := BuildSessionEvidence(context.Background(), other)
			if err != nil {
				t.Error(err)
			}
			results[i] = got
		})
	}
	wg.Wait()
	for i, got := range results {
		other := req
		other.MaxCommandEventsPerSession = i + 1
		sameEvidence(t, fmt.Sprintf("caps %d", i+1), got, fullBuild(t, other))
	}
}

// A projection added to a digest another build replaced under the same key
// does not count toward what the cache holds.
func TestDigestCacheCountsOnlyTheDigestItKeeps(t *testing.T) {
	c := newDigestCache(1 << 20)
	d1 := &sessionDigest{key: "k", cost: 100}
	d2 := &sessionDigest{key: "k", cost: 100}
	c.put(d1)
	c.put(d2)
	held := c.held
	c.addProjection(d1, &projection{item: bundle.Session{Title: strings.Repeat("x", 1000)}})
	if c.held != held {
		t.Errorf("held = %d after a projection on a replaced digest, want %d", c.held, held)
	}
	c.limit = 0
	c.evict()
	if c.held != 0 || c.order.Len() != 0 {
		t.Errorf("an emptied cache holds %d bytes in %d entries, want 0", c.held, c.order.Len())
	}
}

// forget drops key's digest from c, as an eviction would.
func forget(c *digestCache, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if element, ok := c.entries[key]; ok {
		c.held -= element.Value.(*sessionDigest).cost
		c.order.Remove(element)
		delete(c.entries, key)
	}
}

// A session LoadReusing skipped whose digest is gone by the time linking
// reads it does not fail the build: everything is mapped instead.
func TestEvidenceRecoversFromADigestThatWentMissing(t *testing.T) {
	useEvidenceMemo(t)
	useSessionDigests(t)
	f := linktest.New(t)
	f.AddEditor(t, "bbbbbbbb-0000-4000-8000-000000000001", "08", "unrelated.txt")
	req := evidenceRequest(t, f)
	want := rebuild(t, req)
	previous := newBuildBook
	t.Cleanup(func() { newBuildBook = previous })
	newBuildBook = func(cache *digestCache) *digestBook {
		b := newDigestBook(cache)
		b.afterReuse = func(key string) {
			delete(b.held, key)
			forget(cache, key)
		}
		return b
	}
	remapped, decoded := counter("locallink.session_evidence_remapped"), decodes()
	got := rebuild(t, req)
	if counter("locallink.session_evidence_remapped") == remapped {
		t.Error("the build did not load again")
	}
	if n := decodes() - decoded; n != 2 {
		t.Errorf("%d transcripts decoded, want both", n)
	}
	sameEvidence(t, "a digest went missing", got, want)

	b := newDigestBook(newDigestCache(DefaultDigestBytes))
	if _, ok, needsGraph := b.evidence(sessions.Session{ID: "s", Key: "gone", Unmapped: true}, 1, nil, nil, 1, 1); ok || needsGraph || !b.missing {
		t.Errorf("ok %v, needsGraph %v, missing %v; want not ok and missing", ok, needsGraph, b.missing)
	}
}

func TestDigestCacheEvictsPastItsLimit(t *testing.T) {
	c := newDigestCache(100)
	c.put(&sessionDigest{key: "a", cost: 60})
	c.put(&sessionDigest{key: "b", cost: 60})
	if _, ok := c.get("a"); ok {
		t.Error("the least recently used digest was kept past the limit")
	}
	if _, ok := c.get("b"); !ok {
		t.Error("the newest digest was evicted")
	}
	c.put(&sessionDigest{key: "huge", cost: 200})
	if _, ok := c.get("huge"); ok {
		t.Error("a digest larger than the limit was kept")
	}
	d, _ := c.get("b")
	c.addProjection(d, &projection{item: bundle.Session{Title: strings.Repeat("x", 100)}})
	if _, ok := c.get("b"); ok || c.held != 0 {
		t.Errorf("a digest grown past the limit was kept (held %d)", c.held)
	}
}
