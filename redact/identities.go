package redact

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var emailPattern = regexp.MustCompile(`(?i)[\p{L}\p{M}0-9._%+-]+(?:@|%40)[\p{L}\p{M}0-9.-]+\.[\p{L}\p{M}]{2,}`)

// Identities removes what must never be published in text sent to a
// code host: an email address, replaced with "[redacted]", and an @mention, which
// would notify someone on every edit. The mention keeps its text and loses
// only the "@", which reads naturally and cannot ping. It reports whether
// anything was changed.
func Identities(text string) (string, bool) {
	cleaned := redactEmails(text)
	cleaned = mentionPattern.ReplaceAllStringFunc(cleaned, func(match string) string {
		return strings.Replace(match, "@", "", 1)
	})
	return cleaned, cleaned != text
}

// redactEmails replaces each address emailPattern finds, unless a word
// character follows it: "ada@example.com_x" and "ada@example.com2" are no
// address. Go's \b is ASCII-only and the pattern admits Unicode letters, and
// regexp has no lookahead, so the following character is checked here.
func redactEmails(text string) string {
	var b strings.Builder
	last := 0
	for _, m := range emailPattern.FindAllStringIndex(text, -1) {
		if r, _ := utf8.DecodeRuneInString(text[m[1]:]); r != utf8.RuneError &&
			(r == '_' || unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsDigit(r)) {
			continue
		}
		b.WriteString(text[last:m[0]])
		b.WriteString("[redacted]")
		last = m[1]
	}
	b.WriteString(text[last:])
	return b.String()
}
