package review

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

const testDecisionID = "8ca884dc-240d-5b4f-a915-4a3cfe609895"

func decisionDocument(t *testing.T, item map[string]any) []byte {
	t.Helper()
	doc, err := decodeObject(authoredFixture(), "candidate")
	checkNoError(t, err)
	doc["sections"].([]any)[0].(map[string]any)["blocks"] = []any{map[string]any{"kind": "decisions", "items": []any{item}}}
	return tileJSON(t, doc)
}

// A decision item the host attached carries its decision's ID through
// preparation, completion and stored validation; the model is not offered
// the field, and an ID that is not a UUID is rejected.
func TestDecisionItemsCarryTheHostsDecisionID(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	bundle := &bundlepkg.Bundle{
		Repository:  "org/repo",
		PullRequest: bundlepkg.PullRequest{Number: 1, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb"},
		Sources:     bundlepkg.Sources{"diff": true},
		Diff:        []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Role: bundlepkg.RoleCode, Change: "added", StartLine: 1, LineCount: 1, Additions: 1}},
	}
	item := func(id any) map[string]any {
		item := map[string]any{"id": "dec_s1d1", "title": "Choose the parser", "by": "agent", "text": "Chose a parser.",
			"anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}}
		if id != nil {
			item["decision_id"] = id
		}
		return item
	}

	completed, findings := v.Complete(decisionDocument(t, item(testDecisionID)), bundle, t.TempDir(), "fixture-model", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checkEqual(t, "", findings)
	if !strings.Contains(string(completed), `"decision_id":"`+testDecisionID+`"`) {
		t.Fatalf("the completed document lost the decision ID: %s", completed)
	}
	if stored := v.ValidateStored(completed, bundle); stored != nil {
		t.Fatalf("stored validation: %v", stored)
	}
	if _, findings := v.Prepare(decisionDocument(t, item("dec_s1d1")), bundle, t.TempDir()); findings == "" {
		t.Fatal("a decision ID that is not a UUID was accepted")
	}

	// Without the ID the document is what it was before the field existed.
	without, findings := v.Complete(decisionDocument(t, item(nil)), bundle, t.TempDir(), "fixture-model", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	checkEqual(t, "", findings)
	if strings.Contains(string(without), "decision_id") {
		t.Fatalf("a document without decision IDs has one: %s", without)
	}

	for _, kinds := range [][]string{nil, {"bug-fix"}} {
		schema, err := v.ModelSchema(kinds)
		checkNoError(t, err)
		if strings.Contains(string(schema), "decision_id") {
			t.Fatalf("the model schema for tile kinds %v offers decision_id", kinds)
		}
	}
	encoded, err := json.Marshal(v.Contract())
	checkNoError(t, err)
	if !strings.Contains(string(encoded), "decision_id") {
		t.Fatal("the authored contract has no decision_id")
	}
}
