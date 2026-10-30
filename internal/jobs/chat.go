package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/chat"
	"github.com/baum-ai/baum/internal/store"
)

// KindChat is the Item.Kind of one question to the agent that wrote a
// review (internal/chat). The person asked it, so it is queued at once, with
// no estimate to confirm, and runs in its own lane of ChatWorkers beside the
// reviews and fixes, so a question does not wait behind a long review. A
// conversation has at most one question queued or running.
const KindChat = "chat"

// ChatWorkers is how many questions run at once, across conversations.
const ChatWorkers = 2

// ChatInputs are what a chat job answers: the conversation, its turn and the
// review version it is about. The app shows the job as a question on the
// pull request (Item.PR).
type ChatInputs struct {
	Conversation string `json:"conversation,omitempty"`
	Turn         int    `json:"turn,omitempty"`
	Version      int    `json:"version,omitempty"`
}

// ChatService stores questions as turns of conversations and settles them;
// *chat.Service implements it.
type ChatService interface {
	Begin(ctx context.Context, ask chat.Ask, jobID string, ended store.Ended) (chat.Begun, error)
	Conversations(ctx context.Context, clone string, pr int, ended store.Ended) ([]store.Conversation, error)
	Settle(ctx context.Context, ref chat.Ref, state, reason string, cost *float64, costReason string) error
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

// ChatQueued is a question queued as a turn and its job.
type ChatQueued struct {
	Conversation string `json:"conversation_id"`
	Turn         int    `json:"turn"`
	Job          Job    `json:"job"`
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

// Ask stores the question as a turn and queues its job. A conversation with a
// question still queued or running refuses it with chat.ErrBusy.
func (q *Queue) Ask(ctx context.Context, ask chat.Ask) (ChatQueued, error) {
	service := q.chatService()
	if service == nil {
		return ChatQueued{}, ErrChatUnavailable
	}
	// One question at a time from here to the journal: a turn stored but
	// not yet journaled would otherwise look stale to a concurrent ask.
	q.ask.Lock()
	defer q.ask.Unlock()
	q.mu.Lock()
	draining := q.draining || q.ctx.Err() != nil
	q.mu.Unlock()
	if draining {
		return ChatQueued{}, ErrRestarting
	}
	jobID := id()
	begun, err := service.Begin(ctx, ask, jobID, q.ended)
	if err != nil {
		return ChatQueued{}, err
	}
	now := time.Now().UTC()
	j := Job{ID: jobID, State: "queued", Phase: "queued", CreatedAt: now, UpdatedAt: now, Prepared: Prepared{
		Item: Item{Clone: begun.Clone, PR: begun.PR, Kind: KindChat}, Repository: begun.Repository, Backend: begun.Backend, Model: begun.Model,
		Chat: ChatInputs{Conversation: begun.Conversation, Turn: begun.Turn, Version: begun.Version}}}
	q.mu.Lock()
	failed := q.draining
	if !failed {
		q.jobs = append(q.jobs, j)
		if err = q.save(); err != nil {
			q.jobs = q.jobs[:len(q.jobs)-1]
			failed = true
		}
	}
	q.mu.Unlock()
	if failed {
		_ = service.Settle(context.WithoutCancel(ctx), begun.Ref, store.TurnFailed, "internal", nil, "")
		if err == nil {
			err = ErrRestarting
		}
		return ChatQueued{}, err
	}
	q.notify(j)
	q.signal()
	return ChatQueued{Conversation: begun.Conversation, Turn: begun.Turn, Job: j}, nil
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

// chatRef names the turn a chat job answers.
func chatRef(j Job) chat.Ref {
	return chat.Ref{Clone: j.Clone, PR: j.PR, Conversation: j.Chat.Conversation, Turn: j.Chat.Turn}
}

// settleChat settles the turn of a chat job that ended without an answer:
// failed, cancelled or interrupted. A completed job's turn was settled with
// its answer.
func (q *Queue) settleChat(j Job) {
	if j.Kind != KindChat || j.State == "completed" || j.State == "queued" || j.State == "running" {
		return
	}
	service := q.chatService()
	if service == nil {
		return
	}
	_ = service.Settle(context.Background(), chatRef(j), j.State, j.Reason, j.ActualCostUSD, j.CostReason)
}

// ChatEvent is the server-sent event of a chat job queued or settled: which
// turn of which conversation, and its state, without the answer.
func ChatEvent(j Job) (map[string]any, bool) {
	if j.Kind != KindChat || (j.State != "queued" && j.Phase != "finished") {
		return nil, false
	}
	return map[string]any{"conversation": j.Chat.Conversation, "repository": j.Repository, "pr": j.PR, "turn": j.Chat.Turn, "state": j.State}, true
}

// executeChat answers a chat job's question (chat.Service.Run), keeping the
// agent's transcript like a fix's.
func (l *Local) executeChat(ctx context.Context, p Prepared, progress func(Progress)) (*float64, error) {
	if l.Chat == nil {
		return nil, ErrChatUnavailable
	}
	var transcript func(agent.TranscriptEntry)
	hasTranscript := false
	if target, ok := transcriptTargetOf(ctx); ok {
		if writer, err := openTranscript(target.Dir, target.JobID); err == nil {
			defer writer.Close()
			transcript, hasTranscript = func(entry agent.TranscriptEntry) { writer.Add(ctx, entry) }, true
		}
	}
	report, err := l.Chat.Run(ctx, chat.Ref{Clone: p.Clone, PR: p.PR, Conversation: p.Chat.Conversation, Turn: p.Chat.Turn}, chat.RunOptions{
		Transcript: transcript,
		Progress: func(r chat.Report) {
			progress(Progress{Phase: r.Phase, Calls: r.Calls, Tokens: r.Tokens, Repairs: r.Repairs, Accounting: r.Accounting, CostReason: r.CostReason, HasTranscript: hasTranscript})
		},
	})
	progress(Progress{Phase: "finishing", Calls: report.Calls, Tokens: report.Tokens, Repairs: report.Repairs, Accounting: report.Accounting, CostReason: report.CostReason, HasTranscript: hasTranscript})
	return report.CostUSD, err
}
