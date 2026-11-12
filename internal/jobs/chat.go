package jobs

import (
	"context"
	"errors"

	"github.com/baum-ai/baum/internal/store"
)

// KindChat is the Item.Kind of one question an earlier version asked the
// agent that wrote a review (internal/chat). Nothing queues one any more:
// the journal and the archive keep those that were, so they still decode and
// show, and Open ends one a stopped daemon left queued or running as
// interrupted.
const KindChat = "chat"

// ChatInputs are what a chat job answered: the conversation, its turn and the
// review version it is about. The app shows the job as a question on the
// pull request (Item.PR).
type ChatInputs struct {
	Conversation string `json:"conversation,omitempty"`
	Turn         int    `json:"turn,omitempty"`
	Version      int    `json:"version,omitempty"`
}

// ChatService lists the stored conversations of a pull request; *chat.Service
// implements it.
type ChatService interface {
	Conversations(ctx context.Context, clone string, pr int, ended store.Ended) ([]store.Conversation, error)
}

// ErrChatUnavailable is a queue without a chat service.
var ErrChatUnavailable = errors.New("chat is not available")

// SetChat installs the chat service; call it right after Open.
func (q *Queue) SetChat(service ChatService) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.chat = service
}

func (q *Queue) chatService() ChatService {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.chat
}

// ended reports how a job ended for its turn (store.Ended): as the job did,
// or interrupted when the journal no longer holds it or it completed without
// settling the turn, which only a stop between the two leaves. A queued or
// running job has not ended.
func (q *Queue) ended(jobID string) (string, string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.ID != jobID {
			continue
		}
		switch j.State {
		case "queued", "running":
			return "", "", false
		case "completed":
			return store.TurnInterrupted, store.TurnInterrupted, true
		}
		return j.State, j.Reason, true
	}
	return store.TurnInterrupted, store.TurnInterrupted, true
}

// Conversations lists a pull request's conversations; a pending turn whose
// job ended reads as it ended (see ended).
func (q *Queue) Conversations(ctx context.Context, clone string, pr int) ([]store.Conversation, error) {
	service := q.chatService()
	if service == nil {
		return nil, ErrChatUnavailable
	}
	return service.Conversations(ctx, clone, pr, q.ended)
}
