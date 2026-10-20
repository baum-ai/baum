package agenthooks_test

import (
	"strings"
	"testing"

	"github.com/baum-ai/baum/hosted/agenthooks"
)

func encoded(t *testing.T, command string) string {
	t.Helper()
	out, err := agenthooks.EncodeJSON(command)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestOwnCommandsKeepsOneHookPerEvent covers the layout `entire enable` leaves
// behind when it no longer recognises the hooks it wrote: baum's
// dispatcher for an event followed by a fresh copy of Entire's own command. Only
// the first hook entry survives, and the file keeps its formatting.
func TestOwnCommandsKeepsOneHookPerEvent(t *testing.T) {
	spec, _ := agenthooks.Lookup("claude-code")
	dispatcher := encoded(t, agenthooks.Dispatcher(spec, "stop"))
	entire := encoded(t, agenthooks.EntireCommand(spec, "stop"))
	input := `{
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": ` + dispatcher + `
          },
          {
            "type": "command",
            "command": ` + entire + `
          },
          {
            "type": "command",
            "command": ` + dispatcher + `
          },
          {
            "type": "command",
            "command": "./scripts/after-stop.sh"
          }
        ]
      }
    ]
  }
}
`
	want := `{
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": ` + dispatcher + `
          },
          {
            "type": "command",
            "command": "./scripts/after-stop.sh"
          }
        ]
      }
    ]
  }
}
`
	got, err := agenthooks.OwnCommands([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("OwnCommands =\n%s\nwant\n%s", got, want)
	}
	again, err := agenthooks.OwnCommands(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != want {
		t.Errorf("second pass changed the file:\n%s", again)
	}
}

// TestOwnCommandsDropsARepeatThatOpensItsArray takes the comma after the entry
// when there is none before it, so the array stays valid JSON. Cursor's flat
// entries are the shape; the event appears once under each of two keys to put
// the repeat first in its array.
func TestOwnCommandsDropsARepeatThatOpensItsArray(t *testing.T) {
	spec, _ := agenthooks.Lookup("cursor")
	entire := encoded(t, agenthooks.EntireCommand(spec, "stop"))
	dispatcher := encoded(t, agenthooks.Dispatcher(spec, "stop"))
	input := `{"version": 1, "hooks": {"stop": [{"command": ` + entire + `}], "afterStop": [{"command": ` + entire + `}, {"command": "./own.sh"}]}}`
	want := `{"version": 1, "hooks": {"stop": [{"command": ` + dispatcher + `}], "afterStop": [{"command": "./own.sh"}]}}`
	got, err := agenthooks.OwnCommands([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("OwnCommands =\n%s\nwant\n%s", got, want)
	}
}

// TestOwnCommandsLeavesDistinctEventsAlone: two events under one hook type are
// not repeats of each other, and a foreign command never counts as one.
func TestOwnCommandsLeavesDistinctEventsAlone(t *testing.T) {
	spec, _ := agenthooks.Lookup("claude-code")
	input := `{"hooks": {"PostToolUse": [` +
		`{"matcher": "Task", "hooks": [{"type": "command", "command": ` + encoded(t, agenthooks.EntireCommand(spec, "post-task")) + `}]}, ` +
		`{"matcher": "TodoWrite", "hooks": [{"type": "command", "command": ` + encoded(t, agenthooks.EntireCommand(spec, "post-todo")) + `}, {"type": "command", "command": "./own.sh"}, {"type": "command", "command": "./own.sh"}]}` +
		`]}}`
	got, err := agenthooks.OwnCommands([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"post-task", "post-todo"} {
		if !strings.Contains(string(got), encoded(t, agenthooks.Dispatcher(spec, event))) {
			t.Errorf("%s dispatcher missing from\n%s", event, got)
		}
	}
	if strings.Count(string(got), `"./own.sh"`) != 2 {
		t.Errorf("foreign commands were touched:\n%s", got)
	}
}

func TestOwnCommandsKeepsDistinctClaudeMatchersForOneEvent(t *testing.T) {
	spec, _ := agenthooks.Lookup("claude-code")
	entire := encoded(t, agenthooks.EntireCommand(spec, "post-task"))
	dispatcher := encoded(t, agenthooks.Dispatcher(spec, "post-task"))
	input := `{"hooks":{"PostToolUse":[` +
		`{"matcher":"Agent","hooks":[{"type":"command","command":` + entire + `}]},` +
		`{"matcher":"Task","hooks":[{"type":"command","command":` + entire + `}]},` +
		`{"matcher":"Agent","hooks":[{"type":"command","command":` + entire + `}]}` +
		`]}}`
	got, err := agenthooks.OwnCommands([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), dispatcher) != 2 {
		t.Errorf("Agent and Task must each retain one dispatcher:\n%s", got)
	}
	if !strings.Contains(string(got), `"matcher":"Task"`) {
		t.Errorf("legacy Task matcher was removed:\n%s", got)
	}
}

// legacyName is the CLI's name before the product was renamed to baum, spelled
// in parts so the published sources do not carry it.
const legacyName = "recount" + "able"

// legacyCursorStop is the exact dispatcher committed for Cursor's stop event
// before the product was renamed to baum.
var legacyCursorStop = strings.ReplaceAll(`sh -c 'if command -v {old} >/dev/null 2>&1 && {old} hooks agent --check cursor stop >/dev/null 2>&1; then exec {old} hooks agent cursor stop; fi; if ! command -v entire >/dev/null 2>&1; then exit 0; fi; exec entire hooks cursor stop'`, "{old}", legacyName)

// TestOwnedCommandRecognisesThePreRenameDispatcher keeps repositories set up
// before the rename owned, so setup and onboarding replace their hooks rather
// than adding a second one beside them.
func TestOwnedCommandRecognisesThePreRenameDispatcher(t *testing.T) {
	spec, event, ok := agenthooks.OwnedCommand(legacyCursorStop)
	if !ok || spec.Slug != "cursor" || event != "stop" {
		t.Fatalf("OwnedCommand(legacy) = %q %q %v, want cursor stop true", spec.Slug, event, ok)
	}
}

// TestOwnCommandsReplacesThePreRenameDispatcher covers a pre-rename checkout
// after `entire agent add` appended a fresh copy of Entire's own command: one
// baum dispatcher is left and nothing of the old one survives.
func TestOwnCommandsReplacesThePreRenameDispatcher(t *testing.T) {
	spec, _ := agenthooks.Lookup("cursor")
	input := `{
  "hooks": {
    "stop": [
      {
        "command": ` + encoded(t, legacyCursorStop) + `
      },
      {
        "command": ` + encoded(t, agenthooks.EntireCommand(spec, "stop")) + `
      }
    ]
  }
}
`
	want := `{
  "hooks": {
    "stop": [
      {
        "command": ` + encoded(t, agenthooks.Dispatcher(spec, "stop")) + `
      }
    ]
  }
}
`
	got, err := agenthooks.OwnCommands([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("OwnCommands =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(string(got), legacyName) {
		t.Errorf("the pre-rename dispatcher survived:\n%s", got)
	}
}
