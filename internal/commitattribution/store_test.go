package commitattribution

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
)

func observation() Observation {
	return Observation{Repository: "Acme/Widgets", CommitSHA: strings.Repeat("a", 40), Agent: "claude-code", SessionID: "session-1", Kind: Commit, EventID: "event-1", At: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)}
}

func TestDurableObservationsAreScopedAndIdempotent(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
	store := &Store{Path: path}
	o := observation()
	push := o
	push.Kind = Push
	push.EventID = "push-1"
	peer := o
	peer.Repository = "another/widgets"
	for range 2 {
		if err := store.Record([]Observation{o, push, peer}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := (&Store{Path: path}).Lookup(context.Background(), []string{"ACME/WIDGETS"}, []string{o.CommitSHA})
	if err != nil || len(got) != 2 {
		t.Fatalf("observations: %+v %v", got, err)
	}
	if got[0].Agent != "claude_code" || got[0].Kind != Commit || got[1].Kind != Push || !got[0].At.Equal(o.At) {
		t.Fatalf("provenance: %+v", got)
	}
	if other, err := store.Lookup(context.Background(), []string{"elsewhere/widgets"}, []string{o.CommitSHA}); err != nil || len(other) != 0 {
		t.Fatalf("wrong repository: %+v %v", other, err)
	}
	if other, err := store.Lookup(context.Background(), []string{"acme/widgets"}, []string{strings.Repeat("b", 40)}); err != nil || len(other) != 0 {
		t.Fatalf("wrong commit: %+v %v", other, err)
	}
}

func TestConcurrentRecordDoesNotLoseSessions(t *testing.T) {
	path := filepath.Join(appdbtest.TempDir(t), "app.sqlite")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := observation()
			o.SessionID = fmt.Sprintf("session-%d", i)
			o.EventID = fmt.Sprintf("event-%d", i)
			if err := (&Store{Path: path}).Record([]Observation{o}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	o := observation()
	got, err := (&Store{Path: path}).Lookup(context.Background(), []string{"acme/widgets"}, []string{o.CommitSHA})
	if err != nil || len(got) != 8 {
		t.Fatalf("concurrent records: %+v %v", got, err)
	}
}

func TestMissingDatabaseIsNotCreatedByLookup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "app.sqlite")
	got, err := (&Store{Path: path}).Lookup(context.Background(), []string{"acme/widgets"}, []string{observation().CommitSHA})
	if err != nil || len(got) != 0 {
		t.Fatalf("missing: %+v %v", got, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("read created database: %v", err)
	}
}

func TestRecordRejectsInvalidObservations(t *testing.T) {
	store := &Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
	for _, change := range []func(*Observation){func(o *Observation) { o.CommitSHA = "abc" }, func(o *Observation) { o.CommitSHA = strings.Repeat("0", 40) }, func(o *Observation) { o.Kind = "checkout" }, func(o *Observation) { o.EventID = "" }, func(o *Observation) { o.SessionID = "" }} {
		o := observation()
		change(&o)
		if err := store.Record([]Observation{o}); err == nil {
			t.Fatalf("accepted: %+v", o)
		}
	}
}
