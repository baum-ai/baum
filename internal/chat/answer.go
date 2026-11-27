package chat

// Answer is the agent's answer to one question, as a completed turn stores
// it (store.Turn.Answer).
type Answer struct {
	Markdown    string      `json:"answer_markdown"`
	Citations   []Citation  `json:"citations"`
	SuggestsFix *Suggestion `json:"suggests_fix"`
}

// Citation is lines of a file at the pull request's head, by its
// repository-relative path.
type Citation struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// Suggestion is a concrete change the answer says should be made.
type Suggestion struct {
	Summary string `json:"summary"`
}
