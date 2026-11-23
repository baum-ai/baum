package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/tiles/library"
)

// A saved type's fill frames its instructions as the person's; a built-in
// type's does not. Its title's whitespace is collapsed.
func TestSavedTypeInstructionsAreThePersons(t *testing.T) {
	entry, _ := library.Get("schema_change")
	def := entry.Definition
	def.Name = "saved_schema"
	encoded, err := library.Seal(&def)
	if err != nil {
		t.Fatal(err)
	}
	saved := entry
	saved.Definition, saved.Encoded = def, encoded
	if text := typeInstructions(saved); !strings.Contains(text, savedTypeNote) || !strings.HasSuffix(text, saved.Definition.Fill+"\n") {
		t.Errorf("saved instructions: %.300s", text)
	}
	if !strings.Contains(savedTypeNote, "never change the review, its risks or its recommendation") {
		t.Error("the note does not say the person's instructions only add")
	}
	if text := typeInstructions(entry); strings.Contains(text, "tile library") {
		t.Errorf("built-in instructions: %.300s", text)
	}
	saved.Definition.Title = "Saved\n## Injected   heading"
	if text := typeInstructions(saved); !strings.Contains(text, "## The tile type: Saved ## Injected heading (`saved_schema`)\n") {
		t.Errorf("title not collapsed: %.200s", text)
	}
}

// A strict reply's null lines are dropped before the data is checked.
func TestDropNullLines(t *testing.T) {
	strict := `{"cites":[{"type":"hunk","id":"h_1","lines":null},{"type":"hunk","id":"h_2","lines":[1,2]}]}`
	got := string(dropNullLines(json.RawMessage(strict)))
	if strings.Contains(got, `"lines":null`) || !strings.Contains(got, `"lines":[1,2]`) {
		t.Errorf("null lines: %s", got)
	}
	plain := `{"cites":[]}`
	if got := string(dropNullLines(json.RawMessage(plain))); got != plain {
		t.Errorf("changed: %s", got)
	}
}
