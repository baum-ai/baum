package pricing

import (
	"math"
	"strings"
	"testing"
)

// Claude Code writes 1-hour cache writes on almost every call, and an entry
// with cache writes but no 1-hour rate leaves each of those calls unpriced. A
// Claude model synced without its 1-hour rates would therefore silently unprice
// whole Claude Code sessions, so the compiled list must never carry one.
func TestEveryClaudeEntryWithCacheWritesHasOneHourRates(t *testing.T) {
	for canonical, entry := range Embedded().entries {
		if !strings.HasPrefix(canonical, "claude-") || entry.CacheWrite <= 0 {
			continue
		}
		if entry.CacheWrite1h <= 0 {
			t.Errorf("%s has cache_write %v but no cache_write_1h", canonical, entry.CacheWrite)
		}
		if !entry.Above.IsZero() && entry.Above.CacheWrite1h <= 0 {
			t.Errorf("%s: above has no cache_write_1h", canonical)
		}
	}
}

// fixtureList is built here, not from prices.json, so the expectations do not
// move when a rate is corrected.
func fixtureList() *List {
	return NewList(Document{Version: "2026-09-28", Models: map[string]Entry{
		"claude-tiered": {
			Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25, CacheWrite1h: 2,
			Above:  Tier{PromptTokens: 100_000, Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5, CacheWrite1h: 4},
			Source: SourceManual,
		},
		"claude-fast": {
			Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25, CacheWrite1h: 10,
			Fast:   RateSet{Input: 30, Output: 150, CacheRead: 3, CacheWrite: 37.5, CacheWrite1h: 60},
			Source: SourceManual,
		},
		"gpt-fast": {
			Input: 2.5, Output: 15, CacheRead: 0.25, CacheWrite1h: 0,
			Above:     Tier{PromptTokens: 272_000, Input: 5, Output: 22.5, CacheRead: 0.5},
			Fast:      RateSet{Input: 5, Output: 30, CacheRead: 0.5},
			FastAbove: Tier{PromptTokens: 272_000, Input: 10, Output: 45, CacheRead: 1},
			Source:    SourceManual,
		},
		"claude-fast-tiered": {
			Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25, CacheWrite1h: 2,
			Above:     Tier{PromptTokens: 100_000, Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5, CacheWrite1h: 4},
			Fast:      RateSet{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75, CacheWrite1h: 6},
			FastAbove: Tier{PromptTokens: 100_000, Input: 6, Output: 30, CacheRead: 0.6, CacheWrite: 7.5, CacheWrite1h: 12},
			Source:    SourceManual,
		},
		"gpt-fast-no-tier": {
			Input: 2.5, Output: 15, CacheRead: 0.25,
			Above:  Tier{PromptTokens: 272_000, Input: 5, Output: 22.5, CacheRead: 0.5},
			Fast:   RateSet{Input: 5, Output: 30, CacheRead: 0.5},
			Source: SourceManual,
		},
		"claude-plain": {Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75, Source: SourceManual},
		"gpt-tiered":   {Input: 2.5, Output: 15, CacheRead: 0.25, Above: Tier{PromptTokens: 272_000, Input: 5, Output: 22.5, CacheRead: 0.5}, Source: SourceManual},
	}})
}

