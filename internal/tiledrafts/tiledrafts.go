// Package tiledrafts keeps the pending cards of tiles being made in the
// session panel: when a chat starts making a tile for the pull request page
// the person has open (start_tile), the page shows a card where the tile
// will stand, saying how far it got, until the tile is added or the card is
// removed. Cards live in the daemon's memory only, one per conversation.
package tiledrafts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/tiles"
)

// The phases of a card.
const (
	// PhaseWriting is a tile started and not yet checked.
	PhaseWriting = "writing"
	// PhaseChecking is a tile whose render check runs, or ran without
	// passing; Round counts the checks.
	PhaseChecking = "checking"
	// PhaseFailed is a check the host could not run or that hung; Reason
	// says why.
	PhaseFailed = "failed"
	// PhaseUnfinished is a card whose turn ended without the tile being
	// added. It goes after Linger.
	PhaseUnfinished = "unfinished"
)

// Linger is how long a card stays after its turn ended without the tile
// being added.
const Linger = 10 * time.Minute

// MaxTitle bounds a card's title, in characters.
const MaxTitle = 80

// Draft is a pending card as the tile route serves it.
type Draft struct {
	Tile   string       `json:"tile"`
	Title  string       `json:"title"`
	Size   tiles.Size   `json:"size,omitempty"`
	To     *tiles.Place `json:"to,omitempty"`
	Phase  string       `json:"phase"`
	Round  int          `json:"round,omitempty"`
	Reason string       `json:"reason,omitempty"`
}

// ErrUnknown is a card that is not there: never started, added, removed or
// gone.
var ErrUnknown = errors.New("no such pending tile")

type card struct {
	Draft
	owner string
	pr    string
	timer *time.Timer
}

// Store keeps the cards. The zero value is ready; it is safe for concurrent
// use.
type Store struct {
	// Changed is told the pull request whose cards changed (its
	// tilelayout.Key); nil tells nobody.
	Changed func(pr string)
	// After runs f after d; nil is time.AfterFunc. Tests replace it.
	After func(d time.Duration, f func()) *time.Timer

	mu    sync.Mutex
	cards []*card
	next  int
}

func (s *Store) changed(pr string) {
	if s.Changed != nil && pr != "" {
		s.Changed(pr)
	}
}

func (s *Store) after(d time.Duration, f func()) *time.Timer {
	if s.After != nil {
		return s.After(d, f)
	}
	return time.AfterFunc(d, f)
}

// Start shows a new card on a pull request (its tilelayout.Key) for owner,
// a conversation's MCP consumer, and returns it. An earlier card of the same
// owner goes: a conversation makes one tile at a time.
func (s *Store) Start(owner, pr, title string, size tiles.Size, to *tiles.Place) Draft {
	title = strings.Join(strings.Fields(title), " ")
	if r := []rune(title); len(r) > MaxTitle {
		title = string(r[:MaxTitle])
	}
	s.mu.Lock()
	var gone []string
	s.cards = slices.DeleteFunc(s.cards, func(c *card) bool {
		if c.owner != owner {
			return false
		}
		stop(c)
		gone = append(gone, c.pr)
		return true
	})
	s.next++
	c := &card{Draft: Draft{Tile: fmt.Sprintf("draft_%d", s.next), Title: title, Size: size, To: to, Phase: PhaseWriting}, owner: owner, pr: pr}
	s.cards = append(s.cards, c)
	out := c.Draft
	s.mu.Unlock()
	for _, p := range gone {
		if p != pr {
			s.changed(p)
		}
	}
	s.changed(pr)
	return out
}

// Current is owner's card, or with tile set, owner's card of that name.
func (s *Store) Current(owner, tile string) (Draft, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.find(owner, tile)
	if c == nil {
		return Draft{}, "", false
	}
	return c.Draft, c.pr, true
}

func (s *Store) find(owner, tile string) *card {
	for _, c := range s.cards {
		if c.owner == owner && (tile == "" || c.Tile == tile) {
			return c
		}
	}
	return nil
}

// update changes owner's card (tile, or the current one) and tells the page.
func (s *Store) update(owner, tile string, change func(c *card)) (Draft, error) {
	s.mu.Lock()
	c := s.find(owner, tile)
	if c == nil {
		s.mu.Unlock()
		return Draft{}, ErrUnknown
	}
	change(c)
	out, pr := c.Draft, c.pr
	s.mu.Unlock()
	s.changed(pr)
	return out, nil
}

// Checking counts a render check of owner's card.
func (s *Store) Checking(owner, tile string) (Draft, error) {
	return s.update(owner, tile, func(c *card) {
		stop(c)
		c.Round++
		c.Phase, c.Reason = PhaseChecking, ""
	})
}

// Failed says a check of owner's card could not run or hung, and why.
func (s *Store) Failed(owner, tile, reason string) (Draft, error) {
	return s.update(owner, tile, func(c *card) {
		stop(c)
		c.Phase, c.Reason = PhaseFailed, reason
	})
}

// Added takes owner's card away: its tile is on the page now.
func (s *Store) Added(owner, tile string) {
	s.mu.Lock()
	c := s.find(owner, tile)
	if c != nil {
		stop(c)
		s.cards = slices.DeleteFunc(s.cards, func(x *card) bool { return x == c })
	}
	s.mu.Unlock()
	if c != nil {
		s.changed(c.pr)
	}
}

// Remove takes a card off a pull request's page, the person's Remove. It
// reports false when there was none.
func (s *Store) Remove(pr, tile string) bool {
	s.mu.Lock()
	i := slices.IndexFunc(s.cards, func(c *card) bool { return c.pr == pr && c.Tile == tile })
	if i < 0 {
		s.mu.Unlock()
		return false
	}
	stop(s.cards[i])
	s.cards = slices.Delete(s.cards, i, i+1)
	s.mu.Unlock()
	s.changed(pr)
	return true
}

// TurnEnded marks owner's card unfinished, its turn having ended without
// the tile being added; it goes Linger later.
func (s *Store) TurnEnded(owner string) {
	s.mu.Lock()
	c := s.find(owner, "")
	if c == nil || c.Phase == PhaseUnfinished {
		s.mu.Unlock()
		return
	}
	c.Phase = PhaseUnfinished
	stop(c)
	c.timer = s.after(Linger, func() {
		s.mu.Lock()
		i := slices.Index(s.cards, c)
		if i >= 0 && c.Phase == PhaseUnfinished {
			s.cards = slices.Delete(s.cards, i, i+1)
		}
		s.mu.Unlock()
		if i >= 0 {
			s.changed(c.pr)
		}
	})
	pr := c.pr
	s.mu.Unlock()
	s.changed(pr)
}

func stop(c *card) {
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}

// List returns a pull request's cards, oldest first.
func (s *Store) List(pr string) []Draft {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Draft
	for _, c := range s.cards {
		if c.pr == pr {
			out = append(out, c.Draft)
		}
	}
	return out
}

// Key names a pull request's cards for a cache key: empty for none.
func Key(drafts []Draft) string {
	if len(drafts) == 0 {
		return ""
	}
	encoded, err := json.Marshal(drafts)
	if err != nil {
		return "unencodable"
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
