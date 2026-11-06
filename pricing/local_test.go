package pricing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/appdb"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
)

func TestRefreshedListSurvivesRestartOnlyWhileItsSignatureVerifies(t *testing.T) {
	t.Cleanup(func() { Install(Embedded()) })
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	document := testDocument(t)
	document.Version = "2999-01-01"
	data, signature := sign(t, document, private)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sig") {
			_, _ = w.Write(signature)
			return
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	dir := appdbtest.TempDir(t)
	if err = refresh(context.Background(), server.Client(), server.URL+"/prices.json", public, dir); err != nil {
		t.Fatal(err)
	}
	if Version() != "2999-01-01" {
		t.Fatalf("refresh installed %s", Version())
	}

	// A restart starts from the compiled list and reinstalls the stored one.
	Install(Embedded())
	if err = loadPersisted(dir, public); err != nil || Version() != "2999-01-01" {
		t.Fatalf("stored list not reinstalled: %s %v", Version(), err)
	}

	Install(Embedded())
	other, _, _ := ed25519.GenerateKey(nil)
	if err = loadPersisted(dir, other); err == nil || Version() != Embedded().Version() {
		t.Fatalf("list trusted without a valid signature: %s %v", Version(), err)
	}
	path := filepath.Join(dir, "app.sqlite")
	raw, err := appdb.Read(path, "cache", "pricing")
	if err != nil {
		t.Fatal(err)
	}
	var stored persisted
	if err = json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	stored.List = bytes.Replace(stored.List, []byte(`"2999-01-01"`), []byte(`"2999-01-02"`), 1)
	if raw, err = json.Marshal(stored); err != nil {
		t.Fatal(err)
	}
	if err = appdb.Write(path, "cache", "pricing", raw); err != nil {
		t.Fatal(err)
	}
	if err = loadPersisted(dir, public); err == nil || Version() != Embedded().Version() {
		t.Fatalf("tampered stored list installed: %s %v", Version(), err)
	}
	if err = loadPersisted(appdbtest.TempDir(t), public); err != nil {
		t.Fatal("missing stored list is not an error", err)
	}
}
func TestOutcomeCostEstimatesIncompleteUsage(t *testing.T) {
	list := Embedded()
	if list.OutcomeCost("claude-sonnet-4-5", agent.Outcome{}) != nil || list.OutcomeCost("no-such-model", agent.Outcome{Usage: agent.Usage{Input: 10}}) != nil {
		t.Fatal("invented a cost for no tokens or an unpriced model")
	}
	usage := agent.Usage{Input: 100, Output: 20, CacheRead: 50, Speed: new("standard"), MinPrompt: new(int64(150)), MaxPrompt: new(int64(150))}
	cost := list.OutcomeCost("claude-sonnet-4-5", agent.Outcome{Usage: usage})
	want, ok := list.Cost(list.Normalize("claude-sonnet-4-5", ""), Usage{Vendor: VendorClaudeCode, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 50, Speed: SpeedStandard, PromptRangeKnown: true, MinPromptTokens: 150, MaxPromptTokens: 150})
	if !ok || cost == nil || *cost != want {
		t.Fatal("disjoint usage not preserved", cost, want)
	}
	// Missing speed, TTL and prompt range take the lowest rates.
	usage = agent.Usage{Input: 100, Output: 20, CacheRead: 50, Unreported: true}
	if cost := list.OutcomeCost("claude-sonnet-4-5", agent.Outcome{Usage: usage}); cost == nil || *cost != want {
		t.Fatal("incomplete usage not estimated at the base rates", cost, want)
	}
}

