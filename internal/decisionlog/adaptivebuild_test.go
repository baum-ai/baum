package decisionlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/features/adaptive"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

type adaptiveReader struct {
	rows    []map[string]any
	paths   []string
	err     error
	status  int
	headers http.Header
}

func (r *adaptiveReader) Read(_ context.Context, path, _ string) (int, http.Header, []byte, error) {
	r.paths = append(r.paths, path)
	if r.err != nil {
		return 0, nil, nil, r.err
	}
	if r.status != 0 {
		return r.status, r.headers, nil, nil
	}
	u, _ := url.Parse(path)
	if n, err := strconv.Atoi(filepath.Base(u.Path)); err == nil {
		for _, row := range r.rows {
			if row["number"] == n {
				data, _ := json.Marshal(row)
				return http.StatusOK, http.Header{}, data, nil
			}
		}
		return http.StatusNotFound, http.Header{}, nil, nil
	}
	rows := append([]map[string]any{}, r.rows...)
	slices.SortFunc(rows, func(a, b map[string]any) int {
		return strings.Compare(b["updated_at"].(string), a["updated_at"].(string))
	})
	if u.Query().Get("state") == "open" {
		rows = slices.DeleteFunc(rows, func(row map[string]any) bool { return row["state"] != "open" })
	}
	page, _ := strconv.Atoi(u.Query().Get("page"))
	size, _ := strconv.Atoi(u.Query().Get("per_page"))
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 64
	}
	offset := (page - 1) * size
	if offset >= len(rows) {
		rows = nil
	} else {
		rows = rows[offset:min(offset+size, len(rows))]
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	data, _ := json.Marshal(rows)
	return http.StatusOK, http.Header{}, data, nil
}

func adaptiveFixture(t *testing.T) (*featureFixture, string, *adaptiveReader) {
	t.Helper()
	f := newFeatureFixture(t)
	dir := discoveryClone(t)
	head := strings.TrimSpace(linktest.Git(t, dir, "rev-parse", "HEAD"))
	r := &adaptiveReader{}
	for i, age := range []time.Duration{time.Hour, 60 * 24 * time.Hour, 120 * 24 * time.Hour} {
		p := pullJSON(i+1, head, head)
		date := f.clock.now.Add(-age).Format(time.RFC3339)
		p["created_at"], p["updated_at"] = date, date
		p["body"] = fmt.Sprintf("Detailed body %d", i+1)
		if i != 2 {
			p["state"] = "closed"
			p["merged_at"] = date
		}
		r.rows = append(r.rows, p)
	}
	f.worker.GitHub = func(context.Context, github.Repository) (github.Reader, error) { return r, nil }
	return f, dir, r
}

