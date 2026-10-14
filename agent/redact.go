package agent

import (
	"bytes"
	"context"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/baumai/baum/bundle"
	"github.com/baumai/baum/review"
)

// maxDiagnosticStringBytes bounds each string of a returned diagnostic event.
const maxDiagnosticStringBytes = 256

// SecretFinding is the finding of a run rejected for a secret or e-mail
// address in a value a schema closes. It quotes nothing the model wrote.
const SecretFinding = "A secret or e-mail address was found in an identifier, category, reference or other value the schema closes by pattern. It was masked, and the document was rejected: such values must never hold one."

// RedactOutput masks secret spans in everything a Generator returned, before
// any of it is stored, uploaded or shown: the document, a kept rejected
// document, the findings, the model and each diagnostic string. A host applies
// it to every outcome it records, whether the loop ran in its process or its
// result crossed a boundary. It records the masked spans on the outcome, apart
// from the bundle's count, and returns the document to keep. A document with
// nothing to mask is returned byte for byte. On failure the returned strings
// are dropped rather than kept unmasked.
//
// checkout lists the repository paths of the run's checkout, either tree; a
// path in it may stay as it is. The masked spans include those of the loop's
// identifier pass, and closed counts the closed values either pass masked: a
// run with any must be rejected (RejectSecrets).
func RedactOutput(ctx context.Context, validator *review.DocumentValidator, redactor *bundle.Redactor, document []byte, b *bundle.Bundle, checkout map[string]bool, outcome *Outcome) (redacted []byte, closed int, err error) {
	defer func() {
		if err != nil {
			redacted, closed = nil, 0
			outcome.Findings, outcome.Model, outcome.RejectedDocument, outcome.Diagnostics = "", "", nil, nil
		}
	}()
	o, err := review.NewOutputRedactor(validator, redactor, b, checkout)
	if err != nil {
		return nil, 0, err
	}
	if redacted, err = o.Document(ctx, document); err != nil {
		return nil, 0, err
	}
	if len(outcome.RejectedDocument) > 0 {
		kept := redacted
		if !bytes.Equal(outcome.RejectedDocument, document) {
			if kept, err = o.Document(ctx, outcome.RejectedDocument); err != nil {
				return nil, 0, err
			}
		}
		outcome.RejectedDocument = RetainedDocument(kept)
	}
	for _, field := range []*string{&outcome.Findings, &outcome.Model} {
		if *field, err = o.Text(ctx, *field); err != nil {
			return nil, 0, err
		}
	}
	for i := range outcome.Diagnostics {
		event := &outcome.Diagnostics[i]
		event.Kind = boundedBytes(event.Kind, maxDiagnosticStringBytes)
		for _, field := range []*string{&event.ID, &event.Tool, &event.Path, &event.Status} {
			// Masking comes first, so the cap cannot cut a secret below detection.
			masked, err := o.Text(ctx, *field)
			if err != nil {
				return nil, 0, err
			}
			*field = boundedBytes(masked, maxDiagnosticStringBytes)
		}
	}
	outcome.OutputMaskedSpans, closed = o.Masked(), o.MaskedClosed()
	if identifiers := outcome.IdentifierRedaction; identifiers != nil {
		// The counts may come from another process and are untrusted, so the
		// sums saturate: an absurd count can neither wrap a rejection to zero
		// nor a count negative.
		outcome.OutputMaskedSpans = saturatingSum(outcome.OutputMaskedSpans, identifiers.Masked)
		closed = saturatingSum(closed, identifiers.MaskedClosed)
	}
	return redacted, closed, nil
}

// RejectSecrets rejects a run whose output had a secret masked in a closed
// value, as RedactOutput counts them in closed. The masked document may still
// be valid, so the rejection is explicit: it sets ReasonValidationRejected,
// adds SecretFinding to the findings and keeps only the masked copy as the
// rejected document unless the run had already failed. It returns the document
// to keep, which is nil after a rejection, and whether it rejected. With closed
// zero it changes nothing.
func RejectSecrets(outcome *Outcome, document []byte, closed int) (kept []byte, rejected bool) {
	if closed <= 0 {
		return document, false
	}
	if outcome.Reason == "" {
		outcome.RejectedDocument = RetainedDocument(document)
	}
	outcome.Reason = ReasonValidationRejected
	outcome.Findings = strings.TrimPrefix(outcome.Findings+"\n"+SecretFinding, "\n")
	return nil, true
}

// saturatingSum adds two counts, taking a negative one as zero and capping
// the sum at math.MaxInt.
func saturatingSum(a, b int) int {
	a, b = max(a, 0), max(b, 0)
	if b > math.MaxInt-a {
		return math.MaxInt
	}
	return a + b
}

// boundedBytes cuts s to at most limit bytes on a rune boundary.
func boundedBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}
