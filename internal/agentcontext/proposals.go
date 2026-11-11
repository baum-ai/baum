package agentcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// Proposal is a change an agent suggests to something a page edits. Nothing
// changes until the user saves it.
type Proposal struct {
	ID string `json:"id"`
	// Page is the page whose state it changes, Target what on that page
	// (a page's own name for it, such as a prompt's key).
	Page   string `json:"page"`
	Target string `json:"target"`
	// Before is the target's value when the proposal was recorded, and After
	// the value proposed, in the page's own encoding. An agent may send the
	// Before it read; Propose refuses it when the target has changed since,
	// and records the value the proposer read instead.
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after"`
	// Revision is the target's revision Before was read at. Propose sets it
	// from the proposer; whatever the agent sent is ignored.
	Revision string `json:"revision"`
	// Context is the page the consumer had open when it proposed, which the
	// target must still belong to when the user saves.
	Context PageContext `json:"context"`
}

// Proposer is how one page takes proposals. Every method gets the page
// context the proposal was made on, so it can refuse a target that does not
// belong to what that page shows (another pull request's item on a pull
// request's page).
type Proposer interface {
	// Current reads the target's value and revision.
	Current(ctx context.Context, page PageContext, target string) (value json.RawMessage, revision string, err error)
	// Validate refuses a proposal the page would not save.
	Validate(ctx context.Context, page PageContext, p Proposal) error
	// Apply saves p.After when the target is still at revision, comparing
	// and writing in one transaction (as appdb.Update does with its
	// revision); otherwise it changes nothing and returns ErrStale. It is the
	// same change the page's own save makes.
	Apply(ctx context.Context, page PageContext, p Proposal, revision string) error
}

// A consumer holds at most maxProposals proposals; a new one past that is
// refused until the user saves or dismisses one.
const maxProposals = 32

var (
	ErrReadOnly        = errors.New("this agent only reads: it cannot propose changes")
	ErrNotCurrentPage  = errors.New("a proposal must be for the page the user has open")
	ErrNoProposer      = errors.New("this page takes no proposals")
	ErrUnknownProposal = errors.New("unknown proposal")
	// ErrStale is a target that changed since it was read.
	ErrStale            = errors.New("the target changed since it was read; nothing was saved")
	errTooManyProposals = fmt.Errorf("at most %d open proposals", maxProposals)
)

// Proposals holds the proposals of each consumer in memory, until the user
// saves or dismisses them or the consumer's token is revoked.
type Proposals struct {
	registry  *Registry
	proposers func(page string) Proposer
	mu        sync.Mutex
	open      map[string][]Proposal
}

func newProposals(registry *Registry, proposers func(string) Proposer) *Proposals {
	p := &Proposals{registry: registry, proposers: proposers, open: map[string][]Proposal{}}
	registry.onRevoke(func(id string) {
		p.mu.Lock()
		delete(p.open, id)
		p.mu.Unlock()
	})
	return p
}

// Propose records a proposal for the consumer with this id. Only a chat may
// propose, and only for the page it has open. The target's current value and
// revision are read from the proposer, not taken from the agent: a Before the
// agent sent must match the current value.
func (p *Proposals) Propose(ctx context.Context, consumer string, proposal Proposal) (Proposal, error) {
	c, ok := p.registry.Consumer(consumer)
	if !ok {
		return Proposal{}, ErrUnknownConsumer
	}
	if c.Kind != Chat {
		return Proposal{}, ErrReadOnly
	}
	page, ok := p.registry.Page(consumer)
	if !ok || page.Page != proposal.Page {
		return Proposal{}, ErrNotCurrentPage
	}
	proposer := p.proposers(proposal.Page)
	if proposer == nil {
		return Proposal{}, ErrNoProposer
	}
	if !json.Valid(proposal.After) || (len(proposal.Before) > 0 && !json.Valid(proposal.Before)) {
		return Proposal{}, errors.New("before and after must be JSON")
	}
	value, revision, err := proposer.Current(ctx, page, proposal.Target)
	if err != nil {
		return Proposal{}, err
	}
	if len(proposal.Before) > 0 && !sameJSON(proposal.Before, value) {
		return Proposal{}, fmt.Errorf("%w: read it again", ErrStale)
	}
	proposal.Before, proposal.Revision, proposal.Context = value, revision, page
	if err := proposer.Validate(ctx, page, proposal); err != nil {
		return Proposal{}, err
	}
	proposal.ID = random()
	p.mu.Lock()
	defer p.mu.Unlock()
	// Checked under the lock: a revoke that ran already left nothing to find,
	// and one that runs later drops this too.
	if !p.registry.live(consumer) {
		return Proposal{}, ErrUnknownConsumer
	}
	if len(p.open[consumer]) >= maxProposals {
		return Proposal{}, errTooManyProposals
	}
	p.open[consumer] = append(p.open[consumer], proposal)
	return proposal, nil
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}

// List returns the consumer's open proposals, oldest first.
func (p *Proposals) List(consumer string) []Proposal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Proposal(nil), p.open[consumer]...)
}

func (p *Proposals) find(consumer, id string) (Proposal, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, proposal := range p.open[consumer] {
		if proposal.ID == id {
			return proposal, true
		}
	}
	return Proposal{}, false
}

// Save applies a proposal when its target still belongs to the page it was
// made on and is still at the revision it was read at. The proposer compares
// and writes in one step, so an edit that lands meanwhile makes the save fail
// with ErrStale. A refused proposal stays open; a saved one is closed.
func (p *Proposals) Save(ctx context.Context, consumer, id string) error {
	proposal, ok := p.find(consumer, id)
	if !ok {
		return ErrUnknownProposal
	}
	proposer := p.proposers(proposal.Page)
	if proposer == nil {
		return ErrNoProposer
	}
	// Current refuses a target that no longer belongs to the page.
	if _, revision, err := proposer.Current(ctx, proposal.Context, proposal.Target); err != nil {
		return err
	} else if revision != proposal.Revision {
		return ErrStale
	}
	if err := proposer.Apply(ctx, proposal.Context, proposal, proposal.Revision); err != nil {
		return err
	}
	p.remove(consumer, id)
	return nil
}

// Dismiss closes a proposal without applying it.
func (p *Proposals) Dismiss(consumer, id string) error {
	if !p.remove(consumer, id) {
		return ErrUnknownProposal
	}
	return nil
}

func (p *Proposals) remove(consumer, id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	open := p.open[consumer]
	for i, proposal := range open {
		if proposal.ID == id {
			p.open[consumer] = append(open[:i:i], open[i+1:]...)
			return true
		}
	}
	return false
}