func adaptiveTaskReply(t *testing.T, r adaptive.Reply) agent.Result {
	t.Helper()
	if r.Reason == "" {
		r.Reason = "Continue with supported work"
	}
	if r.Numbers == nil {
		r.Numbers = []int{}
	}
	if r.Paths == nil {
		r.Paths = []string{}
	}
	if r.Selected == nil {
		r.Selected = []int{}
	}
	if r.Scopes == nil {
		r.Scopes = []adaptive.Scope{}
	}
	if r.Assignments == nil {
		r.Assignments = []adaptive.Assignment{}
	}
	for i := range r.Scopes {
		if r.Scopes[i].Paths == nil {
			r.Scopes[i].Paths = []string{}
		}
		if r.Scopes[i].Examples == nil {
			r.Scopes[i].Examples = []int{}
		}
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return agent.Result{Document: data, Model: discoveryModel, Calls: 1, Usage: agent.Usage{Input: 100, Output: 20}}
}
func widgetScope() adaptive.Scope {
	return adaptive.Scope{Key: "widgets", Title: "Widget search", Purpose: "Find widgets", Boundaries: "Excludes billing", Paths: []string{"src/search"}, Examples: []int{1}, Digest: "Widget search is available."}
}
func adaptiveInput(t *testing.T, req agent.Request) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(req.Directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		t.Fatal(err)
	}
	data = compact.Bytes()
	var state map[string]json.RawMessage
	if err = json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestAdaptiveCatalogChoosesHistoryAndPersistsScopes(t *testing.T) {
	f, dir, reader := adaptiveFixture(t)
	live := f.live()
	calls := 0
	f.backend.task = func(req agent.Request) (agent.Result, error) {
		calls++
		in := adaptiveInput(t, req)
		if !strings.Contains(req.Prompt, adaptive.Prompt) {
			t.Fatal("not the code-aware prompt")
		}
		if _, err := os.Stat(filepath.Join(req.Directory, "head", "src", "search", "a.go")); err != nil {
			t.Fatal(err)
		}
		switch calls {
		case 1:
			if strings.Contains(string(in["catalog"]), `"number":2`) {
				t.Fatal("old merged PR eagerly catalogued")
			}
			if !strings.Contains(string(in["catalog"]), `"number":3`) {
				t.Fatal("old open PR missing")
			}
			if strings.Contains(string(in["details"]), "Detailed body") {
				t.Fatal("descriptions supplied without a request")
			}
			return adaptiveTaskReply(t, adaptive.Reply{Action: "catalog", Since: f.clock.now.AddDate(0, -3, 0).Format(time.DateOnly)}), nil
		case 2:
			if !strings.Contains(string(in["catalog"]), `"number":2`) {
				t.Fatal("history did not expand")
			}
			return adaptiveTaskReply(t, adaptive.Reply{Action: "details", Numbers: []int{1, 2}}), nil
		case 3:
			if !strings.Contains(adaptiveEvidence(t, req, in["details"]), "Detailed body 2") {
				t.Fatal("requested description missing")
			}
			return adaptiveTaskReply(t, adaptive.Reply{Action: "checkpoint", Selected: []int{1, 2}, Scopes: []adaptive.Scope{widgetScope()}, Assignments: []adaptive.Assignment{{Number: 1, Scope: "widgets", Reason: "Builds widget search"}}}), nil
		default:
			if got := f.count(`SELECT count(*) FROM feature_pull WHERE version_id=(SELECT id FROM feature_version WHERE state='building_adaptive')`); got != 1 {
				t.Fatalf("checkpoint membership %d", got)
			}
			if state, _ := f.versionState(live); state != VersionCurrent {
				t.Fatal("current retired before completion")
			}
			return adaptiveTaskReply(t, adaptive.Reply{Action: "finish", Assignments: []adaptive.Assignment{{Number: 2, Scope: "widgets", Reason: "Continues widget search"}}}), nil
		}
	}
	report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err != nil || !report.Current || calls != 4 {
		t.Fatalf("report %+v calls %d error %v", report, calls, err)
	}
	if report.Coverage.Catalogued != 3 || report.Coverage.Inspected != 2 || report.Coverage.Selected != 2 || report.Coverage.Remaining != 0 {
		t.Fatalf("coverage %+v", report.Coverage)
	}
	if f.count(`SELECT count(*) FROM capture_attempt WHERE stage=?`, StageDiscovery) != 4 {
		t.Fatal("task accounting lost")
	}
	if f.count(`SELECT count(*) FROM capture_attempt WHERE stage IN ('feature_summary','pr_summary')`) != 0 {
		t.Fatal("summary fanout")
	}
	if f.count(`SELECT count(*) FROM pull_commit`) != 0 {
		t.Fatal("catalog/details eagerly read Git history")
	}
	if f.count(`SELECT count(*) FROM feature_seed`) != 0 {
		t.Fatal("legacy seed path used")
	}
	scopes, err := f.store.FeatureScopes(context.Background(), report.Version, repo)
	if err != nil || len(scopes) != 1 || scopes[0].Boundaries != "Excludes billing" || scopes[0].Revision == "" {
		t.Fatalf("scopes %+v %v", scopes, err)
	}
	coverage, err := f.store.FeatureCoverage(context.Background(), repo, VersionCurrent)
	if err != nil || coverage.Selected != 2 {
		t.Fatalf("current coverage %+v %v", coverage, err)
	}
	if len(reader.paths) != 4 {
		t.Fatalf("catalog read paths %v", reader.paths)
	}
}

func TestAdaptiveCheckpointSurvivesReopenAndPinsSourceAndPR(t *testing.T) {
	f, dir, reader := adaptiveFixture(t)
	calls := 0
	var revision string
	var sourceDir string
	f.backend.task = func(req agent.Request) (agent.Result, error) {
		calls++
		sourceDir = req.Directory
		var source SourceContext
		data, _ := os.ReadFile(filepath.Join(req.Directory, "source.json"))
		_ = json.Unmarshal(data, &source)
		revision = source.Revision
		if calls == 1 {
			return adaptiveTaskReply(t, adaptive.Reply{Action: "checkpoint", Selected: []int{1}, Scopes: []adaptive.Scope{widgetScope()}, Assignments: []adaptive.Assignment{{Number: 1, Scope: "widgets", Reason: "Supported search"}}}), nil
		}
		return agent.Result{}, context.Canceled
	}
	first, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err == nil || first.Current {
		t.Fatalf("unexpected first result %+v %v", first, err)
	}
	if _, err = os.Stat(sourceDir); !os.IsNotExist(err) {
		t.Fatalf("source temp directory survived %v", err)
	}
	reader.rows[0]["title"] = "A newer mutable title"
	linktest.Git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--allow-empty", "-q", "-m", "advance")
	dbfile := f.store.dbPath(t)
	if err = f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dbfile, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	f.store = reopened
	f.worker.Store = reopened
	oldReads := len(reader.paths)
	f.backend.task = func(req agent.Request) (agent.Result, error) {
		data, _ := os.ReadFile(filepath.Join(req.Directory, "source.json"))
		var source SourceContext
		_ = json.Unmarshal(data, &source)
		if source.Revision != revision {
			t.Fatalf("source changed %s != %s", source.Revision, revision)
		}
		in := adaptiveInput(t, req)
		if strings.Contains(string(in["catalog"]), "newer mutable") {
			t.Fatal("catalog snapshot changed")
		}
		if !strings.Contains(string(in["assignments"]), "Supported search") {
			t.Fatal("accepted checkpoint lost")
		}
		return adaptiveTaskReply(t, adaptive.Reply{Action: "finish"}), nil
	}
	second, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err != nil || !second.Current || first.Version != second.Version {
		t.Fatalf("resume %+v %v", second, err)
	}
	if len(reader.paths) != oldReads || f.count(`SELECT count(*) FROM feature_pull WHERE version_id=?`, second.Version) != 1 {
		t.Fatal("resume repeated evidence or duplicated membership")
	}
}

func (s *Store) dbPath(t *testing.T) string {
	t.Helper()
	var seq int
	var name, path string
	if err := s.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAdaptiveRefusesUnfinishedSelectedWorkAndBadReferences(t *testing.T) {
	for _, test := range []string{"missing_assignment", "bad_path", "bad_example", "unknown_assignment"} {
		t.Run(test, func(t *testing.T) {
			f, dir, _ := adaptiveFixture(t)
			calls := 0
			f.backend.task = func(req agent.Request) (agent.Result, error) {
				calls++
				if calls == 1 {
					return adaptiveTaskReply(t, adaptive.Reply{Action: "checkpoint", Selected: []int{1}}), nil
				}
				scope := widgetScope()
				reply := adaptive.Reply{Action: "finish", Scopes: []adaptive.Scope{scope}}
				switch test {
				case "bad_path":
					reply.Scopes[0].Paths = []string{"missing/file.go"}
				case "bad_example":
					reply.Scopes[0].Examples = []int{999}
				case "unknown_assignment":
					reply.Assignments = []adaptive.Assignment{{Number: 999, Scope: "widgets", Reason: "invented"}}
				}
				return adaptiveTaskReply(t, reply), nil
			}
			report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
			if err == nil || report.Current || report.Coverage.Remaining != 1 {
				t.Fatalf("accepted incomplete/invalid result %+v %v", report, err)
			}
			if f.count(`SELECT count(*) FROM feature_version WHERE state='current'`) != 0 {
				t.Fatal("invalid candidate promoted")
			}
		})
	}
}

func TestAdaptiveNoProgressAndEmptyCompletion(t *testing.T) {
	t.Run("no_progress", func(t *testing.T) {
		f, dir, _ := adaptiveFixture(t)
		f.backend.task = func(agent.Request) (agent.Result, error) {
			return adaptiveTaskReply(t, adaptive.Reply{Action: "checkpoint", Reason: fmt.Sprintf("Changing narrative %d", f.backend.runs)}), nil
		}
		report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
		if err == nil || !strings.Contains(err.Error(), ReasonBuildNoProgress) || report.Current || f.backend.runs != 3 {
			t.Fatalf("no-progress %+v calls %d %v", report, f.backend.runs, err)
		}
	})
	t.Run("empty", func(t *testing.T) {
		f, dir, reader := adaptiveFixture(t)
		reader.rows = nil
		f.backend.task = func(agent.Request) (agent.Result, error) {
			return adaptiveTaskReply(t, adaptive.Reply{Action: "finish", Reason: "No substantial work exists"}), nil
		}
		report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
		if err != nil || !report.Current || report.Features != 0 {
			t.Fatalf("empty %+v %v", report, err)
		}
	})
}

func TestAdaptiveLegacyWorkflowAndExplicitReplacement(t *testing.T) {
	f, dir, _ := adaptiveFixture(t)
	version, err := featureVersion(context.Background(), f.store.db, repo, VersionBuilding, true, f.clock.now)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO feature(id,version_id,repository,title,changed_at,created_at,updated_at) VALUES ('legacy',?,?,'Legacy',0,0,0)`, version, repo)
	if workflow, err := f.store.FeatureBuildWorkflow(context.Background(), repo); err != nil || workflow != "legacy" {
		t.Fatalf("workflow %s %v", workflow, err)
	}
	if _, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true}); !errors.Is(err, errWorkflowMismatch) {
		t.Fatalf("silently changed legacy workflow: %v", err)
	}
	if err := f.store.ArchiveFeatureBuild(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	f.backend.task = func(agent.Request) (agent.Result, error) {
		return adaptiveTaskReply(t, adaptive.Reply{Action: "finish"}), nil
	}
	report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err != nil || !report.Current || report.Version == version {
		t.Fatalf("replacement %+v %v", report, err)
	}
	if state, _ := f.versionState(version); state != VersionRetired {
		t.Fatal("legacy candidate not archived")
	}
}

func TestAdaptiveRequestsOnlySelectedCodeAndAcceptsNewPRFile(t *testing.T) {
	f, dir, reader := adaptiveFixture(t)
	base := strings.TrimSpace(linktest.Git(t, dir, "rev-parse", "HEAD"))
	linktest.Git(t, dir, "checkout", "-q", "-b", "new-search")
	if err := os.WriteFile(filepath.Join(dir, "src", "search", "new.go"), []byte("package search\n// Brand new behavior.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	linktest.Git(t, dir, "add", ".")
	linktest.Git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "new behavior")
	head := strings.TrimSpace(linktest.Git(t, dir, "rev-parse", "HEAD"))
	linktest.Git(t, dir, "checkout", "-q", "main")
	reader.rows[0]["head"] = map[string]any{"ref": "new-search", "sha": head}
	reader.rows[0]["base"] = map[string]any{"ref": "main", "sha": base}
	calls := 0
	f.backend.task = func(req agent.Request) (agent.Result, error) {
		calls++
		if calls == 1 {
			if _, err := os.Stat(filepath.Join(req.Directory, "head", "src", "search", "new.go")); !os.IsNotExist(err) {
				t.Fatal("source is not pinned to main")
			}
			return adaptiveTaskReply(t, adaptive.Reply{Action: "code", Selected: []int{1}, Numbers: []int{1}, Paths: []string{"src/search/new.go"}}), nil
		}
		in := adaptiveInput(t, req)
		if !strings.Contains(adaptiveEvidence(t, req, in["code"]), "Brand new behavior") {
			t.Fatal("focused patch not supplied")
		}
		scope := widgetScope()
		scope.Paths = []string{"src/search/new.go"}
		return adaptiveTaskReply(t, adaptive.Reply{Action: "finish", Scopes: []adaptive.Scope{scope}, Assignments: []adaptive.Assignment{{Number: 1, Scope: "widgets", Reason: "Adds search behavior"}}}), nil
	}
	report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err != nil || !report.Current || report.Coverage.Inspected != 1 {
		t.Fatalf("code build %+v %v", report, err)
	}
	if f.count(`SELECT count(*) FROM pull_commit WHERE number=1`) != 1 || f.count(`SELECT count(*) FROM pull_commit WHERE number!=1`) != 0 {
		t.Fatal("code read escaped selected work")
	}
}

func TestAdaptiveCheckpointCASAndInvisibleSourceScope(t *testing.T) {
	f := newFeatureFixture(t)
	ctx := context.Background()
	version, err := featureVersion(ctx, f.store.db, repo, VersionAdaptiveBuilding, true, f.clock.now)
	if err != nil {
		t.Fatal(err)
	}
	a := newAdaptiveState()
	a.Source.Revision = strings.Repeat("a", 40)
	a.Scopes["widgets"] = widgetScope()
	if err = f.store.saveAdaptive(ctx, version, repo, a, false, f.clock.now); err != nil {
		t.Fatal(err)
	}
	left, err := f.store.adaptiveState(ctx, version)
	if err != nil {
		t.Fatal(err)
	}
	right, err := f.store.adaptiveState(ctx, version)
	if err != nil {
		t.Fatal(err)
	}
	left.Feedback = "winning worker"
	right.Feedback = "losing worker"
	if err = f.store.saveAdaptive(ctx, version, repo, left, false, f.clock.now); err != nil {
		t.Fatal(err)
	}
	if err = f.store.saveAdaptive(ctx, version, repo, right, false, f.clock.now); !errors.Is(err, errBuildConcurrent) {
		t.Fatalf("lost checkpoint accepted: %v", err)
	}
	saved, err := f.store.adaptiveState(ctx, version)
	if err != nil || saved.Feedback != "winning worker" {
		t.Fatalf("winning checkpoint lost %+v %v", saved, err)
	}
	if f.count(`SELECT count(*) FROM feature`) != 0 {
		t.Fatal("source-only scope exposed as a feature")
	}
	scopes, err := f.store.FeatureScopes(ctx, version, repo)
	if err != nil || len(scopes) != 1 {
		t.Fatal(err)
	}
	f.pull(1, "Search", f.clock.now, "head", []string{"src/search/a.go"})
	pull, err := f.store.placedPull(ctx, version, repo, 1)
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.store.placePull(ctx, version, repo, pull, placeTarget{FeatureID: scopes[0].FeatureID}, f.clock.now)
	if err != nil || id != scopes[0].FeatureID {
		t.Fatalf("source scope first join %s %v", id, err)
	}
	if f.count(`SELECT count(*) FROM feature`) != 1 {
		t.Fatal("first membership not materialized")
	}
	scopes, err = f.store.FeatureScopes(ctx, version, repo)
	if err != nil || len(scopes) != 1 || scopes[0].Boundaries != "Excludes billing" {
		t.Fatal("source scope disappeared after join")
	}
}

func TestAdaptivePendingEvidenceSurvivesFailureAndRetries(t *testing.T) {
	f, dir, reader := adaptiveFixture(t)
	calls := 0
	f.backend.task = func(req agent.Request) (agent.Result, error) {
		calls++
		if calls == 1 {
			reader.err = errors.New("temporary GitHub failure")
			return adaptiveTaskReply(t, adaptive.Reply{Action: "catalog", Since: f.clock.now.AddDate(0, -3, 0).Format(time.DateOnly)}), nil
		}
		return adaptiveTaskReply(t, adaptive.Reply{Action: "finish"}), nil
	}
	first, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err == nil || first.Current {
		t.Fatal("failed request reported complete")
	}
	a, err := f.store.adaptiveState(context.Background(), first.Version)
	if err != nil || a.Pending == nil {
		t.Fatalf("pending request lost %+v %v", a, err)
	}
	if len(a.Catalog) != 2 {
		t.Fatal("accepted catalog lost")
	}
	reader.err = nil
	second, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err != nil || !second.Current || second.Version != first.Version || second.Coverage.Catalogued != 3 {
		t.Fatalf("request resume %+v %v", second, err)
	}
}

func TestAdaptiveCheckpointPersistenceFailureCannotPromote(t *testing.T) {
	f, dir, _ := adaptiveFixture(t)
	f.backend.task = func(agent.Request) (agent.Result, error) {
		f.exec(`CREATE TRIGGER fail_adaptive_write BEFORE UPDATE ON feature_build_state BEGIN SELECT RAISE(ABORT,'checkpoint write failed'); END`)
		return adaptiveTaskReply(t, adaptive.Reply{Action: "finish", Selected: []int{1}, Scopes: []adaptive.Scope{widgetScope()}, Assignments: []adaptive.Assignment{{Number: 1, Scope: "widgets", Reason: "Search"}}}), nil
	}
	report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err == nil || report.Current {
		t.Fatalf("failed checkpoint promoted %+v %v", report, err)
	}
	if f.count(`SELECT count(*) FROM feature_pull`) != 0 || f.count(`SELECT count(*) FROM feature WHERE version_id=?`, report.Version) != 0 {
		t.Fatal("failed checkpoint partially committed")
	}
}

func TestAdaptiveOlderBinaryCannotStartLegacyBesideCandidate(t *testing.T) {
	f := newFeatureFixture(t)
	ctx := context.Background()
	version, err := featureVersion(ctx, f.store.db, repo, VersionAdaptiveBuilding, true, f.clock.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.Exec(`INSERT INTO feature_version(repository,state,created_at) VALUES (?,'building',0)`, repo); err == nil {
		t.Fatal("older legacy insertion bypassed active-candidate guard")
	}
	a := newAdaptiveState()
	if err = f.store.saveAdaptive(ctx, version, repo, a, false, f.clock.now); err != nil {
		t.Fatal(err)
	}
	if _, err = f.worker.Build(ctx, repo, t.TempDir(), BuildOptions{}); !errors.Is(err, errWorkflowMismatch) {
		t.Fatalf("new legacy call reused adaptive state %v", err)
	}
}

func TestAdaptiveProgressingRunIgnoresOuterPerformanceDeadline(t *testing.T) {
	f, dir, _ := adaptiveFixture(t)
	// This is the review's whole-run timeout, not a feature-workflow deadline.
	f.worker.Config.Budgets.RunTimeout = 1
	f.backend.task = func(req agent.Request) (agent.Result, error) {
		return adaptiveTaskReply(t, adaptive.Reply{Action: "finish", Selected: []int{1}, Scopes: []adaptive.Scope{widgetScope()}, Assignments: []adaptive.Assignment{{Number: 1, Scope: "widgets", Reason: "Search"}}}), nil
	}
	report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err != nil || !report.Current {
		t.Fatalf("performance target killed progressing build %+v %v", report, err)
	}
}

func TestAdaptiveReadOnlyOldSchemaHasNoCoverage(t *testing.T) {
	f := newFeatureFixture(t)
	ctx := context.Background()
	f.live()
	f.exec(`DROP TABLE feature_build_state`)
	f.exec(`DROP TABLE feature_scope`)
	path := f.store.dbPath(t)
	reader, err := Open(path, OpenOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if coverage, err := reader.FeatureCoverage(ctx, repo, VersionCurrent); err != nil || coverage != nil {
		t.Fatalf("legacy coverage %+v %v", coverage, err)
	}
	if scopes, err := reader.FeatureScopes(ctx, 1, repo); err != nil || len(scopes) != 0 {
		t.Fatalf("legacy scopes %+v %v", scopes, err)
	}
}

func adaptiveEvidence(t *testing.T, req agent.Request, raw json.RawMessage) string {
	t.Helper()
	var refs map[int]string
	if err := json.Unmarshal(raw, &refs); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, path := range refs {
		data, err := os.ReadFile(filepath.Join(req.Directory, path))
		if err != nil {
			t.Fatal(err)
		}
		out.Write(data)
	}
	return out.String()
}

func TestAdaptiveOlderReferenceRemainsAvailableAfterExplorationGuidance(t *testing.T) {
	f, dir, reader := adaptiveFixture(t)
	calls := 0
	resuming := false
	resumeCalls := 0
	f.backend.task = func(req agent.Request) (agent.Result, error) {
		calls++
		if !resuming && calls > 1 {
			return agent.Result{}, errors.New("pause after accepted selection")
		}
		if !resuming {
			return adaptiveTaskReply(t, adaptive.Reply{Action: "checkpoint", Selected: []int{1}}), nil
		}
		resumeCalls++
		if resumeCalls == 1 {
			return adaptiveTaskReply(t, adaptive.Reply{Action: "details", Numbers: []int{2}}), nil
		}
		in := adaptiveInput(t, req)
		if !strings.Contains(adaptiveEvidence(t, req, in["details"]), "Detailed body 2") {
			t.Fatal("older reference unavailable")
		}
		return adaptiveTaskReply(t, adaptive.Reply{Action: "finish", Assignments: []adaptive.Assignment{{Number: 1, Reason: "Reference resolved; no product change"}}}), nil
	}
	first, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err == nil {
		t.Fatal("expected interruption")
	}
	a, err := f.store.adaptiveState(context.Background(), first.Version)
	if err != nil {
		t.Fatal(err)
	}
	a.ExplorationClosed = true
	if err = f.store.saveAdaptive(context.Background(), first.Version, repo, a, false, f.clock.now); err != nil {
		t.Fatal(err)
	}
	resuming = true
	report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err != nil || !report.Current || report.Coverage.Catalogued != 3 || report.Coverage.Inspected != 1 {
		t.Fatalf("older reference %+v %v", report, err)
	}
	if len(reader.paths) != 3 || !strings.HasSuffix(reader.paths[2], "/pulls/2") {
		t.Fatalf("fetched unnecessary history: %v", reader.paths)
	}
}

func TestAdaptiveRateLimitCheckpointsWaitWithoutImmediateRetry(t *testing.T) {
	f, dir, reader := adaptiveFixture(t)
	reader.status = http.StatusTooManyRequests
	reader.headers = http.Header{"Retry-After": []string{"120"}}
	f.backend.task = func(agent.Request) (agent.Result, error) {
		return adaptiveTaskReply(t, adaptive.Reply{Action: "finish"}), nil
	}
	first, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err == nil || first.Stopped != "rate_limited" || first.Coverage.RetryAt == nil || len(reader.paths) != 1 {
		t.Fatalf("wait %+v %v reads%d", first, err, len(reader.paths))
	}
	second, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err == nil || second.Stopped != "rate_limited" || len(reader.paths) != 1 {
		t.Fatalf("early retry %+v %v reads%d", second, err, len(reader.paths))
	}
	f.clock.now = f.clock.now.Add(48 * time.Hour)
	reader.status = 0
	third, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err != nil || !third.Current || third.Version != first.Version || third.Coverage.RetryAt != nil || third.Coverage.Since != f.clock.now.Add(-48*time.Hour).AddDate(0, -1, 0).Format(time.DateOnly) {
		t.Fatalf("resume %+v %v", third, err)
	}
}

func TestAdaptiveOpenMembershipReconsideredOnMerge(t *testing.T) {
	f, dir, _ := adaptiveFixture(t)
	// Number 3 is open and predates activation, so only the merge trigger applies.
	f.backend.task = func(agent.Request) (agent.Result, error) {
		return adaptiveTaskReply(t, adaptive.Reply{Action: "finish", Selected: []int{3}, Scopes: []adaptive.Scope{widgetScope()}, Assignments: []adaptive.Assignment{{Number: 3, Scope: "widgets", Reason: "Open product work"}}}), nil
	}
	report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err != nil || !report.Current {
		t.Fatal(err)
	}
	f.exec(`UPDATE pull_request SET state='closed',merged_at=?,updated_at=? WHERE number=3`, f.clock.now.UnixMilli(), f.clock.now.UnixMilli())
	if got := f.pending(); !slices.Contains(got, "merged:3") {
		t.Fatalf("merge trigger missing: %v", got)
	}
}

func TestAdaptiveNarrativeOnlyCheckpointsDoNotKeepRunAlive(t *testing.T) {
	f, dir, _ := adaptiveFixture(t)
	calls := 0
	f.backend.task = func(agent.Request) (agent.Result, error) {
		calls++
		scope := widgetScope()
		scope.Purpose = fmt.Sprintf("Search purpose restated %d", calls)
		scope.Digest = fmt.Sprintf("Search digest restated %d", calls)
		return adaptiveTaskReply(t, adaptive.Reply{Action: "checkpoint", Selected: []int{1}, Scopes: []adaptive.Scope{scope}, Assignments: []adaptive.Assignment{{Number: 1, Scope: "widgets", Reason: fmt.Sprintf("Reworded reason %d", calls)}}}), nil
	}
	report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err == nil || report.Stopped != ReasonBuildNoProgress || report.Current || calls != 4 {
		t.Fatalf("narrative loop %+v %v calls%d", report, err, calls)
	}
}

func TestAdaptiveCatalogDoesNotScheduleUnselectedHistoryInBackgroundSync(t *testing.T) {
	f, dir, reader := adaptiveFixture(t)
	for _, n := range []int{1, 2} {
		reader.rows[n]["updated_at"] = f.clock.now.AddDate(-2, 0, 0).Format(time.RFC3339)
		reader.rows[n]["head"].(map[string]any)["sha"] = strings.Repeat("e", 40)
	}
	calls := 0
	f.backend.task = func(agent.Request) (agent.Result, error) {
		calls++
		if calls == 1 {
			return adaptiveTaskReply(t, adaptive.Reply{Action: "catalog", Since: f.clock.now.AddDate(-3, 0, 0).Format(time.DateOnly)}), nil
		}
		return adaptiveTaskReply(t, adaptive.Reply{Action: "finish", Selected: []int{1}, Scopes: []adaptive.Scope{widgetScope()}, Assignments: []adaptive.Assignment{{Number: 1, Scope: "widgets", Reason: "Search outcome"}}}), nil
	}
	report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err != nil || !report.Current || report.Coverage.Catalogued != 3 {
		t.Fatalf("build %+v %v", report, err)
	}
	if f.count(`SELECT count(*) FROM pull_request WHERE number!=1`) != 0 {
		t.Fatal("catalog-only rows leaked into live cache")
	}
	synced, err := f.worker.Sync(context.Background(), repo, dir)
	if err != nil || synced.PathsRead != 1 || synced.PathsFailed != 0 {
		t.Fatalf("background sync %+v %v", synced, err)
	}
	if f.count(`SELECT count(*) FROM pull_request WHERE number!=1`) != 0 {
		t.Fatal("unselected history entered live queue")
	}
}

func TestAdaptiveRequestedDecisionEvidencePinsQuotesCitationsAndSourceAcrossResume(t *testing.T) {
	f, dir, reader := adaptiveFixture(t)
	head := reader.rows[0]["head"].(map[string]any)["sha"].(string)
	d := decisions.Decision{ID: "decision-search", Summary: "Index search results", Evidence: "Use indexed search", EvidenceTurn: 3, Turns: []int{3}, Files: []string{"src/search/a.go"}, Category: decisions.CategoryImplementation, Who: decisions.WhoHuman}
	source := &SourceContext{Version: "repository-context-v1", Revision: head, Repository: repo, References: []string{"src/search/a.go"}}
	if err := f.store.Save(context.Background(), Extraction{Meta: Meta{Agent: "claude-code", ID: "search-session", Repository: repo}, ContentHash: "captured-search", Decisions: []decisions.Decision{d}, Source: source}); err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO session_commit(agent,session_id,repository,sha,method,confidence) VALUES ('claude-code','search-session',?,?,'entire',1)`, repo, head)
	f.saveSession("unrelated", "src/search/other.go")
	resuming := false
	calls := 0
	f.backend.task = func(req agent.Request) (agent.Result, error) {
		calls++
		if !resuming {
			if calls == 1 {
				return adaptiveTaskReply(t, adaptive.Reply{Action: "decisions", Selected: []int{1}, Numbers: []int{1}}), nil
			}
			return agent.Result{}, errors.New("interrupt after decision evidence")
		}
		in := adaptiveInput(t, req)
		text := adaptiveEvidence(t, req, in["decisions"])
		for _, want := range []string{"Use indexed search", "decision-search", "search-session", head} {
			if !strings.Contains(text, want) {
				t.Fatalf("missing %s in evidence %s", want, text)
			}
		}
		var snapshot adaptiveDecisionEvidence
		if err := json.Unmarshal([]byte(text), &snapshot); err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Decisions) != 1 || !slices.Equal(snapshot.Decisions[0].Turns, []int{3}) {
			t.Fatalf("lost cited turns: %+v", snapshot)
		}
		if strings.Contains(text, "Replaced mutable evidence") || strings.Contains(text, "unrelated") {
			t.Fatal("snapshot refreshed or unrelated decisions leaked")
		}
		return adaptiveTaskReply(t, adaptive.Reply{Action: "finish", Scopes: []adaptive.Scope{widgetScope()}, Assignments: []adaptive.Assignment{{Number: 1, Scope: "widgets", Reason: "Captured search decision explains this outcome"}}}), nil
	}
	first, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err == nil || first.Current {
		t.Fatal("expected task interruption")
	}
	f.exec(`UPDATE decision SET evidence='Replaced mutable evidence' WHERE decision_id='decision-search'`)
	resuming = true
	resumed, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err != nil || !resumed.Current || resumed.Version != first.Version || resumed.Coverage.Inspected != 1 {
		t.Fatalf("decision resume %+v %v", resumed, err)
	}
	if f.count(`SELECT count(*) FROM pull_commit`) != 0 {
		t.Fatal("decision evidence fetched code without a request")
	}
}

