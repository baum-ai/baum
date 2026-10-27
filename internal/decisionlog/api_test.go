package decisionlog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
)

func serveDecisions(t *testing.T, api *API, query string) (int, []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	api.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/decisions?"+query, nil))
	return w.Code, w.Body.Bytes()
}

func TestAPISessionAndPullRequest(t *testing.T) {
	ctx := context.Background()
	c := &clock{t0}
	s := openStore(t, c)
	changes := 0
	s.Changed = func() { changes++ }
	// A running session with two decisions, one dated after the review.
	running := Meta{Agent: "claude-code", ID: "run", Repository: "acme/demo", Fingerprint: "f"}
	if err := s.Save(ctx, Extraction{Meta: running, ContentHash: "h", Decisions: []decisions.Decision{decision("d1", "before", 1), decision("d2", "after", 2)},
		TurnTimes: map[int]time.Time{1: t0.Add(-2 * time.Hour), 2: t0.Add(-10 * time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	// An ended session the review links, skipped, with a call in flight.
	ended := Meta{Agent: "codex", ID: "old", Repository: "acme/demo", Fingerprint: "g"}
	if err := s.Skip(ctx, ended, ReasonUnsupported); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(ctx, Attempt{Agent: "codex", ID: "old", Backend: "codex", Model: "gpt-6-luna", Mode: "full"}); err != nil {
		t.Fatal(err)
	}
	if changes != 3 {
		t.Fatalf("changes = %d, want one per write", changes)
	}
	generated := t0.Add(-time.Hour)
	api := &API{Store: s,
		Live: func() []livesessions.Session {
			return []livesessions.Session{
				{Agent: "claude-code", ID: "run", State: livesessions.StateRunning, Repository: "Acme/Demo", PR: 7},
				{Agent: "claude-code", ID: "other-pr", State: livesessions.StateRunning, Repository: "acme/demo", PR: 8},
				{Agent: "claude-code", ID: "gone", State: livesessions.StateEnded, Repository: "acme/demo", PR: 7},
			}
		},
		Linked: func(_ context.Context, repository string, number int) ([]locallink.Session, time.Time, error) {
			if repository != "acme/demo" || number != 7 {
				t.Errorf("linked asked for %s#%d", repository, number)
			}
			return []locallink.Session{{ID: "old", Vendor: "codex", Title: "Port the parser"}, {ID: "run", Vendor: "claude_code", Title: "Live one"},
				{ID: "gone", Vendor: "claude_code"}, {ID: "x", Vendor: "someone_else"}}, generated, nil
		}}

	code, body := serveDecisions(t, api, "agent=claude-code&session=run")
	var one struct{ Session SessionDecisions }
	if code != 200 || json.Unmarshal(body, &one) != nil {
		t.Fatalf("%d %s", code, body)
	}
	if one.Session.Coverage != CoverageExtracted || len(one.Session.Decisions) != 2 || one.Session.Capturing || one.Session.UpdatedAt == nil ||
		one.Session.Decisions[0].Provisional {
		t.Fatalf("session %+v", one.Session)
	}
	code, body = serveDecisions(t, api, "agent=claude-code&session=never")
	if code != 200 || json.Unmarshal(body, &one) != nil || one.Session.Coverage != "" || one.Session.UpdatedAt != nil || one.Session.Decisions == nil {
		t.Fatalf("unknown session %d %s", code, body)
	}

	code, body = serveDecisions(t, api, "repository=acme/demo&pr=7")
	var pr PullRequestDecisions
	if code != 200 || json.Unmarshal(body, &pr) != nil {
		t.Fatalf("%d %s", code, body)
	}
	// The tracker's sessions first, the ended one included, then the
	// review's, each once; an unknown vendor is left out.
	if pr.ReviewGeneratedAt == nil || !pr.ReviewGeneratedAt.Equal(generated) || len(pr.Sessions) != 3 {
		t.Fatalf("pull request %s", body)
	}
	live, gone, old := pr.Sessions[0], pr.Sessions[1], pr.Sessions[2]
	if gone.SessionID != "gone" || gone.Live {
		t.Fatalf("ended tracked session %+v", gone)
	}
	if live.SessionID != "run" || !live.Live || live.Title != "Live one" || len(live.Decisions) != 2 ||
		live.Decisions[0].Provisional || !live.Decisions[1].Provisional {
		t.Fatalf("live session %+v", live)
	}
	if old.SessionID != "old" || old.Live || old.Title != "Port the parser" || old.Coverage != CoverageSkipped ||
		old.SkipReason != ReasonUnsupported || !old.Capturing || len(old.Decisions) != 0 {
		t.Fatalf("linked session %+v", old)
	}

	for _, query := range []string{"", "agent=claude-code", "repository=acme/demo", "repository=acme/demo&pr=0", "agent=a&session=s&repository=acme/demo&pr=7"} {
		if code, _ := serveDecisions(t, api, query); code != http.StatusBadRequest {
			t.Errorf("%q answered %d", query, code)
		}
	}
}

func TestAPIPullRequestWithoutReview(t *testing.T) {
	s := openStore(t, &clock{t0})
	api := &API{Store: s}
	code, body := serveDecisions(t, api, "repository=acme/demo&pr=7")
	var pr PullRequestDecisions
	if code != 200 || json.Unmarshal(body, &pr) != nil || pr.ReviewGeneratedAt != nil || pr.Sessions == nil || len(pr.Sessions) != 0 {
		t.Fatalf("%d %s", code, body)
	}
}

func TestLedgerReadsAttemptsSince(t *testing.T) {
	ctx := context.Background()
	c := &clock{t0}
	s := openStore(t, c)
	first, _ := s.Begin(ctx, Attempt{Agent: "claude-code", ID: "s", Repository: "acme/demo", Backend: "claude", Model: "m", Mode: "full", Reserved: 0.1})
	if err := s.Settle(ctx, first, AttemptSucceeded, "", new(0.02), decisions.Usage{}); err != nil {
		t.Fatal(err)
	}
	c.now = t0.Add(time.Hour)
	second, _ := s.Begin(ctx, Attempt{Agent: "claude-code", ID: "s", Repository: "acme/demo", Backend: "claude", Model: "m", Mode: "extend", Reserved: 0.1})
	if err := s.Settle(ctx, second, AttemptFailed, "timeout", nil, decisions.Usage{}); err != nil {
		t.Fatal(err)
	}
	all, err := s.Ledger(ctx, t0)
	if err != nil || len(all) != 2 || all[0].Cost == nil || *all[0].Cost != 0.02 || all[1].Cost != nil || all[1].Reason != "timeout" || all[1].Backend != "claude" {
		t.Fatalf("%+v %v", all, err)
	}
	if later, _ := s.Ledger(ctx, t0.Add(time.Minute)); len(later) != 1 {
		t.Fatalf("since a minute later: %+v", later)
	}
}

func TestAPISaysWhyCaptureIsOff(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, &clock{t0})
	if err := s.Skip(ctx, Meta{Agent: "claude-code", ID: "kept", Repository: "acme/demo"}, ReasonTooLarge); err != nil {
		t.Fatal(err)
	}
	off := false
	for name, tt := range map[string]struct {
		edit    func(*fakeRegistry)
		session string
		want    string
	}{
		"on":             {func(*fakeRegistry) {}, "kept", ""},
		"paused":         {func(r *fakeRegistry) { r.prefs.Automatic = false }, "kept", CaptureOffPaused},
		"switch off":     {func(r *fakeRegistry) { r.prefs.Decisions.Enabled = &off }, "kept", CaptureOffSwitch},
		"repository off": {func(r *fakeRegistry) { r.settings.Decisions = &off }, "kept", CaptureOffRepository},
		"not picked":     {func(r *fakeRegistry) { r.picked = false }, "kept", CaptureOffNotPicked},
		// A session never captured takes its repository from the tracker.
		"tracked": {func(r *fakeRegistry) { r.settings.Decisions = &off }, "tracked", CaptureOffRepository},
		"unknown": {func(*fakeRegistry) {}, "unknown", CaptureOffNotPicked},
	} {
		registry := &fakeRegistry{prefs: repos.DefaultPreferences(), picked: true}
		tt.edit(registry)
		api := &API{Store: s, Registry: registry, Live: func() []livesessions.Session {
			return []livesessions.Session{{Agent: "claude-code", ID: "tracked", Repository: "acme/demo"}}
		}}
		code, body := serveDecisions(t, api, "agent=claude-code&session="+tt.session)
		var one struct{ Session SessionDecisions }
		if code != 200 || json.Unmarshal(body, &one) != nil || one.Session.CaptureOff != tt.want {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
}
