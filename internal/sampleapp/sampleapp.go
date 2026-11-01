// Package sampleapp serves the sample project the local app's tour shows
// when the person has no data of their own yet. It is a small, invented
// project (example/notes): a git clone with a few pull requests and their
// reviews, agent sessions with their transcripts and decisions, and the
// features they belong to. Nothing in it comes from the person or anyone
// else.
//
// The project is written into its own directory the first time it is asked
// for, through the same stores the daemon keeps, and served by the same
// handlers, so it shows whatever the app shows. GitHub and the model are
// never reached: GitHub's answers are fixed, and every request that would
// change something is refused with a marked answer the app turns into a
// short notice.
package sampleapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/analytics"
	"github.com/baum-ai/baum/internal/automatic"
	"github.com/baum-ai/baum/internal/chat"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/focus"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/livesessions"
	"github.com/baum-ai/baum/internal/planusage"
	"github.com/baum-ai/baum/internal/prupdate"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/transcript"
	"github.com/baum-ai/baum/internal/uiserver"
	"github.com/baum-ai/baum/internal/uistate"
)

// Refused marks the answer to a request the sample project refuses, in its
// X-Baum-Sample header.
const Refused = "refused"

// Workspace serves the sample project's API. Its zero value is not usable;
// set Dir and Own.
type Workspace struct {
	// Dir is where the project is written, emptied first; Close removes it.
	Dir string
	// Own serves the person's own API. The tour's state is theirs, so its
	// requests go there.
	Own http.Handler

	mu      sync.Mutex
	api     http.Handler
	built   time.Time
	closers []func()
}

// fresh is how long a written project serves. Its sessions are timed from
// when it was written, so a later tour gets it written again rather than
// find the running session quiet for hours.
const fresh = 30 * time.Minute

func (w *Workspace) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/ui-state/tour" {
		w.Own.ServeHTTP(rw, r)
		return
	}
	// The sample keeps its own view state (what the Inbox folds, a
	// walkthrough's place), which changes nothing that matters.
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !strings.HasPrefix(r.URL.Path, "/api/ui-state/") {
		rw.Header().Set("X-Baum-Sample", Refused)
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusConflict)
		_, _ = rw.Write([]byte(`{"code":"sample","message":"This is the tour's sample project, so nothing was changed."}`))
		return
	}
	w.mu.Lock()
	if w.api == nil || time.Since(w.built) > fresh {
		w.release()
		api, err := w.build(context.Background(), time.Now())
		if err != nil {
			w.release()
			w.mu.Unlock()
			http.Error(rw, "The sample project could not be prepared: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.api, w.built = api, time.Now()
	}
	api := w.api
	w.mu.Unlock()
	api.ServeHTTP(rw, r)
}

// Close releases the project's stores and removes its directory.
func (w *Workspace) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.release()
	if w.Dir != "" {
		_ = os.RemoveAll(w.Dir)
	}
}

// release closes what the last build opened.
func (w *Workspace) release() {
	for i := len(w.closers) - 1; i >= 0; i-- {
		w.closers[i]()
	}
	w.api, w.closers = nil, nil
}

