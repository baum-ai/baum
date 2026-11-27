package decisionlog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/features/adaptive"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

func adaptiveFeedback(t *testing.T, req agent.Request) string {
	t.Helper()
	var feedback string
	if err := json.Unmarshal(adaptiveInput(t, req)["feedback"], &feedback); err != nil {
		t.Fatal(err)
	}
	return feedback
}

// A request the host can only serve in part is served so far and explained to
// the agent: failing it would leave a pending request no resume could serve.
func TestAdaptiveServesWhatItCanOfAnEvidenceRequest(t *testing.T) {
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
	// Pull request 9 was closed without merging: it is never catalogued.
	closed := pullJSON(9, base, base)
	closed["state"] = "closed"
	reader.rows = append(reader.rows, closed)
	calls := 0
	f.backend.task = func(req agent.Request) (agent.Result, error) {
		calls++
		switch calls {
		case 1:
			// One path list for two pull requests, and a path neither changes.
			return adaptiveTaskReply(t, adaptive.Reply{Action: "code", Selected: []int{1, 3}, Numbers: []int{1, 3}, Paths: []string{"src/search/new.go", "README.md"}}), nil
		case 2:
			in := adaptiveInput(t, req)
			if !strings.Contains(adaptiveEvidence(t, req, in["code"]), "Brand new behavior") {
				t.Fatal("the patch of the path one pull request changes was not supplied")
			}
			if feedback := adaptiveFeedback(t, req); !strings.Contains(feedback, "README.md") || strings.Contains(feedback, "new.go") {
				t.Fatalf("feedback %q", feedback)
			}
			// A closed pull request and a number GitHub has none for.
			return adaptiveTaskReply(t, adaptive.Reply{Action: "details", Numbers: []int{1, 9, 77}}), nil
		case 3:
			in := adaptiveInput(t, req)
			if !strings.Contains(adaptiveEvidence(t, req, in["details"]), "Detailed body 1") {
				t.Fatal("the description of the eligible pull request was not supplied")
			}
			if feedback := adaptiveFeedback(t, req); !strings.Contains(feedback, "#9 ") || !strings.Contains(feedback, "#77 ") {
				t.Fatalf("feedback %q", feedback)
			}
			// The same request again adds nothing, and says so.
			return adaptiveTaskReply(t, adaptive.Reply{Action: "details", Numbers: []int{1}}), nil
		default:
			if feedback := adaptiveFeedback(t, req); !strings.Contains(feedback, "added nothing") {
				t.Fatalf("feedback %q", feedback)
			}
			return adaptiveTaskReply(t, adaptive.Reply{Action: "finish", Scopes: []adaptive.Scope{widgetScope()},
				Assignments: []adaptive.Assignment{{Number: 1, Scope: "widgets", Reason: "Adds search"}, {Number: 3, Reason: "Unrelated"}}}), nil
		}
	}
	report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err != nil || !report.Current || calls != 4 {
		t.Fatalf("build %+v %v after %d calls", report, err, calls)
	}
	if f.count(`SELECT count(*) FROM pull_request WHERE number IN (9, 77)`) != 0 {
		t.Fatal("an ineligible pull request was catalogued")
	}
}

// A checkpoint keeps its valid assignments when others name a scope nobody
// defined, and an assignment selects its pull request.
func TestAdaptiveCheckpointKeepsItsValidAssignments(t *testing.T) {
	f, dir, _ := adaptiveFixture(t)
	calls := 0
	f.backend.task = func(req agent.Request) (agent.Result, error) {
		calls++
		if calls == 1 {
			return adaptiveTaskReply(t, adaptive.Reply{Action: "checkpoint", Scopes: []adaptive.Scope{widgetScope()},
				Assignments: []adaptive.Assignment{{Number: 1, Scope: "widgets", Reason: "Builds widget search"}, {Number: 3, Scope: "billing", Reason: "Bills widgets"}}}), nil
		}
		catalog := string(adaptiveInput(t, req)["catalog"])
		if !strings.Contains(catalog, `{"number":1,"state":"merged"`) || !strings.Contains(catalog, `"selected":true,"scope":"widgets"`) || strings.Count(catalog, "\n") != 2 {
			t.Fatalf("catalog:\n%s", catalog)
		}
		if strings.Contains(catalog, "billing") {
			t.Fatalf("an assignment to an undefined scope was saved:\n%s", catalog)
		}
		if feedback := adaptiveFeedback(t, req); !strings.Contains(feedback, "pull requests 3 ") || !strings.Contains(feedback, "billing") {
			t.Fatalf("feedback %q", feedback)
		}
		// The final step is strict: nothing after it could repair an
		// assignment left out.
		if calls == 2 {
			return adaptiveTaskReply(t, adaptive.Reply{Action: "finish", Assignments: []adaptive.Assignment{{Number: 3, Scope: "billing", Reason: "Bills widgets"}}}), nil
		}
		return adaptiveTaskReply(t, adaptive.Reply{Action: "finish"}), nil
	}
	report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	// The refused finish is repaired by the next call's.
	if err != nil || !report.Current || report.Coverage.Selected != 1 || report.Coverage.Completed != 1 || calls != 3 {
		t.Fatalf("build %+v %v after %d calls", report, err, calls)
	}
}

