package codexsession

import (
	"fmt"

	"github.com/lastpersonlabs/reviewpal/sessions/codextranscript"
	"github.com/lastpersonlabs/reviewpal/sessions/traceprocessing"
)

// Report accounts for every rollout record and every emitted graph row.
type Report struct {
	Sessions         int                   `json:"sessions"`
	Rows             Rows                  `json:"rows"`
	Records          Records               `json:"records"`
	Tokens           Tokens                `json:"tokens"`
	UnsupportedTurns int                   `json:"unsupported_turns"`
	Transcript       codextranscript.Shape `json:"transcript"`
	Notes            []string              `json:"notes,omitempty"`
}

type Rows = traceprocessing.ReportRows

type Records = traceprocessing.LineAccounting

type Tokens struct {
	Records             int `json:"records"`
	AssignedInvocations int `json:"assigned_invocations"`
	AmbiguousRows       int `json:"ambiguous_rows"`
	RepeatedTotals      int `json:"repeated_totals"`
}

func (r *Report) discard(reason string) {
	traceprocessing.AddDiscard(&r.Records.Discarded, reason, 1)
}

func (r *Report) note(format string, args ...any) {
	r.Notes = append(r.Notes, fmt.Sprintf(format, args...))
}
