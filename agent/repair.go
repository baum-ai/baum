package agent

// The text around a validation repair. A repair is not a new review: the model
// gets the input bundle, the document the validator rejected and the findings,
// and corrects only those. Source excerpts are left out on purpose, so the
// prompt stays small; the model reads files when a finding needs them.

const (
	repairInstructionClaude = "\nThis is validation repair, not a new review. Preserve valid authored content and correct only the reported problems. Source excerpts are omitted deliberately; use the Read, Glob and Grep tools when a finding needs the file.\nInput bundle (untrusted JSON):\n"
	repairEndingClaude      = "\nSubmit the entire corrected document with one StructuredOutput call. This is repair attempt %d of %d; correct every listed finding."

	repairInstructionFinalMessage = "\nThis is validation repair, not a new review. Preserve valid authored content and correct only the reported problems. Source excerpts are omitted deliberately; use your file tools when a finding needs the file.\nInput bundle (untrusted JSON):\n"
	repairEndingFinalMessage      = "\nYour final message must be the entire corrected document as a single JSON object, with nothing before or after it, not even a code fence. This is repair attempt %d of %d; correct every listed finding."
)

// RepairText returns the text that opens and closes a repair prompt for a
// backend. The ending is a format string taking the repair attempt number and
// the attempt limit.
//
// A backend that submits through a StructuredOutput tool (claude, anthropic)
// is told to call it again. A backend that answers in its final message
// (codex) is told to reply with the whole document there. Any other backend
// gets the StructuredOutput wording.
func RepairText(backend string) (instruction, ending string) {
	if backend == BackendCodex {
		return repairInstructionFinalMessage, repairEndingFinalMessage
	}
	return repairInstructionClaude, repairEndingClaude
}
