package tiledrafts

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/tiles"
)

// recorder counts the changes a store tells, per pull request.
type recorder struct {
	mu      sync.Mutex
	changes []string
}

func (r *recorder) changed(pr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, pr)
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.changes)
}

// A card goes from writing through its check rounds, says why a check
// failed, and goes once its tile is added; each change is told.
func TestACardsPhases(t *testing.T) {
	r := &recorder{}
	s := &Store{Changed: r.changed}
	d := s.Start("chat1", "acme/widgets#42", "  New environment\n variables  ", tiles.SizeHalf, nil)
	if d.Tile != "draft_1" || d.Phase != PhaseWriting || d.Title != "New environment variables" || d.Size != tiles.SizeHalf {
		t.Fatalf("started: %+v", d)
	}
	if got := s.List("acme/widgets#42"); len(got) != 1 || got[0] != d {
		t.Fatalf("listed: %+v", got)
	}
	for round := 1; round <= 2; round++ {
		d, err := s.Checking("chat1", "")
		if err != nil || d.Phase != PhaseChecking || d.Round != round {
			t.Fatalf("round %d: %+v %v", round, d, err)
		}
	}
	d, err := s.Failed("chat1", "draft_1", "Open the baum app to check tiles")
	if err != nil || d.Phase != PhaseFailed || d.Reason == "" || d.Round != 2 {
		t.Fatalf("failed: %+v %v", d, err)
	}
	if d, _ := s.Checking("chat1", ""); d.Phase != PhaseChecking || d.Reason != "" || d.Round != 3 {
		t.Errorf("a check after a failure: %+v", d)
	}
	if _, err := s.Checking("chat2", ""); err != ErrUnknown {
		t.Errorf("another conversation's card: %v", err)
	}
	s.Added("chat1", "draft_1")
	if got := s.List("acme/widgets#42"); len(got) != 0 {
		t.Errorf("after the add: %+v", got)
	}
	if r.count() != 6 {
		t.Errorf("changes told: %d", r.count())
	}
}

// A conversation makes one tile at a time: a new start replaces its card,
// and leaves another conversation's alone.
func TestOneCardPerConversation(t *testing.T) {
	s := &Store{}
	s.Start("chat1", "acme/widgets#42", "First", "", nil)
	s.Start("chat2", "acme/widgets#42", "Theirs", "", nil)
	second := s.Start("chat1", "acme/widgets#7", "Second", "", &tiles.Place{Edge: tiles.EdgeFirst})
	if got := s.List("acme/widgets#42"); len(got) != 1 || got[0].Title != "Theirs" {
		t.Errorf("the first pull request's cards: %+v", got)
	}
	if d, pr, ok := s.Current("chat1", ""); !ok || d != second || pr != "acme/widgets#7" {
		t.Errorf("the current card: %+v %s %v", d, pr, ok)
	}
	if _, _, ok := s.Current("chat1", "draft_1"); ok {
		t.Error("a replaced card is still current")
	}
}

// A turn that ends without the tile added leaves the card unfinished, until
// Linger later; Remove takes it away at once.
func TestAnUnfinishedCardLingersThenGoes(t *testing.T) {
	r := &recorder{}
	var fire func()
	var waited time.Duration
	s := &Store{Changed: r.changed, After: func(d time.Duration, f func()) *time.Timer {
		waited, fire = d, f
		return time.NewTimer(time.Hour)
	}}
	s.Start("chat1", "acme/widgets#42", "Env vars", "", nil)
	s.TurnEnded("chat1")
	got := s.List("acme/widgets#42")
	if len(got) != 1 || got[0].Phase != PhaseUnfinished || waited != Linger {
		t.Fatalf("after the turn: %+v, waits %v", got, waited)
	}
	s.TurnEnded("chat1")
	before := r.count()
	fire()
	if got := s.List("acme/widgets#42"); len(got) != 0 || r.count() != before+1 {
		t.Errorf("after lingering: %+v", got)
	}

	s.Start("chat1", "acme/widgets#42", "Again", "", nil)
	if s.Remove("acme/widgets#42", "draft_9") || !s.Remove("acme/widgets#42", "draft_2") {
		t.Error("remove")
	}
	if got := s.List("acme/widgets#42"); len(got) != 0 {
		t.Errorf("after the remove: %+v", got)
	}
	s.TurnEnded("chat1")
}

// The cards name a cache key that changes with every change of theirs.
func TestKey(t *testing.T) {
	s := &Store{}
	if Key(s.List("pr")) != "" {
		t.Error("no cards have a key")
	}
	s.Start("chat1", "pr", strings.Repeat("x", 200), "", nil)
	first := Key(s.List("pr"))
	if d := s.List("pr")[0]; len([]rune(d.Title)) != MaxTitle {
		t.Errorf("title not bounded: %d", len(d.Title))
	}
	_, _ = s.Checking("chat1", "")
	if first == "" || Key(s.List("pr")) == first {
		t.Error("a check did not change the key")
	}
	var none *Store
	if none.List("pr") != nil {
		t.Error("a nil store lists cards")
	}
}
