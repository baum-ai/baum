package review

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

func TestDocumentRoundTripPreservesOptionalFields(t *testing.T) {
	validator, _, published := refreshFixture(t)
	for _, tc := range []struct{ name, session string }{
		{"absent", `{"id":"s","vendor":"claude","restricted":true}`},
		{"null and empty", `{"id":"s","vendor":"claude","restricted":false,"models":[],"prompts":[],"events":[],"title":null,"git_branch":null,"started_at":null,"ended_at":null,"duration_minutes":null,"outcome":null,"prompt_count":0,"tool_calls":0}`},
		{"populated", `{"id":"s","vendor":"claude","restricted":false,"models":["model"],"title":"","git_branch":"","started_at":"2026-01-01T00:00:00+00:00","ended_at":"2026-01-01T00:01:00Z","duration_minutes":1,"outcome":"done"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original, err := decodeObject(published, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			var sessionValue any
			if err := json.Unmarshal([]byte(tc.session), &sessionValue); err != nil {
				t.Fatal(err)
			}
			index := original["index"].(map[string]any)
			index["sessions"] = []any{sessionValue}
			// These fields are optional and nullable. Preserve both absence and null.
			delete(index["checks"].([]any)[0].(map[string]any), "started_at")
			index["comments"] = []any{map[string]any{"id": json.Number("9007199254740993"), "kind": "issue", "author": "", "bot": false, "created_at": "2026-01-01T00:00:00+00:00"}}
			anchors := []byte(`[
    {"type":"hunk","id":"h_0123456789ab","lines":[1,2]},
    {"type":"file","path":""}, {"type":"commit","sha":"aaaaaaa"},
    {"type":"pr","number":1}, {"type":"pr_body"}, {"type":"pr_body","section":null}, {"type":"pr_body","section":""},
    {"type":"pr_comment","id":42}, {"type":"pr_comment","id":"42"},
    {"type":"review_thread","id":"thread"},
    {"type":"prompt","session":"s","index":1,"input":""},
    {"type":"session_event","session":"s","event":"e"},
    {"type":"check","id":"ci","test":""}, {"type":"bot_report","id":"report"},
    {"type":"issue","system":"github","key":"1"},
    {"type":"spec","page":"SPEC-1"}, {"type":"spec","page":"SPEC-1","assertion":null},
    {"type":"spec","page":"SPEC-1","assertion":"SPEC-1-A01"},
    {"type":"external_url","url":"https://example.com","label":""},
    {"type":"attachment","url":"https://example.com/image.png"}
   ]`)
			var anchorValues any
			if err := json.Unmarshal(anchors, &anchorValues); err != nil {
				t.Fatal(err)
			}
			evidence := original["sections"].([]any)[0].(map[string]any)["evidence"].([]any)[0].(map[string]any)
			evidence["anchors"], evidence["detail"], evidence["expects"] = anchorValues, "", ""
			original["sections"].([]any)[0].(map[string]any)["risks"] = []any{map[string]any{
				"id": "rsk_parser", "tier": "low", "category": "api_compat", "source": "model", "text": "Risk text",
				"anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}, "finding": nil,
				"state": "active", "introduced_version": 1, "mitigated_version": nil,
			}}
			before, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := validator.decodeFullDocument(before)
			if err != nil {
				t.Fatal(err)
			}
			after, err := validator.encodeFullDocument(doc)
			if err != nil {
				t.Fatal(err)
			}
			assertSameDocumentJSON(t, before, after)
		})
	}
}

func TestCompleteStoresAuthoritativeHunkDiff(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	bundle := &bundlepkg.Bundle{
		Repository:  "org/repo",
		PullRequest: bundlepkg.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"},
		Sources:     bundlepkg.Sources{"diff": true},
		Diff: []bundlepkg.Hunk{{
			ID: testHunk, Path: "main.go", Role: bundlepkg.RoleCode, Change: "modified",
			StartLine: 10, LineCount: 3, Additions: 1, Deletions: 1,
			Patch: "@@ -10,3 +10,3 @@\n before\n-old\n+new\n after",
		}},
	}
	published, findings := validator.Complete(authoredFixture(), bundle, "", "fixture-model", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if findings != "" {
		t.Fatal(findings)
	}
	document, err := decodeObject(published, "published")
	if err != nil {
		t.Fatal(err)
	}
	hunk := document["index"].(map[string]any)["hunks"].([]any)[0].(map[string]any)
	diff := hunk["diff"].(map[string]any)
	if diff["header"] != "@@ -10,3 +10,3 @@" {
		t.Fatalf("unexpected hunk header: %v", diff["header"])
	}
	lines := diff["lines"].([]any)
	if len(lines) != 4 || lines[1].(map[string]any)["kind"] != "remove" || lines[2].(map[string]any)["kind"] != "add" {
		t.Fatalf("unexpected hunk lines: %v", lines)
	}
	if diff := indexedHunkDiff(bundlepkg.Hunk{Path: "main.go", Patch: "@@ -10,3 +10,3 @@\n partial"}); diff != nil {
		t.Fatalf("truncated patch was exposed as a complete hunk: %+v", diff)
	}
	if diff := indexedHunkDiff(bundlepkg.Hunk{Path: "main.go", Patch: "@@ -10,1 +10,1 @@\n valid context\nmalformed"}); diff != nil {
		t.Fatalf("malformed patch was exposed as a complete hunk: %+v", diff)
	}
}

func TestCompleteRecordsReviewPolicyChange(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []bool{false, true} {
		bundle := &bundlepkg.Bundle{
			Repository:   "org/repo",
			PullRequest:  bundlepkg.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"},
			Sources:      bundlepkg.Sources{"diff": true},
			Diff:         []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Role: bundlepkg.RoleCode, Change: "modified", Patch: "@@ -1 +1 @@\n-old\n+new"}},
			ReviewConfig: &bundlepkg.ReviewConfig{Present: true, Digest: "basedigest", ChangedInPullRequest: changed},
		}
		published, findings := validator.Complete(authoredFixture(), bundle, "", "fixture-model", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		if findings != "" {
			t.Fatal(findings)
		}
		document, err := decodeObject(published, "published")
		if err != nil {
			t.Fatal(err)
		}
		generator := document["review"].(map[string]any)["generator"].(map[string]any)
		if generator["review_md_changed"] != changed || generator["review_md"] != "basedigest" {
			t.Fatalf("changed=%t: generator = %v", changed, generator)
		}
	}
}

// The host's prompt version lands in generator.prompt_version; Complete,
// for a host that does not version its prompt, records none, and a
// prompt_version in the model's output never replaces the host's.
func TestCompleteRecordsTheHostPromptVersion(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	bundle := &bundlepkg.Bundle{
		Repository:  "org/repo",
		PullRequest: bundlepkg.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"},
		Sources:     bundlepkg.Sources{"diff": true},
		Diff:        []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Role: bundlepkg.RoleCode, Change: "modified", Patch: "@@ -1 +1 @@\n-old\n+new"}},
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, prompt := range []string{"review-v12", ""} {
		published, findings := validator.CompleteWithPrompt(authoredFixture(), bundle, "", "fixture-model", prompt, now)
		if findings != "" {
			t.Fatal(findings)
		}
		document, err := decodeObject(published, "published")
		if err != nil {
			t.Fatal(err)
		}
		got, present := document["review"].(map[string]any)["generator"].(map[string]any)["prompt_version"]
		if (prompt == "" && present) || (prompt != "" && got != prompt) {
			t.Fatalf("prompt %q: generator.prompt_version = %v", prompt, got)
		}
	}
	authored, err := decodeObject(authoredFixture(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	authored["review"] = map[string]any{"generator": map[string]any{"prompt_version": "model-written"}}
	encoded, _ := json.Marshal(authored)
	published, findings := validator.CompleteWithPrompt(encoded, bundle, "", "fixture-model", "review-v12", now)
	if findings != "" {
		return
	}
	document, err := decodeObject(published, "published")
	if err != nil {
		t.Fatal(err)
	}
	if got := document["review"].(map[string]any)["generator"].(map[string]any)["prompt_version"]; got != "review-v12" {
		t.Fatalf("model-written prompt_version kept: %v", got)
	}
}

func TestCompleteAndRefreshPreserveBlockVariants(t *testing.T) {
	validator, bundle, _ := refreshFixture(t)
	authored, err := decodeObject(authoredFixture(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	section := authored["sections"].([]any)[0].(map[string]any)
	blocks := []byte(`[
  {"kind":"repro","steps":["Run the parser"],"source":[{"type":"hunk","id":"h_0123456789ab"}]},
  {"kind":"behavior","steps":[{"id":"beh_parse","text":"Parse input","anchors":[{"type":"hunk","id":"h_0123456789ab"}],"evidence":[]}]}
 ]`)
	var blockValues any
	if err := json.Unmarshal(blocks, &blockValues); err != nil {
		t.Fatal(err)
	}
	section["blocks"] = blockValues
	authoredJSON, err := json.Marshal(authored)
	if err != nil {
		t.Fatal(err)
	}
	completed, findings := validator.Complete(authoredJSON, bundle, "", "fixture", time.Now())
	if findings != "" {
		t.Fatal(findings)
	}
	refreshed, err := validator.Refresh(completed, bundle, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range [][]byte{completed, refreshed} {
		var doc reviewDocument
		if err := json.Unmarshal(encoded, &doc); err != nil {
			t.Fatal(err)
		}
		assertSameDocumentJSON(t, blocks, doc.Sections[0].Blocks)
	}
}

func assertSameDocumentJSON(t *testing.T, before, after []byte) {
	t.Helper()
	decode := func(data []byte) any {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if !reflect.DeepEqual(decode(before), decode(after)) {
		t.Fatalf("document changed during serialization:\nbefore: %s\nafter: %s", before, after)
	}
}
