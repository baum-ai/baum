package background

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/repos"
)

// Each common git directory is reconciled on the first pass, again on the
// next pass while a bound left work, an hour after a failure, and otherwise
// once a day; none of it reaches the repositories' state.
func TestSyncRetainsEachCommonDirectoryOnItsSchedule(t *testing.T) {
	now := time.Now()
	clones := registry{
		{Repository: "acme/app", CommonDir: "one", Available: true}, {Repository: "acme/app", CommonDir: "one", Available: true},
		{Repository: "acme/lib", CommonDir: "one", Available: true}, {Repository: "acme/lib", CommonDir: "two", Available: true},
		{Repository: "acme/lib", CommonDir: "gone"},
	}
	var calls []string
	more := map[string]bool{"one": true}
	fail := map[string]bool{"two": true}
	s := &Service{Registry: clones, Discovery: &fakeDiscovery{}, Now: func() time.Time { return now },
		Saved: func(context.Context, repos.Clone) ([]int, error) { return nil, nil },
		Refresh: func(context.Context, repos.Clone, int) (discovery.Observation, error) {
			return discovery.Observation{}, nil
		},
		Retain: func(_ context.Context, clone repos.Clone) (bool, error) {
			calls = append(calls, clone.CommonDir)
			if fail[clone.CommonDir] {
				return false, errors.New("clone is gone")
			}
			return more[clone.CommonDir], nil
		}}
	pass := func(want ...string) {
		t.Helper()
		calls = nil
		s.Sync(context.Background())
		if !slices.Equal(calls, want) {
			t.Fatalf("retained %v, want %v", calls, want)
		}
	}
	pass("one", "two")
	for _, name := range []string{"acme/app", "acme/lib"} {
		if state := s.state(name); state.Error != "" {
			t.Fatalf("%s state = %+v", name, state)
		}
	}
	more["one"] = false
	now = now.Add(time.Minute)
	pass("one")
	now = now.Add(time.Minute)
	pass()
	fail["two"] = false
	now = now.Add(time.Hour)
	pass("two")
	now = now.Add(23 * time.Hour)
	pass("one")
	now = now.Add(time.Hour)
	pass("two")
}

func TestRetentionSkipsACloneThatIsGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "removed")
	more, err := Retention{}.Retain(context.Background(), repos.Clone{ID: "c1", Path: dir, CommonDir: filepath.Join(dir, ".git"), Available: true})
	if more || err == nil {
		t.Fatalf("Retain = %v, %v", more, err)
	}
}
