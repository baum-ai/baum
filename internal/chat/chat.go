// Package chat serves, read-only, the stored conversations of the earlier
// Ask feature: questions a reviewer asked the agent that wrote a pull
// request's review, with their answers. Nothing asks new questions any more
// (the session panel, internal/console, replaced it); the conversations stay
// in the clone's review store (internal/store) and the app and the MCP
// server can still read them.
package chat

import (
	"context"
	"errors"
	"fmt"

	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// Errors a listing is refused with. The API answers each with its own code.
var (
	ErrRepositoryNotFound = errors.New("the repository is not registered")
	ErrInvalid            = errors.New("invalid request")
)

// Service reads the stored conversations.
type Service struct {
	Registry *repos.Registry
}

// clone resolves a registered clone.
func (s *Service) clone(ctx context.Context, id string) (repos.Clone, error) {
	clones, err := s.Registry.List(ctx)
	if err != nil {
		return repos.Clone{}, err
	}
	for _, c := range clones {
		if c.ID == id {
			return c, nil
		}
	}
	return repos.Clone{}, ErrRepositoryNotFound
}

// Conversations lists the pull request's conversations, newest first. A turn
// still pending whose job ended reads as ended reports it: the turn was
// settled a moment after its job, and a stopped daemon left it pending.
func (s *Service) Conversations(ctx context.Context, clone string, pr int, ended store.Ended) ([]store.Conversation, error) {
	if clone == "" || pr < 1 {
		return nil, fmt.Errorf("%w: clone and a positive pr are required", ErrInvalid)
	}
	c, err := s.clone(ctx, clone)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(c.CommonDir, "", false)
	if err != nil {
		return nil, err
	}
	list, err := st.Conversations(ctx, store.Key{Repository: c.Repository, Number: pr})
	if err != nil {
		return nil, err
	}
	for i := range list {
		for j := range list[i].Turns {
			turn := &list[i].Turns[j]
			if !store.Pending(turn.State) || ended == nil {
				continue
			}
			if state, reason, done := ended(turn.JobID); done && !store.Pending(state) && state != "" {
				turn.State, turn.Reason = state, reason
			}
		}
	}
	return list, nil
}