// Scripted evaluation verifies host behavior and exact synthetic membership;
// real-model semantic quality and elapsed discovery speed are not measured.
func TestAdaptiveSyntheticVolumeAndInitialSync(t *testing.T) {
	for _, volume := range []int{4, 240} {
		t.Run(strconv.Itoa(volume), func(t *testing.T) {
			f, dir, reader := adaptiveFixture(t)
			head := reader.rows[0]["head"].(map[string]any)["sha"].(string)
			reader.rows = nil
			for number := 1; number <= volume; number++ {
				p := pullJSON(number, head, head)
				age := time.Duration(number) * time.Hour
				if volume == 4 {
					age = time.Duration(number*120) * 24 * time.Hour
				}
				date := f.clock.now.Add(-age).Format(time.RFC3339)
				p["created_at"], p["updated_at"] = date, date
				p["state"] = "closed"
				p["merged_at"] = date
				if number == volume {
					p["state"] = "open"
					p["merged_at"] = nil
					p["created_at"], p["updated_at"] = f.clock.now.AddDate(-2, 0, 0).Format(time.RFC3339), f.clock.now.AddDate(-2, 0, 0).Format(time.RFC3339)
				}
				reader.rows = append(reader.rows, p)
			}
			// Startup can list cheap metadata without fetching pre-activation history.
			sync, err := f.worker.Sync(context.Background(), repo, dir)
			if err != nil || sync.PathsRead != 0 || f.count(`SELECT count(*) FROM pull_commit`) != 0 {
				t.Fatalf("startup fetched history %+v %v", sync, err)
			}
			tasks := 0
			f.backend.task = func(req agent.Request) (agent.Result, error) {
				tasks++
				switch tasks {
				case 1:
					return adaptiveTaskReply(t, adaptive.Reply{Action: "catalog", Since: f.clock.now.AddDate(-3, 0, 0).Format(time.DateOnly)}), nil
				case 2:
					return adaptiveTaskReply(t, adaptive.Reply{Action: "details", Numbers: []int{1, 2, volume}}), nil
				default:
					shared := widgetScope()
					shared.Key = "cross-layer"
					shared.Title = "Search capability"
					shared.Purpose = "One outcome spanning implementation, interface and migration"
					shared.Examples = []int{1, 2}
					separate := widgetScope()
					separate.Key = "independent"
					separate.Title = "Independent outcome"
					separate.Purpose = "An independent outcome sharing generic code"
					separate.Examples = []int{volume}
					return adaptiveTaskReply(t, adaptive.Reply{Action: "finish", Selected: []int{1, 2, volume}, Scopes: []adaptive.Scope{shared, separate}, Assignments: []adaptive.Assignment{{Number: 1, Scope: shared.Key, Reason: "Backend and interface outcome"}, {Number: 2, Scope: shared.Key, Reason: "Follow-up migration and fix for same outcome"}, {Number: volume, Scope: separate.Key, Reason: "Different outcome despite shared path"}}}), nil
				}
			}
			report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
			if err != nil || !report.Current || report.Coverage.Catalogued != volume || report.Coverage.Inspected != 3 || report.Coverage.Completed != 3 || report.Coverage.Remaining != 0 || report.Features != 2 {
				t.Fatalf("volume %d %+v %v", volume, report, err)
			}
			if f.featureOf(1) != f.featureOf(2) || f.featureOf(1) == f.featureOf(volume) {
				t.Fatal("exact synthetic memberships changed")
			}
			if f.count(`SELECT count(*) FROM pull_commit`) != 0 {
				t.Fatal("catalog/details caused Git reads")
			}
			t.Logf("catalog=%d requested-evidence=%d selected=%d completed=%d task-steps=%d metadata-requests=%d", report.Coverage.Catalogued, report.Coverage.Inspected, report.Coverage.Selected, report.Coverage.Completed, tasks, len(reader.paths))
		})
	}
}

