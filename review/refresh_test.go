package review

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
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

// A decision's turn that the bundle carries as a prompt, intermediate or
// not, resolves: the index lists it with its text.
func TestDecisionPromptAnchorsResolveToTheirText(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var prompts []bundlepkg.Prompt
	for _, index := range []int{1, 3, 6} {
		prompts = append(prompts, bundlepkg.Prompt{Index: index, ID: fmt.Sprint("prompt-", index), At: &now, Text: fmt.Sprint("Turn ", index)})
	}
	decisions := []bundlepkg.SessionDecision{{Summary: "Chose a slice.", Who: "human", Category: "implementation", Turns: []int{3}}}
	bundle := &bundlepkg.Bundle{Repository: "org/repo", PullRequest: bundlepkg.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"}, Sources: bundlepkg.Sources{"diff": true}, Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Role: bundlepkg.RoleCode, Change: "added"}},
		Sessions: []bundlepkg.Session{{RootSessionID: "session", Vendor: "claude", Prompts: prompts, Decisions: &decisions}}}
	doc, err := decodeObject(authoredFixture(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	section := doc["sections"].([]any)[0].(map[string]any)
	section["blocks"] = []any{map[string]any{"kind": "decisions", "items": []any{map[string]any{
		"id": "dec_slice", "by": "human", "text": "Chose a slice.",
		"anchors": []any{map[string]any{"type": "prompt", "session": "session", "index": 3}, map[string]any{"type": "prompt", "session": "session", "index": 4}, map[string]any{"type": "hunk", "id": testHunk}},
	}}}}
	evidence := func(id string, index int) map[string]any {
		return map[string]any{"id": id, "kind": "run", "claim": "The human chose a slice", "status": "reported", "source": "author_human", "anchors": []any{map[string]any{"type": "prompt", "session": "session", "index": index}}}
	}
	section["evidence"] = []any{evidence("ev_cited", 3), evidence("ev_absent", 4)}
	authored, _ := json.Marshal(doc)
	published, findings := validator.Complete(authored, bundle, "", "fixture-model", now)
	if findings != "" {
		t.Fatal(findings)
	}
	var stored struct {
		Index struct {
			Sessions []struct {
				Prompts []struct {
					Index int    `json:"index"`
					Text  string `json:"text"`
				} `json:"prompts"`
			} `json:"sessions"`
		} `json:"index"`
		Sections []struct {
			Evidence []struct {
				Status string `json:"status"`
			} `json:"evidence"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(published, &stored); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, prompt := range stored.Index.Sessions[0].Prompts {
		texts = append(texts, fmt.Sprint(prompt.Index, ":", prompt.Text))
	}
	if !slices.Equal(texts, []string{"1:Turn 1", "3:Turn 3", "6:Turn 6"}) {
		t.Fatalf("indexed prompts = %v", texts)
	}
	if cited, absent := stored.Sections[0].Evidence[0].Status, stored.Sections[0].Evidence[1].Status; cited != "reported" || absent != "asserted" {
		t.Fatalf("evidence on a carried turn is %s, on an absent turn %s; want reported and asserted", cited, absent)
	}
}

type refreshedPrompt struct {
	Index int    `json:"index"`
	ID    string `json:"id"`
	At    string `json:"at"`
	Text  string `json:"text"`
}

type refreshedIndex struct {
	Index struct {
		Sessions []struct {
			ID          string            `json:"id"`
			Restricted  bool              `json:"restricted"`
			PromptCount *int              `json:"prompt_count"`
			Prompts     []refreshedPrompt `json:"prompts"`
		} `json:"sessions"`
	} `json:"index"`
	Sections []struct {
		Evidence []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"evidence"`
	} `json:"sections"`
}

func decodeRefreshedIndex(t *testing.T, document []byte) refreshedIndex {
	t.Helper()
	var decoded refreshedIndex
	if err := json.Unmarshal(document, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// A refresh, whose bundle has no decisions, carries the stored index's
// prompts the bundle lacks as they were generated: a cited prompt that byte
// trimming shortened stays shortened, it survives although the decisions
// anchor only hunks, evidence on it still resolves, and a session that is
// restricted or no longer linked carries nothing.
func TestRefreshCarriesTheStoredIndexPrompts(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	generatedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	refreshedAt := generatedAt.Add(time.Minute)
	prompt := func(index int, text string) bundlepkg.Prompt {
		return bundlepkg.Prompt{Index: index, ID: fmt.Sprint("prompt-", index), At: &generatedAt, Text: text}
	}
	long := strings.Repeat("x", 4*bundlepkg.CitedPromptHeadBytes)
	head := long[:bundlepkg.CitedPromptHeadBytes]
	decisions := []bundlepkg.SessionDecision{{Summary: "Chose a slice.", Who: "human", Category: "implementation", Turns: []int{1, 3}}}
	sessions := func(restrictedNow bool) []bundlepkg.Session {
		return []bundlepkg.Session{
			{RootSessionID: "session", Vendor: "claude", Prompts: []bundlepkg.Prompt{prompt(1, "First "+head), prompt(3, head), prompt(6, "Turn 6")}, Decisions: &decisions},
			{RootSessionID: "restricted", Vendor: "claude", Prompts: []bundlepkg.Prompt{prompt(1, "Mine 1"), prompt(2, "Mine 2"), prompt(4, "Mine 4")}, Restricted: restrictedNow},
			{RootSessionID: "unlinked", Vendor: "claude", Prompts: []bundlepkg.Prompt{prompt(1, "Gone 1"), prompt(5, "Gone 5")}},
		}
	}
	generated := &bundlepkg.Bundle{Repository: "org/repo", PullRequest: bundlepkg.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"}, Sources: bundlepkg.Sources{"diff": true}, Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Role: bundlepkg.RoleCode, Change: "added"}},
		Sessions: sessions(false)}
	doc, err := decodeObject(authoredFixture(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	section := doc["sections"].([]any)[0].(map[string]any)
	// The model anchored its decision only to a hunk.
	section["blocks"] = []any{map[string]any{"kind": "decisions", "items": []any{map[string]any{
		"id": "dec_slice", "by": "human", "text": "Chose a slice.", "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}},
	}}}}
	evidence := func(id, session string, index int) map[string]any {
		return map[string]any{"id": id, "kind": "run", "claim": "The human chose a slice", "status": "reported", "source": "author_human", "anchors": []any{map[string]any{"type": "prompt", "session": session, "index": index}}}
	}
	section["evidence"] = []any{evidence("ev_cited", "session", 3), evidence("ev_restricted", "restricted", 2)}
	authored, _ := json.Marshal(doc)
	published, findings := validator.Complete(authored, generated, "", "fixture-model", generatedAt)
	if findings != "" {
		t.Fatal(findings)
	}

	// The refresh bundle reads the first and latest prompts at full length,
	// has no decisions, finds one session restricted and no longer links
	// another.
	refresh := *generated
	refresh.Sessions = sessions(true)[:2]
	refresh.Sessions[0].Decisions = nil
	refresh.Sessions[0].Prompts = []bundlepkg.Prompt{prompt(1, "First "+long), prompt(7, "Turn 7")}
	refreshed, err := validator.Refresh(published, &refresh, refreshedAt)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeRefreshedIndex(t, refreshed)
	if len(got.Index.Sessions) != 2 || got.Index.Sessions[0].ID != "session" || got.Index.Sessions[1].ID != "restricted" {
		t.Fatalf("refreshed sessions = %+v", got.Index.Sessions)
	}
	at := generatedAt.Format(time.RFC3339Nano)
	want := []refreshedPrompt{
		{Index: 1, ID: "prompt-1", At: at, Text: "First " + head},
		{Index: 3, ID: "prompt-3", At: at, Text: head},
		{Index: 6, ID: "prompt-6", At: at, Text: "Turn 6"},
		{Index: 7, ID: "prompt-7", At: at, Text: "Turn 7"},
	}
	if carried := got.Index.Sessions[0]; !reflect.DeepEqual(carried.Prompts, want) || carried.PromptCount == nil || *carried.PromptCount != len(want) {
		t.Fatalf("refreshed prompts = %+v (count %v), want %+v", carried.Prompts, carried.PromptCount, want)
	}
	if restricted := got.Index.Sessions[1]; !restricted.Restricted || len(restricted.Prompts) != 0 {
		t.Fatalf("a session restricted since generation carries %+v", restricted)
	}
	statuses := map[string]string{}
	for _, item := range got.Sections[0].Evidence {
		statuses[item.ID] = item.Status
	}
	if statuses["ev_cited"] != "reported" || statuses["ev_restricted"] != "asserted" {
		t.Fatalf("evidence statuses after refresh = %v, want a carried prompt reported and a restricted one asserted", statuses)
	}

	// A second refresh carries the same prompts again: nothing grows.
	again, err := validator.Refresh(refreshed, &refresh, refreshedAt)
	if err != nil {
		t.Fatal(err)
	}
	if prompts := decodeRefreshedIndex(t, again).Index.Sessions[0].Prompts; !reflect.DeepEqual(prompts, want) {
		t.Fatalf("prompts after a second refresh = %+v", prompts)
	}
}

// A stored index whose prompts the refresh bundle lists too refreshes as it
// did before prompts were carried: the bundle's entries, its text included,
// replace the stored ones.
func TestRefreshWithoutCarriedPromptsUsesTheBundlePrompts(t *testing.T) {
	validator, bundle, published := refreshFixture(t)
	later := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	refresh := *bundle
	refresh.Sessions = slices.Clone(bundle.Sessions)
	refresh.Sessions[0].Prompts = []bundlepkg.Prompt{{Index: 1, ID: "prompt", At: &later, Text: "Implement that"}}
	refreshed, err := validator.Refresh(published, &refresh, later)
	if err != nil {
		t.Fatal(err)
	}
	want := []refreshedPrompt{{Index: 1, ID: "prompt", At: later.Format(time.RFC3339Nano), Text: "Implement that"}}
	if got := decodeRefreshedIndex(t, refreshed).Index.Sessions[0]; !reflect.DeepEqual(got.Prompts, want) || *got.PromptCount != 1 {
		t.Fatalf("refreshed prompts = %+v, want %+v", got.Prompts, want)
	}
}

// carriedFixture publishes a document generated at generatedAt from one
// session whose prompts are at the turns given, the middle ones cited.
func carriedFixture(t *testing.T, generatedAt time.Time, turns ...int) (*DocumentValidator, *bundlepkg.Bundle, []byte) {
	t.Helper()
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	var prompts []bundlepkg.Prompt
	for _, index := range turns {
		at := generatedAt.Add(time.Duration(index-100) * time.Minute)
		prompts = append(prompts, bundlepkg.Prompt{Index: index, ID: fmt.Sprint("prompt-", index), At: &at, Text: fmt.Sprint("Turn ", index)})
	}
	decisions := []bundlepkg.SessionDecision{{Summary: "Chose a slice.", Who: "human", Category: "implementation", Turns: turns[1 : len(turns)-1]}}
	bundle := &bundlepkg.Bundle{Repository: "org/repo", PullRequest: bundlepkg.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"}, Sources: bundlepkg.Sources{"diff": true}, Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Role: bundlepkg.RoleCode, Change: "added"}},
		Sessions: []bundlepkg.Session{{RootSessionID: "session", Vendor: "claude", Prompts: prompts, Decisions: &decisions}}}
	published, findings := validator.Complete(authoredFixture(), bundle, "", "fixture-model", generatedAt)
	if findings != "" {
		t.Fatal(findings)
	}
	return validator, bundle, published
}

func promptTurns(t *testing.T, document []byte) []string {
	t.Helper()
	session := decodeRefreshedIndex(t, document).Index.Sessions[0]
	var turns []string
	for _, prompt := range session.Prompts {
		turns = append(turns, fmt.Sprint(prompt.Index, ":", prompt.ID))
	}
	if session.PromptCount == nil || *session.PromptCount != len(turns) {
		t.Fatalf("prompt count %v for %v", session.PromptCount, turns)
	}
	return turns
}

// Only prompts timestamped at or before generation are carried: a latest
// prompt that only a refresh added is dropped once the bundle moves past it,
// so refreshes with a moving latest prompt keep the index bounded by
// generation's prompts and the bundle's current ones.
func TestRefreshDoesNotAccumulatePromptsOnlyRefreshesAdded(t *testing.T) {
	generatedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	validator, generated, document := carriedFixture(t, generatedAt, 1, 3, 5)
	first := generated.Sessions[0].Prompts[0]
	for _, latest := range []int{6, 7, 8, 9} {
		at := generatedAt.Add(time.Duration(latest) * time.Minute)
		refresh := *generated
		refresh.Sessions = slices.Clone(generated.Sessions)
		refresh.Sessions[0].Decisions = nil
		refresh.Sessions[0].Prompts = []bundlepkg.Prompt{first, {Index: latest, ID: fmt.Sprint("prompt-", latest), At: &at, Text: strings.Repeat("long ", 1000)}}
		var err error
		if document, err = validator.Refresh(document, &refresh, at); err != nil {
			t.Fatal(err)
		}
		want := []string{"1:prompt-1", "3:prompt-3", "5:prompt-5", fmt.Sprint(latest, ":prompt-", latest)}
		if got := promptTurns(t, document); !slices.Equal(got, want) {
			t.Fatalf("prompts after the refresh listing turn %d = %v, want %v", latest, got, want)
		}
	}
}

// The refresh bundle wins over a stale stored entry: a prompt it lists at
// another turn, or a turn it lists with another prompt, drops the stored one.
func TestRefreshDropsStoredPromptsTheBundleRenumbered(t *testing.T) {
	generatedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	validator, generated, document := carriedFixture(t, generatedAt, 1, 3, 4, 6)
	at := generatedAt.Add(-time.Hour)
	refresh := *generated
	refresh.Sessions = slices.Clone(generated.Sessions)
	refresh.Sessions[0].Decisions = nil
	// After a re-ingest prompt-3 is turn 2 and turn 6 is another prompt.
	refresh.Sessions[0].Prompts = []bundlepkg.Prompt{
		{Index: 1, ID: "prompt-1", At: &at, Text: "Turn 1"},
		{Index: 2, ID: "prompt-3", At: &at, Text: "Turn 3"},
		{Index: 6, ID: "prompt-other", At: &at, Text: "Another turn"},
	}
	refreshed, err := validator.Refresh(document, &refresh, generatedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1:prompt-1", "2:prompt-3", "4:prompt-4", "6:prompt-other"}
	if got := promptTurns(t, refreshed); !slices.Equal(got, want) {
		t.Fatalf("prompts after a renumbering refresh = %v, want %v", got, want)
	}
}