func (w *Workspace) build(ctx context.Context, now time.Time) (http.Handler, error) {
	if w.Dir == "" || w.Own == nil {
		return nil, errors.New("no directory or own API")
	}
	if err := os.RemoveAll(w.Dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(w.Dir, 0o700); err != nil {
		return nil, err
	}
	home, appDB := filepath.Join(w.Dir, "home"), filepath.Join(w.Dir, "app.sqlite")
	p, err := writeProject(ctx, filepath.Join(w.Dir, "projects", "notes"), now)
	if err != nil {
		return nil, fmt.Errorf("writing the project: %w", err)
	}
	registry, err := repos.Open(appDB)
	if err != nil {
		return nil, err
	}
	w.closers = append(w.closers, func() { _ = registry.Close() })
	clone, err := registry.Add(ctx, p.dir, nil)
	if err != nil {
		return nil, err
	}
	if err := writeReviews(ctx, p, clone.CommonDir, now); err != nil {
		return nil, fmt.Errorf("writing the reviews: %w", err)
	}
	events, err := hookevents.OpenStore(filepath.Join(w.Dir, "events"), nil)
	if err != nil {
		return nil, err
	}
	w.closers = append(w.closers, func() { _ = events.Close() })
	decided, err := decisionlog.Open(filepath.Join(w.Dir, "decisions.sqlite"), decisionlog.OpenOptions{})
	if err != nil {
		return nil, err
	}
	w.closers = append(w.closers, func() { _ = decided.Close() })
	if err := writeSessions(ctx, p, home, appDB, events, decided, now); err != nil {
		return nil, fmt.Errorf("writing the sessions: %w", err)
	}
	if err := decided.SeedSample(ctx, repository, listedPulls(p, now), summaries, features(now)); err != nil {
		return nil, fmt.Errorf("writing the features: %w", err)
	}
	if err := linkSessions(ctx, p, decided, now); err != nil {
		return nil, fmt.Errorf("linking the sessions: %w", err)
	}

	live := &livesessions.Tracker{Source: events, Linker: linker(p), StateDir: filepath.Join(w.Dir, "live")}
	live.Rebuild(ctx)
	live.ResolvePending(ctx)
	transcripts := &livesessions.Transcripts{Tracker: live, Render: transcript.NewRenderer("").Render, Home: home, Focus: focuser{}}
	queue, err := jobs.Open(filepath.Join(w.Dir, "jobs", "jobs.sqlite"), refusing{}, 1, nil)
	if err != nil {
		return nil, err
	}
	w.closers = append(w.closers, func() { _ = queue.Shutdown(context.Background()) })
	cfg := config.Default()
	cfg.Backend, cfg.Model = config.BackendClaude, "claude-sonnet-4-5"
	queue.SetChat(&chat.Service{Registry: registry, Config: cfg, Root: filepath.Join(w.Dir, "review-runs")})
	groups, err := reviewgroups.Open(appDB)
	if err != nil {
		return nil, err
	}
	w.closers = append(w.closers, func() { _ = groups.Close() })

	prs := pullRequests(p, now)
	byNumber := map[int]discovery.PullRequest{}
	for _, pr := range prs {
		byNumber[pr.Number] = pr
	}
	disc := sampleDiscovery{prs: prs}
	client := github.New(func(context.Context, string) (string, error) { return "sample", nil }, github.Options{Transport: gitHub{p: p, prs: byNumber}})
	index := &reviewindex.Service{Registry: registry, Discovery: disc}
	actions := &analytics.Actions{Path: appDB}
	sessionIndex := &analytics.Index{Path: appDB, Clones: registry, Stores: analytics.Stores{ClaudeProjectsDir: filepath.Join(home, ".claude", "projects")}}
	if err := sessionIndex.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("indexing the sessions: %w", err)
	}
	insights := &analytics.Service{Registry: registry, Index: sessionIndex, Jobs: queue.List, Actions: actions,
		Plans:    &planusage.History{Path: appDB, Service: &planusage.Service{Sources: []planusage.Source{noPlan{}}}},
		Captures: decided.Ledger, Pulls: decided.PullFlow, Login: login}
	timeline := &decisionlog.Timeline{Store: decided, Registry: registry, Home: home, Login: login, Transcripts: transcripts,
		Clone: func(context.Context, string) decisionlog.TimelineClone { return owner{} }}
	uiState := &uistate.Store{Path: appDB}
	quiet := func() {}
	api := uiserver.LocalAPI{Registry: registry, Config: cfg, Changed: quiet,
		PRs:        uiserver.DiscoveryHandler(registry, disc, nil),
		Index:      index.Handler(),
		Jobs:       queue.Handler(),
		Chat:       uiserver.ChatHandler(registry, queue),
		Automatic:  (&automatic.Scheduler{Registry: registry, Path: appDB, Discover: disc.Poll}).Handler(),
		Live:       transcripts.Handler(),
		GitHubAuth: connected,
		Merge:      uiserver.MergeHandler(registry, actions.RecordMerges(client), func(context.Context, github.Repository, int, string) {}, quiet),
		Update:     uiserver.UpdateHandler(registry, actions.RecordUpdates(prupdate.Service{Client: client}), func(context.Context, github.Repository) {}, quiet),
		PlanUsage:  planUsage,
		Analytics:  insights.Handler(),
		Timeline:   timeline.Handler(),
		UIState:    uiState.Handler(),
		Inboxes:    (&inboxes.API{Store: inboxes.Open(appDB)}).Handler(),
		Groups:     (&reviewgroups.API{Store: groups, Registry: registry, Index: index.Build, Changed: quiet}).Handler(),
		Decisions: func(linked uiserver.LinkedSessions) http.Handler {
			return (&decisionlog.API{Store: decided, Registry: registry, Live: live.Sessions, Linked: linked}).Handler()
		},
		DecisionModel: func(context.Context, repos.Preferences) uiserver.DecisionModel {
			return uiserver.DecisionModel{Backend: "claude", Model: "claude-haiku-4-5"}
		},
	}.Handler()
	w.closers = append(w.closers, api.Close)
	return api, nil
}

