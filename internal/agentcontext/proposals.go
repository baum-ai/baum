package agentcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// ProposalKind is what a proposal asks of the person.
type ProposalKind string

const (
	// ProposalDiff is a change to something a page edits, which the person
	// saves (Save) or dismisses.
	ProposalDiff ProposalKind = "diff"
	// ProposalOperation is something the page does with a button (merge,
	// cancel, answer), which the person confirms (Confirm) or dismisses; see
	// Operation.
	ProposalOperation ProposalKind = "operation"
)

// Proposal is a change an agent suggests to something a page edits, or an
// operation it suggests the person runs. Nothing changes until the user
// saves or confirms it.
type Proposal struct {
	ID   string       `json:"id"`
	Kind ProposalKind `json:"kind"`
	// Page is the page whose state it changes, Target what on that page
	// (a page's own name for it, such as a prompt's key).
	Page   string `json:"page"`
	Target string `json:"target"`
	// Before is the target's value when the proposal was recorded, and After
	// the value proposed, in the page's own encoding. An agent may send the
	// Before it read; Propose refuses it when the target has changed since,
	// and records the value the proposer read instead. Before is shown to the
	// person, never returned to the agent.
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after"`
	// Revision is the target's revision Before was read at. Propose sets it
	// from the proposer; whatever the agent sent is ignored.
	Revision string `json:"revision"`
	// Context is the page the consumer had open when it proposed, which the
	// target must still belong to when the user saves.
	Context PageContext `json:"context"`
	// Operation is an operation proposal's operation, request and summary;
	// nil for a diff, which uses Before, After and Revision instead.
	Operation *OperationProposal `json:"operation,omitempty"`
}

// Proposer is how one page takes proposals. Every method gets the page
// context the proposal was made on, so it can refuse a target that does not
// belong to what that page shows (another pull request's item on a pull
// request's page). Its errors reach the agent, so they name no value.
type Proposer interface {
	// Current reads the target's value and revision. The value is kept for
	// the person to compare and never sent to the agent.
	Current(ctx context.Context, page PageContext, target string) (value json.RawMessage, revision string, err error)
	// Validate refuses a proposal the page would not save.
	Validate(ctx context.Context, page PageContext, p Proposal) error
	// Apply saves p.After when the target is still at revision, comparing
	// and writing in one transaction (as appdb.Update does with its
	// revision); otherwise it changes nothing and returns ErrStale. It is the
	// same change the page's own save makes. input is what the person added
	// when saving; a proposer that takes none refuses any. It runs under the
	// proposals' lock and must not call back into Proposals.
	Apply(ctx context.Context, page PageContext, p Proposal, revision string, input SaveInput) error
}

// SaveInput is what the person adds to a proposal when saving it, from the
// app's own save and never through an agent: the value of an agent
// environment variable the agent proposed adding, which the agent never
// learns. It is passed to Apply alone and never kept in the proposal.
type SaveInput struct {
	Value *string `json:"value,omitempty"`
}

// String, GoString and LogValue never print the value, so neither a log line
// nor an error formatted with an input can carry it.
func (in SaveInput) String() string       { return in.redacted() }
func (in SaveInput) GoString() string     { return in.redacted() }
func (in SaveInput) LogValue() slog.Value { return slog.StringValue(in.redacted()) }
func (in SaveInput) redacted() string {
	if in.Value == nil {
		return "SaveInput{}"
	}
	return "SaveInput{Value: [withheld]}"
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
	ErrStale = errors.New("the target changed since it was read; nothing was saved")
	// ErrNotRecorded is a proposal whose Before is not the target's current
	// value. It says no more, so an agent cannot use it to guess the value.
	ErrNotRecorded      = errors.New("the proposal was not recorded")
	errTooManyProposals = fmt.Errorf("at most %d open proposals", maxProposals)
)

// Proposals holds the proposals of each consumer in memory, until the user
// saves or dismisses them or the consumer's token is revoked.
type Proposals struct {
	registry  *Registry
	proposers func(page string) Proposer
	// operation finds an operation by name; reader reads what operations
	// read, and writer runs a confirmed one.
	operation func(name string) *Operation
	reader    *Reader
	writer    writer
	mu        sync.Mutex
	open      map[string][]Proposal
}

