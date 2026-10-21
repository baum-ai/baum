package session

// SubagentDetailKey is the tool-use metadata key on a call that started a
// subagent. Its value says what the tree holds of the subagent's work. It is
// ours, not a vendor field, so every adapter that records it writes the same
// key; an adapter that does not know leaves it out.
const SubagentDetailKey = "subagent_detail"

// SubagentDetail is the value stored under SubagentDetailKey.
type SubagentDetail string

const (
	// SubagentTranscript: the subagent is a nested session spawned by the call.
	SubagentTranscript SubagentDetail = "transcript"
	// SubagentReport: only the subagent's final report, as the call's result.
	SubagentReport SubagentDetail = "report"
	// SubagentNone: the source carried neither. This is missing detail, never
	// a subagent that did nothing.
	SubagentNone SubagentDetail = "none"
)
