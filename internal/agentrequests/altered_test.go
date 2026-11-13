package agentrequests

import (
	"strings"
	"testing"
)

// A request is Altered when what the app shows of its tool, input or
// questions is not what the agent sent: a question cut at a bound, or text
// cleaned or masked. A permission request whose input would be cut is not
// shown at all, since approving applies to the whole input.
func TestAlteredRequestsAreMarked(t *testing.T) {
	s := &Store{Mask: func(text string) string { return strings.ReplaceAll(text, "SECRET", "[masked]") }}
	exact := strings.Repeat("ü", MaxValue)
	if _, ok := s.newPending(Ask{Tool: "Bash", Input: []Row{{Label: "Command", Value: exact + "x"}}}); ok {
		t.Error("a permission request with a cut value was shown")
	}
	for name, tt := range map[string]struct {
		ask     Ask
		altered bool
	}{
		"exact at the bound": {Ask{Tool: "Bash", Input: []Row{{Label: "Command", Value: exact}}}, false},
		"multi-line":         {Ask{Tool: "Bash", Input: []Row{{Label: "Command", Value: "go vet\n\tgo test"}}}, false},
		"masked":             {Ask{Tool: "Bash", Input: []Row{{Label: "Command", Value: "curl -H SECRET"}}}, true},
		"format character":   {Ask{Tool: "Bash", Input: []Row{{Label: "Command", Value: "echo a\u202eb"}}}, true},
		"control character":  {Ask{Tool: "Bash", Input: []Row{{Label: "Command", Value: "echo a\x1bb"}}}, true},
		"tool cleaned":       {Ask{Tool: "Bash\u200b", Input: []Row{{Label: "Command", Value: "ls"}}}, true},
		"label folded":       {Ask{Tool: "Bash", Input: []Row{{Label: "Com  mand", Value: "ls"}}}, true},
		"question cut": {Ask{Tool: "AskUserQuestion", Kind: KindQuestion, Questions: []Question{
			{Question: strings.Repeat("q", maxText+1), Options: []Option{{Label: "a"}}}}}, true},
		"option masked": {Ask{Tool: "AskUserQuestion", Kind: KindQuestion, Questions: []Question{
			{Question: "Which?", Options: []Option{{Label: "SECRET"}}}}}, true},
		"question exact": {Ask{Tool: "AskUserQuestion", Kind: KindQuestion, Questions: []Question{
			{Question: "Which?", Options: []Option{{Label: "a", Description: "the first"}}}}}, false},
	} {
		p, ok := s.newPending(tt.ask)
		if !ok {
			t.Errorf("%s: not shown", name)
			continue
		}
		if p.Altered != tt.altered {
			t.Errorf("%s: altered %v", name, p.Altered)
		}
	}
}
