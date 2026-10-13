package agent

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lastpersonlabs/baum/bundle"
)

// Both are detected by the redactor: a database URL with credentials and a
// provider-shaped key. identifierSecret is a key it detects as written, in
// lowercase, so it reads as an identifier.
const (
	leakedDatabaseURL = "postgres://admin:Sup3rS3cretPassw0rd@db.internal.example.com:5432/app"
	leakedProviderKey = "sk-" + "ant-api03-Ab3dEf6hIj9kLm2nOp5qRs8tUv1wXy4zAb7dEf0hIj3kLm6nOp9qRs2tUv5wXy8zAb1dEf4hIj7kLm0nOp3qRs6tUv9wXy2zAb5dEf8hIjAA"
	identifierSecret  = "sk_live_4ec3b2a1f9d84c7e6b5a3f2e1d0c9b8a"
)

func leakingDocument(t *testing.T) []byte {
	t.Helper()
	document := bytes.Replace(authoredFixture(), []byte("Changes parser behavior."), []byte("Rotates the credential in "+leakedDatabaseURL+"."), 1)
	return bytes.Replace(document, []byte("Adds the parser."), []byte("Stops sending "+leakedProviderKey+" to the provider."), 1)
}

func redactionFixture(t *testing.T) (*bundle.Redactor, *bundle.Bundle) {
	t.Helper()
	redactor, err := bundle.NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	return redactor, fixtureBundle()
}

func TestRedactOutputMasksEverythingReturned(t *testing.T) {
	redactor, b := redactionFixture(t)
	outcome := Outcome{
		Model: "model " + leakedProviderKey, Findings: "quotes " + leakedProviderKey,
		RejectedDocument: leakingDocument(t),
		Diagnostics:      []Event{{Kind: "tool_use", Path: leakedDatabaseURL, Tool: strings.Repeat("é", 200)}},
	}
	document := leakingDocument(t)
	redacted, closed, err := RedactOutput(context.Background(), sharedValidator(t), redactor, document, b, nil, &outcome)
	if err != nil || closed != 0 {
		t.Fatalf("closed=%d %v", closed, err)
	}
	if outcome.OutputMaskedSpans == 0 {
		t.Fatal("no masked spans recorded")
	}
	for name, text := range map[string]string{
		"document": string(redacted), "rejected document": string(outcome.RejectedDocument), "findings": outcome.Findings, "model": outcome.Model,
		"diagnostic path": outcome.Diagnostics[0].Path,
	} {
		if strings.Contains(text, leakedProviderKey) || strings.Contains(text, "Sup3rS3cretPassw0rd") {
			t.Errorf("%s keeps a secret: %s", name, text)
		}
	}
	if tool := outcome.Diagnostics[0].Tool; len(tool) > maxDiagnosticStringBytes || !utf8.ValidString(tool) {
		t.Errorf("the diagnostic tool is not bounded on a rune boundary: %d bytes", len(tool))
	}
}

func TestRedactOutputKeepsACleanDocumentByteForByte(t *testing.T) {
	redactor, b := redactionFixture(t)
	outcome := Outcome{Model: "fixture-model", Findings: "a finding"}
	redacted, closed, err := RedactOutput(context.Background(), sharedValidator(t), redactor, authoredFixture(), b, nil, &outcome)
	if err != nil || closed != 0 || !bytes.Equal(redacted, authoredFixture()) || outcome.OutputMaskedSpans != 0 || outcome.Findings != "a finding" {
		t.Fatalf("%s %d %v %+v", redacted, closed, err, outcome)
	}
}

func TestRedactOutputDropsTheStringsWhenItFails(t *testing.T) {
	redactor, b := redactionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := Outcome{Findings: leakedProviderKey, Model: leakedProviderKey, RejectedDocument: leakingDocument(t), Diagnostics: []Event{{Kind: "tool_use", Path: leakedDatabaseURL}}}
	if _, _, err := RedactOutput(ctx, sharedValidator(t), redactor, leakingDocument(t), b, nil, &outcome); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if outcome.Findings != "" || outcome.Model != "" || outcome.RejectedDocument != nil || outcome.Diagnostics != nil {
		t.Fatalf("an unmasked string was kept: %+v", outcome)
	}
}

