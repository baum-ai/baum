package review

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

func refreshFixture(t *testing.T) (*DocumentValidator, *bundlepkg.Bundle, []byte) {
	t.Helper()
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	required := true
	bundle := &bundlepkg.Bundle{Repository: "org/repo", PullRequest: bundlepkg.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"}, Sources: bundlepkg.Sources{"diff": true}, Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Role: bundlepkg.RoleCode, Change: "added"}}, Checks: []bundlepkg.Check{{ID: "ci", Name: "CI", Kind: "status", Status: "pending", Conclusion: "pending", Required: &required}}, Sessions: []bundlepkg.Session{{RootSessionID: "session", Vendor: "claude", Models: []string{"model"}, Prompts: []bundlepkg.Prompt{{Index: 1, ID: "prompt", At: &now, Text: "Implement this"}}, CommandEvents: []bundlepkg.CommandEvent{{ID: "event", Time: &now, Outcome: "succeeded"}}}}}
	doc, err := decodeObject(authoredFixture(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	section := doc["sections"].([]any)[0].(map[string]any)
	section["evidence"] = []any{
		map[string]any{"id": "ev_ci", "kind": "ci", "claim": "CI passes", "status": "pending", "source": "ci", "anchors": []any{map[string]any{"type": "check", "id": "ci"}}},
		map[string]any{"id": "ev_session", "kind": "run", "claim": "Step performed", "status": "observed", "source": "session", "anchors": []any{map[string]any{"type": "session_event", "session": "session", "event": "event"}}},
	}
	authored, _ := json.Marshal(doc)
	published, findings := validator.Complete(authored, bundle, "", "fixture-model", now)
	if findings != "" {
		t.Fatal(findings)
	}
	return validator, bundle, published
}

func TestRefreshUpdatesChecksSessionsAndDerivedSummaries(t *testing.T) {
	for _, tc := range []struct{ conclusion, status string }{{"success", "verified"}, {"failure", "failed"}, {"pending", "pending"}, {"neutral", "skipped"}, {"error", "failed"}} {
		t.Run(tc.conclusion, func(t *testing.T) {
			validator, bundle, published := refreshFixture(t)
			bundle.Checks[0].Conclusion = tc.conclusion
			bundle.Sessions = []bundlepkg.Session{{RootSessionID: "restricted", Vendor: "codex", Restricted: true}}
			bundle.PullRequest.State, bundle.PullRequest.MergedSHA = "merged", "ccccccc"
			now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
			refreshed, err := validator.Refresh(published, bundle, now)
			if err != nil {
				t.Fatal(err)
			}
			oldDoc, _ := decodeObject(published, "old")
			doc, _ := decodeObject(refreshed, "new")
			section := doc["sections"].([]any)[0].(map[string]any)
			evidence := section["evidence"].([]any)
			if evidence[0].(map[string]any)["status"] != tc.status || evidence[1].(map[string]any)["status"] != "asserted" {
				t.Fatalf("wrong refreshed evidence: %s", refreshed)
			}
			index := doc["index"].(map[string]any)
			sessions := index["sessions"].([]any)
			if len(sessions) != 1 || len(sessions[0].(map[string]any)) != 3 {
				t.Fatal("session unlink/restriction was not reflected")
			}
			summary := section["derived"].(map[string]any)["evidence_summary"].(map[string]any)
			if summary[tc.status] != float64(1) || summary["asserted"] != float64(1) {
				t.Fatalf("wrong evidence summary: %v", summary)
			}
			header := doc["review"].(map[string]any)
			oldHeader := oldDoc["review"].(map[string]any)
			for _, key := range []string{"version", "previous_version", "generated_at", "head", "base", "merge_base", "generator"} {
				if !reflect.DeepEqual(header[key], oldHeader[key]) {
					t.Errorf("refresh changed generation field %s", key)
				}
			}
			if header["merged_commit"] != "ccccccc" || header["evidence_refreshed_at"] != now.Format(time.RFC3339Nano) {
				t.Fatal("refresh did not publish merge/timestamp metadata")
			}
			oldSection := oldDoc["sections"].([]any)[0].(map[string]any)
			for _, key := range []string{"id", "title", "kind", "hunks", "summary", "attention", "risks", "blocks"} {
				if !reflect.DeepEqual(section[key], oldSection[key]) {
					t.Errorf("refresh changed authored section field %s", key)
				}
			}
		})
	}
}

// A document stored before blank titles were rejected still refreshes, with
// the section labelled by position as discussion and approval label it.
func TestRefreshLabelsAStoredBlankSectionTitle(t *testing.T) {
	validator, bundle, published := refreshFixture(t)
	doc, err := decodeObject(published, "published")
	if err != nil {
		t.Fatal(err)
	}
	doc["sections"].([]any)[0].(map[string]any)["title"] = " "
	stored, _ := json.Marshal(doc)
	refreshed, err := validator.Refresh(stored, bundle, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	doc, _ = decodeObject(refreshed, "refreshed")
	if title := doc["sections"].([]any)[0].(map[string]any)["title"]; title != "Section 1" {
		t.Fatalf("title = %q", title)
	}
}

func TestRefreshDoesNotVerifyChecksAgainstANewerHead(t *testing.T) {
	validator, bundle, published := refreshFixture(t)
	bundle.PullRequest.HeadSHA = "ccccccc"
	bundle.Checks[0].Conclusion = "success"
	refreshed, err := validator.Refresh(published, bundle, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := decodeObject(refreshed, "new")
	evidence := doc["sections"].([]any)[0].(map[string]any)["evidence"].([]any)[0].(map[string]any)
	if evidence["status"] != "asserted" {
		t.Fatal("new-head check verified old-head evidence")
	}
}

// A refresh after a push keeps the reviewed head's checks in the index, under
// the reviewed head: the pull request's own head has moved on, but these are
// the runs that speak for the revision this document describes.
func TestRefreshIndexesChecksUnderTheReviewedHeadAfterAPush(t *testing.T) {
	validator, bundle, published := refreshFixture(t)
	bundle.PullRequest.HeadSHA = "ccccccc"
	bundle.Checks[0].Conclusion = "success"
	refreshed, err := validator.Refresh(published, bundle, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := decodeObject(refreshed, "new")
	checks := doc["index"].(map[string]any)["checks"].([]any)
	if len(checks) != 1 {
		t.Fatalf("checks after refresh = %v, want the one loaded for the reviewed head", checks)
	}
	check := checks[0].(map[string]any)
	if check["head"] != "aaaaaaa" || check["conclusion"] != "success" {
		t.Fatalf("check = %v, want success under the reviewed head aaaaaaa", check)
	}
}

func TestRefreshEvidencePrecedence(t *testing.T) {
	check := func(id string) map[string]any { return map[string]any{"type": "check", "id": id} }
	for _, tc := range []struct {
		name, kind, want string
		anchors          []any
		stale            bool
	}{
		{name: "failure overrides success", kind: "ci", anchors: []any{check("pass"), check("fail")}, want: "failed"},
		{name: "failure overrides pending", kind: "ci", anchors: []any{check("pending"), check("fail")}, want: "failed"},
		{name: "pending overrides skipped", kind: "ci", anchors: []any{check("skip"), check("pending")}, want: "pending"},
		{name: "skipped overrides success", kind: "ci", anchors: []any{check("pass"), check("skip")}, want: "skipped"},
		{name: "missing check overrides failure", kind: "ci", anchors: []any{check("fail"), check("missing")}, want: "asserted"},
		{name: "missing report overrides success", kind: "test", anchors: []any{check("pass"), map[string]any{"type": "bot_report", "id": "missing"}}, want: "asserted"},
		{name: "test requires supporting evidence", kind: "test", anchors: []any{check("pass")}, want: "asserted"},
		{name: "test hunk supports success", kind: "test", anchors: []any{check("pass"), map[string]any{"type": "hunk", "id": testHunk}}, want: "verified"},
		{name: "stale evidence excluded from counts", kind: "ci", anchors: []any{check("pass")}, want: "verified", stale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validator, bundle, published := refreshFixture(t)
			bundle.Checks = nil
			for id, conclusion := range map[string]string{"pass": "success", "fail": "failure", "pending": "pending", "skip": "skipped"} {
				bundle.Checks = append(bundle.Checks, bundlepkg.Check{ID: id, Name: id, Kind: "status", Status: conclusion, Conclusion: conclusion})
			}
			doc, err := decodeObject(published, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			section := doc["sections"].([]any)[0].(map[string]any)
			evidence := section["evidence"].([]any)[0].(map[string]any)
			evidence["kind"], evidence["anchors"] = tc.kind, tc.anchors
			section["evidence"] = []any{evidence}
			if tc.stale {
				section["derived"].(map[string]any)["state"] = "stale"
				section["hunks"] = []any{}
			}
			doc["index"].(map[string]any)["hunks"].([]any)[0].(map[string]any)["role"] = "test"
			published, err = json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			refreshed, err := validator.Refresh(published, bundle, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			doc, err = decodeObject(refreshed, "refreshed")
			if err != nil {
				t.Fatal(err)
			}
			section = doc["sections"].([]any)[0].(map[string]any)
			evidence = section["evidence"].([]any)[0].(map[string]any)
			if evidence["status"] != tc.want {
				t.Fatalf("status = %v, want %s", evidence["status"], tc.want)
			}
			if (evidence["granularity"] == "job") != (tc.want == "verified") {
				t.Fatalf("unexpected granularity: %v", evidence["granularity"])
			}
			counts := section["derived"].(map[string]any)["evidence_summary"].(map[string]any)
			if tc.stale && len(counts) != 0 || !tc.stale && counts[tc.want] != float64(1) {
				t.Fatalf("unexpected evidence counts: %v", counts)
			}
		})
	}
}

// The prompt version is generation provenance: a refresh keeps the value the
// refreshed version was generated with, and adds none to a document stored
// without one.
func TestRefreshKeepsThePromptVersion(t *testing.T) {
	for _, recorded := range []string{"review-v12", ""} {
		validator, bundle, published := refreshFixture(t)
		doc, err := decodeObject(published, "published")
		if err != nil {
			t.Fatal(err)
		}
		generator := doc["review"].(map[string]any)["generator"].(map[string]any)
		delete(generator, "prompt_version")
		if recorded != "" {
			generator["prompt_version"] = recorded
		}
		stored, _ := json.Marshal(doc)
		refreshed, err := validator.Refresh(stored, bundle, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		after, _ := decodeObject(refreshed, "refreshed")
		got, present := after["review"].(map[string]any)["generator"].(map[string]any)["prompt_version"]
		if recorded == "" && present {
			t.Errorf("refresh added prompt_version %v to a document without one", got)
		}
		if recorded != "" && got != recorded {
			t.Errorf("refresh changed prompt_version to %v, want %s", got, recorded)
		}
	}
}