func TestLiveSyncKeepsSelectedOldWorkAndLegacyCanReadHistory(t *testing.T) {
	f, dir, reader := adaptiveFixture(t)
	ctx := context.Background()
	if _, err := f.worker.Sync(ctx, repo, dir); err != nil {
		t.Fatal(err)
	}
	if f.count(`SELECT count(*) FROM pull_commit`) != 0 {
		t.Fatal("startup read old commits")
	}
	version := f.live()
	f.feature(version, "selected", 1)
	live, err := f.store.livePullsWithoutPaths(ctx, repo)
	if err != nil || len(live) != 1 || live[0].Number != 1 {
		t.Fatalf("active selection %+v %v", live, err)
	}
	// Older open work stays available to explicit full-history preparation.
	reader.rows = nil
	report := SyncReport{}
	if err = f.worker.readPaths(ctx, repo, dir, &report, false, func() bool { return false }); err != nil || report.PathsRead != 3 {
		t.Fatalf("legacy path %+v %v", report, err)
	}
}

type guardedBuildBackend struct {
	run func(context.Context, agent.Request) (agent.Result, error)
}

func (*guardedBuildBackend) Name() string                          { return agent.BackendClaude }
func (*guardedBuildBackend) Check(context.Context) (string, error) { return "fixture", nil }
func (b *guardedBuildBackend) Run(ctx context.Context, req agent.Request) (agent.Result, error) {
	return b.run(ctx, req)
}

