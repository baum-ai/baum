package notices

import (
	"context"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/livesessions"
)

// noRecord is a live sessions tracker's record with nothing in it; the test
// applies events directly.
type noRecord struct{}

func (noRecord) Recent(int) ([]hookevents.Record, error) { return nil, nil }
func (noRecord) SubscribeCounted(int, func(hookevents.Event) bool) (<-chan hookevents.Record, func(), func() uint64) {
	return nil, func() {}, func() uint64 { return 0 }
}

type demoLinker struct{}

func (demoLinker) Link(context.Context, string) (livesessions.Link, bool) {
	return livesessions.Link{Repository: "acme/widgets", Branch: "feature", PR: 7}, true
}

// The tracker hands each wait to SessionWaiting once, as the daemon wires
// them: a permission prompt and a question are notices, once each, and a
// finished turn is none.
func TestSessionWaitsAreNoticedOncePerWait(t *testing.T) {
	s, got := service()
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	tracker := &livesessions.Tracker{Source: noRecord{}, Linker: demoLinker{}, Now: func() time.Time { return now },
		Waiting: func(session livesessions.Session) { s.SessionWaiting(context.Background(), session) }}
	send := func(name, kind, message string) {
		now = now.Add(time.Minute)
		e := hookevents.New(hookevents.KindAgent, "claude-code", name, "/work", "test")
		e.Time = now
		e.Session = &hookevents.Session{ID: "s1", Cwd: "/work", NotificationType: kind, Message: message}
		tracker.Apply(context.Background(), e)
		tracker.ResolvePending(context.Background())
	}
	send("user-prompt-submit", "", "")
	send("notification", "permission_prompt", "Claude needs your permission to use Bash")
	send("notification", "permission_prompt", "Claude needs your permission to use Bash")
	send("post-tool-use", "", "")
	send("stop", "", "")
	send("notification", "idle_prompt", "Claude is waiting for your input")
	send("notification", "elicitation_dialog", "Pick a\x1b[0m database")
	want := []Notice{
		{Kind: KindSessionPermission, Repository: "Acme/Widgets", PR: 7, Agent: "claude-code", Session: "s1", Branch: "feature", Message: "Claude needs your permission to use Bash"},
		{Kind: KindSessionInput, Repository: "Acme/Widgets", PR: 7, Agent: "claude-code", Session: "s1", Branch: "feature", Message: "Pick a [0m database"},
	}
	if len(*got) != len(want) {
		t.Fatalf("notices = %+v", *got)
	}
	for i := range want {
		if (*got)[i].Kind != want[i].Kind || (*got)[i].Repository != want[i].Repository || (*got)[i].PR != want[i].PR ||
			(*got)[i].Agent != want[i].Agent || (*got)[i].Session != want[i].Session || (*got)[i].Branch != want[i].Branch || (*got)[i].Message != want[i].Message {
			t.Errorf("notice %d = %+v, want %+v", i, (*got)[i], want[i])
		}
	}
}

func TestSessionWaitingIgnoresAFinishedTurn(t *testing.T) {
	s, got := service()
	for _, reason := range []string{livesessions.WaitTurnFinished, "", "something_new"} {
		s.SessionWaiting(context.Background(), livesessions.Session{Agent: "codex", ID: "c1", Repository: "acme/widgets", WaitingReason: reason})
	}
	if len(*got) != 0 {
		t.Fatalf("notices = %+v", *got)
	}
	// A repository the registry does not know keeps the tracker's name, and
	// a branch without a pull request has none.
	s.SessionWaiting(context.Background(), livesessions.Session{Agent: "codex", ID: "c1", Repository: "acme/other", WaitingReason: livesessions.WaitPermission})
	if len(*got) != 1 || (*got)[0].Kind != KindSessionPermission || (*got)[0].Repository != "acme/other" || (*got)[0].PR != 0 || (*got)[0].Message != "" {
		t.Fatalf("notices = %+v", *got)
	}
}
