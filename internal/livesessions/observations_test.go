package livesessions

import (
	"context"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
)

type recordedLinker struct {
	current Link
	saved   []Session
	pr      int
}

func (l *recordedLinker) Link(context.Context, string) (Link, bool) { return l.current, true }
func (l *recordedLinker) Relink(_ context.Context, s Session) (Link, bool) {
	l.saved = append(l.saved, s)
	return Link{Repository: s.Repository, Branch: s.Branch, HeadSHA: s.HeadSHA, Upstream: s.Upstream, PR: l.pr, Status: "resolved"}, true
}
func TestEndedSessionRelinksRecordedWorkAfterBranchSwitchAndRestart(t *testing.T) {
	c := &clock{t0}
	l := &recordedLinker{current: Link{Repository: "acme/demo", Branch: "original", HeadSHA: "old-head", Status: "unmatched"}}
	tr := &Tracker{Linker: l, Now: c.Now, StateDir: t.TempDir()}
	apply(tr, event("s1", "session-start", c.now, "/work/demo"))
	apply(tr, event("s1", "session-end", c.now, "/work/demo"))
	tr.persist()
	l.current = Link{Repository: "acme/demo", Branch: "different", HeadSHA: "new-head", PR: 99}
	l.pr = 7
	c.advance(RelinkEvery)
	tr.Sweep()
	tr.ResolvePending(context.Background())
	got := tr.Sessions()[0]
	if got.PR != 7 || got.Branch != "original" || got.HeadSHA != "old-head" {
		t.Fatalf("ended work changed: %+v", got)
	}
	restored := &Tracker{Linker: l, Now: c.Now, StateDir: tr.StateDir, Source: &source{}}
	restored.Rebuild(context.Background())
	c.advance(RelinkEvery)
	restored.Sweep()
	restored.ResolvePending(context.Background())
	got = restored.Sessions()[0]
	if got.PR != 7 || got.Branch != "original" {
		t.Fatalf("restart changed historical work: %+v", got)
	}
}
func TestLiveSweepRechecksAlreadyLinkedSession(t *testing.T) {
	c := &clock{t0}
	l := &recordedLinker{current: Link{Repository: "acme/demo", Branch: "feature", PR: 7}}
	tr := &Tracker{Linker: l, Now: c.Now}
	apply(tr, event("s1", "session-start", c.now, "/work/demo"))
	l.current.PR = 0
	l.current.Status = "unmatched"
	c.advance(RelinkEvery)
	tr.Sweep()
	tr.ResolvePending(context.Background())
	if got := tr.Sessions()[0]; got.PR != 0 {
		t.Fatalf("closed PR remained current: %+v", got)
	}
}
func TestEndedReplayDoesNotInventHistoricalBranch(t *testing.T) {
	c := &clock{t0.Add(time.Minute)}
	l := &recordedLinker{current: Link{Repository: "acme/demo", Branch: "today", PR: 99}}
	src := &source{records: []hookevents.Record{record(event("s1", "session-start", t0, "/work/demo")), record(event("s1", "session-end", t0.Add(time.Second), "/work/demo"))}}
	tr := &Tracker{Linker: l, Now: c.Now, Source: src}
	tr.Rebuild(context.Background())
	tr.ResolvePending(context.Background())
	got := tr.Sessions()[0]
	if got.PR != 0 || got.Branch != "" || got.LinkStatus != "unknown_history" {
		t.Fatalf("invented history: %+v", got)
	}
}

func TestWorkHistoryRetainsPreviousBranchWithoutRewritingIt(t *testing.T) {
	c := &clock{t0}
	l := &recordedLinker{current: Link{Repository: "acme/demo", Branch: "one", HeadSHA: "first", PR: 1}}
	tr := &Tracker{Linker: l, Now: c.Now}
	apply(tr, event("s1", "session-start", c.now, "/work/demo"))
	l.current = Link{Repository: "acme/demo", Branch: "two", HeadSHA: "second", PR: 2}
	c.advance(RelinkEvery)
	tr.Sweep()
	tr.ResolvePending(context.Background())
	history := tr.Sessions()[0].WorkHistory.Entries
	if len(history) != 2 || history[0].Branch != "one" || history[0].PR != 1 || history[1].Branch != "two" {
		t.Fatalf("history = %+v", history)
	}
}