func TestAdaptiveCancelledOrTimedOutUnitRetainsAcceptedCheckpoint(t *testing.T) {
	for _, kind := range []string{"cancel", "unit timeout"} {
		t.Run(kind, func(t *testing.T) {
			f, dir, _ := adaptiveFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			resume := false
			if kind == "unit timeout" {
				f.worker.Config.Budgets.PassTimeout = config.Duration(100 * time.Millisecond)
			}
			backend := &guardedBuildBackend{run: func(unit context.Context, req agent.Request) (agent.Result, error) {
				calls++
				if calls == 1 {
					return adaptiveTaskReply(t, adaptive.Reply{Action: "checkpoint", Selected: []int{1}, Scopes: []adaptive.Scope{widgetScope()}, Assignments: []adaptive.Assignment{{Number: 1, Scope: "widgets", Reason: "Accepted outcome"}}}), nil
				}
				if !resume {
					if kind == "cancel" {
						cancel()
					}
					<-unit.Done()
					return agent.Result{}, unit.Err()
				}
				return adaptiveTaskReply(t, adaptive.Reply{Action: "finish"}), nil
			}}
			f.worker.Backends.Claude = func(string) generate.CheckedBackend { return backend }
			first, err := f.worker.Build(ctx, repo, dir, BuildOptions{Adaptive: true})
			if err == nil || first.Current || first.Coverage.Completed != 1 {
				t.Fatalf("guard %+v %v", first, err)
			}
			state, err := f.store.adaptiveState(context.Background(), first.Version)
			if err != nil || state.Assignments[1].Scope != "widgets" {
				t.Fatal("accepted checkpoint lost", err)
			}
			resume = true
			f.worker.Config.Budgets.PassTimeout = config.Default().Budgets.PassTimeout
			next, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
			if err != nil || !next.Current || next.Version != first.Version || next.Coverage.Completed != 1 {
				t.Fatalf("guard resume %+v %v", next, err)
			}
		})
	}
}
