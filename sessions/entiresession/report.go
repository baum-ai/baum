package entiresession

import (
	"encoding/json"

	"github.com/baum-ai/baum/sessions/claudesession"
	"github.com/baum-ai/baum/sessions/codexsession"
	"github.com/baum-ai/baum/sessions/cursorsession"
	"github.com/baum-ai/baum/sessions/entirecheckpoint"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

// Report accounts for all Entire-owned source records and carries delegated
// mapper reports without rewriting them.
type Report struct {
	Sessions        int               `json:"sessions"`
	FailedSnapshots int               `json:"failed_snapshots"`
	Rows            Rows              `json:"rows"`
	Compact         CompactAccounting `json:"compact"`
	Metadata        Accounting        `json:"metadata"`
	Envelope        EnvelopeReport    `json:"envelope"`
	Delegated       []DelegateReport  `json:"delegated,omitempty"`
	// Tasks accounts for the checkpoint's subagent directories (tasks.go).
	Tasks    *Accounting            `json:"tasks,omitempty"`
	Shape    entirecheckpoint.Shape `json:"shape"`
	Findings []string               `json:"findings,omitempty"`
}

type Rows struct {
	Sessions          int `json:"sessions"`
	UserInputs        int `json:"user_inputs"`
	ModelInvocations  int `json:"model_invocations"`
	AssistantMessages int `json:"assistant_messages"`
	ToolUses          int `json:"tool_uses"`
	ToolResults       int `json:"tool_results"`
	FileTouches       int `json:"file_touches"`
	Attributes        int `json:"session_attributes"`
	TokenUsage        int `json:"token_usage"`
	VendorIDs         int `json:"vendor_ids"`
}

func rowsFrom(t traceprocessing.RowTally) Rows {
	return Rows{t.Sessions, t.UserInputs, t.ModelInvocations, t.AssistantMessages, t.ToolUses, t.ToolResults, t.FileTouches, t.Attributes, t.TokenUsage, t.VendorIDs}
}

type CompactAccounting struct {
	Lines  Accounting `json:"lines"`
	Blocks Accounting `json:"blocks"`
}

type Accounting struct {
	Total     int            `json:"total"`
	Mapped    int            `json:"mapped"`
	Evidence  int            `json:"envelope_evidence"`
	Discarded map[string]int `json:"discarded,omitempty"`
}

func (a Accounting) Accounted() int {
	total := a.Mapped + a.Evidence
	for _, count := range a.Discarded {
		total += count
	}
	return total
}

func (a Accounting) Balanced() bool { return a.Total == a.Accounted() }

func (a Accounting) DiscardReasons() []string {
	return traceprocessing.SortedDiscardReasons(a.Discarded)
}

type DelegateReport struct {
	SessionID string                        `json:"session_id"`
	Agent     string                        `json:"agent"`
	Claude    *claudesession.Report         `json:"claude,omitempty"`
	Codex     *codexsession.Report          `json:"codex,omitempty"`
	Cursor    *cursorsession.PortableReport `json:"cursor,omitempty"`
	// Tasks are the checkpoint's subagent transcripts this session claimed,
	// and whether each became a nested session.
	Tasks    []DelegatedTask `json:"tasks,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
}

type DelegatedTask struct {
	Directory string `json:"directory"`
	Nested    bool   `json:"nested"`
}

type EnvelopeReport struct {
	profile                  traceprocessing.MappingProfile
	AttributesAdded          int `json:"attributes_added"`
	VendorIDsAdded           int `json:"vendor_ids_added"`
	AggregateUsageAdded      int `json:"aggregate_usage_added"`
	AggregateUsageAttributed int `json:"aggregate_usage_attributed"`
	AggregateUsageRejected   int `json:"aggregate_usage_rejected"`
	FilesConsidered          int `json:"files_considered"`
	FilesAlreadyCovered      int `json:"files_already_covered"`
	FilesUnrepresentable     int `json:"files_unrepresentable"`
	FilesShared              int `json:"files_shared"`
	HashChecked              int `json:"hash_checked"`
	HashMismatches           int `json:"hash_mismatches"`
}

func (r *Report) Balanced() bool {
	if r == nil || !r.Compact.Lines.Balanced() || !r.Compact.Blocks.Balanced() || !r.Metadata.Balanced() || (r.Tasks != nil && !r.Tasks.Balanced()) {
		return false
	}
	for _, delegated := range r.Delegated {
		if delegated.Claude != nil && !delegated.Claude.Lines.Balanced() {
			return false
		}
		if delegated.Codex != nil && (!delegated.Codex.Records.Balanced() || delegated.Codex.UnsupportedTurns != 0) {
			return false
		}
		if delegated.Cursor != nil && !delegated.Cursor.Balanced() {
			return false
		}
	}
	return true
}

func (a *Accounting) discard(reason string, count int) {
	traceprocessing.AddDiscard(&a.Discarded, reason, count)
}

func (r *Report) addRows(graphRows traceprocessing.RowTally) {
	rows := rowsFrom(graphRows)
	r.Rows.Sessions += rows.Sessions
	r.Rows.UserInputs += rows.UserInputs
	r.Rows.ModelInvocations += rows.ModelInvocations
	r.Rows.AssistantMessages += rows.AssistantMessages
	r.Rows.ToolUses += rows.ToolUses
	r.Rows.ToolResults += rows.ToolResults
	r.Rows.FileTouches += rows.FileTouches
	r.Rows.Attributes += rows.Attributes
	r.Rows.TokenUsage += rows.TokenUsage
	r.Rows.VendorIDs += rows.VendorIDs
}

// MarshalJSON keeps local report fields unchanged while hosted reports include
// the rejected-aggregate counter even when zero.
func (r EnvelopeReport) MarshalJSON() ([]byte, error) {
	var rejected *int
	if r.profile == traceprocessing.Hosted {
		rejected = &r.AggregateUsageRejected
	}
	return json.Marshal(struct {
		AttributesAdded          int  `json:"attributes_added"`
		VendorIDsAdded           int  `json:"vendor_ids_added"`
		AggregateUsageAdded      int  `json:"aggregate_usage_added"`
		AggregateUsageAttributed int  `json:"aggregate_usage_attributed"`
		AggregateUsageRejected   *int `json:"aggregate_usage_rejected,omitempty"`
		FilesConsidered          int  `json:"files_considered"`
		FilesAlreadyCovered      int  `json:"files_already_covered"`
		FilesUnrepresentable     int  `json:"files_unrepresentable"`
		FilesShared              int  `json:"files_shared"`
		HashChecked              int  `json:"hash_checked"`
		HashMismatches           int  `json:"hash_mismatches"`
	}{
		AttributesAdded:          r.AttributesAdded,
		VendorIDsAdded:           r.VendorIDsAdded,
		AggregateUsageAdded:      r.AggregateUsageAdded,
		AggregateUsageAttributed: r.AggregateUsageAttributed,
		AggregateUsageRejected:   rejected,
		FilesConsidered:          r.FilesConsidered,
		FilesAlreadyCovered:      r.FilesAlreadyCovered,
		FilesUnrepresentable:     r.FilesUnrepresentable,
		FilesShared:              r.FilesShared,
		HashChecked:              r.HashChecked,
		HashMismatches:           r.HashMismatches,
	})
}