func TestListCostPicksTheRateSet(t *testing.T) {
	list := fixtureList()
	const m = 1_000_000
	tests := []struct {
		name   string
		model  string
		usage  Usage
		want   float64
		priced bool
	}{
		{"prompt exactly at the threshold is base", "claude-tiered",
			Usage{Vendor: VendorClaudeCode, InputTokens: 100_000, OutputTokens: m, SingleRequest: true}, 0.1 + 5, true},
		{"one token over, single request, is above", "claude-tiered",
			Usage{Vendor: VendorClaudeCode, InputTokens: 100_001, OutputTokens: m, SingleRequest: true}, 100_001*2.0/m + 10, true},
		{"over without SingleRequest is not priced", "claude-tiered",
			Usage{Vendor: VendorClaudeCode, InputTokens: 100_001, OutputTokens: m}, 0, false},
		{"multi-request at the threshold is base", "claude-tiered",
			Usage{Vendor: VendorClaudeCode, InputTokens: 60_000, CacheReadTokens: 40_000, OutputTokens: m}, 0.06 + 0.004 + 5, true},
		{"multi-request under the threshold is base", "claude-tiered",
			Usage{Vendor: VendorClaudeCode, InputTokens: 10, OutputTokens: m}, 5 + 10.0/m, true},
		{"claude prompt sums the three buckets", "claude-tiered",
			Usage{Vendor: VendorClaudeCode, InputTokens: 30_000, CacheReadTokens: 40_000, CacheWriteTokens: 30_001, SingleRequest: true},
			30_000*2.0/m + 40_000*0.2/m + 30_001*2.5/m, true},
		{"claude buckets summing to the threshold stay base", "claude-tiered",
			Usage{Vendor: VendorClaudeCode, InputTokens: 30_000, CacheReadTokens: 40_000, CacheWriteTokens: 30_000, SingleRequest: true},
			30_000*1.0/m + 40_000*0.1/m + 30_000*1.25/m, true},
		{"codex prompt is raw input, cached included", "gpt-tiered",
			Usage{Vendor: VendorCodex, InputTokens: 272_001, CacheReadTokens: 90_000, SingleRequest: true},
			(272_001-90_000)*5.0/m + 90_000*0.5/m, true},
		{"codex cached input is not added to its prompt again", "gpt-tiered",
			Usage{Vendor: VendorCodex, InputTokens: 200_000, CacheReadTokens: 90_000, SingleRequest: true},
			(200_000-90_000)*2.5/m + 90_000*0.25/m, true},
		{"fast uses fast rates on every bucket", "claude-fast",
			Usage{Vendor: VendorClaudeCode, InputTokens: m, OutputTokens: m, CacheReadTokens: m, Speed: SpeedFast}, 30 + 150 + 3, true},
		{"fast on an entry without fast is not priced", "claude-plain",
			Usage{Vendor: VendorClaudeCode, InputTokens: m, Speed: SpeedFast}, 0, false},
		{"1h and 5m writes price separately", "claude-fast",
			Usage{Vendor: VendorClaudeCode, CacheWriteTokens: 3 * m / 10, CacheWrite1hTokens: m / 10}, 0.2*6.25 + 0.1*10, true},
		{"1h writes in fast mode use the fast 1h rate", "claude-fast",
			Usage{Vendor: VendorClaudeCode, CacheWriteTokens: m, CacheWrite1hTokens: m, Speed: SpeedFast}, 60, true},
		{"1h tokens where the rate set has no 1h rate are not priced", "claude-plain",
			Usage{Vendor: VendorClaudeCode, CacheWriteTokens: m, CacheWrite1hTokens: 1}, 0, false},
		{"unreported 1h split prices writes at the 5m rate", "claude-fast",
			Usage{Vendor: VendorClaudeCode, CacheWriteTokens: m}, 6.25, true},
		{"1h tokens beyond total writes are not priced", "claude-fast",
			Usage{Vendor: VendorClaudeCode, CacheWriteTokens: m, CacheWrite1hTokens: 5 * m}, 0, false},
		{"negative 1h tokens are not priced", "claude-fast",
			Usage{Vendor: VendorClaudeCode, CacheWriteTokens: m, CacheWrite1hTokens: -1}, 0, false},
		{"5m and 1h writes in fast mode", "claude-fast",
			Usage{Vendor: VendorClaudeCode, CacheWriteTokens: m, CacheWrite1hTokens: m / 4, Speed: SpeedFast}, 0.75*37.5 + 0.25*60, true},
		{"5m writes in fast mode", "claude-fast",
			Usage{Vendor: VendorClaudeCode, CacheWriteTokens: m, Speed: SpeedFast}, 37.5, true},
		{"1h writes on the above tier", "claude-tiered",
			Usage{Vendor: VendorClaudeCode, CacheWriteTokens: m, CacheWrite1hTokens: m / 2, SingleRequest: true}, 0.5*2.5 + 0.5*4, true},
		{"fast at the threshold uses fast", "gpt-fast",
			Usage{Vendor: VendorCodex, InputTokens: 272_000, OutputTokens: m, Speed: SpeedFast, SingleRequest: true}, 272_000*5.0/m + 30, true},
		{"fast one over, single request, uses fast_above", "gpt-fast",
			Usage{Vendor: VendorCodex, InputTokens: 272_001, CacheReadTokens: 100_000, OutputTokens: m, Speed: SpeedFast, SingleRequest: true},
			(172_001)*10.0/m + 100_000*1.0/m + 45, true},
		{"fast one over without SingleRequest is not priced", "gpt-fast",
			Usage{Vendor: VendorCodex, InputTokens: 272_001, OutputTokens: m, Speed: SpeedFast}, 0, false},
		{"fast multi-request at the threshold is exact", "gpt-fast",
			Usage{Vendor: VendorCodex, InputTokens: 272_000, OutputTokens: m, Speed: SpeedFast}, 272_000*5.0/m + 30, true},
		{"standard call on a fast_above entry ignores it", "gpt-fast",
			Usage{Vendor: VendorCodex, InputTokens: 272_001, OutputTokens: m, SingleRequest: true}, 272_001*5.0/m + 22.5, true},
		{"1h writes at fast", "claude-fast-tiered",
			Usage{Vendor: VendorClaudeCode, CacheWriteTokens: 50_000, CacheWrite1hTokens: 25_000, Speed: SpeedFast, SingleRequest: true}, 25_000*3.75/m + 25_000*6.0/m, true},
		{"1h writes on fast_above", "claude-fast-tiered",
			Usage{Vendor: VendorClaudeCode, CacheWriteTokens: m, CacheWrite1hTokens: m / 2, Speed: SpeedFast, SingleRequest: true}, 0.5*7.5 + 0.5*12, true},
		{"fast at the threshold without fast_above uses fast", "gpt-fast-no-tier",
			Usage{Vendor: VendorCodex, InputTokens: 272_000, OutputTokens: m, Speed: SpeedFast, SingleRequest: true}, 272_000*5.0/m + 30, true},
		{"fast multi-request under the threshold without fast_above is exact", "gpt-fast-no-tier",
			Usage{Vendor: VendorCodex, InputTokens: 100_000, OutputTokens: m, Speed: SpeedFast}, 100_000*5.0/m + 30, true},
		{"fast over the threshold without fast_above is not priced", "gpt-fast-no-tier",
			Usage{Vendor: VendorCodex, InputTokens: 272_001, OutputTokens: m, Speed: SpeedFast, SingleRequest: true}, 0, false},
		{"fast over the threshold without fast_above and SingleRequest is not priced", "gpt-fast-no-tier",
			Usage{Vendor: VendorCodex, InputTokens: 272_001, OutputTokens: m, Speed: SpeedFast}, 0, false},
		{"ultrafast is not priced", "claude-plain",
			Usage{Vendor: VendorClaudeCode, InputTokens: m, Speed: SpeedUltrafast}, 0, false},
		{"standard speed uses base rates", "claude-fast",
			Usage{Vendor: VendorClaudeCode, InputTokens: m, Speed: SpeedStandard}, 5, true},
		{"unknown speed uses base rates", "claude-fast",
			Usage{Vendor: VendorClaudeCode, InputTokens: m}, 5, true},
		{"entry without a tier ignores SingleRequest", "claude-plain",
			Usage{Vendor: VendorClaudeCode, InputTokens: 500_000, SingleRequest: true}, 1.5, true},
		{"range at or under the threshold is base", "claude-tiered",
			Usage{Vendor: VendorClaudeCode, InputTokens: 150_000, OutputTokens: m, PromptRangeKnown: true, MinPromptTokens: 10, MaxPromptTokens: 100_000}, 150_000*1.0/m + 5, true},
		{"range over the threshold is above", "claude-tiered",
			Usage{Vendor: VendorClaudeCode, InputTokens: 300_000, OutputTokens: m, PromptRangeKnown: true, MinPromptTokens: 100_001, MaxPromptTokens: 150_000}, 300_000*2.0/m + 10, true},
		{"range straddling the threshold is not priced", "claude-tiered",
			Usage{Vendor: VendorClaudeCode, InputTokens: 200_000, PromptRangeKnown: true, MinPromptTokens: 100_000, MaxPromptTokens: 100_001}, 0, false},
		{"inverted range is not priced", "claude-tiered",
			Usage{Vendor: VendorClaudeCode, InputTokens: 10, PromptRangeKnown: true, MinPromptTokens: 5, MaxPromptTokens: 4}, 0, false},
		{"range with a zero minimum over the threshold is not priced", "claude-tiered",
			Usage{Vendor: VendorClaudeCode, InputTokens: 200_000, PromptRangeKnown: true, MinPromptTokens: 0, MaxPromptTokens: 200_000}, 0, false},
		{"fast range over the threshold uses fast_above", "gpt-fast",
			Usage{Vendor: VendorCodex, InputTokens: 600_000, Speed: SpeedFast, PromptRangeKnown: true, MinPromptTokens: 272_001, MaxPromptTokens: 300_000}, 600_000 * 10.0 / m, true},
		{"fast range at the threshold uses fast", "gpt-fast",
			Usage{Vendor: VendorCodex, InputTokens: 500_000, Speed: SpeedFast, PromptRangeKnown: true, MinPromptTokens: 1, MaxPromptTokens: 272_000}, 500_000 * 5.0 / m, true},
		{"fast range straddling the threshold is not priced", "gpt-fast",
			Usage{Vendor: VendorCodex, InputTokens: 500_000, Speed: SpeedFast, PromptRangeKnown: true, MinPromptTokens: 272_000, MaxPromptTokens: 272_001}, 0, false},
		{"fast range over the threshold without fast_above is not priced", "gpt-fast-no-tier",
			Usage{Vendor: VendorCodex, InputTokens: 500_000, Speed: SpeedFast, PromptRangeKnown: true, MinPromptTokens: 272_001, MaxPromptTokens: 300_000}, 0, false},
		{"entry missing from the list is not priced", "claude-missing",
			Usage{Vendor: VendorClaudeCode, InputTokens: m}, 0, false},
	}
	for _, tt := range tests {
		got, ok := list.Cost(Model{Canonical: tt.model}, tt.usage)
		if ok != tt.priced {
			t.Errorf("%s: priced = %v, want %v", tt.name, ok, tt.priced)
			continue
		}
		if ok && math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("%s: cost = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestUsagePromptTokens(t *testing.T) {
	if got := (Usage{Vendor: VendorCodex, InputTokens: 100, CacheReadTokens: 60}).PromptTokens(); got != 100 {
		t.Errorf("codex prompt = %d, want 100", got)
	}
	if got := (Usage{Vendor: VendorClaudeCode, InputTokens: 1, CacheReadTokens: 20, CacheWriteTokens: 300}).PromptTokens(); got != 321 {
		t.Errorf("claude prompt = %d, want 321", got)
	}
}

func TestListPromptBands(t *testing.T) {
	got := fixtureList().PromptBands()
	want := []int64{100_000, 272_000}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("PromptBands() = %v, want %v", got, want)
	}
	if bands := NewList(Document{Models: map[string]Entry{"flat": {Input: 1}}}).PromptBands(); bands == nil || len(bands) != 0 {
		t.Fatalf("flat list bands = %#v, want an empty non-nil slice", bands)
	}
}

// RatesFor is the selection Cost prices with.
func TestRatesForReturnsTheRateSetCostUses(t *testing.T) {
	list := Embedded()
	model := list.Normalize("claude-haiku-5-5", VendorClaudeCode)
	usage := func(prompt int64, single bool) Usage {
		return Usage{Vendor: VendorClaudeCode, InputTokens: prompt, SingleRequest: single}
	}
	entry, _ := list.entry(model.Canonical)
	if got, ok := list.RatesFor(model, usage(100_000, true)); !ok || got != entry.Base() {
		t.Errorf("at the threshold = %+v %v, want the base rates", got, ok)
	}
	if got, ok := list.RatesFor(model, usage(100_001, true)); !ok || got != entry.Above.RateSet() {
		t.Errorf("over the threshold = %+v %v, want the above rates", got, ok)
	}
	if _, ok := list.RatesFor(model, usage(100_001, false)); ok {
		t.Error("a total with no range straddles the threshold and must not be priced")
	}
	if _, ok := list.RatesFor(Model{}, usage(1, true)); ok {
		t.Error("an unknown model has no rates")
	}
}

// Codex's input_tokens includes both its cache reads and its cache writes, so
// neither is charged again at the input rate.
func TestCodexUncachedInputExcludesCacheReadsAndWrites(t *testing.T) {
	codex := Usage{Vendor: VendorCodex, InputTokens: 1_000, CacheReadTokens: 600, CacheWriteTokens: 100}
	if got := codex.UncachedInput(); got != 300 {
		t.Errorf("codex uncached input = %d, want 300", got)
	}
	if got := (Usage{Vendor: VendorCodex, InputTokens: 50, CacheReadTokens: 40, CacheWriteTokens: 40}).UncachedInput(); got != 0 {
		t.Errorf("codex uncached input = %d, want it floored at 0", got)
	}
	claude := Usage{Vendor: VendorClaudeCode, InputTokens: 1_000, CacheReadTokens: 600, CacheWriteTokens: 100}
	if got := claude.UncachedInput(); got != 1_000 {
		t.Errorf("claude uncached input = %d, want 1000", got)
	}
}
