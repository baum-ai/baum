package entirecheckpoint

import (
	"regexp"
	"strings"
)

// TrailerKey is the commit trailer Entire writes to name the checkpoint that
// recorded the commit's sessions.
const TrailerKey = "Entire-Checkpoint"

// Lengths of the two checkpoint id formats Entire writes.
const (
	legacyIDLength = 12
	ulidIDLength   = 26
)

// CanonicalID reports whether s is an Entire checkpoint id and returns its
// canonical spelling. Entire writes two formats: a legacy 12-character hex id,
// always lower-case, and a 26-character Crockford base32 ULID, always
// upper-case. Either is accepted in any case and returned in the case Entire
// writes it, so the same checkpoint is one key wherever it is stored.
//
// A ULID is validated as Entire validates it: the Crockford alphabet without
// the aliases I, L, O and U, which Entire never emits, and a first character
// of at most 7, since a larger one overflows the 48-bit timestamp.
func CanonicalID(s string) (string, bool) {
	switch len(s) {
	case legacyIDLength:
		id := strings.ToLower(s)
		if len(id) != legacyIDLength {
			return "", false
		}
		for i := range len(id) {
			if !isHex(id[i]) {
				return "", false
			}
		}
		return id, true
	case ulidIDLength:
		id := strings.ToUpper(s)
		// Case mapping can change a non-ASCII value's length ("ſ" upper-cases
		// to "S"), so the length is checked again on what is validated.
		if len(id) != ulidIDLength || id[0] < '0' || id[0] > '7' {
			return "", false
		}
		for i := range len(id) {
			if !isCrockford(id[i]) {
				return "", false
			}
		}
		return id, true
	default:
		return "", false
	}
}

// ParseTrailers reads a commit's Entire-Checkpoint trailers, given as
// "Key: value" lines. The key is matched without regard to case. ids holds
// every valid checkpoint id in canonical form, in trailer order, each once,
// as Entire's own ParseAllCheckpoints returns them. malformed holds every
// other value, trimmed, in trailer order, each once, so a caller can report
// what it skipped. Both are empty when the commit names no checkpoint at all.
func ParseTrailers(trailers []string) (ids, malformed []string) {
	seen := map[string]bool{}
	skipped := map[string]bool{}
	for _, trailer := range trailers {
		key, value, found := strings.Cut(trailer, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(key), TrailerKey) {
			continue
		}
		value = strings.TrimSpace(value)
		id, ok := CanonicalID(value)
		if !ok {
			if !skipped[value] {
				skipped[value] = true
				malformed = append(malformed, value)
			}
			continue
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, malformed
}

// ParseMessage reads every Entire-Checkpoint a commit message names, wherever
// in the message it sits, and returns what ParseTrailers returns for them.
// This is how a commit is read for its checkpoints, not from git's trailer
// block alone: a GitHub squash merge copies each squashed commit's message,
// trailers included, into its body, so the checkpoints of all but the last
// squashed commit sit in body paragraphs the trailer block does not include.
func ParseMessage(message string) (ids, malformed []string) {
	return ParseTrailers(MessageTrailers(message))
}

// entireCheckpointPattern is Entire's own checkpointTrailerRegex
// (cmd/entire/cli/trailers in entireio/cli), which its ParseAllCheckpoints
// runs over the whole commit message: the key, case-sensitive and anywhere in
// the text, whitespace that may span lines, then a legacy 12-hex id or the
// shape of a ULID in the case Entire writes it, ending at whitespace or at the
// end of the message.
var entireCheckpointPattern = regexp.MustCompile(
	TrailerKey + `:\s*((?:[0-9a-f]{12}|[0-9ABCDEFGHJKMNPQRSTVWXYZ]{26}))(?:\s|$)`)

// MessageTrailers returns, as "Entire-Checkpoint: <value>" lines in message
// order for ParseTrailers, every Entire-Checkpoint value a commit message
// names. It is the union of two readings:
//
//   - Every id Entire's own pattern finds that CanonicalID accepts, which is
//     what Entire links the commit to. The pattern is not anchored to a line,
//     so an id after the key in the middle of a line counts, as it does for
//     Entire.
//   - Every line shaped like the trailer (the key at the start of the line, in
//     any case, then optional blanks and a colon), as git's trailer block
//     would hold it, wherever the line sits. This keeps what ParseTrailers
//     accepts beyond Entire's pattern (a key or an id in another case) and
//     reports a malformed value. A line on which Entire's pattern found a
//     valid id is that id and nothing more, so text after the id is not
//     reported as malformed.
//
// A value the key introduces in the middle of a line that is not an id is
// prose about the trailer, not a malformed trailer, and is ignored.
func MessageTrailers(message string) []string {
	matches := entireCheckpointPattern.FindAllStringSubmatchIndex(message, -1)
	var trailers []string
	next := 0
	for start := 0; start <= len(message); {
		end := len(message)
		if newline := strings.IndexByte(message[start:], '\n'); newline >= 0 {
			end = start + newline
		}
		found := false
		for ; next < len(matches) && matches[next][0] <= end; next++ {
			id := message[matches[next][2]:matches[next][3]]
			if _, ok := CanonicalID(id); ok {
				found = true
				trailers = append(trailers, TrailerKey+": "+id)
			}
		}
		if line := message[start:end]; !found && isTrailerLine(line) {
			_, value, _ := strings.Cut(line, ":")
			trailers = append(trailers, TrailerKey+":"+value)
		}
		start = end + 1
	}
	return trailers
}

// isTrailerLine reports whether a message line is shaped like an
// Entire-Checkpoint trailer as git reads one: the key at the very start of the
// line, in any case, then optional spaces or tabs and the separator.
func isTrailerLine(line string) bool {
	if len(line) < len(TrailerKey) || !strings.EqualFold(line[:len(TrailerKey)], TrailerKey) {
		return false
	}
	return strings.HasPrefix(strings.TrimLeft(line[len(TrailerKey):], " \t"), ":")
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f'
}

// isCrockford reports whether an upper-case byte is in Crockford's base32
// alphabet: digits and letters, without I, L, O and U.
func isCrockford(c byte) bool {
	switch {
	case '0' <= c && c <= '9':
		return true
	case 'A' <= c && c <= 'Z':
		return c != 'I' && c != 'L' && c != 'O' && c != 'U'
	default:
		return false
	}
}