// A step whose reply stays invalid through its repairs costs that step, not
// the build: the agent is told why and goes on from the accepted checkpoint.
func TestAdaptiveRefusedStepDoesNotStopTheBuild(t *testing.T) {
	f, dir, _ := adaptiveFixture(t)
	calls := 0
	f.backend.task = func(req agent.Request) (agent.Result, error) {
		calls++
		switch {
		case calls == 1:
			return adaptiveTaskReply(t, adaptive.Reply{Action: "checkpoint", Selected: []int{1}}), nil
		case calls <= 4:
			// The first reply of the step and both of its repairs.
			return adaptiveTaskReply(t, adaptive.Reply{Action: "checkpoint", Selected: []int{999}}), nil
		}
		if feedback := adaptiveFeedback(t, req); !strings.Contains(feedback, "refused") || !strings.Contains(feedback, "999") {
			t.Fatalf("feedback %q", feedback)
		}
		return adaptiveTaskReply(t, adaptive.Reply{Action: "finish", Scopes: []adaptive.Scope{widgetScope()}, Assignments: []adaptive.Assignment{{Number: 1, Scope: "widgets", Reason: "Builds widget search"}}}), nil
	}
	report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err != nil || !report.Current || report.Coverage.Completed != 1 || calls != 5 {
		t.Fatalf("build %+v %v after %d calls", report, err, calls)
	}
}

// A decision keeps where its extraction's source came from, with the
// omissions as a count; their list stays with the capture.
func TestDecisionProvenanceCountsOmissions(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	source := &SourceContext{Version: sourceContextVersion, Repository: "o/r", Revision: "abc", Resolution: "recorded_commit",
		Omissions: []bundle.CheckoutOmission{{Path: "a.png", Reason: "binary"}, {Path: "b.png", Reason: "binary"}}, References: []string{"a.go"}}
	d := decision("D1", "Use SQLite.", 1)
	if err := f.store.Save(ctx, Extraction{Meta: Meta{Agent: "claude-code", ID: "s1", Repository: "o/r", Fingerprint: "f"}, ContentHash: "h", Decisions: []decisions.Decision{d}, Source: source}); err != nil {
		t.Fatal(err)
	}
	var capture, kept string
	if err := f.store.db.QueryRow(`SELECT source_json FROM capture_source WHERE session_id='s1'`).Scan(&capture); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRow(`SELECT source_json FROM decision_source WHERE decision_id='D1'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(capture, "a.png") || strings.Contains(kept, "a.png") || !strings.Contains(kept, `"omitted":2`) || !strings.Contains(kept, `"a.go"`) || !strings.Contains(kept, `"abc"`) {
		t.Fatalf("capture %s\ndecision %s", capture, kept)
	}
	if len(source.Omissions) != 2 || source.Omitted != 0 {
		t.Fatalf("the capture's context was changed: %+v", source)
	}
}

// A finish that leaves most of a sizeable catalog unselected is kept as a
// checkpoint and asked about once; the second finish stands.
func TestAdaptiveFinishAsksOnceAboutAnUnselectedCatalog(t *testing.T) {
	f, dir, reader := adaptiveFixture(t)
	head := reader.rows[0]["head"].(map[string]any)["sha"].(string)
	reader.rows = nil
	for number := 1; number <= 90; number++ {
		p := pullJSON(number, head, head)
		date := f.clock.now.Add(-time.Duration(number) * time.Hour).Format(time.RFC3339)
		p["created_at"], p["updated_at"], p["state"], p["merged_at"] = date, date, "closed", date
		reader.rows = append(reader.rows, p)
	}
	live := f.live()
	finish := adaptive.Reply{Action: "finish", Scopes: []adaptive.Scope{widgetScope()}, Assignments: []adaptive.Assignment{{Number: 1, Scope: "widgets", Reason: "Builds widget search"}}}
	calls := 0
	f.backend.task = func(req agent.Request) (agent.Result, error) {
		calls++
		if calls == 1 {
			return adaptiveTaskReply(t, finish), nil
		}
		if feedback := adaptiveFeedback(t, req); calls != 2 || !strings.Contains(feedback, "89 of the 90") {
			t.Fatalf("call %d feedback %q", calls, feedback)
		}
		// The first finish was kept: its assignment is in the catalog.
		if catalog := string(adaptiveInput(t, req)["catalog"]); !strings.Contains(catalog, `"scope":"widgets"`) {
			t.Fatalf("the finish's changes were not kept:\n%s", catalog)
		}
		if state, _ := f.versionState(live); state != VersionCurrent {
			t.Fatal("the candidate was promoted before the question was answered")
		}
		return adaptiveTaskReply(t, adaptive.Reply{Action: "finish", Reason: "The other 89 are small unrelated maintenance."}), nil
	}
	report, err := f.worker.Build(context.Background(), repo, dir, BuildOptions{Adaptive: true})
	if err != nil || !report.Current || calls != 2 || report.Coverage.Catalogued != 90 || report.Coverage.Completed != 1 {
		t.Fatalf("build %+v %v after %d calls", report, err, calls)
	}
}