func login(context.Context) string { return me }

// owner is who the sample's sessions belong to; the project keeps no
// saved review the Timeline would read.
type owner struct{}

func (owner) Person(context.Context) *decisionlog.Person {
	name := myName
	return &decisionlog.Person{Email: me + "@example.com", Name: &name}
}

func (owner) Review(context.Context, int, string) (string, string) { return "", "none" }

// linker places each session's directory on its branch's pull request.
func linker(p project) livesessions.Linker {
	links := map[string]livesessions.Link{}
	for _, s := range agentSessions {
		if s.live != "" {
			links[s.cwd(p)] = livesessions.Link{Repository: repository, Branch: s.branch, PR: s.pr}
		}
	}
	return fixedLinks(links)
}

type fixedLinks map[string]livesessions.Link

func (l fixedLinks) Link(_ context.Context, cwd string) (livesessions.Link, bool) {
	link, ok := l[cwd]
	return link, ok
}

// focuser offers to bring each running session forward; the request to do
// it is refused like any other change.
type focuser struct{}

func (focuser) Predict(string, *hookevents.Terminal) string { return "session" }
func (focuser) Focus(context.Context, string, string, *hookevents.Terminal) focus.Result {
	return focus.Result{}
}

// refusing runs no job: the sample refuses every request to start one.
type refusing struct{}

func (refusing) Prepare(context.Context, jobs.Item) (jobs.Prepared, string, error) {
	return jobs.Prepared{}, "", errors.New("the sample project runs no jobs")
}
func (refusing) Execute(context.Context, jobs.Prepared, func(jobs.Progress)) (*float64, error) {
	return nil, errors.New("the sample project runs no jobs")
}

// noPlan reads no plan: the history is written beforehand.
type noPlan struct{}

func (noPlan) Read(context.Context) planusage.Plan { return planusage.Plan{Provider: "claude"} }

// connected says GitHub is connected as the sample's person.
var connected = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"state":"connected","host":"github.com","login":"` + me + `","override":false,"saved":true}`))
})

// planUsage reads Claude's two windows, part used.
var planUsage = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"plans":[{"provider":"claude","available":true,"windows":[{"name":"5-hour","used_percent":34,"resets_at":%q},{"name":"weekly","used_percent":46,"resets_at":%q}],"fetched_at":%q}]}`,
		at(2*time.Hour), at(3*day), at(0))
})
