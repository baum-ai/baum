package agenthooks_test

import (
	"slices"
	"testing"

	"github.com/baum-ai/baum/hosted/agenthooks"
)

// TestUserDispatcherIsFrozen pins the user-wide command. Codex remembers a
// user's trust in a hook by its exact definition, so changing this string
// makes every user review baum's hooks again.
func TestUserDispatcherIsFrozen(t *testing.T) {
	spec, _ := agenthooks.Lookup("claude-code")
	want := `sh -c 'if command -v baum >/dev/null 2>&1 && baum hooks agent --check --user claude-code stop >/dev/null 2>&1; then exec baum hooks agent --user claude-code stop; fi'`
	if got := agenthooks.UserDispatcher(spec, "stop"); got != want {
		t.Errorf("UserDispatcher = %s\nwant %s", got, want)
	}
}

func TestUserWidePlacementsCoverEveryEvent(t *testing.T) {
	for _, spec := range agenthooks.All() {
		settings, ok := agenthooks.LookupUserWide(spec.Slug)
		if !ok {
			continue
		}
		var events []string
		for _, placement := range settings.Placements {
			events = append(events, placement.Event)
		}
		slices.Sort(events)
		want := slices.Clone(spec.Events)
		slices.Sort(want)
		if !slices.Equal(events, want) {
			t.Errorf("%s placements cover %v, want %v", spec.Slug, events, want)
		}
		if !slices.Contains(settings.ProjectFiles, spec.SettingsFile) {
			t.Errorf("%s project files %v miss the committed %s", spec.Slug, settings.ProjectFiles, spec.SettingsFile)
		}
	}
	if _, ok := agenthooks.LookupUserWide("cursor"); ok {
		t.Error("cursor has user-wide hooks")
	}
}

func TestHandlesEventMatchesEveryCommandThatRunsEntire(t *testing.T) {
	spec, _ := agenthooks.Lookup("claude-code")
	for name, command := range map[string]string{
		"dispatcher":  agenthooks.Dispatcher(spec, "stop"),
		"Entire":      agenthooks.EntireCommand(spec, "stop"),
		"wrapped":     "./before.sh && entire hooks claude-code stop",
		"end of file": "entire hooks claude-code stop",
	} {
		if !agenthooks.HandlesEvent([]byte(encoded(t, command)), spec, "stop") && !agenthooks.HandlesEvent([]byte(command), spec, "stop") {
			t.Errorf("%s: %q does not handle stop", name, command)
		}
	}
	other := encoded(t, agenthooks.Dispatcher(spec, "subagent-stop"))
	if agenthooks.HandlesEvent([]byte(other), spec, "stop") {
		t.Error("subagent-stop counted as stop")
	}
	if agenthooks.HandlesEvent([]byte(encoded(t, agenthooks.UserDispatcher(spec, "stop"))), spec, "stop") {
		t.Error("the user-wide dispatcher counted as a project hook")
	}
	codex, _ := agenthooks.Lookup("codex")
	if agenthooks.HandlesEvent([]byte(encoded(t, agenthooks.Dispatcher(spec, "stop"))), codex, "stop") {
		t.Error("a Claude Code hook counted for Codex")
	}
}
