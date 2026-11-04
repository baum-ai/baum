package sessions

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/model"
	"github.com/baum-ai/baum/sessions/vendors"
)

// useCache gives the test its own map cache of limit bytes.
func useCache(t *testing.T, limit int64) *mapCache {
	t.Helper()
	previous := cache
	cache = newMapCache(limit)
	t.Cleanup(func() { cache = previous })
	return cache
}

func (c *mapCache) missCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.misses
}

func loadHome(t *testing.T, fx fixture) []Session {
	t.Helper()
	opts := fx.options()
	opts.Sources = []vendors.Slug{vendors.ClaudeCode, vendors.Codex}
	found, report, err := Load(context.Background(), fx.repo, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Failures) != 0 {
		t.Fatalf("failures = %+v", report.Failures)
	}
	return found
}

func TestMapReadsUnchangedSessionsOnce(t *testing.T) {
	fx := newFixture(t)
	c := useCache(t, DefaultMapCacheBytes)

	first := loadHome(t, fx)
	if len(first) != 2 {
		t.Fatalf("sessions = %d, want the Claude Code and Codex ones", len(first))
	}
	mapped := c.missCount()
	if mapped != 2 {
		t.Fatalf("first load mapped %d sessions, want 2", mapped)
	}

	second := loadHome(t, fx)
	if got := c.missCount(); got != mapped {
		t.Errorf("an unchanged store was mapped again: %d mappings, want %d", got, mapped)
	}
	for i := range first {
		if first[i].Graph != second[i].Graph {
			t.Errorf("session %s: the second load did not reuse the mapped graph", first[i].ID)
		}
	}

	// A rollout that grew is read again, and only it.
	rollouts, err := filepath.Glob(filepath.Join(fx.codexDir, "*", "*", "*", "rollout-*.jsonl"))
	if err != nil || len(rollouts) != 1 {
		t.Fatalf("rollouts = %v, %v", rollouts, err)
	}
	f, err := os.OpenFile(rollouts[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"timestamp":"2026-07-01T10:00:09.000Z","type":"event_msg","payload":{"type":"user_message","message":"again"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	third := loadHome(t, fx)
	if got := c.missCount(); got != mapped+1 {
		t.Errorf("after one rollout grew: %d mappings, want %d", got, mapped+1)
	}
	for i := range third {
		changed := third[i].Source == vendors.Codex
		if reused := third[i].Graph == first[i].Graph; reused == changed {
			t.Errorf("session %s (%s): graph reused = %v", third[i].ID, third[i].Source, reused)
		}
	}
}

func TestMapCachesFailures(t *testing.T) {
	c := useCache(t, DefaultMapCacheBytes)
	path := filepath.Join(t.TempDir(), "broken.jsonl")
	write(t, path, "{not json\n")
	s := collector.Session{Source: vendors.Codex, Files: []model.SessionFile{{Path: path, Name: filepath.Base(path), Kind: model.KindTranscript}}}
	for range 2 {
		if _, notes, err := Map(s); err == nil || notes != nil {
			t.Fatalf("Map = notes %v, err %v; want an error and no notes", notes, err)
		}
	}
	if got := c.missCount(); got != 1 {
		t.Errorf("an unchanged broken file was read %d times, want 1", got)
	}
}

func TestMapKeepsDiscoveryWarningsOutOfTheCache(t *testing.T) {
	useCache(t, DefaultMapCacheBytes)
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	write(t, path, `{"timestamp":"2026-07-01T10:00:00.000Z","type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-000000000002","cwd":"/repo","cli_version":"0.69.0","model_provider":"openai"}}`+"\n")
	files := []model.SessionFile{{Path: path, Name: filepath.Base(path), Kind: model.KindTranscript}}
	_, first, err := Map(collector.Session{Source: vendors.Codex, Files: files, Warnings: []string{"one"}})
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := Map(collector.Session{Source: vendors.Codex, Files: files, Warnings: []string{"two"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 || first[0] != "one" || len(second) == 0 || second[0] != "two" {
		t.Errorf("notes = %q then %q; each must lead with its own discovery warning", first, second)
	}
}

func TestMapCacheLimit(t *testing.T) {
	fx := newFixture(t)
	c := useCache(t, 1)
	loadHome(t, fx)
	loadHome(t, fx)
	if got := c.missCount(); got != 4 {
		t.Errorf("sessions larger than the limit: %d mappings over two loads, want 4", got)
	}

	c = useCache(t, DefaultMapCacheBytes)
	loadHome(t, fx)
	SetMapCacheLimit(0)
	if c.order.Len() != 0 || c.held != 0 {
		t.Errorf("a zero limit left %d entries holding %d bytes", c.order.Len(), c.held)
	}
}

func TestMapKeyFollowsModificationTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	write(t, path, "same size\n")
	s := collector.Session{Source: vendors.Codex, Files: []model.SessionFile{{Path: path, Name: "rollout.jsonl", Kind: model.KindTranscript}}}
	before, _, ok := mapKey(s)
	if !ok {
		t.Fatal("no key for a readable file")
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if after, _, _ := mapKey(s); after == before {
		t.Error("a rewritten file of the same size kept its key")
	}
	s.Files[0].Path = filepath.Join(t.TempDir(), "missing.jsonl")
	if _, _, ok := mapKey(s); ok {
		t.Error("a missing file must be mapped without the cache")
	}
}
