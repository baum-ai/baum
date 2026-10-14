package redact

import (
	"regexp"
	"strings"
)

var emailPattern = regexp.MustCompile(`(?i)[\p{L}\p{M}0-9._%+-]+(?:@|%40)[\p{L}\p{M}0-9.-]+\.[\p{L}\p{M}]{2,}`)

// Identities removes what must never be published in text sent to a
// code host: an email address, replaced with "[redacted]", and an @mention, which
// would notify someone on every edit. The mention keeps its text and loses
// only the "@", which reads naturally and cannot ping. It reports whether
// anything was changed.
func Identities(text string) (string, bool) {
	cleaned := emailPattern.ReplaceAllString(text, "[redacted]")
	cleaned = mentionPattern.ReplaceAllStringFunc(cleaned, func(match string) string {
		return strings.Replace(match, "@", "", 1)
	})
	return cleaned, cleaned != text
}
