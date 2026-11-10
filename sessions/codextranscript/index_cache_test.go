package codextranscript

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestThreadNameReadsTheIndexAgainOnlyWhenItChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session_index.jsonl")
	write := func(content string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	name := func(id string) string {
		t.Helper()
		got, err := ThreadName(path, id)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	start := time.Now().Add(-time.Hour)
	write(`{"id":"a","thread_name":"first"}`+"\n"+`{"id":"a","thread_name":"second"}`+"\n"+`{"id":"b","thread_name":" "}`+"\n", start)
	if got := name("a"); got != "second" {
		t.Fatalf("a = %q, want the last name", got)
	}
	if got := name("b"); got != "" {
		t.Fatalf("b = %q, want none for a blank name", got)
	}
	// Same size and mtime: the cache answers, not the file.
	write(`{"id":"a","thread_name":"SECOND"}`+"\n"+`{"id":"a","thread_name":"second"}`+"\n"+`{"id":"b","thread_name":" "}`+"\n", start)
	if got := name("a"); got != "second" {
		t.Fatalf("a = %q after an unchanged-stat rewrite, want the cached name", got)
	}
	write(`{"id":"a","thread_name":"third"}`+"\n", start.Add(time.Minute))
	if got := name("a"); got != "third" {
		t.Fatalf("a = %q after a change, want third", got)
	}
}
