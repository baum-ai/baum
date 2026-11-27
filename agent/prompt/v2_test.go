package prompt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/tiles/view"
)

func TestV2Prompts(t *testing.T) {
	ctx := context.Background()
	p, err := prompt.V2Prompts{}.ReviewV2Instructions(ctx, agent.BackendClaude)
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != prompt.V2Version || p.Shared != readFile(t, "review-v2-shared.md") {
		t.Errorf("version %q or shared text", p.Version)
	}
	for name, text := range map[string]string{"map": p.Map, "section": p.Section, "context": p.Context, "tile": p.Tile, "overview": p.Overview} {
		if strings.Count(text, "## Submitting\n") != 1 || !strings.Contains(text, "StructuredOutput") {
			t.Errorf("%s does not end with one submit block", name)
		}
		if !strings.Contains(text, "## What you receive") {
			t.Errorf("%s does not say what it receives", name)
		}
	}
	if !strings.Contains(p.Map, "Submit the map by calling") || !strings.Contains(p.Overview, "Submit the overview by calling") {
		t.Error("submit blocks do not name the part")
	}
	for _, kind := range view.NativeTypes {
		if !strings.HasPrefix(p.Natives[kind], "## The type: "+kind) {
			t.Errorf("no part for %s", kind)
		}
	}
	// The copy rules are in the shared text, verbatim.
	if !strings.Contains(p.Shared, `never the word "evidence"`) || !strings.Contains(p.Shared, "no bare\n  PR numbers") {
		t.Error("the shared text lacks the copy rules")
	}
	codex, err := prompt.V2Prompts{}.ReviewV2Instructions(ctx, agent.BackendCodex)
	if err != nil || codex.Digest == p.Digest || !strings.Contains(codex.Map, "your final\nmessage is the map") {
		t.Errorf("codex: %v", err)
	}
	if _, err := (prompt.V2Prompts{}).ReviewV2Instructions(ctx, "other"); err == nil {
		t.Error("an unknown backend got prompts")
	}
	again, _ := prompt.V2Prompts{}.ReviewV2Instructions(ctx, agent.BackendClaude)
	if again.Digest != p.Digest || prompt.V2Digest(p) != p.Digest {
		t.Error("the digest is not stable")
	}
	custom, _ := prompt.V2Prompts{Overrides: prompt.Overrides{prompt.IDReviewMap: "Map it my way."}}.ReviewV2Instructions(ctx, agent.BackendClaude)
	if custom.Version != prompt.V2Version+prompt.CustomSuffix || !strings.HasPrefix(custom.Map, "Map it my way.\n## Submitting\n") || custom.Digest == p.Digest {
		t.Errorf("override: %q %q", custom.Version, custom.Map[:40])
	}
	for _, id := range prompt.V2ReviewIDs() {
		e, ok := prompt.Lookup(id)
		if !ok {
			t.Fatalf("%s is not in the catalogue", id)
		}
		if err := prompt.Validate(id, e.Default); err != nil {
			t.Errorf("%s: %v", id, err)
		}
		if missing := prompt.MissingSafetyRules(id, e.Default); len(missing) > 0 {
			t.Errorf("%s misses %v", id, missing)
		}
	}
}
