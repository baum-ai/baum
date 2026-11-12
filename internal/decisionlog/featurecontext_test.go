package decisionlog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/features/adaptive"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

func TestLiveTaskRetainsSourceScopesAndIncomingRevision(t *testing.T) {
	f := newFeatureFixture(t)
	ctx := context.Background()
	version := f.live()
	f.clock.now = t0.Add(time.Hour)
	dir := discoveryClone(t)
	old := strings.TrimSpace(linktest.Git(t, dir, "rev-parse", "HEAD"))
	scope := adaptive.Scope{Key: "settings", Title: "Repository Settings", Purpose: "Repository model preferences", Boundaries: "Excludes Linux packaging", Paths: []string{"src/search"}, Examples: []int{1}, Digest: "Configures models"}
	data, _ := json.Marshal(scope)
	f.exec(`INSERT INTO feature_scope(feature_id,version_id,repository,revision,scope_json)VALUES('reserved',?,?,?,?)`, version, repo, old, string(data))
	if f.count(`SELECT COUNT(*) FROM feature`) != 0 {
		t.Fatal("source-only map exposed empty feature")
	}
	emptyRun, err := f.worker.newRun(ctx, repo, dir, FeatureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if candidates, err := emptyRun.consolidationFeatures(ctx); err != nil || len(candidates) != 0 {
		t.Fatalf("source-only scopes offered to consolidation %+v %v", candidates, err)
	}
	write(t, filepath.Join(dir, "new-settings.go"), "package settings\n// Adds model preferences.\n")
	linktest.Git(t, dir, "add", ".")
	linktest.Git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "incoming settings change")
	head := strings.TrimSpace(linktest.Git(t, dir, "rev-parse", "HEAD"))
	f.pull(1, "Repository model settings", t0, head, []string{"new-settings.go"})
	f.worker.source = nil
	backend := &sourceBackend{read: func(req agent.Request) (agent.Result, error) {
		if !req.Task {
			t.Fatal("live placement did not run a task")
		}
		if _, err := os.Stat(filepath.Join(req.Directory, "head", "new-settings.go")); err != nil {
			t.Fatal("incoming source missing", err)
		}
		var source SourceContext
		data, _ := os.ReadFile(filepath.Join(req.Directory, "source.json"))
		if err := json.Unmarshal(data, &source); err != nil || source.Revision != head {
			t.Fatalf("incoming revision %+v %v", source, err)
		}
		user, _ := os.ReadFile(filepath.Join(req.Directory, "transcript.txt"))
		var input struct {
			Scopes []FeatureScope `json:"source_scopes"`
		}
		if err := json.Unmarshal(user, &input); err != nil || len(input.Scopes) != 1 || input.Scopes[0].Revision != old || input.Scopes[0].Boundaries != "Excludes Linux packaging" {
			t.Fatalf("durable map context %s %v", user, err)
		}
		wrapped, _ := json.Marshal(sourceReply{Extraction: json.RawMessage(choose("existing", "F1", "")), References: []string{"new-settings.go"}})
		return agent.Result{Document: wrapped, Calls: 1, Model: "claude-haiku-5-5"}, nil
	}}
	f.worker.Backends.Claude = func(string) generate.CheckedBackend { return backend }
	report, err := f.worker.Assign(ctx, repo, dir, FeatureOptions{Manual: true})
	if err != nil || len(report.Items) != 1 || report.Items[0].Feature != "reserved" {
		t.Fatalf("first join %+v %v", report, err)
	}
	if f.count(`SELECT COUNT(*) FROM feature`) != 1 {
		t.Fatal("source scope first join failed")
	}
	var provenance string
	if err = f.store.db.QueryRow(`SELECT context_json FROM feature_judgment WHERE kind='pull' AND item='1'`).Scan(&provenance); err != nil || !strings.Contains(provenance, head) || !strings.Contains(provenance, old) || !strings.Contains(provenance, placementContextVersion) {
		t.Fatalf("placement provenance %s %v", provenance, err)
	}
	// Consolidation aliases retain the scope and a valid target on later joins.
	f.feature(version, "survivor")
	expected := map[string]int64{}
	for _, id := range []string{"reserved", "survivor"} {
		expected[id] = int64(f.membership(id))
	}
	if done, err := f.store.mergeFeature(ctx, version, repo, "survivor", "reserved", expected, f.clock.now); err != nil || !done {
		t.Fatalf("merge %v %v", done, err)
	}
	candidates, err := f.store.candidateFeatures(ctx, version, repo, time.Time{}, "")
	if err != nil || len(candidates) != 1 || candidates[0].ID != "survivor" || len(candidates[0].Scopes) != 1 {
		t.Fatalf("scope alias candidates %+v %v", candidates, err)
	}
	f.pull(2, "More repository model settings", t0.Add(time.Minute), head, []string{"new-settings.go"})
	report, err = f.worker.Assign(ctx, repo, dir, FeatureOptions{Manual: true})
	if err != nil || len(report.Items) != 1 || report.Items[0].Feature != "survivor" {
		t.Fatalf("later scope join %+v %v", report, err)
	}
	if backend.calls != 2 {
		t.Fatalf("task calls %d", backend.calls)
	}
}

