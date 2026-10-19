package entirecheckpoint

import "strings"

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
// other value, trimmed, in trailer order, so a caller can report what it
// skipped. Both are empty when the commit names no checkpoint at all.
func ParseTrailers(trailers []string) (ids, malformed []string) {
	seen := map[string]bool{}
	for _, trailer := range trailers {
		key, value, found := strings.Cut(trailer, ":")
		if !found || !strings.EqualFold(strings.TrimSpace(key), TrailerKey) {
			continue
		}
		value = strings.TrimSpace(value)
		id, ok := CanonicalID(value)
		if !ok {
			malformed = append(malformed, value)
			continue
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, malformed
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