func newProposals(registry *Registry, proposers func(string) Proposer, operation func(string) *Operation, reader *Reader, api http.Handler) *Proposals {
	p := &Proposals{registry: registry, proposers: proposers, operation: operation, reader: reader, writer: writer{api}, open: map[string][]Proposal{}}
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
	// An operation is proposed only through ProposeOperation.
	if (proposal.Kind != "" && proposal.Kind != ProposalDiff) || proposal.Operation != nil {
		return Proposal{}, ErrNotADiff
	}
	proposal.Kind = ProposalDiff
	page, ok := p.registry.Page(consumer)
	if !ok || page.Page != proposal.Page {
		return Proposal{}, ErrNotCurrentPage
	}
	proposer := p.proposers(proposal.Page)
	if proposer == nil {
		return Proposal{}, ErrNoProposer
	}
	// The store keeps its own copies: a caller that reuses its buffers after
	// this returns cannot change what was validated, listed or saved.
	proposal = proposal.clone()
	if !json.Valid(proposal.After) || (len(proposal.Before) > 0 && !json.Valid(proposal.Before)) {
		return Proposal{}, errors.New("before and after must be JSON")
	}
	value, revision, err := proposer.Current(ctx, page, proposal.Target)
	if err != nil {
		return Proposal{}, err
	}
	if len(proposal.Before) > 0 && !sameJSON(proposal.Before, value) {
		return Proposal{}, ErrNotRecorded
	}
	proposal.Before, proposal.Revision, proposal.Context = bytes.Clone(value), revision, page.clone()
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
	return proposal.clone(), nil
}

// clone copies p with nothing shared with the stored proposal.
func (p Proposal) clone() Proposal {
	p.Before, p.After, p.Context = bytes.Clone(p.Before), bytes.Clone(p.After), p.Context.clone()
	p.Operation = p.Operation.clone()
	return p
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}

// List returns the consumer's open proposals, oldest first.
func (p *Proposals) List(consumer string) []Proposal {
	p.mu.Lock()
	defer p.mu.Unlock()
	list := make([]Proposal, 0, len(p.open[consumer]))
	for _, proposal := range p.open[consumer] {
		list = append(list, proposal.clone())
	}
	return list
}

func (p *Proposals) find(consumer, id string) (Proposal, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, proposal := range p.open[consumer] {
		if proposal.ID == id {
			return proposal.clone(), true
		}
	}
	return Proposal{}, false
}

// Save applies a proposal, with what the person added (SaveInput), when the
// person still has open the page it was made on, showing the same thing,
// when its target still belongs to that page as it is now, and when the
// target is still at the revision it was read at. The proposer compares and
// writes in one step, so an edit that lands meanwhile makes the save fail
// with ErrStale. A refused proposal stays open; a saved one is closed. The
// write runs under the lock a revoke takes to drop proposals, after checking
// the consumer is still live and the proposal still open: a revoke either
// comes first and the save fails, or waits for the write.
func (p *Proposals) Save(ctx context.Context, consumer, id string, input SaveInput) error {
	proposal, ok := p.find(consumer, id)
	if !ok {
		return ErrUnknownProposal
	}
	if proposal.Kind != ProposalDiff {
		return ErrNotADiff
	}
	proposer := p.proposers(proposal.Page)
	if proposer == nil {
		return ErrNoProposer
	}
	// The person may have navigated since: the page must still be the one
	// the proposal was made on, showing the same thing.
	page, ok := p.registry.Page(consumer)
	if !ok || !samePage(page, proposal.Context) {
		return ErrNotCurrentPage
	}
	// Current refuses a target that does not belong to the page as it is
	// now (another section, or a target that left the page).
	if _, revision, err := proposer.Current(ctx, page, proposal.Target); err != nil {
		return err
	} else if revision != proposal.Revision {
		return ErrStale
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.registry.live(consumer) {
		return ErrUnknownConsumer
	}
	if !slices.ContainsFunc(p.open[consumer], func(open Proposal) bool { return open.ID == id }) {
		return ErrUnknownProposal
	}
	if err := proposer.Apply(ctx, page, proposal, proposal.Revision, input); err != nil {
		return err
	}
	p.removeLocked(consumer, id)
	return nil
}

// samePage reports whether two contexts are the same page showing the same
// thing: the same repository, pull request, group, session, job and feature.
// Sections, filters and focus may differ; a proposer refuses a target of
// another section.
func samePage(a, b PageContext) bool {
	return a.Page == b.Page && strings.EqualFold(a.Repository, b.Repository) && a.PullRequest == b.PullRequest &&
		a.Group == b.Group && a.Session == b.Session && a.Job == b.Job && a.Feature == b.Feature
}

// Dismiss closes a proposal without applying or running it.
func (p *Proposals) Dismiss(consumer, id string) error {
	if !p.remove(consumer, id) {
		return ErrUnknownProposal
	}
	return nil
}

func (p *Proposals) remove(consumer, id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.removeLocked(consumer, id)
}

func (p *Proposals) removeLocked(consumer, id string) bool {
	open := p.open[consumer]
	for i, proposal := range open {
		if proposal.ID == id {
			p.open[consumer] = append(open[:i:i], open[i+1:]...)
			return true
		}
	}
	return false
}
