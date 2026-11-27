package decisionlog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/features"
	"github.com/baum-ai/baum/features/adaptive"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/internal/repos"
)

// Frozen public synthetic outcomes deliberately share a file and a session.
func semanticFixture(t *testing.T) (*featureFixture, string, string) {
	t.Helper()
	f := newFeatureFixture(t)
	f.clock.now = t0.Add(time.Hour)
	dir := discoveryClone(t)
	write(t, filepath.Join(dir, "settings.go"), "package widgets\n// Settings controls per-repository preferences.\n")
	write(t, filepath.Join(dir, "packaging.go"), "package widgets\n// AppImage builds the Linux distribution archive.\n")
	linktest.Git(t, dir, "add", ".")
	linktest.Git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "public fixture scopes")
	head := strings.TrimSpace(linktest.Git(t, dir, "rev-parse", "HEAD"))
	f.pull(1, "Repository Settings", t0, head, []string{"shared.go"})
	f.pull(2, "AppImage distribution", t0, head, []string{"shared.go"})
	version := f.live()
	f.feature(version, "settings", 1)
	f.feature(version, "appimage", 2)
	for id, scope := range map[string]adaptive.Scope{
		"settings": {Key: "settings", Title: "Repository Settings", Purpose: "Choose per-repository preferences", Boundaries: "Excludes packaging", Paths: []string{"settings.go"}, Digest: "Repository preferences"},
		"appimage": {Key: "appimage", Title: "AppImage distribution", Purpose: "Distribute Linux AppImage packages", Boundaries: "Excludes user settings", Paths: []string{"packaging.go"}, Digest: "Linux packages"},
	} {
		data, _ := json.Marshal(scope)
		f.exec(`INSERT INTO feature_scope(feature_id,version_id,repository,revision,scope_json)VALUES(?,?,?,?,?)`, id, version, repo, head, string(data))
	}
	choices := []decisions.Decision{
		{ID: "settings-choice", Summary: "Keep extraction model preferences per repository.", Rationale: "Teams use different models.", Who: decisions.WhoHuman, Category: decisions.CategoryBehavior, Turns: []int{4}, Files: []string{"shared.go"}},
		{ID: "publish-test", Summary: "Run and publish the test results before preparing the PR.", Who: decisions.WhoAgent, Category: decisions.CategoryTesting, Turns: []int{6}, Files: []string{"shared.go"}},
		{ID: "ambiguous", Summary: "Keep this handling as it is.", Who: decisions.WhoAgent, Category: decisions.CategoryImplementation, Turns: []int{8}, Files: []string{"shared.go"}},
		{ID: "appimage-choice", Summary: "Use AppImage for portable Linux distribution.", Who: decisions.WhoHuman, Category: decisions.CategoryDependency, Turns: []int{3}, Files: []string{"shared.go"}},
	}
	// A pre-source capture: branch no longer exists, but the linked commit does.
	if err := f.store.Save(context.Background(), Extraction{Meta: Meta{Agent: "claude-code", ID: "mixed", Repository: repo, Branch: "gone-branch"}, ContentHash: "frozen", Decisions: choices}); err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO session_commit(agent,session_id,repository,sha,method,confidence)VALUES('claude-code','mixed',?,?,'checkpoint',1)`, repo, head)
	for _, n := range []int{1, 2} {
		f.exec(`INSERT INTO pull_commit(repository,number,sha,committed_at)VALUES(?,?,?,?)`, repo, n, head, t0.UnixMilli())
	}
	// The checkout now differs: attribution must read the old linked commit.
	write(t, filepath.Join(dir, "settings.go"), "package unrelated\n")
	linktest.Git(t, dir, "add", ".")
	linktest.Git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "unrelated later source")
	f.worker.source = nil
	return f, dir, head
}

func attributionAnswer(t *testing.T, req agent.Request) agent.Result {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(req.Directory, "transcript.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var input attributionInput
	if err = json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	reply := struct {
		Decisions []decisionResolution `json:"decisions"`
	}{}
	for _, d := range input.Decisions {
		r := decisionResolution{DecisionID: d.ID, Reason: "The quoted choice is directly about its named scope."}
		switch d.ID {
		case "settings-choice":
			r.FeatureID = "settings"
			r.Pull = 1
			r.KeyEligible = true
		case "appimage-choice":
			r.FeatureID = "appimage"
			r.Pull = 2
			r.KeyEligible = true
		case "publish-test":
			r.FeatureID = "appimage"
			r.KeyEligible = false
			r.Reason = "Routine agent checks are activity, not a consequential feature choice."
		default:
			r.Reason = "Ambiguous evidence supports neither outcome."
		}
		reply.Decisions = append(reply.Decisions, r)
	}
	slices.Reverse(reply.Decisions)
	inner, _ := json.Marshal(reply)
	wrapped, _ := json.Marshal(sourceReply{Extraction: inner, References: []string{"settings.go", "packaging.go"}})
	return agent.Result{Document: wrapped, Calls: 1, Model: "claude-haiku-5-5", Usage: agent.Usage{Input: 100, Output: 50}}
}

func TestRepositorySemanticAttributionAndReadAgreement(t *testing.T) {
	f, dir, head := semanticFixture(t)
	ctx := context.Background()
	backend := &sourceBackend{read: func(req agent.Request) (agent.Result, error) {
		code, err := os.ReadFile(filepath.Join(req.Directory, "head", "settings.go"))
		if err != nil || !strings.Contains(string(code), "per-repository") {
			t.Fatalf("wrong/inaccessible linked source: %q %v", code, err)
		}
		var source SourceContext
		data, _ := os.ReadFile(filepath.Join(req.Directory, "source.json"))
		if err = json.Unmarshal(data, &source); err != nil || source.Revision != head || source.Resolution != "linked_session_commit" {
			t.Fatalf("source provenance %+v %v", source, err)
		}
		return attributionAnswer(t, req), nil
	}}
	f.worker.Backends.Claude = func(string) generate.CheckedBackend { return backend }
	report, err := f.worker.AttributeDecisions(ctx, repo, dir, FeatureOptions{Manual: true})
	if err != nil || report.Decisions != 4 || backend.calls != 1 {
		t.Fatalf("%+v %v calls=%d", report, err, backend.calls)
	}
	report, err = f.worker.AttributeDecisions(ctx, repo, dir, FeatureOptions{Manual: true})
	if err != nil || report.Decisions != 0 || backend.calls != 1 {
		t.Fatalf("not cached: %+v %v calls=%d", report, err, backend.calls)
	}
	version := f.live()
	settings, err := f.store.summarySource(ctx, version, repo, "settings")
	if err != nil || len(settings.Decisions) != 1 || settings.Decisions[0].ID != "settings-choice" || !settings.KeyEligible["settings-choice"] {
		t.Fatalf("settings source %+v %v", settings, err)
	}
	app, err := f.store.summarySource(ctx, version, repo, "appimage")
	if err != nil || len(app.Decisions) != 2 || app.KeyEligible["publish-test"] || !app.KeyEligible["appimage-choice"] {
		t.Fatalf("app source %+v %v", app, err)
	}
	timeline := &Timeline{Store: f.store, Registry: &timelineRegistry{prefs: repos.DefaultPreferences(), list: []repos.Repository{{Repository: repo}}}}
	snap, err := timeline.snapshot(ctx, timeline.reader(), repo, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{"settings": 1, "appimage": 2} {
		if len(snap.features[id].decisions) != want {
			t.Fatalf("timeline %s decisions %+v", id, snap.features[id].decisions)
		}
	}
	if backend.calls != 1 {
		t.Fatal("read path called an agent")
	}
	// No dominant-feature fallback for shared paths, fileless or unrelated choices.
	current, _ := f.store.Current(ctx, SessionKey{Agent: "claude-code", ID: "mixed"})
	links, _ := f.store.SessionLinks(ctx, repo, "claude-code", "mixed")
	for _, d := range current {
		if got := resolveDecision(d, links, nil); got.FeatureID != "" {
			t.Fatalf("unsupported fallback %+v", got)
		}
	}
	if got := resolveDecision(Decision{Decision: decisions.Decision{ID: "unique", Files: []string{"settings.go"}}}, features.SessionLinks{PullRequests: []features.LinkedPullRequest{{FeatureID: "settings", Paths: []string{"settings.go"}}}}, nil); got.FeatureID != "settings" || got.KeyEligible {
		t.Fatalf("positive file fallback %+v", got)
	}
	// Scope text changes invalidate cached results, including key eligibility.
	f.exec(`UPDATE feature_scope SET scope_json=json_set(scope_json,'$.boundaries','New exclusion boundary') WHERE feature_id='settings'`)
	stale, err := f.store.summarySource(ctx, version, repo, "settings")
	if err != nil || len(stale.Decisions) != 0 {
		t.Fatalf("stale semantic answer read: %+v %v", stale, err)
	}
	if backend.calls != 1 {
		t.Fatal("invalidation called a model while reading")
	}
}

func TestSemanticAttributionRejectsConcurrentInputsAndCancellation(t *testing.T) {
	for _, change := range []string{"membership", "decision", "source", "pull", "cancel"} {
		t.Run(change, func(t *testing.T) {
			f, dir, _ := semanticFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			backend := &sourceBackend{read: func(req agent.Request) (agent.Result, error) {
				result := attributionAnswer(t, req)
				switch change {
				case "membership":
					f.exec(`UPDATE feature_pull SET feature_id='settings' WHERE number=2`)
				case "decision":
					f.exec(`UPDATE decision SET summary='Changed explicit choice' WHERE decision_id='settings-choice'`)
				case "source":
					source, _ := json.Marshal(SourceContext{Repository: repo, Revision: "different", Version: sourceContextVersion})
					f.exec(`INSERT INTO capture_source(agent,session_id,source_json)VALUES('claude-code','mixed',?)`, string(source))
				case "pull":
					f.exec(`UPDATE pull_request SET body='Changed the purpose' WHERE number=1`)
				case "cancel":
					cancel()
				}
				return result, nil
			}}
			f.worker.Backends.Claude = func(string) generate.CheckedBackend { return backend }
			_, err := f.worker.AttributeDecisions(ctx, repo, dir, FeatureOptions{Manual: true})
			if change != "cancel" && err != nil {
				t.Fatal(err)
			}
			if count := f.count(`SELECT COUNT(*) FROM feature_decision`); count != 0 {
				t.Fatalf("stale answers written: %d", count)
			}
		})
	}
}

func TestSemanticAttributionVersionAndEmptyFeature(t *testing.T) {
	f := newFeatureFixture(t)
	version := f.live()
	f.feature(version, "empty")
	report, err := f.worker.AttributeDecisions(context.Background(), repo, "", FeatureOptions{})
	if err != nil || report.Sessions != 0 || len(f.backend.calls) != 0 {
		t.Fatalf("empty %+v %v", report, err)
	}
	links := features.SessionLinks{OwnFeature: "empty"}
	d := Decision{Decision: decisions.Decision{ID: "fileless", Summary: "Prepare a PR"}}
	if r := resolveDecision(d, links, nil); r.FeatureID != "" || r.KeyEligible {
		t.Fatalf("unsupported own-feature guess %+v", r)
	}
}

func TestFileFallbackRejectsUnequalCrossOutcomeOverlap(t *testing.T) {
	d := Decision{Decision: decisions.Decision{ID: "mixed", Files: []string{"shared.go", "settings.go"}}}
	links := features.SessionLinks{PullRequests: []features.LinkedPullRequest{
		{FeatureID: "settings", Paths: []string{"shared.go", "settings.go"}},
		{FeatureID: "appimage", Paths: []string{"shared.go"}},
	}}
	if got := resolveDecision(d, links, nil); got.FeatureID != "" {
		t.Fatalf("cross-outcome counts guessed a feature: %+v", got)
	}
}

func TestLocalSummaryRejectsRoutineHighlightAndKeepsHumanChoice(t *testing.T) {
	f, dir, _ := semanticFixture(t)
	ctx := context.Background()
	backend := &sourceBackend{read: func(req agent.Request) (agent.Result, error) { return attributionAnswer(t, req), nil }}
	f.worker.Backends.Claude = func(string) generate.CheckedBackend { return backend }
	if _, err := f.worker.AttributeDecisions(ctx, repo, dir, FeatureOptions{Manual: true}); err != nil {
		t.Fatal(err)
	}
	f.worker.Backends.Claude = func(string) generate.CheckedBackend { return f.backend }
	// A reply tries the routine first decision in AppImage. The stored result
	// must still have zero keys, while Settings retains its explicit human choice.
	report, err := f.worker.Summarize(ctx, repo, dir, FeatureOptions{Manual: true})
	if err != nil || len(report.Features) != 2 {
		t.Fatalf("%+v %v", report, err)
	}
	if count := f.count(`SELECT COUNT(*) FROM capture_attempt WHERE stage='pr_summary'`); count != 0 {
		t.Fatalf("semantic refresh fanned out to %d per-PR model calls", count)
	}
	_, keys, _ := f.text("appimage")
	if keys != "[]" {
		t.Fatalf("routine test publication was highlighted: %s", keys)
	}
	_, keys, _ = f.text("settings")
	if !strings.Contains(keys, "settings-choice") {
		t.Fatalf("explicit human choice lost: %s", keys)
	}
}

func TestMovedRepositoryDecisionKeepsOriginalProvenance(t *testing.T) {
	f, _, head := semanticFixture(t)
	ctx := context.Background()
	original, _ := json.Marshal(SourceContext{Version: sourceContextVersion, Repository: repo, Revision: head})
	f.exec(`INSERT INTO decision_source(decision_id,source_json)VALUES('settings-choice',?)`, string(original))
	f.exec(`UPDATE capture_session SET repository='acme/other' WHERE session_id='mixed'`)
	version, err := f.store.LiveVersion(ctx, "acme/other")
	if err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO feature(id,version_id,repository,title,description,created_at,updated_at,changed_at)VALUES('other',?,'acme/other','Unrelated feature','Another repository',0,0,0)`, version)
	f.exec(`INSERT INTO feature_session(version_id,repository,agent,session_id,feature_id,assigned_at)VALUES(?,'acme/other','claude-code','mixed','other',0)`, version)
	key := SessionKey{Agent: "claude-code", ID: "mixed"}
	current, err := f.store.currentOf(ctx, []SessionKey{key})
	if err != nil {
		t.Fatal(err)
	}
	links, err := f.store.sessionLinksOf(ctx, version, "acme/other", []SessionKey{key})
	if err != nil {
		t.Fatal(err)
	}
	set, err := f.store.attributionSet(ctx, version, "acme/other", []SessionKey{key}, current, links)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range current[key] {
		if r := resolveDecision(d, links[key], set.Resolved); r.FeatureID != "" || r.KeyEligible {
			t.Fatalf("cross-repository decision retagged %+v", r)
		}
	}
	if source := set.Inputs[key].DecisionSources["settings-choice"]; source == nil || source.Repository != repo {
		t.Fatal("immutable original decision provenance lost")
	}
}
