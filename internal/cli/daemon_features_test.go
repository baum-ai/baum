package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/features/adaptive"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/repos"
)

type buildRecoveryBackend struct {
	fail        atomic.Bool
	unavailable atomic.Bool
	failCommit  atomic.Bool
	db          *sql.DB
	calls       atomic.Int32
}

func (*buildRecoveryBackend) Name() string { return agent.BackendClaude }
func (b *buildRecoveryBackend) Check(context.Context) (string, error) {
	if b.unavailable.Load() {
		return "", errors.New("synthetic missing backend")
	}
	return "fixture", nil
}
func (b *buildRecoveryBackend) Run(ctx context.Context, req agent.Request) (agent.Result, error) {
	call := b.calls.Add(1)
	if call > 1 && b.fail.Load() {
		return agent.Result{}, errors.New("synthetic provider failure after checkpoint")
	}
	reply := adaptive.Reply{Action: "finish", Numbers: []int{}, Paths: []string{}, Selected: []int{}, Scopes: []adaptive.Scope{}, Assignments: []adaptive.Assignment{}, Reason: "Complete selected work"}
	if call == 1 {
		reply.Action = "checkpoint"
		reply.Selected = []int{1}
		reply.Scopes = []adaptive.Scope{{Key: "search", Title: "Search", Purpose: "Search local data", Boundaries: "Excludes unrelated work", Paths: []string{"timeline.txt"}, Examples: []int{1}, Digest: "Local search"}}
		reply.Assignments = []adaptive.Assignment{{Number: 1, Scope: "search", Reason: "Implements this outcome"}}
	} else {
		data, err := os.ReadFile(filepath.Join(req.Directory, "state.json"))
		if err != nil {
			return agent.Result{}, err
		}
		var state struct{ Assignments map[int]adaptive.Assignment }
		if err = json.Unmarshal(data, &state); err != nil {
			return agent.Result{}, err
		}
		if state.Assignments[1].Scope != "search" {
			return agent.Result{}, errors.New("accepted membership missing on resume")
		}
	}
	if b.failCommit.Load() {
		if _, err := b.db.Exec(`CREATE TRIGGER reject_build_checkpoint BEFORE UPDATE ON feature_build_state BEGIN SELECT RAISE(ABORT,'synthetic commit failure'); END`); err != nil {
			return agent.Result{}, err
		}
		reply.Selected = []int{2}
		reply.Assignments = []adaptive.Assignment{{Number: 2, Scope: "search", Reason: "Proposed additional work"}}
	}
	data, _ := json.Marshal(reply)
	return agent.Result{Document: data, Model: "claude-haiku-5-5", Calls: 1, Usage: agent.Usage{Input: 100, Output: 40}}, ctx.Err()
}

type buildRecoveryReader struct{ head string }

func (r buildRecoveryReader) Read(_ context.Context, _ string, _ string) (int, http.Header, []byte, error) {
	at := time.Now().UTC().Format(time.RFC3339)
	data, _ := json.Marshal([]map[string]any{{"number": 1, "title": "Search local data", "state": "open", "created_at": at, "updated_at": at, "user": map[string]any{"login": "fixture", "type": "User"}, "head": map[string]any{"ref": "feature", "sha": r.head}, "base": map[string]any{"ref": "main", "sha": r.head}}})
	var rows []map[string]any
	_ = json.Unmarshal(data, &rows)
	extra := map[string]any{}
	for key, value := range rows[0] {
		extra[key] = value
	}
	extra["number"] = 2
	extra["title"] = "Follow-up search work"
	rows = append(rows, extra)
	data, _ = json.Marshal(rows)
	return http.StatusOK, http.Header{}, data, nil
}

