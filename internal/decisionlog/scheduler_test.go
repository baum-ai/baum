package decisionlog

import (
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/livesessions"
)

func TestDue(t *testing.T) {
	now := t0.Add(time.Hour)
	running := livesessions.Session{State: livesessions.StateRunning, LastEvent: livesessions.EventTool, StartedAt: now.Add(-5 * time.Minute), LastActivityAt: now}
	stopped := livesessions.Session{State: livesessions.StateIdle, LastEvent: livesessions.EventStop, WaitingReason: livesessions.WaitTurnFinished, StartedAt: t0}
	cases := []struct {
		name    string
		session livesessions.Session
		memo    memo
		want    string
	}{
		{"running briefly", running, memo{}, ""},
		{"running long", with(running, func(s *livesessions.Session) { s.StartedAt = now.Add(-RunningEvery) }), memo{}, "running"},
		{"running long, captured lately", with(running, func(s *livesessions.Session) { s.StartedAt = t0 }), memo{lastRun: now.Add(-time.Minute)}, ""},
		{"stop just now", with(stopped, func(s *livesessions.Session) { s.LastActivityAt = now.Add(-time.Minute) }), memo{}, ""},
		{"stop waited", with(stopped, func(s *livesessions.Session) { s.LastActivityAt = now.Add(-StopWait) }), memo{}, "stop"},
		{"stop already fired", with(stopped, func(s *livesessions.Session) { s.LastActivityAt = now.Add(-StopWait) }), memo{fired: now.Add(-StopWait)}, ""},
		{"asks permission", livesessions.Session{State: livesessions.StateIdle, LastEvent: livesessions.EventWait, WaitingReason: livesessions.WaitPermission, LastActivityAt: t0}, memo{}, ""},
		{"silent", livesessions.Session{State: livesessions.StateIdle, LastEvent: livesessions.EventTool, LastActivityAt: now.Add(-livesessions.IdleAfter - time.Second)}, memo{}, "idle"},
		{"ended", livesessions.Session{State: livesessions.StateEnded, LastEvent: livesessions.EventEnd, LastActivityAt: now}, memo{}, "ended"},
		{"ended after stop fired", livesessions.Session{State: livesessions.StateEnded, LastEvent: livesessions.EventEnd, LastActivityAt: now}, memo{fired: now.Add(-time.Minute)}, "ended"},
		{"ended already fired", livesessions.Session{State: livesessions.StateEnded, LastEvent: livesessions.EventEnd, LastActivityAt: now}, memo{fired: now}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := due(tc.session, tc.memo, now); got != tc.want {
				t.Fatalf("due = %q, want %q", got, tc.want)
			}
		})
	}
}

func with(s livesessions.Session, edit func(*livesessions.Session)) livesessions.Session {
	edit(&s)
	return s
}

func TestThrottled(t *testing.T) {
	now := t0
	cases := []struct {
		name  string
		stats Stats
		ended bool
		want  string
	}{
		{"never", Stats{}, false, ""},
		{"recent attempt", Stats{Count: 1, LastStart: now.Add(-time.Minute)}, false, "attempted_recently"},
		{"older attempt", Stats{Count: 1, LastStart: now.Add(-MinInterval)}, false, ""},
		{"recent failure", Stats{Count: 1, LastStart: now.Add(-6 * time.Minute), LastFail: now.Add(-6 * time.Minute)}, false, "failed_recently"},
		{"older failure", Stats{Count: 1, LastStart: now.Add(-FailureDelay), LastFail: now.Add(-FailureDelay)}, false, ""},
		{"daily limit", Stats{Count: MaxPerDay, LastStart: now.Add(-time.Hour)}, false, "daily_attempts"},
		{"daily limit, ended", Stats{Count: MaxPerDay, LastStart: now.Add(-time.Hour)}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := throttled(tc.stats, tc.ended, now); got != tc.want {
				t.Fatalf("throttled = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCovered(t *testing.T) {
	day := startOfDay(t0)
	cases := []struct {
		row  Session
		want bool
	}{
		{Session{Fingerprint: "f", Coverage: CoverageExtracted}, true},
		{Session{Fingerprint: "g", Coverage: CoverageExtracted}, false},
		{Session{Fingerprint: "f", Coverage: CoverageSkipped, SkipReason: ReasonOverTokenCap, UpdatedAt: day.Add(-time.Hour)}, true},
		{Session{Fingerprint: "f", Coverage: CoverageSkipped, SkipReason: ReasonOverCostCap, UpdatedAt: day.Add(time.Hour)}, true},
		{Session{Fingerprint: "f", Coverage: CoverageSkipped, SkipReason: ReasonOverCostCap, UpdatedAt: day.Add(-time.Hour)}, false},
	}
	for i, tc := range cases {
		if got := covered(tc.row, "f", day); got != tc.want {
			t.Errorf("%d: covered = %v", i, got)
		}
	}
}
