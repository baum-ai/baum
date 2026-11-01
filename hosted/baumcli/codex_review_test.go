package baumcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexAutoReviewUsesTheActiveTurn(t *testing.T) {
	auto := `{"type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"auto_review"}}` + "\n"
	manual := `{"type":"turn_context","payload":{"turn_id":"t","approvals_reviewer":"user"}}` + "\n"
	for _, tc := range []struct {
		name, rollout string
		want          bool
	}{
		{"auto", auto, true},
		{"manual", manual, false},
		{"overridden to manual", auto + manual, false},
		{"overridden to auto", manual + auto, true},
		{"different turn", strings.ReplaceAll(auto, `"t"`, `"other"`), false},
		{"reviewer absent", `{"type":"turn_context","payload":{"turn_id":"t"}}` + "\n", false},
		{"context absent", `{"type":"response_item","payload":{"approvals_reviewer":"auto_review"}}` + "\n", false},
		{"invalid context", `{"type":"turn_context",` + "\n", false},
		{"crosses read boundary", auto + strings.Repeat("x", (64<<10)-40) + "\n", true},
		{"large tool output", auto + `{"type":"response_item","payload":"` + strings.Repeat("x", 200<<10) + `"}` + "\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			if err := os.WriteFile(path, []byte(tc.rollout), 0o600); err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(map[string]string{"turn_id": "t", "transcript_path": path})
			input := newHookInput(strings.NewReader(string(payload)))
			input.read()
			if got := codexAutoReview(input); got != tc.want {
				t.Fatalf("auto review = %v, want %v", got, tc.want)
			}
		})
	}
	for _, payload := range []string{
		`{}`, `{"turn_id":"t","transcript_path":"/missing/rollout.jsonl"}`,
		`{"turn_id":"t","transcript_path":` + quoted(t.TempDir()) + `}`,
	} {
		input := newHookInput(strings.NewReader(payload))
		input.read()
		if codexAutoReview(input) {
			t.Fatalf("unavailable context enabled auto review: %s", payload)
		}
	}
}

func quoted(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}