// A fresh job is executed once. Failed/interrupted jobs are terminal after
// reopen; Resume is a new item without fresh mode and retains the candidate.
func TestFreshFeatureJobRecoveryAndAdmissionPreserveVersions(t *testing.T) {
	ctx := context.Background()
	source := assembletest.NewRepo(t, assembletest.Options{})
	assembletest.Git(t, source.Dir, "remote", "add", "origin", "https://github.com/acme/recovery")
	if err := os.WriteFile(filepath.Join(source.Dir, "timeline.txt"), []byte("Search local data"), 0600); err != nil {
		t.Fatal(err)
	}
	assembletest.Git(t, source.Dir, "add", ".")
	assembletest.Git(t, source.Dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "-qm", "Timeline source")
	head := strings.TrimSpace(assembletest.Git(t, source.Dir, "rev-parse", "HEAD"))
	stateDir := appdbtest.TempDir(t)
	registry, err := repos.Open(filepath.Join(stateDir, "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	clone, err := registry.Add(ctx, source.Dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "decisions.sqlite")
	store, err := decisionlog.Open(path, decisionlog.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	current, err := store.LiveVersion(ctx, "acme/recovery")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec(`INSERT INTO feature_version(repository,state,created_at)VALUES('acme/recovery','building',0)`); err != nil {
		t.Fatal(err)
	}
	var legacy int64
	if err = db.QueryRow(`SELECT id FROM feature_version WHERE state='building'`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	backend := &buildRecoveryBackend{db: db}
	backend.fail.Store(true)
	worker := &decisionlog.FeatureWorker{Store: store, Config: config.Default(), Backends: generate.Backends{Claude: func(string) generate.CheckedBackend { return backend }}, GitHub: func(context.Context, github.Repository) (github.Reader, error) { return buildRecoveryReader{head}, nil }}
	builder := &featureBuilder{}
	builder.worker.Store(worker)
	local := &jobs.Local{Registry: registry, Features: builder}
	journal := filepath.Join(stateDir, "jobs.sqlite")
	queue, err := jobs.OpenHeld(journal, local, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh := jobs.Item{Clone: clone.ID, Kind: jobs.KindFeatureBuild, FeatureBuildMode: "fresh"}
	admitted, err := queue.Generate(ctx, []jobs.Item{fresh}, "single", false)
	if err != nil {
		queue.Close()
		t.Fatal(err)
	}
	// Admission is read-only; duplicate queued fresh work cannot replace progress.
	var state string
	if err = db.QueryRow(`SELECT state FROM feature_version WHERE id=?`, legacy).Scan(&state); err != nil || state != "building" {
		t.Fatalf("admission mutated candidate %s %v", state, err)
	}
	if _, err = queue.Generate(ctx, []jobs.Item{fresh}, "single", false); err == nil {
		t.Fatal("duplicate fresh request admitted")
	}
	if err = db.QueryRow(`SELECT state FROM feature_version WHERE id=?`, legacy).Scan(&state); err != nil || state != "building" {
		t.Fatal("rejected fresh mutated candidate")
	}
	// Eligibility changed while queued: execution must not archive the old candidate.
	backend.unavailable.Store(true)
	queue.Resume()
	waitFeatureJob(t, queue, admitted[0].ID, "failed")
	if err = db.QueryRow(`SELECT state FROM feature_version WHERE id=?`, legacy).Scan(&state); err != nil || state != "building" {
		t.Fatal("execution preflight replaced an ineligible candidate")
	}
	backend.unavailable.Store(false)
	admitted, err = queue.Generate(ctx, []jobs.Item{fresh}, "single", false)
	if err != nil {
		t.Fatal(err)
	}
	waitFeatureJob(t, queue, admitted[0].ID, "failed")
	var candidate int64
	if err = db.QueryRow(`SELECT id FROM feature_version WHERE state='building_adaptive'`).Scan(&candidate); err != nil {
		t.Fatal(err)
	}
	if candidate == legacy {
		t.Fatal("fresh silently converted legacy candidate")
	}
	assertFeatureRecovery(t, db, current, candidate)
	queue.Close()
	reopened, err := jobs.Open(journal, local, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	before := backend.calls.Load()
	// Reopened failed jobs never execute again, including a persisted fresh item.
	if j, ok := reopened.Find(admitted[0].ID); !ok || j.State != "failed" || j.FeatureBuildMode != "fresh" || j.BuildCoverage == nil || j.BuildCoverage.Completed != 1 {
		t.Fatalf("journal lost failed fresh progress %+v", j)
	}
	backend.fail.Store(false)
	resumed, err := reopened.Generate(ctx, []jobs.Item{{Clone: clone.ID, Kind: jobs.KindFeatureBuild}}, "single", false)
	if err != nil {
		t.Fatal(err)
	}
	done := waitFeatureJob(t, reopened, resumed[0].ID, "completed")
	if done.FeatureBuildMode != "" || done.BuildCoverage == nil || done.BuildCoverage.Completed != 1 || backend.calls.Load() != before+1 {
		t.Fatalf("resume lost state %+v", done)
	}
	var promoted int64
	if err = db.QueryRow(`SELECT id FROM feature_version WHERE state='current'`).Scan(&promoted); err != nil || promoted != candidate {
		t.Fatalf("wrong promotion %d %v", promoted, err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM feature_pull WHERE version_id=?`, candidate).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate/lost membership %d %v", count, err)
	}
	backend.calls.Store(0)
	backend.fail.Store(true)
	distinct, err := reopened.Generate(ctx, []jobs.Item{fresh}, "single", false)
	if err != nil {
		t.Fatal(err)
	}
	waitFeatureJob(t, reopened, distinct[0].ID, "failed")
	var newCandidate int64
	if err = db.QueryRow(`SELECT id FROM feature_version WHERE state='building_adaptive'`).Scan(&newCandidate); err != nil || newCandidate == candidate {
		t.Fatal("distinct fresh request did not create a new candidate")
	}
	assertFeatureRecovery(t, db, candidate, newCandidate)
	backend.fail.Store(false)
	backend.failCommit.Store(true)
	rejected, err := reopened.Generate(ctx, []jobs.Item{{Clone: clone.ID, Kind: jobs.KindFeatureBuild}}, "single", false)
	if err != nil {
		t.Fatal(err)
	}
	failed := waitFeatureJob(t, reopened, rejected[0].ID, "failed")
	durable, err := store.FeatureCoverage(ctx, "acme/recovery", decisionlog.VersionBuilding)
	if err != nil || durable == nil || failed.BuildCoverage == nil || failed.BuildCoverage.Selected != durable.Selected || failed.BuildCoverage.Completed != durable.Completed || durable.Completed != 1 {
		t.Fatalf("job reported rejected checkpoint %+v durable %+v %v", failed.BuildCoverage, durable, err)
	}
	response := httptest.NewRecorder()
	(&decisionlog.Timeline{Store: store, Registry: registry}).Handler().ServeHTTP(response, httptest.NewRequest("GET", "/api/timeline/build?repository=acme%2Frecovery", nil))
	var api struct {
		Building struct {
			Coverage *adaptive.Coverage `json:"coverage"`
		} `json:"building"`
	}
	if response.Code != http.StatusOK {
		t.Fatalf("build API %d %s", response.Code, response.Body.String())
	}
	if err = json.Unmarshal(response.Body.Bytes(), &api); err != nil || api.Building.Coverage == nil || api.Building.Coverage.Completed != failed.BuildCoverage.Completed || api.Building.Coverage.Selected != failed.BuildCoverage.Selected {
		t.Fatalf("job/API disagreement %s job%+v %v", response.Body.String(), failed.BuildCoverage, err)
	}
	assertFeatureRecovery(t, db, candidate, newCandidate)
}
func assertFeatureRecovery(t *testing.T, db *sql.DB, current, candidate int64) {
	t.Helper()
	var state string
	var count int
	if err := db.QueryRow(`SELECT state FROM feature_version WHERE id=?`, current).Scan(&state); err != nil || state != "current" {
		t.Fatal("current changed before completion")
	}
	if err := db.QueryRow(`SELECT count(*) FROM feature_pull WHERE version_id=?`, candidate).Scan(&count); err != nil || count != 1 {
		t.Fatalf("accepted checkpoint lost %d %v", count, err)
	}
}
func waitFeatureJob(t *testing.T, q *jobs.Queue, id, state string) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if job, ok := q.Find(id); ok && job.State == state {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %s", id, state)
	return jobs.Job{}
}
