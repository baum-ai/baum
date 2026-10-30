package uistate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
)

const draft = `{"seen":["first"],"notes":{"first":"Keep this draft"},"note":"Overall"}`
const inbox = `{"day":"2026-10-30","seen":{},"snoozed":{"acme/demo#7":1793450000000},"held":{"acme/demo#9":"abc"}}`

func store(t *testing.T) *Store {
	t.Helper()
	return &Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
}
func save(t *testing.T, s *Store, kind, key string, revision int, id, value string) Record {
	t.Helper()
	record, err := s.Replace(kind, key, revision, id, json.RawMessage(value))
	if err != nil {
		t.Fatal(err)
	}
	return record
}
func TestPersistenceAndRevisionAcrossInstances(t *testing.T) {
	s := store(t)
	key := "acme/demo#7@v1"
	if record, err := s.Read("walkthrough", key); err != nil || record.Revision != 0 || string(record.Value) != "null" {
		t.Fatalf("missing: %+v %v", record, err)
	}
	save(t, s, "walkthrough", key, 0, "mutation-00000001", draft)
	save(t, s, "inbox", "", 0, "mutation-00000002", inbox)
	reopened := &Store{Path: s.Path}
	for _, test := range []struct{ kind, key, value string }{{"walkthrough", key, draft}, {"inbox", "", inbox}} {
		got, err := reopened.Read(test.kind, test.key)
		if err != nil || got.Revision != 1 || string(got.Value) != test.value {
			t.Fatalf("reopen: %+v %v", got, err)
		}
	}
	_, err := reopened.Replace("walkthrough", key, 0, "mutation-00000003", json.RawMessage(draft))
	var stale *StaleError
	if !errors.As(err, &stale) || stale.Current.Revision != 1 {
		t.Fatalf("stale: %v", err)
	}
}
func TestCommittedMutationRetryDoesNotOverwriteLaterChanges(t *testing.T) {
	s := store(t)
	key := "acme/demo#7@v1"
	save(t, s, "walkthrough", key, 0, "mutation-00000001", draft)
	later := `{"seen":["first","second"],"notes":{"first":"Keep this draft","second":"Another window"},"note":"Overall"}`
	save(t, s, "walkthrough", key, 1, "mutation-00000002", later)
	// First response was lost; retry its original revision and id after another edit.
	got := save(t, s, "walkthrough", key, 0, "mutation-00000001", draft)
	if got.Revision != 2 || string(got.Value) != later {
		t.Fatalf("retry changed data: %+v", got)
	}
}
func TestConcurrentCASAndIndependentReviews(t *testing.T) {
	s := store(t)
	key := "acme/demo#7@v1"
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for i := range 2 {
		wg.Go(func() {
			_, err := (&Store{Path: s.Path}).Replace("walkthrough", key, 0, fmt.Sprintf("mutation-%08d", i), json.RawMessage(draft))
			outcomes <- err
		})
	}
	wg.Wait()
	close(outcomes)
	successes, stales := 0, 0
	for err := range outcomes {
		var stale *StaleError
		switch {
		case err == nil:
			successes++
		case errors.As(err, &stale):
			stales++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || stales != 1 {
		t.Fatalf("successes %d stale %d", successes, stales)
	}
	outcomes = make(chan error, 2)
	for i := range 2 {
		wg.Go(func() {
			_, err := (&Store{Path: s.Path}).Replace("walkthrough", fmt.Sprintf("acme/demo#%d@v1", i+8), 0, fmt.Sprintf("mutation-other-%08d", i), json.RawMessage(draft))
			outcomes <- err
		})
	}
	wg.Wait()
	close(outcomes)
	for err := range outcomes {
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestRetentionKeepsFortyMostRecentlySavedVersions(t *testing.T) {
	s := store(t)
	now := time.Unix(1000, 0)
	s.Now = func() time.Time { return now }
	for i := range 42 {
		now = now.Add(time.Second)
		save(t, s, "walkthrough", fmt.Sprintf("acme/demo#7@v%d", i+1), 0, fmt.Sprintf("mutation-%08d", i), draft)
		if i == 39 {
			now = now.Add(time.Second)
			save(t, s, "walkthrough", "acme/demo#7@v1", 1, "mutation-refresh", draft)
		}
	}
	if got, err := s.Read("walkthrough", "acme/demo#7@v1"); err != nil || got.Revision != 2 {
		t.Fatalf("recently edited: %+v %v", got, err)
	}
	for _, key := range []string{"acme/demo#7@v2", "acme/demo#7@v3"} {
		if got, err := s.Read("walkthrough", key); err != nil || got.Revision != 0 {
			t.Fatalf("old version: %+v %v", got, err)
		}
	}
}
func TestCorruptionAndInvalidWritesLeaveStoredBytesUntouched(t *testing.T) {
	for _, raw := range []string{"null", "[]", "7", "broken", `{"acme/demo#7@v1":null}`} {
		t.Run(raw, func(t *testing.T) {
			s := store(t)
			if err := appdb.Write(s.Path, "ui", "walkthrough", []byte(raw)); err != nil {
				t.Fatal(err)
			}
			_, err := s.Read("walkthrough", "acme/demo#7@v1")
			if !errors.Is(err, ErrUnreadable) {
				t.Fatalf("read: %v", err)
			}
			_, err = s.Replace("walkthrough", "acme/demo#7@v1", 0, "mutation-00000001", json.RawMessage(draft))
			if !errors.Is(err, ErrUnreadable) {
				t.Fatalf("write: %v", err)
			}
			got, err := appdb.Read(s.Path, "ui", "walkthrough")
			if err != nil || !bytes.Equal(got, []byte(raw)) {
				t.Fatalf("corruption overwritten: %q %v", got, err)
			}
		})
	}
	s := store(t)
	save(t, s, "inbox", "", 0, "mutation-00000001", inbox)
	if _, err := s.Replace("inbox", "", 1, "mutation-00000002", json.RawMessage(`null`)); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if got, err := s.Read("inbox", ""); err != nil || string(got.Value) != inbox {
		t.Fatalf("invalid write: %+v %v", got, err)
	}
}

func TestRetentionUsesSaveOrderWhenClockRepeatsOrMovesBackwards(t *testing.T) {
	s := store(t)
	now := time.Unix(2000, 0)
	s.Now = func() time.Time { return now }
	for i := range 40 {
		save(t, s, "walkthrough", fmt.Sprintf("acme/demo#%d@v1", i+1), 0, fmt.Sprintf("mutation-%08d", i), draft)
	}
	now = time.Unix(1000, 0)
	save(t, s, "walkthrough", "acme/demo#1@v1", 1, "mutation-old-edit", draft)
	save(t, s, "walkthrough", "acme/demo#41@v1", 0, "mutation-new-key1", draft)
	for _, key := range []string{"acme/demo#1@v1", "acme/demo#41@v1"} {
		if got, err := s.Read("walkthrough", key); err != nil || got.Revision == 0 {
			t.Fatalf("newly saved draft pruned: %s %+v %v", key, got, err)
		}
	}
	if got, err := s.Read("walkthrough", "acme/demo#2@v1"); err != nil || got.Revision != 0 {
		t.Fatalf("oldest saved draft retained: %+v %v", got, err)
	}
}
