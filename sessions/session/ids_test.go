package session

import (
	"testing"

	"github.com/google/uuid"
)

func TestMintIDIsStableAndAValidUUID(t *testing.T) {
	first := MintID("user_input", "claude_code", "S", "", "line-1")
	second := MintID("user_input", "claude_code", "S", "", "line-1")
	if first != second {
		t.Fatalf("same key minted %s then %s; ids must be reproducible", first, second)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("minted id %q is not a uuid: %v", first, err)
	}
}

func TestMintIDSeparatesEntityKinds(t *testing.T) {
	// A tool use and the result answering it are named by the same vendor id.
	use := MintID("tool_use", "claude_code", "S", "", "toolu_1")
	result := MintID("tool_result", "claude_code", "S", "", "toolu_1")
	if use == result {
		t.Fatal("tool use and tool result minted the same id from one vendor id")
	}
}

// The parts of a key are vendor strings — file paths, attribute keys, prompt ids
// — and any printable separator lets two different tuples spell the same key.
// Two entities sharing an id would be merged by the database, not rejected.
func TestMintIDDoesNotConfuseAdjacentParts(t *testing.T) {
	split := MintID("file_touch", "tool-a", "b/c.go")
	joined := MintID("file_touch", "tool-a:b", "c.go")
	if split == joined {
		t.Fatal("parts ran together: two different tuples minted one id")
	}
	nested := MintID("file_touch", "tool-a", "b", "c.go")
	if split == nested {
		t.Fatal("a part boundary was lost: two different tuples minted one id")
	}
}

func TestMintIDSeparatesSessionsThatShareVendorIDs(t *testing.T) {
	// A forked Claude transcript copies its origin's lines verbatim, so the same
	// line uuid appears under two different top-level sessions.
	origin := MintID("user_input", "claude_code", "S-origin", "", "line-1")
	fork := MintID("user_input", "claude_code", "S-fork", "", "line-1")
	if origin == fork {
		t.Fatal("a forked session reused its origin's id for the same vendor line id")
	}

	// A subagent transcript carries its parent's sessionId, so the agent id is
	// what keeps their contents apart.
	main := MintID("user_input", "claude_code", "S", "", "line-1")
	sub := MintID("user_input", "claude_code", "S", "A", "line-1")
	if main == sub {
		t.Fatal("a subagent reused its parent's id for the same vendor line id")
	}
}
