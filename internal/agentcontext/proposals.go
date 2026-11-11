package agentcontext

import (
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
	// Before and After are the target's value as the agent read it and as
	// it proposes it, in the page's own encoding.
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after"`
	// Revision is the target's revision Before was read at.
	Revision string `json:"revision"`
}

// Proposer is how one page takes proposals.
type Proposer interface {
	// Validate refuses a proposal the page would not save.
	Validate(ctx context.Context, p Proposal) error
	// Revision reads the target's current revision.
	Revision(ctx context.Context, target string) (string, error)
	// Apply saves the proposal: the same change the page's own save makes.
	Apply(ctx context.Context, p Proposal) error
}

// A consumer holds at most maxProposals proposals; a new one past that is
// refused until the user saves or dismisses one.
const maxProposals = 32

var (
	ErrReadOnly         = errors.New("this agent only reads: it cannot propose changes")
	ErrNotCurrentPage   = errors.New("a proposal must be for the page the user has open")
	ErrNoProposer       = errors.New("this page takes no proposals")
	ErrUnknownProposal  = errors.New("unknown proposal")
	ErrStale            = errors.New("the target changed since the proposal was made; nothing was saved")
	errTooManyProposals = fmt.Errorf("at most %d open proposals", maxProposals)
)

// Proposals holds the proposals of each consumer in memory, until the user
// saves or dismisses them or the consumer's token is revoked.
type Proposals struct {
	registry  *Registry
	proposers func(page string) Proposer
	mu        sync.Mutex
	open      map[string][]Proposal
	// saving serializes saves, so a target is read and written by one at a
	// time.
	saving sync.Mutex
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

// Propose records p for the consumer with this id, after its page's proposer
// validated it. Only a chat may propose, and only for the page it has open.
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
	if err := proposer.Validate(ctx, proposal); err != nil {
		return Proposal{}, err
	}
	proposal.ID = random()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.open[consumer]) >= maxProposals {
		return Proposal{}, errTooManyProposals
	}
	p.open[consumer] = append(p.open[consumer], proposal)
	return proposal, nil
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

// Save applies a proposal when its target is still at the revision it was
// read at. A changed target is refused with ErrStale, and the proposal stays
// open; a saved one is closed.
func (p *Proposals) Save(ctx context.Context, consumer, id string) error {
	p.saving.Lock()
	defer p.saving.Unlock()
	proposal, ok := p.find(consumer, id)
	if !ok {
		return ErrUnknownProposal
	}
	proposer := p.proposers(proposal.Page)
	if proposer == nil {
		return ErrNoProposer
	}
	current, err := proposer.Revision(ctx, proposal.Target)
	if err != nil {
		return err
	}
	if current != proposal.Revision {
		return ErrStale
	}
	if err := proposer.Apply(ctx, proposal); err != nil {
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