// The usage of a Claude Code run that hit the output cap four times and was
// then cut off by its wall clock: complete calls over the long-prompt
// threshold, the zero-token <synthetic> message Claude Code writes for the
// API error, and a call cut off mid-stream.
func TestAccountingEstimatesACutOffRun(t *testing.T) {
	list := Embedded()
	complete := func(cacheRead, cacheWrite int64) agent.UsageAttempt {
		prompt := 2 + cacheRead + cacheWrite
		return agent.UsageAttempt{ModelSource: "provider_reported", Model: "claude-haiku-5-5", Calls: 1, Usage: agent.Usage{
			Input: 2, Output: 32000, CacheRead: cacheRead, CacheCreate: cacheWrite, CacheCreate1h: new(cacheWrite), Speed: new("standard"), MinPrompt: new(prompt), MaxPrompt: new(prompt)}}
	}
	a := &agent.Accounting{Attempts: []agent.UsageAttempt{complete(0, 111639), complete(17269, 94432)}}
	exact, reason := list.AccountingCost(a)
	if exact == nil || reason != "" {
		t.Fatal("complete calls not priced exactly", exact, reason)
	}
	a.Attempts = append(a.Attempts, agent.UsageAttempt{ModelSource: "provider_reported", Model: "<synthetic>", Calls: 1, Usage: agent.Usage{CacheCreate1h: new(int64(0))}})
	if cost, reason := list.AccountingCost(a); cost == nil || *cost != *exact || reason != "" {
		t.Fatal("a zero-token synthetic message changed the cost", cost, reason)
	}
	cutOff := agent.UsageAttempt{ModelSource: "provider_reported", Model: "claude-haiku-5-5", Calls: 1, Failed: true, Usage: agent.Usage{
		Input: 2, Output: 2, CacheRead: 111639, CacheCreate1h: new(int64(0)), Speed: new("standard"), MinPrompt: new(int64(111641)), MaxPrompt: new(int64(111641)), Unreported: true}}
	a.Attempts = append(a.Attempts, cutOff)
	a.Usage.Unreported = true
	cost, reason := list.AccountingCost(a)
	if cost == nil || *cost <= *exact || reason != ReasonUsageIncomplete {
		t.Fatal("a cut-off call should leave a lower bound including what it reported", cost, *exact, reason)
	}
}

// A run whose calls straddle a long-prompt threshold is priced at the base
// tier, the lower of the two.
func TestAccountingEstimatesAnAmbiguousPromptTier(t *testing.T) {
	list := Embedded()
	usage := agent.Usage{Input: 1000, Output: 100, Speed: new("standard"), MinPrompt: new(int64(1000)), MaxPrompt: new(int64(200000))}
	cost, reason := list.AccountingCost(&agent.Accounting{Usage: usage, Attempts: []agent.UsageAttempt{{Model: "claude-haiku-5-5", Usage: usage}}})
	base, _ := list.Cost(list.Normalize("claude-haiku-5-5", ""), Usage{Vendor: VendorClaudeCode, InputTokens: 1000, OutputTokens: 100, Speed: SpeedStandard, PromptRangeKnown: true})
	if cost == nil || *cost != base || reason != ReasonRatesUnknown {
		t.Fatal(cost, base, reason)
	}
	if cost, reason := list.AccountingCost(&agent.Accounting{Attempts: []agent.UsageAttempt{{Model: "no-such-model", Usage: usage}}}); cost != nil || reason != ReasonRatesUnknown {
		t.Fatal("an unpriced model was priced", cost, reason)
	}
	if cost, reason := list.AccountingCost(&agent.Accounting{Attempts: []agent.UsageAttempt{{Model: "claude-haiku-5-5"}}}); cost != nil || reason != ReasonUsageNotReported {
		t.Fatal("no tokens were priced", cost, reason)
	}
}

func TestAccountingPricesModelsIndependentlyAndKeepsUnknownReason(t *testing.T) {
	list := Active()
	a := &agent.Accounting{}
	for _, model := range []string{"claude-sonnet-4-5", "gpt-5-mini"} {
		a.Add(agent.Result{Model: model, Calls: 1, Usage: agent.Usage{Input: 100, Output: 20, CacheRead: 900, Speed: new("standard"), MinPrompt: new(int64(1000)), MaxPrompt: new(int64(1000))}}, false)
	}
	cost, reason := list.AccountingCost(a)
	expected := 0.0
	for _, attempt := range a.Attempts {
		c := list.OutcomeCost(attempt.Model, agent.Outcome{Usage: attempt.Usage})
		if c == nil {
			t.Fatal("fixture model unpriced", attempt.Model)
		}
		expected += *c
	}
	if cost == nil || *cost != expected || reason != "" {
		t.Fatal(cost, reason, expected)
	}
	a.Attempts[0].ModelSource = "configured"
	if cost, reason = list.AccountingCost(a); cost == nil || *cost != expected || reason != "estimated_from_configured_model" {
		t.Fatal("configured model should produce a labelled estimate", cost, reason)
	}
	// A missing speed is priced at standard, so the estimate is unchanged
	// but labelled; incomplete usage outranks it.
	a.Attempts[1].Usage.Speed = nil
	if cost, reason = list.AccountingCost(a); cost == nil || *cost != expected || reason != ReasonSpeedNotReported {
		t.Fatal(cost, reason)
	}
	a.Usage.Unreported = true
	if cost, reason = list.AccountingCost(a); cost == nil || *cost != expected || reason != ReasonUsageIncomplete {
		t.Fatal(cost, reason)
	}
}
