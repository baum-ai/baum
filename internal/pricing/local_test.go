package pricing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

func TestLocalEstimateRequiresKnownExplicitModel(t *testing.T) {
	for _, model := range []string{"", "not-a-model"} {
		if Embedded().Estimate(model, 1000, 3) != nil {
			t.Fatal("invented model price", model)
		}
	}
	if estimate := Embedded().Estimate("claude-sonnet-4-5", 1000, 3); estimate == nil || *estimate <= 0 {
		t.Fatal(estimate)
	}
}
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
	dir := t.TempDir()
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
	path := filepath.Join(dir, persistedName)
	raw, err := os.ReadFile(path)
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
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = loadPersisted(dir, public); err == nil || Version() != Embedded().Version() {
		t.Fatalf("tampered stored list installed: %s %v", Version(), err)
	}
	if err = loadPersisted(t.TempDir(), public); err != nil {
		t.Fatal("missing stored list is not an error", err)
	}
}
func TestOutcomeCostKeepsMissingAndAmbiguousUsageUnknown(t *testing.T) {
	list := Embedded()
	for _, usage := range []agent.Usage{{}, {Input: 10, Unreported: true}, {Input: 10}, {Input: 10, CacheCreate: 5, Speed: new("standard")}} {
		if list.OutcomeCost("claude-sonnet-4-5", agent.Outcome{Usage: usage}) != nil {
			t.Fatal("invented cost", usage)
		}
	}
	usage := agent.Usage{Input: 100, Output: 20, CacheRead: 50, Speed: new("standard"), MinPrompt: new(int64(150)), MaxPrompt: new(int64(150))}
	cost := list.OutcomeCost("claude-sonnet-4-5", agent.Outcome{Usage: usage})
	want, ok := list.Cost(list.Normalize("claude-sonnet-4-5", ""), Usage{Vendor: VendorClaudeCode, InputTokens: 100, OutputTokens: 20, CacheReadTokens: 50, Speed: SpeedStandard, PromptRangeKnown: true, MinPromptTokens: 150, MaxPromptTokens: 150})
	if !ok || cost == nil || *cost != want {
		t.Fatal("disjoint usage not preserved", cost, want)
	}
}
