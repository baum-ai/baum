package prompt_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
)

func readFile(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestInstructionsComposeBackendSubmitBlock(t *testing.T) {
	shared := readFile(t, "review-public-v3.md")
	if !strings.HasSuffix(shared, "## 10. Submitting\n") {
		t.Fatal("shared text must end with the Submitting heading")
	}
	for _, phrase := range []string{"`review_minutes`: whole minutes", "native English speaker", "not the time to review the diff independently"} {
		if !strings.Contains(shared, phrase) {
			t.Errorf("the shared text does not ask for the review time estimate: no %q", phrase)
		}
	}
	for _, phrase := range []string{
		"summary\": { text, context,", "- `context`: how the change fits into the codebase", "`area` (always)",
		"omit a part rather than guess or fill it", "without the `head/` or `merge-base/` prefix", "`summary.context` has an `area`",
	} {
		if !strings.Contains(shared, phrase) {
			t.Errorf("the shared text does not ask for the codebase context: no %q", phrase)
		}
	}
	cases := map[string]string{
		agent.BackendClaude:      "submit-claude.md",
		agent.BackendAnthropic:   "submit-claude.md",
		agent.BackendCodex:       "submit-codex.md",
		agent.BackendCursorAgent: "submit-cursor.md",
	}
	for backend, file := range cases {
		got, err := (prompt.Prompts{}).Instructions(context.Background(), backend)
		if err != nil {
			t.Fatalf("%s: %v", backend, err)
		}
		block := readFile(t, file)
		if !strings.HasPrefix(got.Body, shared) || !strings.HasSuffix(got.Body, block) {
			t.Fatalf("%s: body is not the shared text then %s", backend, file)
		}
		if got.Version != "review-public-v3" {
			t.Fatalf("version %q", got.Version)
		}
		sum := sha256.Sum256([]byte(got.Body))
		if got.Digest != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s: digest is not the sha256 of the body", backend)
		}
		again, _ := (prompt.Prompts{}).Instructions(context.Background(), backend)
		if again.Digest != got.Digest {
			t.Fatalf("%s: digest not stable", backend)
		}
	}
	claude, _ := (prompt.Prompts{}).Instructions(context.Background(), agent.BackendClaude)
	codex, _ := (prompt.Prompts{}).Instructions(context.Background(), agent.BackendCodex)
	if claude.Digest == codex.Digest {
		t.Fatal("claude and codex bodies must differ")
	}
}

func TestUnknownBackendIsAnError(t *testing.T) {
	for _, backend := range []string{"", "cursor", "nope"} {
		if _, err := (prompt.Prompts{}).Instructions(context.Background(), backend); err == nil {
			t.Fatalf("%q: want an error", backend)
		}
	}
}
