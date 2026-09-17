// Package textclean makes text captured from agents safe to store and print:
// tool output is captured verbatim from processes that emit binary, so it can
// carry NUL bytes and invalid UTF-8. Substituting U+FFFD keeps the surrounding
// text readable and marks where a byte was dropped rather than silently
// shortening the value.
package textclean

import (
	"strings"
	"unicode/utf8"
)

const replacement = "�"

// SafeText returns s with NUL bytes and invalid UTF-8 replaced by U+FFFD.
func SafeText(s string) string {
	if s == "" {
		return s
	}
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", replacement)
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, replacement)
	}
	return s
}
