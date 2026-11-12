package decisionlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/livesessions"
)

type relevanceBackend struct {
	calls  int
	title  string
	inputs [][]relevanceCandidate
	reply  func([]relevanceCandidate) ([]relevanceResult, error)
}

func (b *relevanceBackend) Name() string                          { return agent.BackendClaude }
func (b *relevanceBackend) Check(context.Context) (string, error) { return "1", nil }
func (b *relevanceBackend) Run(context.Context, agent.Request) (agent.Result, error) {
	panic("not an agent task")
}
func (b *relevanceBackend) Complete(_ context.Context, req agent.CompleteRequest) (agent.Completion, error) {
	b.calls++
	var input struct {
		Title     string               `json:"title"`
		Decisions []relevanceCandidate `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(req.User), &input); err != nil {
		return agent.Completion{}, err
	}
	b.inputs = append(b.inputs, input.Decisions)
	b.title = input.Title
	results, err := b.reply(input.Decisions)
	encoded, _ := json.Marshal(struct {
		Decisions []relevanceResult `json:"decisions"`
	}{results})
	return agent.Completion{Document: encoded, Model: "claude-haiku-5-5", Usage: agent.Usage{Input: 100, Output: 20}}, err
}

func relevanceFixture(t *testing.T) (*Relevance, *relevanceBackend, *clock) {
	t.Helper()
	c := &clock{t0}
	s := openStore(t, c)
	b := &relevanceBackend{reply: func(ds []relevanceCandidate) ([]relevanceResult, error) {
		out := make([]relevanceResult, len(ds))
		for i, d := range ds {
			out[i] = relevanceResult{ID: d.ID, State: "relevant", Reason: "The diff implements this requirement."}
		}
		return out, nil
	}}
	backend := func(string) generate.CheckedBackend { return b }
	r := &Relevance{Store: s, Config: config.Config{Backend: config.BackendClaude}, Now: c.Now, Backends: generate.Backends{Claude: backend},
		Patch: func(context.Context, RelevanceContext) (string, error) {
			return "diff --git a/queue.go b/queue.go\n--- a/queue.go\n+++ b/queue.go\n@@ -1 +1 @@\n-limit = 0\n+limit = 3\n", nil
		}}
	for _, n := range []int{7, 8} {
		if err := s.SavePulls(context.Background(), "acme/demo", []github.ListedPull{{Number: n, Title: fmt.Sprintf("PR %d", n), BaseSHA: "base", HeadSHA: "head", CreatedAt: t0, UpdatedAt: t0}}, "", t0); err != nil {
			t.Fatal(err)
		}
	}
	return r, b, c
}

func saveRelevanceDecisions(t *testing.T, r *Relevance, ds ...decisions.Decision) {
	t.Helper()
	if err := r.Store.Save(context.Background(), Extraction{Meta: Meta{Agent: "claude-code", ID: "session", Repository: "acme/demo"}, ContentHash: relevanceHash(ds), Decisions: ds}); err != nil {
		t.Fatal(err)
	}
}

func filtered(t *testing.T, r *Relevance, number int, head string) PullRequestSession {
	t.Helper()
	views, err := r.Sessions(context.Background(), "Acme/Demo", number, head, []SessionKey{{"claude-code", "session"}})
	if err != nil {
		t.Fatal(err)
	}
	return PullRequestSession{SessionDecisions: views[0]}
}

func processRelevance(t *testing.T, r *Relevance, number int) bool {
	t.Helper()
	more, err := r.process(context.Background(), relevanceWork{repository: "acme/demo", number: number, keys: []SessionKey{{"claude-code", "session"}}})
	if err != nil {
		t.Fatal(err)
	}
	return more
}

func TestRelevanceFiltersEachPRAndKeepsSessionHistory(t *testing.T) {
	r, b, _ := relevanceFixture(t)
	fileless := decision("human", "Retry three times", 1)
	fileless.Files = nil
	saveRelevanceDecisions(t, r, fileless, decision("same-file", "Change the queue's colours", 2), decision("both", "Bound memory used by queues", 3))
	b.reply = func(ds []relevanceCandidate) ([]relevanceResult, error) {
		out := make([]relevanceResult, len(ds))
		for i, d := range ds {
			state := "relevant"
			if d.ID == "same-file" || (b.title == "PR 8" && d.ID == "human") {
				state = "unrelated"
			}
			out[i] = relevanceResult{ID: d.ID, State: state, Reason: "Supported by the changes."}
		}
		return out, nil
	}
	first := filtered(t, r, 7, "")
	if len(first.Decisions) != 0 || first.Relevance.Pending != 3 || b.calls != 0 {
		t.Fatalf("read ran classification: %+v", first)
	}
	processRelevance(t, r, 7)
	shown := filtered(t, r, 7, "")
	if len(shown.Decisions) != 2 || shown.Decisions[0].ID != "human" || shown.Relevance.Unrelated != 1 {
		t.Fatalf("filtered: %+v", shown)
	}
	// Same session, different PR: a decision is never inherited by session.
	if s := filtered(t, r, 8, ""); len(s.Decisions) != 0 || s.Relevance.Pending != 3 {
		t.Fatalf("PR 8: %+v", s)
	}
	processRelevance(t, r, 8)
	if s := filtered(t, r, 8, ""); len(s.Decisions) != 1 || s.Decisions[0].ID != "both" {
		t.Fatalf("PR 8 after check: %+v", s)
	}
	full, err := r.Store.Sessions(context.Background(), SessionKey{"claude-code", "session"})
	if err != nil || len(full[0].Decisions) != 3 || full[0].Relevance != nil {
		t.Fatalf("history changed: %+v %v", full, err)
	}
}

func TestRelevanceCacheReusesDiffAndOnlyClassifiesChangedDecisions(t *testing.T) {
	r, b, c := relevanceFixture(t)
	patches := 0
	patch := r.Patch
	r.Patch = func(ctx context.Context, p RelevanceContext) (string, error) { patches++; return patch(ctx, p) }
	saveRelevanceDecisions(t, r, decision("a", "Retry three times", 1))
	processRelevance(t, r, 7)
	for range 20 {
		if s := filtered(t, r, 7, ""); len(s.Decisions) != 1 {
			t.Fatal("cache missed")
		}
	}
	processRelevance(t, r, 7)
	if b.calls != 1 || patches != 1 {
		t.Fatalf("unchanged work repeated: calls=%d patches=%d", b.calls, patches)
	}
	c.now = c.now.Add(time.Minute)
	saveRelevanceDecisions(t, r, decision("a", "Retry three times", 1), decision("b", "Set a timeout", 2))
	processRelevance(t, r, 7)
	if b.calls != 2 || patches != 1 || len(b.inputs[1]) != 1 || b.inputs[1][0].ID != "b" {
		t.Fatalf("new decision repeated old work: %+v patches=%d", b.inputs, patches)
	}
	revised := decision("a", "Retry three times", 1)
	revised.Evidence = "Use three retries"
	saveRelevanceDecisions(t, r, revised, decision("b", "Set a timeout", 2))
	if s := filtered(t, r, 7, ""); len(s.Decisions) != 1 || s.Decisions[0].ID != "b" || s.Relevance.Pending != 1 {
		t.Fatalf("stale evidence shown: %+v", s)
	}
	processRelevance(t, r, 7)
	if b.calls != 3 || patches != 1 || b.inputs[2][0].ID != "a" {
		t.Fatal("evidence did not invalidate")
	}
	// Persistence, not a per-process memory cache.
	var sequence int
	var name, path string
	if err := r.Store.db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	other, err := Open(path, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	copy := &Relevance{Store: other, Config: r.Config, Now: r.Now}
	if s := filtered(t, copy, 7, ""); len(s.Decisions) != 2 {
		t.Fatal("new store connection lost cached relevance")
	}
	rows, err := r.Store.relevanceRows(context.Background(), "acme/demo", 7)
	if err != nil || len(rows) != 2 {
		t.Fatalf("cache rows: %v %v", rows, err)
	}
	var ledger int
	if err := r.Store.db.QueryRow(`SELECT count(*) FROM capture_attempt WHERE stage='decision_relevance' AND state='succeeded'`).Scan(&ledger); err != nil || ledger != 3 {
		t.Fatalf("ledger %d %v", ledger, err)
	}
}

func TestRelevanceInvalidatesPRAndModelChanges(t *testing.T) {
	for _, column := range []string{"head_sha", "base_sha", "title", "body"} {
		t.Run(column, func(t *testing.T) {
			r, b, _ := relevanceFixture(t)
			saveRelevanceDecisions(t, r, decision("a", "Retry", 1))
			processRelevance(t, r, 7)
			if _, err := r.Store.db.Exec(`UPDATE pull_request SET ` + column + `='changed' WHERE number=7`); err != nil {
				t.Fatal(err)
			}
			if s := filtered(t, r, 7, ""); len(s.Decisions) != 0 || s.Relevance.Pending != 1 {
				t.Fatalf("old result survived %s: %+v", column, s)
			}
			processRelevance(t, r, 7)
			if b.calls != 2 {
				t.Fatal("not reclassified")
			}
		})
	}
	r, _, _ := relevanceFixture(t)
	saveRelevanceDecisions(t, r, decision("a", "Retry", 1))
	processRelevance(t, r, 7)
	if s := filtered(t, r, 7, "new-page-head"); len(s.Decisions) != 0 || s.Relevance.Unavailable != 1 {
		t.Fatalf("stale feature metadata shown: %+v", s)
	}
	r.Config.Effort = "high"
	// Extraction effort is separate from the review effort.
	f := newFeatureFixture(t)
	f.store = r.Store
	r.Registry = registryOf(f)
	f.prefs.Decisions.Model = new("claude-test")
	if s := filtered(t, r, 7, ""); len(s.Decisions) != 0 || s.Relevance.Pending != 1 {
		t.Fatalf("model did not invalidate: %+v", s)
	}
}

func TestRelevanceFailureBackoffUnknownAndLimits(t *testing.T) {
	r, b, c := relevanceFixture(t)
	saveRelevanceDecisions(t, r, decision("a", "Retry", 1))
	b.reply = func([]relevanceCandidate) ([]relevanceResult, error) { return nil, errors.New("provider failed") }
	_, err := r.process(context.Background(), relevanceWork{repository: "acme/demo", number: 7, keys: []SessionKey{{"claude-code", "session"}}})
	if err == nil {
		t.Fatal("failure hidden")
	}
	if s := filtered(t, r, 7, ""); len(s.Decisions) != 0 || s.Relevance.Failed != 1 {
		t.Fatalf("failure: %+v", s)
	}
	processRelevance(t, r, 7)
	if b.calls != 1 {
		t.Fatal("backoff ignored")
	}
	c.now = c.now.Add(relevanceRetry)
	b.reply = func(ds []relevanceCandidate) ([]relevanceResult, error) {
		return []relevanceResult{{ID: ds[0].ID, State: "uncertain", Reason: "Missing implementation evidence."}}, nil
	}
	processRelevance(t, r, 7)
	if s := filtered(t, r, 7, ""); s.Relevance.Uncertain != 1 || len(s.Decisions) != 0 {
		t.Fatalf("unknown shown: %+v", s)
	}
	processRelevance(t, r, 7)
	if b.calls != 2 {
		t.Fatal("uncertain result retried unchanged")
	}
	// Missing PR context is distinct from no extracted decisions.
	if s := filtered(t, r, 99, ""); s.Relevance.Unavailable != 1 || s.Relevance.Pending != 0 {
		t.Fatalf("missing: %+v", s)
	}
}

func TestRelevanceBatchesAndQueueAreBounded(t *testing.T) {
	r, b, _ := relevanceFixture(t)
	ds := make([]decisions.Decision, relevanceBatchLimit+5)
	for i := range ds {
		ds[i] = decision(fmt.Sprint(i), "Choice", i+1)
	}
	saveRelevanceDecisions(t, r, ds...)
	if !processRelevance(t, r, 7) || processRelevance(t, r, 7) {
		t.Fatal("batch continuation wrong")
	}
	if len(b.inputs) != 2 || len(b.inputs[0]) != relevanceBatchLimit || len(b.inputs[1]) != 5 {
		t.Fatalf("batch sizes: %v", b.inputs)
	}
	for range 100 {
		r.enqueue(relevanceWork{repository: "acme/demo", number: 7, keys: []SessionKey{{"claude-code", "session"}}})
	}
	if len(r.queue) != 1 || len(r.queued) != 1 {
		t.Fatal("queue did not deduplicate")
	}
	for i := 0; i < 100; i++ {
		r.enqueue(relevanceWork{repository: "other/repo", number: i})
	}
	if len(r.queued) != relevanceQueueLimit || len(r.queue) != relevanceQueueLimit {
		t.Fatal("queue not bounded")
	}
	if r.enqueue(relevanceWork{repository: "huge/repo", number: 1, keys: make([]SessionKey, relevanceSessionLimit+1)}) {
		t.Fatal("oversized session request accepted")
	}
}

func TestRelevanceRejectsMalformedIDsAndOversizedContext(t *testing.T) {
	for _, raw := range []string{`{"decisions":[]}`, `{"decisions":[{"id":"invented","state":"relevant","reason":"r"}]}`, `{"decisions":[{"id":"a","state":"sure","reason":"r"}]}`, `{"decisions":[{"id":"a","state":"relevant","reason":""}]}`} {
		if _, err := relevanceReply([]byte(raw), []relevanceCandidate{{ID: "a"}}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	r, b, _ := relevanceFixture(t)
	saveRelevanceDecisions(t, r, decision("a", "Retry", 1))
	r.Patch = func(context.Context, RelevanceContext) (string, error) {
		return strings.Repeat("x", relevancePatchLimit+1), nil
	}
	_, err := r.process(context.Background(), relevanceWork{repository: "acme/demo", number: 7, keys: []SessionKey{{"claude-code", "session"}}})
	if err == nil || b.calls != 0 {
		t.Fatal("oversized diff classified")
	}
}

func TestRelevanceWorkerStopsAndDoesNotPoll(t *testing.T) {
	r, _, _ := relevanceFixture(t)
	var patches atomic.Int64
	r.Patch = func(context.Context, RelevanceContext) (string, error) { patches.Add(1); return "diff", nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	if patches.Load() != 0 || len(r.queued) != 0 {
		t.Fatal("idle worker did work")
	}
}

func TestRelevanceWorkerDeduplicatesRunningWorkAndStopsAfterFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			r, b, _ := relevanceFixture(t)
			count := 1
			if fail {
				count = relevanceBatchLimit + 1
			}
			ds := make([]decisions.Decision, count)
			for i := range ds {
				ds[i] = decision(fmt.Sprint(i), "Choice", i+1)
			}
			saveRelevanceDecisions(t, r, ds...)
			started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
			original := b.reply
			b.reply = func(ds []relevanceCandidate) ([]relevanceResult, error) {
				close(started)
				<-release
				if fail {
					return nil, errors.New("provider failed")
				}
				return original(ds)
			}
			changes := 0
			r.Store.Changed = func() {
				changes++
				if changes == 3 {
					close(finished)
				}
			}
			filtered(t, r, 7, "")
			ctx, cancel := context.WithCancel(context.Background())
			stopped := make(chan struct{})
			go func() { r.Run(ctx); close(stopped) }()
			defer func() {
				cancel()
				select {
				case <-release:
				default:
					close(release)
				}
				<-stopped
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not start")
			}
			for range 20 {
				filtered(t, r, 7, "")
			}
			close(release)
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not finish")
			}
			if b.calls != 1 {
				t.Fatalf("duplicate work: %d calls", b.calls)
			}
			p, _ := r.Store.relevanceContext(context.Background(), "acme/demo", 7)
			prefs, _, _ := r.gate(context.Background(), "acme/demo")
			if fail && r.enqueue(relevanceWork{repository: "acme/demo", number: 7, version: r.fingerprint(p, prefs)}) {
				t.Fatal("failed PR was not backed off")
			}
		})
	}
}

func TestRelevanceCooldownDoesNotBlockChangedInputs(t *testing.T) {
	r, _, _ := relevanceFixture(t)
	r.init()
	r.retryAt["acme/demo#7"] = relevanceCooldown{until: t0.Add(time.Hour), version: "old"}
	if r.enqueue(relevanceWork{repository: "acme/demo", number: 7, version: "old"}) {
		t.Fatal("same input bypassed cooldown")
	}
	if !r.enqueue(relevanceWork{repository: "acme/demo", number: 7, version: "new"}) {
		t.Fatal("changed PR/model input was blocked by old failure")
	}
}

func TestRelevanceBadModelReferenceIsAFailedLedgerAttempt(t *testing.T) {
	r, b, _ := relevanceFixture(t)
	saveRelevanceDecisions(t, r, decision("a", "Choice", 1))
	b.reply = func([]relevanceCandidate) ([]relevanceResult, error) {
		return []relevanceResult{{ID: "invented", State: "relevant", Reason: "A guess"}}, nil
	}
	_, err := r.process(context.Background(), relevanceWork{repository: "acme/demo", number: 7, keys: []SessionKey{{"claude-code", "session"}}})
	if err == nil {
		t.Fatal("invented ID accepted")
	}
	var state string
	if err := r.Store.db.QueryRow(`SELECT state FROM capture_attempt WHERE stage='decision_relevance'`).Scan(&state); err != nil || state != AttemptFailed {
		t.Fatalf("ledger state=%s err=%v", state, err)
	}
	if s := filtered(t, r, 7, ""); len(s.Decisions) != 0 || s.Relevance.Failed != 1 {
		t.Fatal("invalid classification shown")
	}
}

func TestSessionReadDoesNotDecodeExtractionAndKeepsPendingWithoutCapture(t *testing.T) {
	r, _, _ := relevanceFixture(t)
	saveRelevanceDecisions(t, r, decision("a", "Retry", 1))
	if _, err := r.Store.db.Exec(`UPDATE capture_session SET decisions_json='damaged extraction'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Store.Begin(context.Background(), Attempt{Agent: "codex", ID: "new", Backend: "codex", Model: "test"}); err != nil {
		t.Fatal(err)
	}
	views, err := r.Store.Sessions(context.Background(), SessionKey{"claude-code", "session"}, SessionKey{"codex", "new"})
	if err != nil || len(views[0].Decisions) != 1 || !views[1].Capturing || views[1].UpdatedAt != nil {
		t.Fatalf("views=%+v err=%v", views, err)
	}
}

func TestRelevanceAPIWithoutReviewAndProvisionalMarks(t *testing.T) {
	r, _, _ := relevanceFixture(t)
	saveRelevanceDecisions(t, r, decision("a", "Retry", 1))
	api := &API{Store: r.Store, Relevance: r, Live: func() []livesessions.Session {
		return []livesessions.Session{{Agent: "claude-code", ID: "session", Repository: "acme/demo", PR: 7, State: livesessions.StateRunning}}
	}}
	read := func(query string) PullRequestDecisions {
		t.Helper()
		code, body := serveDecisions(t, api, query)
		var answer PullRequestDecisions
		if code != 200 || json.Unmarshal(body, &answer) != nil {
			t.Fatalf("API %d %s", code, body)
		}
		return answer
	}
	if answer := read("repository=acme/demo&pr=7&head=head"); answer.ReviewGeneratedAt != nil || answer.Sessions[0].Relevance.Pending != 1 || len(answer.Sessions[0].Decisions) != 0 {
		t.Fatalf("pending without review: %+v", answer)
	}
	processRelevance(t, r, 7)
	if answer := read("repository=acme/demo&pr=7&head=head"); len(answer.Sessions[0].Decisions) != 1 || answer.Sessions[0].Decisions[0].Provisional {
		t.Fatalf("classified without review: %+v", answer)
	}
	if answer := read("repository=acme/demo&pr=7&head=force-pushed"); len(answer.Sessions[0].Decisions) != 0 || answer.Sessions[0].Relevance.Unavailable != 1 {
		t.Fatal("stale head shown")
	}
}

func TestRelevanceHoldsOtherRepositoryDecisionsAndHonoursGate(t *testing.T) {
	r, b, _ := relevanceFixture(t)
	saveRelevanceDecisions(t, r, decision("a", "Retry", 1))
	processRelevance(t, r, 7)
	// A session's repository can change; a decision's own repository is the boundary.
	if _, err := r.Store.db.Exec(`UPDATE decision SET repository='different/repo'`); err != nil {
		t.Fatal(err)
	}
	if s := filtered(t, r, 7, ""); len(s.Decisions) != 0 || s.Relevance.Pending != 0 {
		t.Fatal("another repository's decision leaked")
	}
	f := newFeatureFixture(t)
	f.store = r.Store
	f.prefs.Automatic = false
	r.Registry = registryOf(f)
	saveRelevanceDecisions(t, r, decision("a", "new choice", 1))
	if s := filtered(t, r, 7, ""); s.Relevance.Unavailable != 1 {
		t.Fatal("paused capture queued classification")
	}
	processRelevance(t, r, 7)
	if b.calls != 1 {
		t.Fatal("paused worker called model")
	}
}

func TestRelevanceOversizedDiffIsNotReadForEachBatch(t *testing.T) {
	r, b, c := relevanceFixture(t)
	patches := 0
	r.Patch = func(context.Context, RelevanceContext) (string, error) {
		patches++
		return strings.Repeat("x", relevancePatchLimit+1), nil
	}
	ds := make([]decisions.Decision, relevanceBatchLimit+1)
	for i := range ds {
		ds[i] = decision(fmt.Sprint(i), "Choice", i+1)
	}
	saveRelevanceDecisions(t, r, ds...)
	for range 2 {
		_, err := r.process(context.Background(), relevanceWork{repository: "acme/demo", number: 7, keys: []SessionKey{{"claude-code", "session"}}})
		if !errors.Is(err, errRelevanceContextLarge) {
			t.Fatalf("size failure: %v", err)
		}
	}
	c.now = c.now.Add(24 * time.Hour)
	processRelevance(t, r, 7)
	if patches != 1 || b.calls != 0 {
		t.Fatalf("oversized revision repeated work: patches=%d calls=%d", patches, b.calls)
	}
	if s := filtered(t, r, 7, ""); s.Relevance.Uncertain != len(ds) {
		t.Fatalf("large diff state: %+v", s.Relevance)
	}
}

func TestRelevanceQueriesUseIndexes(t *testing.T) {
	r, _, _ := relevanceFixture(t)
	for _, query := range []string{
		`SELECT title, body, base_sha, head_sha FROM pull_request WHERE repository='acme/demo' AND number=7`,
		`SELECT decision_id, context_hash, decision_hash, state FROM pr_decision_relevance WHERE repository='acme/demo' AND number=7`,
		`SELECT patch FROM pr_relevance_patch WHERE repository='acme/demo' AND number=7 AND revision='revision'`,
	} {
		rows, err := r.Store.db.Query(`EXPLAIN QUERY PLAN ` + query)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		if !strings.Contains(strings.Join(plan, " "), "SEARCH") || strings.Contains(strings.Join(plan, " "), "SCAN") {
			t.Fatalf("unindexed query: %s\n%v", query, plan)
		}
	}
}

func TestRelevanceCachedReadDoesNotDecodeHiddenDecisionsAndUpgradesOldCaptures(t *testing.T) {
	r, b, _ := relevanceFixture(t)
	saveRelevanceDecisions(t, r, decision("show", "Retry", 1), decision("hide", "Other work", 2))
	// This is an existing capture from before fingerprints were introduced.
	if _, err := r.Store.db.Exec(`DELETE FROM decision_relevance_input`); err != nil {
		t.Fatal(err)
	}
	if s := filtered(t, r, 7, ""); s.Relevance.Pending != 2 {
		t.Fatal("older capture did not queue")
	}
	b.reply = func(ds []relevanceCandidate) ([]relevanceResult, error) {
		out := make([]relevanceResult, len(ds))
		for i, d := range ds {
			state := "relevant"
			if d.ID == "hide" {
				state = "unrelated"
			}
			out[i] = relevanceResult{ID: d.ID, State: state, Reason: "Compared with the changes."}
		}
		return out, nil
	}
	processRelevance(t, r, 7)
	// A malformed hidden payload would fail full decision materialization.
	if _, err := r.Store.db.Exec(`UPDATE decision SET turns_json='broken JSON' WHERE decision_id='hide'`); err != nil {
		t.Fatal(err)
	}
	if s := filtered(t, r, 7, ""); len(s.Decisions) != 1 || s.Decisions[0].ID != "show" || s.Relevance.Unrelated != 1 {
		t.Fatalf("hidden content was read: %+v", s)
	}
	var hashes int
	if err := r.Store.db.QueryRow(`SELECT count(*) FROM decision_relevance_input`).Scan(&hashes); err != nil || hashes != 2 {
		t.Fatalf("lazy fingerprints=%d %v", hashes, err)
	}
}

func BenchmarkRelevanceCachedRead(b *testing.B) {
	ctx := context.Background()
	s, err := Open(filepath.Join(b.TempDir(), "state", "app.sqlite"), OpenOptions{})
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	r := &Relevance{Store: s, Config: config.Config{Backend: config.BackendClaude}}
	if err := s.SavePulls(ctx, "acme/demo", []github.ListedPull{{Number: 7, Title: "Retry", BaseSHA: "base", HeadSHA: "head", CreatedAt: t0, UpdatedAt: t0}}, "", t0); err != nil {
		b.Fatal(err)
	}
	ds := make([]decisions.Decision, 500)
	for i := range ds {
		ds[i] = decision(fmt.Sprint(i), "Retry requests at most three times", i+1)
	}
	if err := s.Save(ctx, Extraction{Meta: Meta{Agent: "claude-code", ID: "session", Repository: "acme/demo"}, ContentHash: "h", Decisions: ds}); err != nil {
		b.Fatal(err)
	}
	p, err := s.relevanceContext(ctx, "acme/demo", 7)
	if err != nil {
		b.Fatal(err)
	}
	prefs, _, _ := r.gate(ctx, "acme/demo")
	hash := r.fingerprint(p, prefs)
	current, err := s.Current(ctx, SessionKey{"claude-code", "session"})
	if err != nil {
		b.Fatal(err)
	}
	rows := make([]relevanceRow, len(current))
	for i, d := range current {
		state := "unrelated"
		if i == 0 {
			state = "relevant"
		}
		rows[i] = relevanceRow{ID: d.ID, ContextHash: hash, DecisionHash: decisionFingerprint(d.Repository, d.Decision), State: state}
	}
	if err := s.saveRelevance(ctx, p, rows); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		views, err := r.Sessions(ctx, "acme/demo", 7, "head", []SessionKey{{"claude-code", "session"}})
		if err != nil {
			b.Fatal(err)
		}
		if len(views[0].Decisions) != 1 {
			b.Fatal("wrong cached result")
		}
	}
}
