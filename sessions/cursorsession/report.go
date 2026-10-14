package cursorsession

import (
	"fmt"

	"github.com/baumai/baum/sessions/traceprocessing"
)

// Report accounts for every bubble read and every row emitted.
type Report struct {
	Sessions int      `json:"sessions"`
	Rows     Rows     `json:"rows"`
	Bubbles  Bubbles  `json:"bubbles"`
	Tokens   Tokens   `json:"tokens"`
	Notes    []string `json:"notes,omitempty"`
}

type Rows = traceprocessing.ReportRows

type Bubbles = traceprocessing.ProvenanceAccounting

type Tokens struct {
	AssignedInvocations  int `json:"assigned_invocations"`
	EstimatedInvocations int `json:"estimated_invocations"`
	AmbiguousBubbles     int `json:"ambiguous_bubbles"`
}

func (r *Report) mapped() { r.Bubbles.Mapped++ }

func (r *Report) provenance() { r.Bubbles.Provenance++ }

func (r *Report) discard(reason string) { r.discardN(reason, 1) }

func (r *Report) discardN(reason string, count int) {
	traceprocessing.AddDiscard(&r.Bubbles.Discarded, reason, count)
}

func (r *Report) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}
