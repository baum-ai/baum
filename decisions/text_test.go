package decisions

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"
)

func TestPrefixHashAndExtensionCutoff(t *testing.T) {
	earlier := &Transcript{SessionID: "s", Turns: []Turn{
		{Ordinal: 1, Human: "add a gate", EditedFiles: []string{"a.go"}},
		{Ordinal: 4, Human: "use a header"},
		{Ordinal: 7, Human: "add tests", Events: []Event{{Kind: EventAgentText, Text: "Adding"}}},
	}}
	if sum := sha256.Sum256([]byte(earlier.Text())); ContentHash(earlier) != hex.EncodeToString(sum[:]) {
		t.Errorf("ContentHash = %s", ContentHash(earlier))
	}
	prefix := PrefixHash(earlier)
	if prefix != ContentHash(&Transcript{Turns: earlier.Turns[:2]}) {
		t.Errorf("PrefixHash = %s, want the hash of the first two turns", prefix)
	}
	if PrefixHash(&Transcript{Turns: earlier.Turns[:1]}) != "" {
		t.Error("PrefixHash of one turn is not empty")
	}
	grown := &Transcript{SessionID: "s", Turns: slices.Clone(earlier.Turns)}
	// The open last turn keeps growing, and a turn follows it.
	grown.Turns[2].Events = append(slices.Clone(grown.Turns[2].Events), Event{Kind: EventAgentText, Text: "Added"})
	grown.Turns = append(grown.Turns, Turn{Ordinal: 9, Human: "ship it"})
	if cutoff, ok := ExtensionCutoff(grown, prefix); !ok || cutoff != 4 {
		t.Errorf("grown: cutoff %d, %v; want 4, true", cutoff, ok)
	}
	if cutoff, ok := ExtensionCutoff(earlier, prefix); !ok || cutoff != 4 {
		t.Errorf("unchanged: cutoff %d, %v; want 4, true", cutoff, ok)
	}
	changed := &Transcript{SessionID: "s", Turns: slices.Clone(grown.Turns)}
	changed.Turns[0].Human = "add a login gate"
	if _, ok := ExtensionCutoff(changed, prefix); ok {
		t.Error("changed earlier turn: matched")
	}
	if _, ok := ExtensionCutoff(&Transcript{Turns: earlier.Turns[:2]}, prefix); ok {
		t.Error("prefix alone, without the turn after it: matched")
	}
	if _, ok := ExtensionCutoff(grown, ""); ok {
		t.Error("empty prefix hash: matched")
	}
}
