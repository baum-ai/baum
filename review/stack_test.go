package review

import (
	"encoding/json"
	"strings"
	"testing"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

func stackFixture() map[string]any {
	return map[string]any{
		"title": "Split the importer",
		"members": []any{
			map[string]any{"number": 10, "head_sha": "abcdef1", "review_version": 1},
			map[string]any{"number": 11, "head_sha": "abcdef2", "review_version": 3},
		},
	}
}

// storedStack is the stored example as a stack review: a stack and a section
// covering the second member.
func storedStack(t *testing.T) map[string]any {
	t.Helper()
	doc := storedExample(t)
	doc["stack"] = stackFixture()
	firstSection(doc)["pull_requests"] = []any{11}
	return doc
}

func TestValidateStoredAcceptsAStackReview(t *testing.T) {
	if findings := validateStored(t, storedStack(t), nil); findings != nil {
		t.Fatalf("stack review rejected: %q", findings)
	}
}

func TestValidateStoredAcceptsAStackWithoutSectionPullRequests(t *testing.T) {
	doc := storedExample(t)
	doc["stack"] = stackFixture()
	if findings := validateStored(t, doc, nil); findings != nil {
		t.Fatalf("stack review rejected: %q", findings)
	}
}

func TestValidateStoredRejectsAStackReview(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(doc map[string]any)
		want   string
	}{
		{"section pull request outside the stack", func(doc map[string]any) {
			firstSection(doc)["pull_requests"] = []any{10, 12}
		}, "pull request #12, which is not a stack member"},
		{"section pull requests without a stack", func(doc map[string]any) {
			delete(doc, "stack")
		}, "names pull request #11 but the document has no stack"},
		{"duplicate member", func(doc map[string]any) {
			members := doc["stack"].(map[string]any)["members"].([]any)
			doc["stack"].(map[string]any)["members"] = append(members, members[0])
		}, "duplicate stack member: #10"},
		{"empty title", func(doc map[string]any) { doc["stack"].(map[string]any)["title"] = "" }, "title"},
		{"blank title", func(doc map[string]any) { doc["stack"].(map[string]any)["title"] = " \t\n " }, "title"},
		{"title over 80 characters", func(doc map[string]any) {
			doc["stack"].(map[string]any)["title"] = strings.Repeat("x", 81)
		}, "title"},
		{"no members", func(doc map[string]any) { doc["stack"].(map[string]any)["members"] = []any{} }, "members"},
		{"member without a review version", func(doc map[string]any) {
			delete(doc["stack"].(map[string]any)["members"].([]any)[0].(map[string]any), "review_version")
		}, "review_version"},
		{"unknown member field", func(doc map[string]any) {
			doc["stack"].(map[string]any)["members"].([]any)[0].(map[string]any)["base_sha"] = "abcdef0"
		}, "base_sha"},
		{"empty section pull requests", func(doc map[string]any) { firstSection(doc)["pull_requests"] = []any{} }, "pull_requests"},
		{"repeated section pull request", func(doc map[string]any) { firstSection(doc)["pull_requests"] = []any{11, 11} }, "pull_requests"},
		{"non-positive section pull request", func(doc map[string]any) { firstSection(doc)["pull_requests"] = []any{0} }, "pull_requests"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := storedStack(t)
			tc.mutate(doc)
			findings := validateStored(t, doc, nil)
			if len(findings) == 0 {
				t.Fatal("document accepted")
			}
			requireFinding(t, findings, tc.want)
		})
	}
}

func TestValidateStoredAcceptsAnEightyCharacterStackTitle(t *testing.T) {
	doc := storedStack(t)
	doc["stack"].(map[string]any)["title"] = strings.Repeat("x", 80)
	if findings := validateStored(t, doc, nil); findings != nil {
		t.Fatalf("80-character title rejected: %q", findings)
	}
}

// A single-pull-request document has neither field and stays valid.
func TestSinglePullRequestDocumentHasNoStackFields(t *testing.T) {
	doc := storedExample(t)
	if _, present := doc["stack"]; present {
		t.Fatal("example carries a stack")
	}
	if _, present := firstSection(doc)["pull_requests"]; present {
		t.Fatal("example section carries pull_requests")
	}
	if findings := validateStored(t, doc, nil); findings != nil {
		t.Fatalf("single-pull-request document rejected: %q", findings)
	}
}

func TestAuthoredContractsAcceptAndCheckStackFields(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	candidate := func(stack any, pullRequests any) []byte {
		doc, err := decodeObject(historyCandidate(t, []any{historySection("a", testHunk)}), "fixture")
		checkNoError(t, err)
		if stack != nil {
			doc["stack"] = stack
		}
		if pullRequests != nil {
			doc["sections"].([]any)[0].(map[string]any)["pull_requests"] = pullRequests
		}
		encoded, err := json.Marshal(doc)
		checkNoError(t, err)
		return encoded
	}
	bundle := &bundlepkg.Bundle{Diff: []bundlepkg.Hunk{{ID: testHunk}}}
	var lenient any
	checkNoError(t, json.Unmarshal(candidate(stackFixture(), []any{10}), &lenient))
	checkNoError(t, resolvedSchema(t, v.Schema).Validate(lenient))
	if findings := v.Validate(candidate(stackFixture(), []any{10}), nil); findings != "" {
		t.Fatalf("strict authored contract rejected a stack: %s", findings)
	}
	if findings := v.Validate(candidate(stackFixture(), []any{12}), nil); !strings.Contains(findings, "not a stack member") {
		t.Fatalf("findings = %q", findings)
	}
	if findings := v.Validate(candidate(nil, []any{10}), nil); !strings.Contains(findings, "no stack") {
		t.Fatalf("findings = %q", findings)
	}
	kept, findings := v.Prepare(candidate(stackFixture(), []any{10}), bundle, t.TempDir())
	if findings != "" || !strings.Contains(string(kept), `"stack"`) || !strings.Contains(string(kept), `"pull_requests":[10]`) {
		t.Fatalf("stack fields lost in preparation (%q): %s", findings, kept)
	}
	// Optional fields the model leaves null are dropped, not rejected.
	nulls, err := decodeObject(candidate(nil, nil), "fixture")
	checkNoError(t, err)
	nulls["stack"] = nil
	nulls["sections"].([]any)[0].(map[string]any)["pull_requests"] = nil
	encoded, err := json.Marshal(nulls)
	checkNoError(t, err)
	checkNoError(t, json.Unmarshal(encoded, &lenient))
	checkNoError(t, resolvedSchema(t, v.Schema).Validate(lenient))
	prepared, findings := v.Prepare(encoded, bundle, t.TempDir())
	if findings != "" {
		t.Fatalf("null stack fields rejected: %s", findings)
	}
	if strings.Contains(string(prepared), `"stack"`) || strings.Contains(string(prepared), `"pull_requests"`) {
		t.Fatalf("null stack fields kept: %s", prepared)
	}
}
