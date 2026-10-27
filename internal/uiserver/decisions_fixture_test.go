package uiserver_test

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/uiserver"
)

// browserDecisions opens the browser fixture's decision store and fills it as
// capture would have: the decisions of the two live sessions on PR 7, one
// with a call in flight, and a ledger of calls with a known cost, an unknown
// one and a failure. The fixture's review of PR 7 was generated on
// 2026-09-10, so the decisions dated later are provisional.
func browserDecisions(home string) (*decisionlog.Store, error) {
	store, err := decisionlog.Open(filepath.Join(home, "decisions.sqlite"), decisionlog.OpenOptions{})
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	now := time.Now()
	decision := func(id, title, summary, rationale string, category decisions.Category, who decisions.Who, turn int, files ...string) decisions.Decision {
		return decisions.Decision{ID: id, Title: title, Summary: summary, Rationale: rationale, Category: category, Who: who, Turns: []int{turn}, Files: files}
	}
	seed := []decisionlog.Extraction{
		{Meta: decisionlog.Meta{Agent: "claude-code", ID: "live-e2e", Repository: "acme/demo", Branch: "feature", Fingerprint: "fixture"},
			Decisions: []decisions.Decision{
				decision("dec-viewer", "Reuse the hosted PR page's viewer", "Serve saved reviews with the hosted viewer instead of a second implementation.",
					"One viewer keeps the local and hosted pages in step.", decisions.CategoryArchitecture, decisions.WhoHuman, 1, "app/src/review.tsx"),
				decision("dec-namespace", "Keep every app request under the session namespace", "Build API and event URLs from the namespaced base path.",
					"The cookie is scoped to the namespace, so nothing outside it authenticates.", decisions.CategoryInterface, decisions.WhoApproved, 2, "app/src/api.ts", "internal/uiserver/server.go"),
				decision("dec-retry", "Retry a failed sessions read once", "Read the linked sessions again once before showing the failure.",
					"", decisions.CategoryBehavior, decisions.WhoAgent, 3, "internal/viewserver/sessions.go"),
			},
			TurnTimes: map[int]time.Time{1: time.Date(2026, 9, 8, 14, 5, 0, 0, time.UTC), 2: now.Add(-40 * time.Minute), 3: now.Add(-12 * time.Minute)}},
		{Meta: decisionlog.Meta{Agent: "claude-code", ID: "needs-e2e", Repository: "acme/demo", Branch: "feature", Fingerprint: "fixture"},
			Decisions: []decisions.Decision{
				decision("dec-migrations", "Run migrations in one transaction", "Apply the migration set atomically so a failure leaves the schema as it was.",
					"", decisions.CategoryImplementation, decisions.WhoHuman, 1, "migrations/runner.go"),
			},
			TurnTimes: map[int]time.Time{1: time.Date(2026, 9, 2, 9, 30, 0, 0, time.UTC)}},
	}
	for _, e := range seed {
		e.ContentHash = "fixture"
		if err := store.Save(ctx, e); err != nil {
			_ = store.Close()
			return nil, err
		}
	}
	for _, call := range []struct {
		id, state, reason string
		cost              *float64
	}{
		{"live-e2e", decisionlog.AttemptSucceeded, "", new(0.0123)},
		{"live-e2e", decisionlog.AttemptSucceeded, "", nil},
		{"needs-e2e", decisionlog.AttemptFailed, "timeout", nil},
		{"needs-e2e", decisionlog.AttemptSucceeded, "", new(0.0087)},
		{"needs-e2e", "", "", nil},
	} {
		attempt, err := store.Begin(ctx, decisionlog.Attempt{Agent: "claude-code", ID: call.id, Repository: "acme/demo", Backend: "claude", Model: "claude-haiku-5-5", Mode: "full", Reserved: 0.05})
		if err == nil && call.state != "" {
			err = store.Settle(ctx, attempt, call.state, call.reason, call.cost, decisions.Usage{})
		}
		if err != nil {
			_ = store.Close()
			return nil, err
		}
	}
	return store, nil
}

// browserDecisionsAPI serves the fixture's store as the daemon does.
func browserDecisionsAPI(store *decisionlog.Store, registry decisionlog.Registry, live *livesessions.Tracker) func(uiserver.LinkedSessions) http.Handler {
	return func(linked uiserver.LinkedSessions) http.Handler {
		return (&decisionlog.API{Store: store, Registry: registry, Live: live.Sessions, Linked: linked}).Handler()
	}
}
