package entirecheckpoint

import (
	"slices"
	"strings"
	"testing"
)

const (
	testHexID  = "a3b2c4d5e6f7"
	testULID   = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	otherHexID = "0123456789ab"
)

func TestCanonicalID(t *testing.T) {
	for _, test := range []struct {
		name, in, want string
		ok             bool
	}{
		{"hex lower", testHexID, testHexID, true},
		{"hex upper", "A3B2C4D5E6F7", testHexID, true},
		{"hex mixed", "a3B2c4D5e6F7", testHexID, true},
		{"ulid upper", testULID, testULID, true},
		{"ulid lower", "01arz3ndektsv4rrffq69g5fav", testULID, true},
		{"ulid mixed", "01ArZ3nDeKtSv4RrFfQ69g5FaV", testULID, true},
		{"ulid first char 7", "7ZZZZZZZZZZZZZZZZZZZZZZZZZ", "7ZZZZZZZZZZZZZZZZZZZZZZZZZ", true},
		{"ulid first char 8 overflows", "8ZZZZZZZZZZZZZZZZZZZZZZZZZ", "", false},
		{"ulid alias I", "01ARZ3NDEKTSV4RRFFQ69G5FAI", "", false},
		{"ulid alias L", "01ARZ3NDEKTSV4RRFFQ69G5FAL", "", false},
		{"ulid alias O", "01ARZ3NDEKTSV4RRFFQ69G5FAO", "", false},
		{"ulid alias U", "01ARZ3NDEKTSV4RRFFQ69G5FAU", "", false},
		{"ulid lower alias", "01arz3ndektsv4rrffq69g5fal", "", false},
		{"hex with a non-hex letter", "a3b2c4d5e6fg", "", false},
		{"hex too short", "a3b2c4d5e6f", "", false},
		{"hex too long", "a3b2c4d5e6f70", "", false},
		{"ulid too short", testULID[:25], "", false},
		{"ulid too long", testULID + "0", "", false},
		{"ulid punctuation", "01ARZ3NDEKTSV4RRFFQ69G5FA-", "", false},
		{"non-ASCII that upper-cases shorter", "01ARZ3NDEKTSV4RRFFQ69G5Fſ", "", false},
		{"empty", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := CanonicalID(test.in)
			if got != test.want || ok != test.ok {
				t.Fatalf("CanonicalID(%q) = %q, %v; want %q, %v", test.in, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestParseTrailers(t *testing.T) {
	for _, test := range []struct {
		name           string
		trailers       []string
		ids, malformed []string
	}{
		{name: "absent", trailers: []string{"Co-authored-by: Codex <x@example.com>"}},
		{name: "one hex", trailers: []string{"Entire-Checkpoint: " + testHexID}, ids: []string{testHexID}},
		{
			name:     "key in any case, value trimmed and canonical",
			trailers: []string{"entire-checkpoint:   A3B2C4D5E6F7  ", "ENTIRE-CHECKPOINT: 01arz3ndektsv4rrffq69g5fav"},
			ids:      []string{testHexID, testULID},
		},
		{
			name:     "repeats collapse whatever their case",
			trailers: []string{"Entire-Checkpoint: " + testHexID, "Entire-Checkpoint: A3B2C4D5E6F7", "Entire-Checkpoint: " + testHexID},
			ids:      []string{testHexID},
		},
		{
			name: "order kept",
			trailers: []string{
				"Entire-Checkpoint: " + testULID, "Entire-Checkpoint: " + otherHexID,
				"Entire-Checkpoint: " + testHexID, "Entire-Checkpoint: " + otherHexID,
			},
			ids: []string{testULID, otherHexID, testHexID},
		},
		{
			name: "malformed among valid is skipped",
			trailers: []string{
				"Entire-Checkpoint: " + testHexID, "Entire-Checkpoint: nope",
				"Entire-Checkpoint: " + testULID,
			},
			ids: []string{testHexID, testULID}, malformed: []string{"nope"},
		},
		{
			name:      "all malformed",
			trailers:  []string{"Entire-Checkpoint: nope", "Entire-Checkpoint:", "Entire-Checkpoint: 01ARZ3NDEKTSV4RRFFQ69G5FAL"},
			malformed: []string{"nope", "", "01ARZ3NDEKTSV4RRFFQ69G5FAL"},
		},
		{name: "not a trailer line", trailers: []string{"Entire-Checkpoint " + testHexID}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ids, malformed := ParseTrailers(test.trailers)
			if !slices.Equal(ids, test.ids) || !slices.Equal(malformed, test.malformed) {
				t.Fatalf("ParseTrailers() = %q, %q; want %q, %q", ids, malformed, test.ids, test.malformed)
			}
		})
	}
}

// squashMergeMessage is the message GitHub writes for a squash merge: the pull
// request title, then each squashed commit's message, trailers included, as a
// body paragraph, then the co-authors in the trailer block.
func squashMergeMessage(trailerBlock ...string) string {
	return strings.Join(append([]string{
		"Add the widget (#42)",
		"",
		"* feat: first step",
		"",
		"Entire-Checkpoint: " + testHexID,
		"",
		"* fix: second step",
		"",
		"Explain why.",
		"",
		"Co-authored-by: Claude <noreply@anthropic.com>",
		"Entire-Checkpoint: " + testULID,
		"",
		"* chore: third step",
		"",
		"---------",
		"",
	}, trailerBlock...), "\n")
}

func TestParseMessage(t *testing.T) {
	for _, test := range []struct {
		name           string
		message        string
		ids, malformed []string
	}{
		{name: "no checkpoint", message: "fix: a bug\n\nCo-authored-by: Codex <x@example.com>"},
		{
			name:    "trailer block only",
			message: "fix: a bug\n\nBody.\n\nEntire-Checkpoint: " + testHexID,
			ids:     []string{testHexID},
		},
		{
			name: "squash merge: body paragraphs and the trailer block, a repeat counted once",
			message: squashMergeMessage(
				"Co-authored-by: Ada <ada@example.com>",
				"Entire-Checkpoint: "+otherHexID,
				"Entire-Checkpoint: "+testHexID,
			),
			ids: []string{testHexID, testULID, otherHexID},
		},
		{
			name: "malformed body value beside valid ones",
			message: "Add the widget (#42)\n\n* feat: first\n\nEntire-Checkpoint: not-a-checkpoint\n\n" +
				"* feat: second\n\nEntire-Checkpoint: " + testHexID + "\n\nCo-authored-by: Ada <ada@example.com>",
			ids: []string{testHexID}, malformed: []string{"not-a-checkpoint"},
		},
		{
			name: "only malformed values, a repeat reported once",
			message: "Add the widget (#42)\n\n* feat: first\n\nEntire-Checkpoint: nope\n\n* feat: second\n\n" +
				"Entire-Checkpoint: 01ARZ3NDEKTSV4RRFFQ69G5FAL\n\nEntire-Checkpoint: nope",
			malformed: []string{"nope", "01ARZ3NDEKTSV4RRFFQ69G5FAL"},
		},
		{
			name:    "an id after the key mid-line counts, as Entire's pattern finds it",
			message: "Revert the change\n\nThis drops Entire-Checkpoint: " + testHexID + " from the release.",
			ids:     []string{testHexID},
		},
		{
			name:    "a key mid-line before prose is not a malformed trailer",
			message: "docs: explain the Entire-Checkpoint: trailer\n\nRead Entire-Checkpoint: lines anywhere.",
		},
		{
			name:    "a key not followed by its colon is not a trailer",
			message: "fix: x\n\nEntire-Checkpoint " + testHexID + "\nEntire-Checkpoints: " + testHexID,
		},
		{
			name:    "an id must end at whitespace or the end of the message",
			message: "fix: x\n\nsee Entire-Checkpoint: " + testHexID + ".",
		},
		{
			name: "text after a valid id on a trailer line is not reported",
			message: "fix: x\n\nEntire-Checkpoint: " + testHexID + " (squashed)\n" +
				"Entire-Checkpoint: " + testULID + "\r\nEntire-Checkpoint:\n  " + otherHexID,
			ids: []string{testHexID, testULID, otherHexID},
		},
		{
			name:    "a trailer line in another case is read as ParseTrailers reads it",
			message: "fix: x\n\n* feat: y\n\nentire-checkpoint : A3B2C4D5E6F7\n\n* feat: z",
			ids:     []string{testHexID},
		},
		{
			name:      "a ULID shape whose timestamp overflows is malformed",
			message:   "fix: x\n\nEntire-Checkpoint: 8ZZZZZZZZZZZZZZZZZZZZZZZZZ",
			malformed: []string{"8ZZZZZZZZZZZZZZZZZZZZZZZZZ"},
		},
		{name: "empty", message: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			ids, malformed := ParseMessage(test.message)
			if !slices.Equal(ids, test.ids) || !slices.Equal(malformed, test.malformed) {
				t.Fatalf("ParseMessage() = %q, %q; want %q, %q", ids, malformed, test.ids, test.malformed)
			}
		})
	}
}
