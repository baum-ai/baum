package console

import (
	"context"

	"github.com/baum-ai/baum/internal/agentcontext"
)

// EventProposals is the kind of Event that says a conversation's open
// proposals changed; the app reads them again (Proposals).
const EventProposals = "proposals"

// consumerOf is the MCP consumer of the conversation's live token, "" when
// it holds none (it was closed, or has not run since the daemon started).
func (s *Service) consumerOf(id string) (string, error) {
	if _, err := s.store.get(id); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if l := s.live[id]; l != nil {
		return l.consumer, nil
	}
	return "", nil
}

// Proposals returns what the conversation's agent proposed and the person
// has neither saved, confirmed nor dismissed, oldest first. They live with
// its token: none survive a restart or its closing.
func (s *Service) Proposals(id string) ([]agentcontext.Proposal, error) {
	consumer, err := s.consumerOf(id)
	if err != nil || consumer == "" || s.opts.Proposals == nil {
		return []agentcontext.Proposal{}, err
	}
	return s.opts.Proposals.List(consumer), nil
}

// SaveProposal saves a proposed change with what the person typed
// (agentcontext.Proposals.Save): only while the person has the page it was
// made on open, which SetPage, Create and Send keep up to date.
func (s *Service) SaveProposal(ctx context.Context, id, proposal string, input agentcontext.SaveInput) error {
	consumer, err := s.proposalConsumer(id)
	if err != nil {
		return err
	}
	return s.opts.Proposals.Save(ctx, consumer, proposal, input)
}

// ConfirmProposal runs a proposed operation whose summary the person read at
// revision seen (agentcontext.Proposals.Confirm). When the summary changed,
// it returns agentcontext.ErrOperationChanged and the proposal, which now
// holds the new summary to show.
func (s *Service) ConfirmProposal(ctx context.Context, id, proposal, seen string) (agentcontext.OperationOutcome, *agentcontext.Proposal, error) {
	consumer, err := s.proposalConsumer(id)
	if err != nil {
		return agentcontext.OperationOutcome{}, nil, err
	}
	outcome, err := s.opts.Proposals.Confirm(ctx, consumer, proposal, seen)
	if err != nil {
		for _, p := range s.opts.Proposals.List(consumer) {
			if p.ID == proposal {
				return outcome, &p, err
			}
		}
	}
	return outcome, nil, err
}

// DismissProposal closes a proposal without applying or running it.
func (s *Service) DismissProposal(id, proposal string) error {
	consumer, err := s.proposalConsumer(id)
	if err != nil {
		return err
	}
	return s.opts.Proposals.Dismiss(consumer, proposal)
}

// proposalConsumer is the consumer whose proposal the person acts on; a
// conversation without a live token has none to act on.
func (s *Service) proposalConsumer(id string) (string, error) {
	consumer, err := s.consumerOf(id)
	if err != nil {
		return "", err
	}
	if consumer == "" || s.opts.Proposals == nil {
		return "", agentcontext.ErrUnknownProposal
	}
	return consumer, nil
}

// proposalsChanged announces a change to a conversation's open proposals.
// It runs while a revoke under s.mu may be in progress, so it reads only the
// consumers map.
func (s *Service) proposalsChanged(consumer string) {
	s.cmu.Lock()
	id, ok := s.consumers[consumer]
	s.cmu.Unlock()
	if ok {
		s.publish(Event{Conversation: id, Kind: EventProposals})
	}
}
