// Package prompttext decides what part of a recorded user turn is a prompt
// somebody actually typed.
//
// It lives on its own because two places need the same answer and they must not
// drift: the Claude Code adapter uses it at parse time to pick which record wins
// a turn, and the analytics read path uses it to choose which turn to show as
// "the prompt that produced this commit". Restating the rule in SQL for the
// second would give the product two definitions of the same thing, and the
// symptom of a disagreement is not an error — it is a commit page confidently
// captioned "/model".
//
// Pure functions over strings, no I/O. Reviewed as data, like the price table.
package prompttext

import (
	"regexp"
	"strings"
	"unicode"
)

// boilerplate are the wrapper constructs Claude Code writes into user records
// that carry no prompt of their own: the slash-command echo and its local
// output, the caveat prepended to them, the IDE's open-file/selection context,
// and the interrupt marker. Each is always emitted closed, so stripping them
// leaves exactly the prose the person typed.
var boilerplate = []*regexp.Regexp{
	regexp.MustCompile(`(?s)<command-name>.*?</command-name>`),
	regexp.MustCompile(`(?s)<command-message>.*?</command-message>`),
	regexp.MustCompile(`(?s)<command-args>.*?</command-args>`),
	regexp.MustCompile(`(?s)<local-command-stdout>.*?</local-command-stdout>`),
	regexp.MustCompile(`(?s)<local-command-caveat>.*?</local-command-caveat>`),
	regexp.MustCompile(`(?s)<ide_opened_file>.*?</ide_opened_file>`),
	regexp.MustCompile(`(?s)<ide_selection>.*?</ide_selection>`),
	regexp.MustCompile(`\[Request interrupted by user[^\]]*\]`),
}

// Strip removes the wrapper constructs, leaving the prose around them.
func Strip(text string) string {
	for _, pattern := range boilerplate {
		text = pattern.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(text)
}

// IsBoilerplate reports whether text is only wrapper constructs, and so
// describes no prompt a person actually typed.
func IsBoilerplate(text string) bool {
	return Strip(text) == ""
}

// Preview is the stripped prose, cut to limit characters.
//
// Characters, not bytes: a byte-wise cut splits a multi-byte rune and produces
// a replacement character in the middle of a word. The cut also backs up to the
// last space when one is near enough, because a preview ending mid-word reads
// as corruption rather than as truncation.
func Preview(text string, limit int) (preview string, truncated bool) {
	stripped := Strip(text)
	if limit <= 0 {
		return "", stripped != ""
	}

	runes := []rune(stripped)
	if len(runes) <= limit {
		return stripped, false
	}

	cut := string(runes[:limit])
	if space := strings.LastIndexFunc(cut, unicode.IsSpace); space > limit/2 {
		cut = cut[:space]
	}
	return strings.TrimRightFunc(cut, unicode.IsSpace), true
}
