package sessions

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/workstats"
	"github.com/baum-ai/baum/sessions/codextranscript"
	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
	collectcodex "github.com/baum-ai/baum/sessions/collector/codex"
	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/vendors"
)

// useTails gives the test its own tail cache of limit bytes.
func useTails(t *testing.T, limit int64) *tailCache {
	t.Helper()
	previous := tails
	tails = newTailCache(limit)
	t.Cleanup(func() { tails = previous })
	return tails
}

type tailCounts struct{ full, appended, lines int }

func (c *tailCache) counts() tailCounts {
	c.mu.Lock()
	defer c.mu.Unlock()
	return tailCounts{c.full, c.appended, c.lines}
}

func (c *tailCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// lines splits data after each newline.
func lines(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n') + 1
		if end == 0 {
			end = len(data)
		}
		out = append(out, data[:end])
		data = data[end:]
	}
	return out
}

func appendTo(t *testing.T, path string, data ...[]byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range data {
		if _, err := f.Write(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	if err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		write(t, filepath.Join(dst, rel), string(data))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// mapsAsWhole checks that mapping s through the tail cache gives what
// decoding every file whole, with every line as read, gives.
func mapsAsWhole(t *testing.T, s collector.Session, step string) {
	t.Helper()
	graph, notes, err := mapSessionWith(s, tails)
	wantGraph, wantNotes, wantErr := mapSessionWith(s, nil)
	if (err == nil) != (wantErr == nil) || err != nil && err.Error() != wantErr.Error() {
		t.Fatalf("%s: error %v, a whole read gives %v", step, err, wantErr)
	}
	if !reflect.DeepEqual(graph, wantGraph) || !slices.Equal(notes, wantNotes) {
		t.Fatalf("%s: the graph or notes differ from a whole read", step)
	}
}

// claudeTree copies the Claude Code fixture tree, its main transcript cut to
// its first keep lines, and returns its session and the lines left out.
func claudeTree(t *testing.T, keep int) (collector.Session, string, [][]byte) {
	t.Helper()
	const id = "11111111-2222-3333-4444-555555555555"
	dir := t.TempDir()
	copyTree(t, filepath.Join("claudetranscript", "testdata", id), filepath.Join(dir, id))
	data, err := os.ReadFile(filepath.Join("claudetranscript", "testdata", id+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	all := lines(data)
	main := filepath.Join(dir, id+".jsonl")
	write(t, main, string(bytes.Join(all[:keep], nil)))
	return collector.Session{Source: vendors.ClaudeCode, SessionID: id, Files: claudecode.SessionFiles(main)}, main, all[keep:]
}

// codexTree copies the Codex collaboration fixture, a root rollout and its
// children, the root cut to its first keep lines, and returns its session and
// the lines left out.
func codexTree(t *testing.T, keep int) (collector.Session, string, [][]byte) {
	t.Helper()
	home := t.TempDir()
	copyTree(t, filepath.Join("codexsession", "testdata", "collaboration_items"), home)
	index, _, err := collectcodex.ScanStore(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range index {
		if entry.Class != codextranscript.ThreadRoot {
			continue
		}
		files, err := collectcodex.Find(index, entry)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(entry.Path)
		if err != nil {
			t.Fatal(err)
		}
		all := lines(data)
		write(t, entry.Path, string(bytes.Join(all[:keep], nil)))
		return collector.Session{Source: vendors.Codex, SessionID: entry.Meta.ID, Files: files}, entry.Path, all[keep:]
	}
	t.Fatal("the fixture has no root rollout")
	return collector.Session{}, "", nil
}

// A transcript that grew is decoded from where the last read stopped: only
// its new lines are decoded, every file of the tree keeps its own offset, and
// the graph is the one a whole read gives.
func TestMapDecodesOnlyAppendedLines(t *testing.T) {
	for _, tt := range []struct {
		name  string
		tree  func(*testing.T, int) (collector.Session, string, [][]byte)
		files int
	}{
		{"claude code", claudeTree, 3},
		{"codex", codexTree, 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := useTails(t, DefaultTailCacheBytes)
			s, main, missing := tt.tree(t, 6)
			mapsAsWhole(t, s, "first read")
			start := c.counts()
			if start.full != tt.files || start.appended != 0 {
				t.Fatalf("first read = %+v, want %d whole files", start, tt.files)
			}

			for len(missing) > 0 {
				n := min(4, len(missing))
				// The decode budget is taken for the appended bytes, and the
				// sidecars, which are always read whole.
				_, cost, _ := mapKey(s)
				sidecars := c.pending(s, cost)
				appendTo(t, main, missing[:n]...)
				_, cost, _ = mapKey(s)
				if want := int64(len(bytes.Join(missing[:n], nil))); c.pending(s, cost)-sidecars != want {
					t.Fatalf("pending = %d of %d bytes, %d before; want the %d appended", c.pending(s, cost), cost, sidecars, want)
				}
				missing = missing[n:]
				before := c.counts()
				mapsAsWhole(t, s, "after growing")
				after := c.counts()
				if after.full != before.full || after.appended != before.appended+tt.files {
					t.Fatalf("after %d lines were appended: %+v, then %+v; want every file continued", n, before, after)
				}
				if decoded := after.lines - before.lines; decoded != n {
					t.Fatalf("after %d lines were appended, %d were decoded", n, decoded)
				}
			}
			if graph, _, err := mapSessionWith(s, tails); err != nil || len(graph.Sessions) != tt.files {
				t.Fatalf("the whole fixture maps to %v, %v; want %d sessions", graph, err, tt.files)
			}
		})
	}
}

// A file that shrank, was replaced or was rewritten is read from its start,
// and maps as a fresh read of it does.
func TestMapRereadsAChangedFileWhole(t *testing.T) {
	c := useTails(t, DefaultTailCacheBytes)
	data, err := os.ReadFile(filepath.Join("codexsession", "testdata", "lifecycle", "basic_rollout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	all := lines(data)
	path := filepath.Join(t.TempDir(), "rollout-2026-06-20T08-00-00-019e9999-1111-7000-8000-aaaabbbbcccc.jsonl")
	s := collector.Session{Source: vendors.Codex, Files: []model.SessionFile{{Path: path, Name: filepath.Base(path), Kind: model.KindTranscript}}}
	write(t, path, string(bytes.Join(all[:10], nil)))
	mapsAsWhole(t, s, "first read")

	restarts := func() map[string]int64 {
		counters := workstats.Take().Counters
		return map[string]int64{
			"shrunk": counters["sessions.tail.restarts.shrunk"], "replaced": counters["sessions.tail.restarts.replaced"],
			"rewritten": counters["sessions.tail.restarts.rewritten"],
		}
	}
	for _, tt := range []struct {
		reason string
		change func()
	}{
		{"shrunk", func() { write(t, path, string(bytes.Join(all[:5], nil))) }},
		{"replaced", func() {
			next := path + ".new"
			write(t, next, string(bytes.Join(all[:12], nil)))
			if err := os.Rename(next, path); err != nil {
				t.Fatal(err)
			}
		}},
		// The same file, longer, with its first line changed in place.
		{"rewritten", func() {
			edited := strings.Replace(string(bytes.Join(all[:15], nil)), `"cwd":"/work/api"`, `"cwd":"/work/web"`, 1)
			f, err := os.OpenFile(path, os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString(edited); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		before, counts := restarts(), c.counts()
		tt.change()
		mapsAsWhole(t, s, tt.reason)
		if after := restarts(); after[tt.reason] != before[tt.reason]+1 {
			t.Errorf("%s: restarts %v, then %v", tt.reason, before, after)
		}
		if got := c.counts(); got.full != counts.full+1 || got.appended != counts.appended {
			t.Errorf("%s: reads %+v, then %+v; want one whole read", tt.reason, counts, got)
		}
	}
}

// A final line still being written is not taken as read: the next read
// decodes it again from its first byte, whole.
func TestMapRereadsATornFinalLine(t *testing.T) {
	c := useTails(t, DefaultTailCacheBytes)
	s, main, missing := claudeTree(t, 10)
	half := len(missing[0]) / 2
	appendTo(t, main, missing[0][:half])
	mapsAsWhole(t, s, "torn")
	graph, _, err := mapSessionWith(s, tails)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.Sessions) == 0 {
		t.Fatal("a torn final line cost the session")
	}

	before := c.counts()
	appendTo(t, main, missing[0][half:], missing[1])
	mapsAsWhole(t, s, "completed")
	if after := c.counts(); after.full != before.full || after.lines-before.lines != 2 {
		t.Errorf("completing a torn line: %+v, then %+v; want the two lines decoded", before, after)
	}
}

// A transcript that has not changed for a while is decoded whole and its
// lines are not kept: it is unlikely to grow.
func TestMapKeepsOnlyChangingTranscripts(t *testing.T) {
	c := useTails(t, DefaultTailCacheBytes)
	s, main, _ := claudeTree(t, 31)
	old := time.Now().Add(-2 * tailActiveFor)
	for _, f := range s.Files {
		if err := os.Chtimes(f.Path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	mapsAsWhole(t, s, "old")
	if c.len() != 0 {
		t.Errorf("kept %d old transcripts", c.len())
	}

	now := time.Now()
	if err := os.Chtimes(main, now, now); err != nil {
		t.Fatal(err)
	}
	mapsAsWhole(t, s, "changed")
	if c.len() != 1 {
		t.Errorf("kept %d transcripts, want the one that changed", c.len())
	}
	SetTailCacheLimit(0)
	if c.len() != 0 || c.held != 0 {
		t.Errorf("a zero limit left %d entries holding %d bytes", c.len(), c.held)
	}
}

// A tree larger than the cache does not push out the files read before it
// in the same read: what fits is kept and continued next time, and the rest
// is decoded whole each time.
func TestTailCacheKeepsWhatFitsOfALargeTree(t *testing.T) {
	c := useTails(t, DefaultTailCacheBytes)
	s, main, missing := claudeTree(t, 25)
	mapsAsWhole(t, s, "measure")
	c.mu.Lock()
	var mainCost int64
	for _, element := range c.entries {
		if e := element.Value.(*tailEntry); strings.Contains(e.key, main) {
			mainCost = e.cost
		}
	}
	c.mu.Unlock()
	if mainCost == 0 {
		t.Fatal("the main transcript was not kept")
	}

	c = useTails(t, mainCost)
	mapsAsWhole(t, s, "first read")
	if c.len() != 1 {
		t.Fatalf("kept %d files, want the main transcript alone", c.len())
	}
	appendTo(t, main, missing[0])
	before := c.counts()
	mapsAsWhole(t, s, "grown")
	if after := c.counts(); after.appended != before.appended+1 || after.full != before.full+2 {
		t.Errorf("reads %+v, then %+v; want the main transcript continued and the two subagents read whole", before, after)
	}
}

// The mapping of a session's files as they were is dropped once they grew
// and were mapped again: it will not be asked for again.
func TestMapReplacesTheMappingOfAGrownSession(t *testing.T) {
	useTails(t, DefaultTailCacheBytes)
	c := useCache(t, DefaultMapCacheBytes)
	s, main, missing := claudeTree(t, 20)
	if _, _, err := Map(s); err != nil {
		t.Fatal(err)
	}
	appendTo(t, main, missing...)
	if _, _, err := Map(s); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.order.Len() != 1 || len(c.sessions) != 1 || c.transcripts <= 0 || c.held <= 0 {
		t.Errorf("cache holds %d mappings, %d sessions, %d transcript bytes; want the latest mapping alone", c.order.Len(), len(c.sessions), c.transcripts)
	}
}