func TestSmallMixedFeatureRepairPreservesMembersAndDecisionLinks(t *testing.T) {
	f := newFeatureFixture(t)
	ctx := context.Background()
	version := f.live()
	f.pull(1, "Repository Settings", t0, "h1", []string{"shared.go"})
	f.pull(2, "AppImage distribution", t0.Add(time.Minute), "h2", []string{"shared.go"})
	f.feature(version, "mixed", 1, 2)
	f.saveSession("choice", "shared.go")
	f.exec(`INSERT INTO pull_commit(repository,number,sha,committed_at)VALUES(?,2,'choice-commit',0)`, repo)
	f.exec(`INSERT INTO session_commit(agent,session_id,repository,sha,method,confidence)VALUES('claude-code','choice',?,'choice-commit','checkpoint',1)`, repo)
	before, err := f.store.Current(ctx, SessionKey{Agent: "claude-code", ID: "choice"})
	if err != nil {
		t.Fatal(err)
	}
	f.backend.split = func(user string) (string, error) {
		if !strings.Contains(user, "Repository Settings") || !strings.Contains(user, "AppImage") {
			t.Fatal("missing outcome evidence")
		}
		return `{"parts":[{"title":"Repository Settings","description":"Configure repository preferences.","pull_requests":["P1"]},{"title":"AppImage distribution","description":"Distribute Linux packages.","pull_requests":["P2"]}]}`, nil
	}
	report := f.consolidate(FeatureOptions{Manual: true})
	if report.Split != 1 || report.NewFeatures != 1 || f.featureOf(1) != "mixed" || f.featureOf(2) == "mixed" || f.featureOf(2) == "" {
		t.Fatalf("small repair %+v memberships %s %s", report, f.featureOf(1), f.featureOf(2))
	}
	after, err := f.store.Current(ctx, SessionKey{Agent: "claude-code", ID: "choice"})
	if err != nil || len(after) != len(before) || after[0].ID != before[0].ID || !slices.Equal(after[0].Turns, before[0].Turns) {
		t.Fatalf("decision identity/citations changed %+v %v", after, err)
	}
	links, err := f.store.SessionLinks(ctx, repo, "claude-code", "choice")
	if err != nil || len(links.PullRequests) != 1 || links.PullRequests[0].FeatureID != f.featureOf(2) {
		t.Fatalf("session links lost %+v %v", links, err)
	}
	if f.count(`SELECT COUNT(*) FROM feature_pull WHERE version_id=`+strconv.FormatInt(version, 10)) != 2 {
		t.Fatal("membership duplicated or lost")
	}
	if resolved, err := f.store.ResolveFeature(ctx, "mixed"); err != nil || resolved.ID != "mixed" || resolved.Archived {
		t.Fatalf("feature ID/link changed %+v %v", resolved, err)
	}
}
