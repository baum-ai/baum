package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	chatpkg "github.com/baum-ai/baum/internal/chat"
	"github.com/baum-ai/baum/internal/fix"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// FixChatRef names the conversation a fix is made from. The app sends only
// these ids: the daemon reads the questions and answers from the clone's
// store. Turn is the last turn the fix uses; zero at the estimate means the
// newest answered turn, and the estimate records the turn it resolved, so
// the confirmed fix uses the conversation up to that turn even when more
// turns arrive afterwards.
type FixChatRef struct {
	Conversation string `json:"conversation_id"`
	Turn         int    `json:"turn,omitempty"`
}

// fixChat reads the conversation a fix is made from, for a pull request of
// the clone: the answered turns up to ref.Turn (the newest answered one when
// zero), capped (fix.CapChat). reason is why it cannot be used, a conversation
// of another pull request or without an answered turn.
func fixChat(ctx context.Context, c repos.Clone, pr int, ref FixChatRef) (chat fix.ChatContext, turn int, reason string, err error) {
	st, err := store.Open(c.CommonDir, "", false)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fix.ChatContext{}, 0, fix.ReasonNoChat, nil
		}
		return fix.ChatContext{}, 0, "", err
	}
	conversation, err := st.Conversation(ctx, store.Key{Repository: c.Repository, Number: pr}, ref.Conversation)
	if errors.Is(err, store.ErrConversationNotFound) {
		return fix.ChatContext{}, 0, fix.ReasonNoChat, nil
	}
	if err != nil {
		return fix.ChatContext{}, 0, "", err
	}
	// Only the newest review's conversation can be fixed from: an older
	// review describes code that has since been reviewed again.
	latest, err := st.Latest(ctx, store.Key{Repository: c.Repository, Number: pr})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fix.ChatContext{}, 0, "", err
	}
	if err != nil || latest.Metadata.Version != conversation.Version {
		return fix.ChatContext{}, 0, fix.ReasonChatOutdated, nil
	}
	var turns []fix.ChatTurn
	for _, t := range conversation.Turns {
		if ref.Turn > 0 && t.Seq > ref.Turn {
			break
		}
		if t.State != store.TurnCompleted || len(t.Answer) == 0 {
			if t.Seq == ref.Turn {
				return fix.ChatContext{}, 0, fix.ReasonNoChat, nil
			}
			continue
		}
		var answer chatpkg.Answer
		if json.Unmarshal(t.Answer, &answer) != nil {
			continue
		}
		ft := fix.ChatTurn{Seq: t.Seq, Question: t.Question, Answer: answer.Markdown}
		for _, cite := range answer.Citations {
			ft.Citations = append(ft.Citations, fmt.Sprintf("%s:%d-%d", cite.Path, cite.StartLine, cite.EndLine))
		}
		if answer.SuggestsFix != nil {
			ft.Suggestion = answer.SuggestsFix.Summary
		}
		turns = append(turns, ft)
	}
	if len(turns) == 0 || (ref.Turn > 0 && turns[len(turns)-1].Seq != ref.Turn) {
		return fix.ChatContext{}, 0, fix.ReasonNoChat, nil
	}
	return fix.CapChat(conversation.ID, turns), turns[len(turns)-1].Seq, "", nil
}
