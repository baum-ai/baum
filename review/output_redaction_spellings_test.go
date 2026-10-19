package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

// identifierDocument holds one evidence ID and one risk ID, with prose.
func identifierDocument(evidence, risk, text string) string {
	return `{"sections":[{"evidence":[{"id":` + quoted(evidence) + `}],"risks":[{"id":` + quoted(risk) + `}],"title":` + quoted(text) + `}]}`
}

func quoted(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

// A key a rule finds together with its context (a URL's host, an address's
// domain, a hyphenated tail) is a source on its own too, so its other
// spellings are matched without that context; a password no rule detects
// alone is matched without the host.
func TestOutputRedactionMatchesKeysWithoutTheirContext(t *testing.T) {
	access := strings.ToLower(leakedAccessKey)
	provider := identifierToken(leakedProviderKey)
	for _, c := range []struct{ name, evidence, risk string }{
		{"URL credential", "postgres://admin:" + leakedAccessKey + "@db.internal.example.com:5432/app", "rsk_" + access + "_reuse"},
		{"URL password", "https://deploy:Sup3rS3cretPassw0rd@db.internal.example.com", "rsk_sup3rs3cretpassw0rd"},
		{"address", "ev-" + leakedAccessKey + "@example.com", "rsk_" + access + "_reuse"},
		{"hyphenated tail", leakedProviderKey + "-prod-east", "rsk_" + provider},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, count, err := testRedactor(t).RedactString(context.Background(), c.risk)
			checkNoError(t, err)
			checkEqual(t, 0, count)
			o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
			raw, err := o.Identifiers(context.Background(), []byte(identifierDocument(c.evidence, c.risk, "T")))
			checkNoError(t, err)
			if !strings.Contains(string(raw), quoted(testFormatMask(c.risk))) {
				t.Fatalf("the risk ID is not masked: %s", raw)
			}
			checkEqual(t, 2, o.MaskedClosed())
		})
	}
}

// A key past more runs of separators than closedCopyLimit, in a value over
// its bound, is found: it counts and is a source, so its other spellings are
// masked. The value is still masked whole.
func TestOutputRedactionFindsKeysPastManyRestarts(t *testing.T) {
	// An underscore hides the key from its rule, which needs a word
	// boundary, so only a copy from its restart finds it.
	const stripe = "sk_live_4ec3b2a1f9d84c7e6b5a3f2e1d0c9b8a"
	upper := "Rsk-" + strings.ToUpper(stripe)
	_, count, err := testRedactor(t).RedactString(context.Background(), "a_"+stripe+" "+upper)
	checkNoError(t, err)
	checkEqual(t, 0, count)
	hidden := strings.Repeat("a_", 70) + stripe
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	raw, _, redacted := identifiersThenPrepare(t, o, identifierDocument(hidden, upper, "T"))
	checkEqual(t, identifierDocument(testFormatMask(hidden), testFormatMask(upper), "T"), raw)
	if strings.Contains(strings.ToLower(redacted), stripe[8:]) {
		t.Fatalf("the key survives: %s", redacted)
	}
	checkEqual(t, 2, o.MaskedClosed())

	// Document alone finds it in a closed value over its bound.
	category := strings.Repeat("a_", 70) + stripe
	o = newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	masked, err := o.Document(context.Background(), []byte(`{"sections":[{"risks":[{"category":"`+category+`"}]}]}`))
	checkNoError(t, err)
	checkEqual(t, `{"sections":[{"risks":[{"category":"`+testFormatMask(category)+`"}]}]}`, string(masked))
	checkEqual(t, 1, o.MaskedClosed())
}

// Scanning every restart of a value over its bound costs a small window per
// restart, and a value repeating itself costs its distinct windows only.
func TestOutputRedactionBoundsTheScanOfEveryRestart(t *testing.T) {
	words := make([]string, 8<<10)
	for i := range words {
		words[i] = fmt.Sprintf("w%x", i)
	}
	distinct := strings.Join(words, "_")
	repeated := strings.Repeat("a_", 32<<10) + leakedAccessKey
	for _, c := range []struct {
		name, value string
		windows     int
	}{
		{"distinct", distinct, scanRestarts(distinct)},
		// Only the windows that reach the key differ.
		{"repeated", repeated, closedRestartWindow},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
			_, err := o.Identifiers(context.Background(), []byte(identifierDocument(c.value, "rsk_x", "T")))
			checkNoError(t, err)
			bound := len(c.value) + closedCopyLimit*closedScanWindow + c.windows*(closedRestartWindow+1) + 1<<10
			if o.o.scanned > bound {
				t.Fatalf("scanned %d bytes, over %d", o.o.scanned, bound)
			}
		})
	}
}

// Free text that repeats an identifier's secret in a spelling no rule
// detects (respelled, upper-cased) has those words masked, and only those.
func TestOutputRedactionMasksFreeTextSpellingsOfAnIdentifierSecret(t *testing.T) {
	lower := strings.ToLower(leakedAccessKey)
	provider := identifierToken(leakedProviderKey)
	upper := strings.ToUpper(leakedProviderKey)
	for _, spelling := range []string{lower, upper, provider} {
		_, count, err := testRedactor(t).RedactString(context.Background(), "Rotate "+spelling+" now")
		checkNoError(t, err)
		checkEqual(t, 0, count)
	}
	title := "Rotate " + lower + " and " + upper + " (" + provider + "), not x" + lower + "."
	document := `{"sections":[{"attention":{"refs":[],"text":` + quoted("Keep "+lower) + `},"evidence":[{"id":"` + leakedAccessKey + `"},{"id":"` + leakedProviderKey + `"}],"title":` + quoted(title) + `}]}`
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	raw, _, redacted := identifiersThenPrepare(t, o, document)
	want := "Rotate " + testFormatMask(lower) + " and " + testFormatMask(upper) + " (" + testFormatMask(provider) + "), not x" + lower + "."
	if !strings.Contains(raw, quoted(want)) || !strings.Contains(raw, quoted("Keep "+testFormatMask(lower))) {
		t.Fatalf("the free text is not masked as expected: %s", raw)
	}
	for _, spelling := range []string{upper, provider, "Keep " + lower} {
		if strings.Contains(redacted, spelling) {
			t.Fatalf("redacted keeps %q: %s", spelling, redacted)
		}
	}
	checkEqual(t, 2, o.MaskedClosed())
	checkEqual(t, 6, o.Masked())
}

// Spellings are whole words of the text as written, respelled rune by rune.
func TestTokenMatcherSpellings(t *testing.T) {
	m := newTokenMatcher([]string{"akia_key", "k9", "a_b"})
	for text, want := range map[string][]string{
		"Use AKIA-key now":         {"AKIA-key"},
		"akia key, K9 and xk9 k9x": {"akia key", "K9"},
		"--akia__KEY--":            {"akia__KEY"},
		"A b":                      {"A b"},
		"a-b-a-b":                  {"a-b", "a-b"},
		"Kelvin K9 sign":           {"K9"},
		"nothing here":             nil,
		"akia_keyx":                nil,
	} {
		var got []string
		m.spellings(text, func(start, end int) { got = append(got, text[start:end]) })
		checkEqual(t, fmt.Sprint(want), fmt.Sprint(got))
	}
}
