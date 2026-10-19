package entirecheckpoint

import (
	"slices"
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
