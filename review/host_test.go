package review

import (
	"encoding/json"
	"testing"
	"time"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

// HostParts derives the index, stats and risk score exactly as a completed
// baum.review/1 document has them, from the same bundle and sections.
func TestHostPartsMatchCompletion(t *testing.T) {
	validator, bundle, _ := refreshFixture(t)
	at := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	bundle.Diff[0].StartLine, bundle.Diff[0].LineCount, bundle.Diff[0].Additions = 10, 3, 3
	bundle.Diff[0].Patch = "@@ -9,1 +10,3 @@\n context\n+added one\n+added two\n"
	bundle.Diff = append(bundle.Diff, bundlepkg.Hunk{ID: "h_0123456789ac", Path: "go.sum", Role: bundlepkg.RoleGenerated, Change: "modified", Additions: 1, Deletions: 1})
	document := mustDecode(t, authoredFixture())
	sections := document["sections"].([]any)
	sections[0].(map[string]any)["assessment"] = map[string]any{"level": 3, "factors": []any{}, "reasons": []any{map[string]any{"text": "It parses untrusted input.", "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}}}}
	sections[0].(map[string]any)["risks"] = []any{map[string]any{"id": "rsk_nil", "tier": "high", "category": "correctness", "source": "model", "text": "The parser can be nil.", "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}}}
	completed, findings := validator.CompleteWithPrompt(encodeJSON(t, document), bundle, "", "fixture-model", "v1", at)
	if findings != "" {
		t.Fatal(findings)
	}
	var doc struct {
		Review  json.RawMessage `json:"review"`
		Index   json.RawMessage `json:"index"`
		Stats   json.RawMessage `json:"stats"`
		Summary struct {
			RiskScore json.RawMessage `json:"risk_score"`
		} `json:"summary"`
		Sections []struct {
			Derived json.RawMessage `json:"derived"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(completed, &doc); err != nil {
		t.Fatal(err)
	}
	level := 3
	host, err := HostParts(bundle, []HostSection{{ID: "sec_parser", Kind: "logical", Hunks: []string{testHunk}, Level: &level, Reasons: []string{"It parses untrusted input."},
		Risks: []HostRisk{{ID: "rsk_nil", Tier: "high", Text: "The parser can be nil.", State: "active", Anchors: []json.RawMessage{json.RawMessage(`{"type":"hunk","id":"` + testHunk + `"}`)}}}}},
		HostInput{Version: 1, Model: "fixture-model", PromptVersion: "v1", SchemaRevision: validator.Revision, Now: at})
	if err != nil {
		t.Fatal(err)
	}
	same := func(name string, got, want json.RawMessage) {
		t.Helper()
		var g, w any
		_ = json.Unmarshal(got, &g)
		_ = json.Unmarshal(want, &w)
		if string(mustJSON(t, g)) != string(mustJSON(t, w)) {
			t.Errorf("%s differs:\n got %s\nwant %s", name, got, want)
		}
	}
	same("index", host.Index, doc.Index)
	same("stats", host.Stats, doc.Stats)
	same("risk score", host.RiskScore, doc.Summary.RiskScore)
	same("derived", host.Derived["sec_parser"], doc.Sections[0].Derived)
	var header, completedHeader map[string]any
	_ = json.Unmarshal(host.Review, &header)
	_ = json.Unmarshal(doc.Review, &completedHeader)
	for _, key := range []string{"repository", "pull_request", "head", "base", "version", "generated_at", "sources"} {
		if string(mustJSON(t, header[key])) != string(mustJSON(t, completedHeader[key])) {
			t.Errorf("review.%s = %v, want %v", key, header[key], completedHeader[key])
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestMinutesForWords(t *testing.T) {
	for words, want := range map[int]int{0: 1, 400: 1, 401: 2, 1_000_000: 240} {
		if got := MinutesForWords(words); got != want {
			t.Errorf("MinutesForWords(%d) = %d, want %d", words, got, want)
		}
	}
}
