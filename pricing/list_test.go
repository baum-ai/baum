package pricing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func testDocument(t *testing.T) Document {
	t.Helper()
	document, err := ParseDocument(EmbeddedDocument())
	if err != nil {
		t.Fatalf("compiled prices.json: %v", err)
	}
	return document
}

func sign(t *testing.T, document Document, key ed25519.PrivateKey) ([]byte, []byte) {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return data, []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, data)) + "\n")
}

func TestParseDocumentRejectsListsThatCouldNotHaveBeenMeant(t *testing.T) {
	valid := `{"version":"2026-09-28","models":{"m":{"input":1,"output":2,"cache_read":0.1,"cache_write":1.25,"source":"manual"}}}`
	if _, err := ParseDocument([]byte(valid)); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
	tests := map[string]string{
		"unknown field":     `{"version":"2026-09-28","models":{"m":{"input":1,"output":2,"cache_read":0,"cache_write":0,"source":"manual","batch":1}}}`,
		"bad version":       `{"version":"latest","models":{"m":{"input":1,"output":2,"cache_read":0,"cache_write":0,"source":"manual"}}}`,
		"dot-one version":   `{"version":"2026-09-28.1","models":{"m":{"input":1,"output":2,"cache_read":0,"cache_write":0,"source":"manual"}}}`,
		"no models":         `{"version":"2026-09-28","models":{}}`,
		"negative rate":     `{"version":"2026-09-28","models":{"m":{"input":-1,"output":2,"cache_read":0,"cache_write":0,"source":"manual"}}}`,
		"no input nor out":  `{"version":"2026-09-28","models":{"m":{"input":0,"output":0,"cache_read":1,"cache_write":0,"source":"manual"}}}`,
		"unknown source":    `{"version":"2026-09-28","models":{"m":{"input":1,"output":2,"cache_read":0,"cache_write":0,"source":"guess"}}}`,
		"non-canonical key": `{"version":"2026-09-28","models":{"Claude-Opus":{"input":1,"output":2,"cache_read":0,"cache_write":0,"source":"manual"}}}`,
	}
	for name, document := range tests {
		if _, err := ParseDocument([]byte(document)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCompareVersionsOrdersSameDayLists(t *testing.T) {
	ordered := []string{"2026-09-27", "2026-09-28", "2026-09-28.2", "2026-09-28.10", "2026-09-29"}
	for i := 1; i < len(ordered); i++ {
		if CompareVersions(ordered[i-1], ordered[i]) >= 0 || CompareVersions(ordered[i], ordered[i-1]) <= 0 {
			t.Errorf("%s does not sort before %s", ordered[i-1], ordered[i])
		}
	}
	if CompareVersions("2026-09-28", "2026-09-28.1") != 0 {
		t.Error("an unsuffixed version is the day's first list")
	}
}

func TestParsePublishedAcceptsOnlyASignedSupersetOfTheCompiledList(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	document := testDocument(t)
	document.Version = "2999-01-01"
	data, signature := sign(t, document, private)

	list, err := ParsePublished(data, signature, public)
	if err != nil {
		t.Fatalf("signed list rejected: %v", err)
	}
	if list.Version() != "2999-01-01" {
		t.Errorf("Version() = %q", list.Version())
	}

	tampered := []byte(strings.Replace(string(data), `"2999-01-01"`, `"2999-01-02"`, 1))
	if _, err := ParsePublished(tampered, signature, public); err == nil {
		t.Error("a list changed after signing was accepted")
	}
	otherKey, _, _ := ed25519.GenerateKey(nil)
	if _, err := ParsePublished(data, signature, otherKey); err == nil {
		t.Error("a list signed by another key was accepted")
	}

	delete(document.Models, "claude-opus-5")
	truncated, truncatedSignature := sign(t, document, private)
	if _, err := ParsePublished(truncated, truncatedSignature, public); err == nil {
		t.Error("a signed list dropping a compiled model was accepted")
	}
}

// The point of a published list: a model the rules already resolve, priced by
// data alone, without a release.
func TestInstallingAListPricesAModelWithoutARelease(t *testing.T) {
	t.Cleanup(func() { Install(Embedded()) })
	const raw = "gpt-9-nova"
	if _, ok := Rates(Normalize(raw, VendorCodex)); ok {
		t.Fatalf("%s is already priced; pick a name the compiled list lacks", raw)
	}

	document := testDocument(t)
	document.Version = "2999-01-01"
	document.Models[raw] = Entry{Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 1.25, Source: SourceLiteLLM}
	Install(Newest(Embedded(), NewList(document)))

	rate, ok := Rates(Normalize(raw, VendorCodex))
	if !ok || rate.Output != 2 {
		t.Fatalf("Rates(%q) = %+v, %v after install; want output 2", raw, rate, ok)
	}
	if Version() != "2999-01-01" {
		t.Errorf("Version() = %q after install", Version())
	}
}

func TestNewestPrefersTheLaterVersion(t *testing.T) {
	older := &List{version: "2026-01-01"}
	newer := &List{version: "2026-01-01.2"}
	if Newest(older, newer) != newer || Newest(newer, older) != newer {
		t.Error("Newest did not pick the later version")
	}
	if Newest(older, nil) != older {
		t.Error("Newest(list, nil) must keep the list")
	}
}

func TestPublishedKeyDecodes(t *testing.T) {
	if len(PublishedKey()) != ed25519.PublicKeySize {
		t.Fatal("published key has the wrong size")
	}
}

func TestParseDocumentReadsAndFormatsNewFields(t *testing.T) {
	const doc = `{"version":"2026-09-28","models":{"m":{"input":1,"output":2,"cache_read":0.1,"cache_write":1.25,"cache_write_1h":2,"above":{"prompt_tokens":100000,"input":2,"output":3,"cache_read":0.2,"cache_write":2.5,"cache_write_1h":4},"source":"manual"}}}`
	document, err := ParseDocument([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	entry := document.Models["m"]
	if entry.CacheWrite1h != 2 || entry.Above.PromptTokens != 100000 || entry.Above.Output != 3 || !entry.Fast.IsZero() {
		t.Fatalf("entry = %+v", entry)
	}
	out, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != doc {
		t.Fatalf("round trip = %s", out)
	}

	const fastDoc = `{"version":"2026-09-28","models":{"m":{"input":1,"output":2,"cache_read":0.1,"cache_write":1.25,"fast":{"input":2,"output":4,"cache_read":0.2,"cache_write":2.5},"source":"manual"}}}`
	fastDocument, err := ParseDocument([]byte(fastDoc))
	if err != nil {
		t.Fatal(err)
	}
	if fast := fastDocument.Models["m"].Fast; fast.Output != 4 || fast.CacheWrite != 2.5 || !fastDocument.Models["m"].Above.IsZero() {
		t.Fatalf("fast = %+v", fast)
	}
	out, err = json.Marshal(fastDocument)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != fastDoc {
		t.Fatalf("fast round trip = %s", out)
	}
}

func TestValidateRejectsInconsistentTiers(t *testing.T) {
	base := Entry{Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 1.25, Source: SourceManual}
	tier := Tier{PromptTokens: 100000, Input: 2, Output: 3, CacheRead: 0.2, CacheWrite: 2.5}
	fast := RateSet{Input: 2, Output: 3, CacheRead: 0.2, CacheWrite: 2.5}
	fastTier := Tier{PromptTokens: 100000, Input: 4, Output: 6, CacheRead: 0.4, CacheWrite: 5}
	with := func(edit func(*Entry)) Entry {
		entry := base
		edit(&entry)
		return entry
	}
	valid := map[string]Entry{
		"base only":                     base,
		"above":                         with(func(e *Entry) { e.Above = tier }),
		"fast":                          with(func(e *Entry) { e.Fast = fast }),
		"above and fast, no fast_above": with(func(e *Entry) { e.Above = tier; e.Fast = fast }),
		"above, fast and fast_above":    with(func(e *Entry) { e.Above = tier; e.Fast = fast; e.FastAbove = fastTier }),
		"fast_above with 1h": with(func(e *Entry) {
			e.CacheWrite1h = 2
			e.Above = tier
			e.Above.CacheWrite1h = 4
			e.Fast = fast
			e.Fast.CacheWrite1h = 4
			e.FastAbove = fastTier
			e.FastAbove.CacheWrite1h = 8
		}),
		"1h on all": with(func(e *Entry) {
			e.CacheWrite1h = 2
			e.Above = tier
			e.Above.CacheWrite1h = 4
		}),
	}
	for name, entry := range valid {
		if err := (Document{Version: "2026-09-28", Models: map[string]Entry{"m": entry}}).Validate(); err != nil {
			t.Errorf("%s: rejected: %v", name, err)
		}
	}
	invalid := map[string]Entry{
		"negative 1h":              with(func(e *Entry) { e.CacheWrite1h = -1 }),
		"NaN 1h":                   with(func(e *Entry) { e.CacheWrite1h = math.NaN() }),
		"infinite above rate":      with(func(e *Entry) { e.Above = tier; e.Above.CacheRead = math.Inf(1) }),
		"negative above rate":      with(func(e *Entry) { e.Above = tier; e.Above.Output = -3 }),
		"negative fast rate":       with(func(e *Entry) { e.Fast = fast; e.Fast.CacheWrite = -1 }),
		"NaN fast rate":            with(func(e *Entry) { e.Fast = fast; e.Fast.Input = math.NaN() }),
		"above no threshold":       with(func(e *Entry) { e.Above = tier; e.Above.PromptTokens = 0 }),
		"above negative limit":     with(func(e *Entry) { e.Above = tier; e.Above.PromptTokens = -5 }),
		"threshold only":           with(func(e *Entry) { e.Above = Tier{PromptTokens: 100000} }),
		"above no in or out":       with(func(e *Entry) { e.Above = Tier{PromptTokens: 100000, CacheRead: 1} }),
		"fast no in or out":        with(func(e *Entry) { e.Fast = RateSet{CacheRead: 1} }),
		"fast_above alone":         with(func(e *Entry) { e.FastAbove = fastTier }),
		"fast_above without above": with(func(e *Entry) { e.Fast = fast; e.FastAbove = fastTier }),
		"fast_above without fast":  with(func(e *Entry) { e.Above = tier; e.FastAbove = fastTier }),
		"fast_above other limit": with(func(e *Entry) {
			e.Above = tier
			e.Fast = fast
			e.FastAbove = fastTier
			e.FastAbove.PromptTokens = 200000
		}),
		"fast_above skips output": with(func(e *Entry) { e.Above = tier; e.Fast = fast; e.FastAbove = fastTier; e.FastAbove.Output = 0 }),
		"fast_above skips what above prices": with(func(e *Entry) {
			e.Above = tier
			e.Above.CacheWrite1h = 4
			e.Fast = fast
			e.FastAbove = fastTier
		}),
		"fast_above skips cache": with(func(e *Entry) { e.Above = tier; e.Fast = fast; e.FastAbove = fastTier; e.FastAbove.CacheRead = 0 }),
		"fast_above lacks 1h": with(func(e *Entry) {
			e.Above = tier
			e.Above.CacheWrite1h = 4
			e.CacheWrite1h = 2
			e.Fast = fast
			e.Fast.CacheWrite1h = 4
			e.FastAbove = fastTier
		}),
		"negative fast_above rate":  with(func(e *Entry) { e.Above = tier; e.Fast = fast; e.FastAbove = fastTier; e.FastAbove.Input = -1 }),
		"above lacks 1h":            with(func(e *Entry) { e.CacheWrite1h = 2; e.Above = tier }),
		"fast lacks 1h":             with(func(e *Entry) { e.CacheWrite1h = 2; e.Fast = fast }),
		"above skips cache buckets": with(func(e *Entry) { e.Above = Tier{PromptTokens: 100000, Input: 2, Output: 3} }),
		"above skips cache read":    with(func(e *Entry) { e.Above = tier; e.Above.CacheRead = 0 }),
		"above skips cache write":   with(func(e *Entry) { e.Above = tier; e.Above.CacheWrite = 0 }),
		"fast skips cache buckets":  with(func(e *Entry) { e.Fast = RateSet{Input: 2, Output: 3} }),
		"fast skips output":         with(func(e *Entry) { e.Fast = fast; e.Fast.Output = 0 }),
		"above skips input":         with(func(e *Entry) { e.Above = tier; e.Above.Input = 0 }),
	}
	for name, entry := range invalid {
		if err := (Document{Version: "2026-09-28", Models: map[string]Entry{"m": entry}}).Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_, err := ParseDocument([]byte(`{"version":"2026-09-28","models":{"m":{"input":1,"output":2,"cache_read":0,"cache_write":0,"above":{"prompt_tokens":5,"input":1,"output":1,"cache_read":0,"cache_write":0,"surprise":1},"source":"manual"}}}`))
	if err == nil {
		t.Error("unknown field inside above accepted")
	}
}

func TestFormatMarkerIdentifiesTheFullFormat(t *testing.T) {
	full, err := ParseDocument(EmbeddedDocument())
	if err != nil {
		t.Fatal(err)
	}
	if full.Format != FormatVersion || !NewList(full).Full() || !Embedded().Full() {
		t.Fatal("the compiled list is not marked as the full format")
	}
	full.Format = 3
	if err := full.Validate(); err == nil {
		t.Fatal("an unknown format was accepted")
	}
}