func TestRedactOutputCountsClosedValues(t *testing.T) {
	redactor, b := redactionFixture(t)
	document := bytes.Replace(authoredFixture(), []byte(`"risks":[]`), []byte(`"risks":[{"id":"rsk_leak","tier":"high","category":"`+identifierSecret+`","source":"model","text":"The key reaches the provider.","anchors":[{"type":"hunk","id":"`+testHunk+`"}]}]`), 1)
	outcome := Outcome{}
	redacted, closed, err := RedactOutput(context.Background(), sharedValidator(t), redactor, document, b, nil, &outcome)
	if err != nil || closed == 0 || outcome.OutputMaskedSpans == 0 {
		t.Fatalf("closed=%d %+v %v", closed, outcome, err)
	}
	if strings.Contains(strings.ToLower(string(redacted)), identifierSecret) {
		t.Fatalf("the secret survives: %s", redacted)
	}
	// RejectSecrets turns the count into the host's rejection.
	kept, rejected := RejectSecrets(&outcome, redacted, closed)
	if !rejected || kept != nil || outcome.Reason != ReasonValidationRejected || outcome.Findings != SecretFinding || !bytes.Equal(outcome.RejectedDocument, redacted) {
		t.Fatalf("%v %s %+v", rejected, kept, outcome)
	}
}

func TestRejectSecrets(t *testing.T) {
	outcome := Outcome{Findings: "validator findings"}
	if kept, rejected := RejectSecrets(&outcome, []byte(`{}`), 0); rejected || string(kept) != `{}` || outcome.Reason != "" || outcome.Findings != "validator findings" {
		t.Fatalf("nothing masked must change nothing: %v %s %+v", rejected, kept, outcome)
	}
	if kept, rejected := RejectSecrets(&outcome, []byte(`{"a":1}`), 2); !rejected || kept != nil || outcome.Reason != ReasonValidationRejected || outcome.Findings != "validator findings\n"+SecretFinding || string(outcome.RejectedDocument) != `{"a":1}` {
		t.Fatalf("%v %s %+v", rejected, kept, outcome)
	}
	// A run that had already failed keeps the rejected document it recorded.
	failed := Outcome{Reason: ReasonValidationRejected, RejectedDocument: []byte(`{"kept":true}`)}
	RejectSecrets(&failed, []byte(`{"other":true}`), 1)
	if string(failed.RejectedDocument) != `{"kept":true}` || failed.Findings != SecretFinding {
		t.Fatalf("%+v", failed)
	}
}

// The counts of the loop's identifier pass may come from another process: one
// too large to add to can wrap neither the closed count to zero or below nor
// the masked-span count.
func TestReportedCountsSaturate(t *testing.T) {
	redactor, b := redactionFixture(t)
	outcome := Outcome{IdentifierRedaction: &IdentifierRedaction{Masked: math.MaxInt, MaskedClosed: math.MaxInt}}
	_, closed, err := RedactOutput(context.Background(), sharedValidator(t), redactor, leakingDocument(t), b, nil, &outcome)
	if err != nil || closed != math.MaxInt || outcome.OutputMaskedSpans != math.MaxInt {
		t.Fatalf("closed=%d spans=%d %v", closed, outcome.OutputMaskedSpans, err)
	}
	if saturatingSum(3, -5) != 3 || saturatingSum(math.MaxInt, 1) != math.MaxInt {
		t.Fatal("saturatingSum")
	}
	for report, want := range map[*IdentifierRedaction]bool{nil: false, {}: true, {Masked: 1, MaskedClosed: 1}: true, {Masked: -1}: false, {MaskedClosed: -1}: false} {
		if report.Reported() != want {
			t.Errorf("%+v reported = %v", report, !want)
		}
	}
}

func TestBoundedBytesCutsOnARuneBoundary(t *testing.T) {
	if got := boundedBytes("aé", 2); got != "a" {
		t.Errorf("cut inside a rune: %q", got)
	}
	if got := boundedBytes("abc", 5); got != "abc" {
		t.Errorf("short string: %q", got)
	}
}
