package agent

import "encoding/json"

// Outcome is what a generation run reports besides the document: what the
// loop did and what it spent. A host embeds it in its own outcome type; the
// field names are the wire format of the hosted agent, so they must not change.
type Outcome struct {
	Accounting     *Accounting `json:"accounting,omitempty"`
	Model          string      `json:"model,omitempty"`
	SchemaRevision string      `json:"schema_revision"`
	// Reason is why the run failed, one of the fixed reasons (ReasonValidationRejected
	// and the budget and provider reasons). Empty means the run produced a
	// valid document.
	Reason   string `json:"reason,omitempty"`
	Findings string `json:"findings,omitempty"`
	// RejectedDocument is the last document the validator rejected when the
	// run failed, kept next to the findings so a maintainer can read both.
	RejectedDocument           json.RawMessage `json:"rejected_document,omitempty"`
	Diagnostics                []Event         `json:"diagnostics,omitempty"`
	Calls                      int             `json:"calls"`
	Tokens                     int             `json:"tokens"`
	ProviderAttempts           int             `json:"provider_attempts"`
	ErroredAttempts            int             `json:"errored_attempts"`
	ValidationRepairs          int             `json:"validation_repairs"`
	Usage                      Usage           `json:"usage"`
	StructuredOutputRejections int             `json:"structured_output_rejections"`
	// OutputMaskedSpans counts the secret spans RedactOutput masked in the
	// returned output, apart from the bundle's own count.
	OutputMaskedSpans int `json:"output_masked_spans"`
	// IdentifierRedaction is what the loop's identifier pass masked over every
	// pass of the run. The loop sets it on every outcome it builds; nil is a
	// producer that predates the pass.
	IdentifierRedaction *IdentifierRedaction `json:"identifier_redaction,omitempty"`
	// SessionID is the backend session of the last successful call, set only
	// when the run asked to persist its session (Run.PersistSession). It is
	// not part of the diagnostics.
	SessionID string `json:"-"`
}

// IdentifierRedaction counts the spans the identifier pass masked, and the
// values they were in. Every value it masks held a secret, so MaskedClosed is
// not zero whenever Masked is not, and the run is then rejected.
type IdentifierRedaction struct {
	Masked       int `json:"masked"`
	MaskedClosed int `json:"masked_closed"`
}

// Reported reports whether the producer ran the identifier pass and says so
// with counts that can be true.
func (r *IdentifierRedaction) Reported() bool {
	return r != nil && r.Masked >= 0 && r.MaskedClosed >= 0
}

// MaxRetainedDocumentBytes bounds the rejected document kept in an Outcome.
const MaxRetainedDocumentBytes = 2 << 20

// RetainedDocument returns a copy of a rejected document for Outcome.RejectedDocument,
// or nil when it is empty, over MaxRetainedDocumentBytes or not JSON.
func RetainedDocument(document []byte) json.RawMessage {
	if len(document) == 0 || len(document) > MaxRetainedDocumentBytes || !json.Valid(document) {
		return nil
	}
	return json.RawMessage(append([]byte(nil), document...))
}
