package pricing

import (
	"math"
	"testing"
)

// The transposition case the whole decomposition exists for: Cursor writes
// `claude-4.5-sonnet`, Anthropic writes `claude-sonnet-4-5`. Family and size
// are swapped, so prefix matching resolves one and misses the other — and the
// miss is silent, costing zero.
func TestNormalizeAcceptsEitherFieldOrdering(t *testing.T) {
	for _, raw := range []string{
		"claude-sonnet-4-5",
		"claude-4.5-sonnet",
		"claude-4-5-sonnet",
		"anthropic/claude-sonnet-4-5",
		"us.anthropic.claude-sonnet-4-5-v1:0",
		"claude-sonnet-4-5-20250929",
		"claude-sonnet-4-5-latest",
		"  CLAUDE-Sonnet-4-5  ",
	} {
		if got := Normalize(raw, "").Canonical; got != "claude-sonnet-4-5" {
			t.Errorf("Normalize(%q) = %q, want claude-sonnet-4-5", raw, got)
		}
	}
}

func TestNormalizeStripsDeploymentSuffixes(t *testing.T) {
	tests := map[string]string{
		"claude-opus-4-6[1m]":     "claude-opus-4-6",
		"claude-opus-4-6-1m":      "claude-opus-4-6",
		"claude-opus-4-8-fast":    "claude-opus-4-8",
		"claude-fable-5":          "claude-fable-5",
		"anthropic.claude-opus-5": "claude-opus-5",
		"openai/gpt-5-codex":      "gpt-5-codex",
		"gpt-5.5":                 "gpt-5-5",
	}
	for raw, want := range tests {
		if got := Normalize(raw, "").Canonical; got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Reasoning effort is recorded on its own field of a model invocation and does
// not change the token rate, so an effort-suffixed id is the same model.
func TestNormalizeStripsReasoningEffort(t *testing.T) {
	tests := map[string]string{
		"gpt-5.6-sol-medium":  "gpt-5-6-sol",
		"gpt-5.6-sol-high":    "gpt-5-6-sol",
		"gpt-5.6-sol-low":     "gpt-5-6-sol",
		"gpt-5.6-sol-minimal": "gpt-5-6-sol",
		"claude-opus-5-high":  "claude-opus-5",
	}
	for raw, want := range tests {
		if got := Normalize(raw, "").Canonical; got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

// The GPT ids Codex and Cursor actually write. Each one has to resolve *and*
// carry a rate: an unpriced GPT model is the whole Codex spend missing from the
// dashboard, reported as a plausible smaller number rather than as an error.
func TestGPTFamilyIsPriced(t *testing.T) {
	tests := map[string]Rate{
		"gpt-5.6-sol":        {Input: 4, Output: 20},
		"gpt-5.6-sol-medium": {Input: 4, Output: 20},
		"gpt-5.6-terra":      {Input: 2, Output: 12},
		"gpt-5.6-luna":       {Input: 0.2, Output: 1.2},
		"gpt-5.5":            {Input: 5, Output: 30},
		"gpt-5.4":            {Input: 2.5, Output: 15},
		"gpt-5.4-mini":       {Input: 0.75, Output: 4.5},
		"gpt-6-sol":          {Input: 2, Output: 10},
	}
	for raw, want := range tests {
		model := Normalize(raw, "")
		if !model.Known() {
			t.Errorf("Normalize(%q) did not resolve", raw)
			continue
		}
		if model.Family != FamilyGPT {
			t.Errorf("Normalize(%q).Family = %q, want gpt", raw, model.Family)
		}
		rate, priced := Rates(model)
		if !priced {
			t.Errorf("%q resolves to %q with no rate", raw, model.Canonical)
			continue
		}
		if rate.Input != want.Input || rate.Output != want.Output {
			t.Errorf("Rates(%q) = %+v, want input %v output %v", raw, rate, want.Input, want.Output)
		}
		// Cached input is 10% of input across these tiers.
		if math.Abs(rate.CacheRead-want.Input*0.1) > 1e-9 {
			t.Errorf("Rates(%q).CacheRead = %v, want %v", raw, rate.CacheRead, want.Input*0.1)
		}
	}
}

// Cursor serves other vendors' models under its own name and adds both a
// serving mode and an effort to the id, so all three have to come off before
// the model is recognisable.
func TestNormalizeStripsCursorRoute(t *testing.T) {
	tests := map[string]string{
		"cursor-grok-4.5-high-fast": "grok-4-5",
		"grok-4.5":                  "grok-4-5",
		"cursor-claude-4.5-sonnet":  "claude-sonnet-4-5",
	}
	for raw, want := range tests {
		if got := Normalize(raw, "").Canonical; got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
	model := Normalize("cursor-grok-4.5-high-fast", "")
	if !model.Known() {
		t.Fatal("cursor-grok-4.5-high-fast must resolve")
	}
	if model.Family != FamilyGrok {
		t.Errorf("family = %q, want grok", model.Family)
	}
}

func TestCursorSelectableBaseModelsArePriced(t *testing.T) {
	tests := map[string]Rate{
		"gpt-5.3-codex":    {Input: 1.75, CacheRead: 0.175, Output: 14},
		"gpt-5.2":          {Input: 1.75, CacheRead: 0.175, Output: 14},
		"gpt-5.4-nano":     {Input: 0.2, CacheRead: 0.02, Output: 1.25},
		"gpt-5.1":          {Input: 1.25, CacheRead: 0.125, Output: 10},
		"cursor-grok-4.5":  {Input: 2, CacheRead: 0.5, Output: 6},
		"cursor-grok-4.6":  {Input: 2, CacheRead: 0.5, Output: 6},
		"composer-2.5":     {Input: 0.5, CacheRead: 0.2, Output: 2.5},
		"gemini-3.6-flash": {Input: 1.5, CacheRead: 0.15, Output: 7.5},
		"gemini-3.1-pro":   {Input: 2, CacheRead: 0.2, Output: 12},
		"gemini-3.5-flash": {Input: 1.5, CacheRead: 0.15, Output: 9},
		"gemini-3-flash":   {Input: 0.5, CacheRead: 0.05, Output: 3},
		"kimi-k3":          {Input: 3, CacheRead: 0.3, Output: 15},
		"kimi-k2.7-code":   {Input: 0.95, CacheRead: 0.19, Output: 4},
		"glm-5.2":          {Input: 1.4, CacheRead: 0.26, Output: 4.4},
	}
	for raw, want := range tests {
		model := Normalize(raw, "")
		rate, ok := Rates(model)
		if !model.Known() || !ok || rate != want {
			t.Errorf("Rates(Normalize(%q)) = %+v, %v; want %+v, true", raw, rate, ok, want)
		}
	}
}

// Fable 5.1 and Opus 5.5 read cache below the usual 0.1× of input.
func TestClaudeModelsWithReducedCacheReadsKeepTheirRates(t *testing.T) {
	tests := map[string]Rate{
		"claude-fable-5-1":    {Input: 10, CacheRead: 0.25, CacheWrite: 12.5, Output: 50},
		"claude-opus-5-5":     {Input: 4, CacheRead: 0.2, CacheWrite: 5, Output: 20},
		"claude-opus-5-5[1m]": {Input: 4, CacheRead: 0.2, CacheWrite: 5, Output: 20},
	}
	for raw, want := range tests {
		rate, ok := Rates(Normalize(raw, "claude_code"))
		if !ok || rate != want {
			t.Errorf("Rates(Normalize(%q)) = %+v, %v; want %+v, true", raw, rate, ok, want)
		}
	}
}

func TestDeepSeekFlashIsPricedAtThePeakRate(t *testing.T) {
	want := Rate{Input: 0.3, CacheRead: 0.006, Output: 1.2}
	for _, raw := range []string{"deepseek-flash", "DeepSeek-V4.1-Flash"} {
		model := Normalize(raw, "")
		rate, ok := Rates(model)
		if model.Canonical != "deepseek-flash" || !ok || rate != want {
			t.Errorf("Rates(Normalize(%q)) = %+v, %v (canonical %q); want %+v, true", raw, rate, ok, model.Canonical, want)
		}
	}
}

// An unknown model must never fail a request — it costs nothing and keeps its
// raw name so it can be surfaced by name rather than as a silent zero.
func TestUnknownModelsAreCarriedNotDropped(t *testing.T) {
	model := Normalize("some-model-nobody-has-heard-of", "")
	if model.Known() {
		t.Fatalf("Normalize() resolved an unknown model to %q", model.Canonical)
	}
	if model.Raw != "some-model-nobody-has-heard-of" {
		t.Fatalf("Raw = %q, want the original string", model.Raw)
	}
	if _, ok := Cost(model, Usage{InputTokens: 1_000_000}); ok {
		t.Fatal("Cost() priced an unknown model")
	}

	empty := Normalize("", "")
	if empty.Known() {
		t.Fatal("the empty model string resolved to something")
	}
}

// The second finding from the plan, in the one place it can be tested in
// isolation: identical raw columns must produce different uncached input
// depending on the vendor.
func TestCodexInputSubsumesItsCacheReads(t *testing.T) {
	raw := Usage{InputTokens: 1000, CacheReadTokens: 800, OutputTokens: 200}

	codex := raw
	codex.Vendor = "codex"
	claude := raw
	claude.Vendor = "claude_code"

	if got := codex.UncachedInput(); got != 200 {
		t.Fatalf("codex UncachedInput() = %d, want 200 — its input_tokens subsumes cache reads", got)
	}
	if got := claude.UncachedInput(); got != 1000 {
		t.Fatalf("claude_code UncachedInput() = %d, want 1000 — its buckets are disjoint", got)
	}

	// A negative result would mean the vendor reported more cache reads than
	// input; clamping keeps that from turning into a negative cost.
	odd := Usage{Vendor: "codex", InputTokens: 100, CacheReadTokens: 900}
	if got := odd.UncachedInput(); got != 0 {
		t.Fatalf("UncachedInput() = %d, want 0 rather than a negative token count", got)
	}
}

// The model has no long-prompt tier, so summed usage of any size prices at its
// base rates.
func TestCostUsesEveryTokenBucket(t *testing.T) {
	model := Normalize("claude-sonnet-4-6", "")
	rate, ok := Rates(model)
	if !ok {
		t.Fatal("claude-sonnet-4-6 is not priced")
	}
	if rate.CacheRead == 0 || rate.CacheWrite == 0 {
		t.Fatalf("claude-sonnet-4-6 has no cache rates: %+v", rate)
	}

	cost, ok := Cost(model, Usage{
		Vendor:           "claude_code",
		InputTokens:      1_000_000,
		OutputTokens:     1_000_000,
		CacheReadTokens:  1_000_000,
		CacheWriteTokens: 1_000_000,
	})
	if !ok {
		t.Fatal("Cost() reported no price for a priced model")
	}
	want := rate.Input + rate.Output + rate.CacheRead + rate.CacheWrite
	if cost != want {
		t.Fatalf("Cost() = %v, want %v", cost, want)
	}
}

// Platform fixture traversal remains tested in its owning module.

// Model names observed in the live dev database. They are not in any adapter
// fixture, so TestEveryFixtureModelIsAccountedFor cannot catch them — and an
// unresolved name shows up on the dashboard as something for a human to chase.
func TestProductionObservedModelsResolve(t *testing.T) {
	for _, raw := range []string{
		"claude-fable-5", "claude-fable-5-1", "claude-opus-5", "claude-opus-5-5", "claude-sonnet-5",
		"gpt-5.6-sol", "gpt-5.5", "gpt-6-sol", "codex-auto-review", "<synthetic>",
	} {
		model := Normalize(raw, "")
		if !model.Known() {
			t.Errorf("%q does not resolve; it will surface as an unknown model name", raw)
			continue
		}
		if _, priced := Rates(model); priced {
			continue
		}
		if UnpricedReason(model) == "" {
			t.Errorf("%q resolves to %q with neither a rate nor a stated reason", raw, model.Canonical)
		}
	}
}

// Every alias and every unpriced entry must be reachable — a stale key is a
// rule nobody is applying and a maintenance trap.
func TestTableKeysAreConsistent(t *testing.T) {
	for raw, canonical := range aliases {
		if _, priced := Embedded().rate(canonical); priced {
			continue
		}
		if _, unpriced := knownUnpriced[canonical]; unpriced {
			continue
		}
		t.Errorf("alias %q points at %q, which is neither priced nor listed as unpriced", raw, canonical)
	}
	for canonical, reason := range knownUnpriced {
		if _, priced := Embedded().rate(canonical); priced {
			t.Errorf("%q is both priced and listed as unpriced", canonical)
		}
		if reason == "" {
			t.Errorf("%q is unpriced with no reason given", canonical)
		}
	}
}

// A price list with no version cannot be reproduced later.
func TestPriceListVersionIsSet(t *testing.T) {
	if !versionPattern.MatchString(Version()) {
		t.Fatalf("Version() = %q, want YYYY-MM-DD or YYYY-MM-DD.N", Version())
	}
}
