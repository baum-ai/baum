package sessions

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
)

// rewriteInPlace overwrites path with data without changing its inode, and
// moves its modification time forward so the mapping cache key changes.
func rewriteInPlace(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

func rewrites() int64 {
	return workstats.Take().Counters["sessions.tail.restarts.rewritten"]
}

// editMiddle changes one timestamp in the middle of the Claude Code fixture,
// away from its first and last few KiB, keeping the size.
func editMiddle(t *testing.T, data []byte) []byte {
	t.Helper()
	edited := bytes.Replace(data, []byte(`"uuid":"u08","parentUuid":"u07","timestamp":"2026-07-30T09:10:00.000Z"`),
		[]byte(`"uuid":"u08","parentUuid":"u07","timestamp":"2026-07-30T09:10:59.000Z"`), 1)
	if bytes.Equal(edited, data) || len(edited) != len(data) {
		t.Fatal("the edit did not apply")
	}
	return edited
}

// A same-size rewrite in the middle of a file, such as a scrubbed key, is
// read whole again.
func TestTailRereadsASameSizeMiddleRewrite(t *testing.T) {
	useTails(t, DefaultTailCacheBytes)
	s, main, _ := claudeTree(t, 32)
	mapsAsWhole(t, s, "first read")
	data, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	before := rewrites()
	rewriteInPlace(t, main, editMiddle(t, data))
	mapsAsWhole(t, s, "same-size middle rewrite")
	if rewrites() != before+1 {
		t.Error("the rewrite was not detected")
	}
}

// A rewrite in the middle of a file that also grew is read whole again, not
// continued from the old offset.
func TestTailRereadsAGrowingMiddleRewrite(t *testing.T) {
	useTails(t, DefaultTailCacheBytes)
	s, main, missing := claudeTree(t, 28)
	mapsAsWhole(t, s, "first read")
	data, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	before := rewrites()
	rewriteInPlace(t, main, append(editMiddle(t, data), bytes.Join(missing, nil)...))
	mapsAsWhole(t, s, "growing middle rewrite")
	if rewrites() != before+1 {
		t.Error("the rewrite was not detected")
	}
}

// A file that kept its size but has a new modification time is read whole
// again: nothing was appended, so something was rewritten.
func TestTailRereadsAFileTouchedAtTheSameSize(t *testing.T) {
	c := useTails(t, DefaultTailCacheBytes)
	s, main, _ := claudeTree(t, 20)
	mapsAsWhole(t, s, "first read")
	before, counts := rewrites(), c.counts()
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(main, later, later); err != nil {
		t.Fatal(err)
	}
	mapsAsWhole(t, s, "touched")
	if rewrites() != before+1 || c.counts().full != counts.full+1 {
		t.Error("a file touched at the same size was continued")
	}
}

// CRLF line endings, appended with the \n of a line arriving later than its
// \r.
func TestTailReadsCRLFAppendedInPieces(t *testing.T) {
	useTails(t, DefaultTailCacheBytes)
	s, main, missing := claudeTree(t, 5)
	data, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	write(t, main, strings.ReplaceAll(string(data), "\n", "\r\n"))
	mapsAsWhole(t, s, "crlf first")
	for _, line := range missing {
		crlf := bytes.ReplaceAll(line, []byte("\n"), []byte("\r\n"))
		cut := len(crlf) - 1
		appendTo(t, main, crlf[:cut])
		mapsAsWhole(t, s, "crlf before its newline")
		appendTo(t, main, crlf[cut:])
		mapsAsWhole(t, s, "crlf completed")
	}
}

// A multibyte character split across appends.
func TestTailReadsAUTF8CharacterSplitAcrossAppends(t *testing.T) {
	useTails(t, DefaultTailCacheBytes)
	s, main, missing := claudeTree(t, 5)
	mapsAsWhole(t, s, "first")
	for _, line := range missing {
		l := bytes.Replace(line, []byte(`"sessionId"`), []byte(`"x":"€😀","sessionId"`), 1)
		i := bytes.Index(l, []byte("😀")) + 2
		appendTo(t, main, l[:i])
		mapsAsWhole(t, s, "split character")
		appendTo(t, main, l[i:])
		mapsAsWhole(t, s, "joined character")
	}
}

// Subagent files disappearing and reappearing between reads.
func TestTailFollowsSubagentsThatComeAndGo(t *testing.T) {
	useTails(t, DefaultTailCacheBytes)
	s, main, missing := claudeTree(t, 32)
	mapsAsWhole(t, s, "first")
	dir := filepath.Join(filepath.Dir(main), "11111111-2222-3333-4444-555555555555", "subagents")
	a2, err := os.ReadFile(filepath.Join(dir, "agent-a2.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	meta, err := os.ReadFile(filepath.Join(dir, "agent-a2.meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent-a2.jsonl", "agent-a2.meta.json"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	s.Files = claudecode.SessionFiles(main)
	mapsAsWhole(t, s, "subagent gone")
	write(t, filepath.Join(dir, "agent-a2.meta.json"), string(meta))
	half := lines(a2)
	write(t, filepath.Join(dir, "agent-a2.jsonl"), string(bytes.Join(half[:len(half)/2], nil)))
	s.Files = claudecode.SessionFiles(main)
	mapsAsWhole(t, s, "subagent half back")
	appendTo(t, filepath.Join(dir, "agent-a2.jsonl"), half[len(half)/2:]...)
	appendTo(t, main, missing...)
	mapsAsWhole(t, s, "subagent whole")
}

// Concurrent maps of one session while it grows, with lines written in two
// halves. Run with -race.
func TestTailMapsConcurrentlyWhileGrowing(t *testing.T) {
	useTails(t, DefaultTailCacheBytes)
	s, main, missing := claudeTree(t, 6)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, _, err := mapSessionWith(s, tails); err != nil {
					t.Errorf("concurrent map: %v", err)
					return
				}
			}
		}()
	}
	for _, line := range missing {
		half := len(line) / 2
		appendTo(t, main, line[:half])
		time.Sleep(2 * time.Millisecond)
		appendTo(t, main, line[half:])
		time.Sleep(2 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	mapsAsWhole(t, s, "after concurrent growth")
}

// Concurrent maps through a cache too small to keep anything.
func TestTailMapsConcurrentlyThroughATinyCache(t *testing.T) {
	useTails(t, 1)
	s, main, missing := claudeTree(t, 6)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 30 {
				if _, _, err := mapSessionWith(s, tails); err != nil {
					t.Errorf("map: %v", err)
					return
				}
			}
		}()
	}
	appendTo(t, main, missing...)
	wg.Wait()
	mapsAsWhole(t, s, "tiny")
}

// Lines of files unread for tailIdleFor are released on the next read of any
// file, though nothing needed the room.
func TestTailReleasesIdleFiles(t *testing.T) {
	c := useTails(t, DefaultTailCacheBytes)
	s, _, _ := claudeTree(t, 20)
	mapsAsWhole(t, s, "first read")
	if c.len() != 3 {
		t.Fatalf("kept %d files, want 3", c.len())
	}
	c.mu.Lock()
	for element := c.order.Front(); element != nil; element = element.Next() {
		element.Value.(*tailEntry).used = time.Now().Add(-2 * tailIdleFor)
	}
	c.mu.Unlock()

	other, _, _ := codexTree(t, 6)
	mapsAsWhole(t, other, "another session")
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, element := range c.entries {
		if e := element.Value.(*tailEntry); strings.HasPrefix(e.key, "claude") {
			t.Errorf("idle file %s still kept", e.path)
		}
	}
}
