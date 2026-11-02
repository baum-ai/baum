package fix

import (
	"context"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent/prompt"
)

func TestBuildPromptUsesTheFixOverridesAndKeepsTheDataMarkers(t *testing.T) {
	entry, _ := prompt.Lookup(prompt.IDFix)
	overrides := prompt.Overrides{prompt.IDFix: entry.Default + "\nFIX-OVERRIDE-MARKER\n"}
	text, tag, err := buildPrompt(context.Background(), task{Repository: "o/r", Number: 1, Title: "t", HeadRef: "f", BaseRef: "main", Prompts: overrides}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	override := strings.Index(text, "FIX-OVERRIDE-MARKER")
	data := strings.Index(text, "BEGIN UNTRUSTED DATA "+tag)
	if override < 0 || data < override || !strings.Contains(text, "END UNTRUSTED DATA "+tag) {
		t.Fatalf("override at %d, data at %d:\n%s", override, data, text)
	}
	builtin, _, err := buildPrompt(context.Background(), task{Repository: "o/r", Number: 1, Title: "t", HeadRef: "f", BaseRef: "main"}, false, false)
	if err != nil || strings.Contains(builtin, "FIX-OVERRIDE-MARKER") {
		t.Fatal("the built-in prompt carries the override", err)
	}
}
