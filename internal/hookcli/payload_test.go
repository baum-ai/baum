//go:build !windows

package hookcli

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestTopLevelStringsKeepsOnlyNamedTopLevelStrings(t *testing.T) {
	reader := newTopLevelStrings("id", "path", "count")
	input := `{"nested":{"id":"inner","list":["id",{"id":"x"}]},"count":3,` +
		`"path":"C:\\dir\\\"quoted\" \u00e9","skipped":"` + strings.Repeat("v", maxPayloadValue+1) + `","id":"outer","id":"second"}`
	// Fed in small pieces, as a pipe delivers it.
	for chunk := range slices.Chunk([]byte(input), 7) {
		_, _ = reader.Write(chunk)
	}
	want := map[string]string{"id": "second", "path": `C:\dir\"quoted" é`}
	if !maps.Equal(reader.values, want) {
		t.Errorf("values = %q, want %q", reader.values, want)
	}
	for _, input := range []string{`["id","x"]`, `"id"`, `nonsense {"id":"x"}`} {
		reader := newTopLevelStrings("id")
		_, _ = reader.Write([]byte(input))
		if len(reader.values) != 0 {
			t.Errorf("%s yielded %q", input, reader.values)
		}
	}
	long := newTopLevelStrings("id")
	_, _ = long.Write([]byte(`{"id":"` + strings.Repeat("v", maxPayloadValue+1) + `"}`))
	if len(long.values) != 0 {
		t.Error("a value over the bound was kept")
	}
}

func TestLargeAgentPayloadStillYieldsItsIdentifiers(t *testing.T) {
	paths := useTestHookDaemon(t)
	payload := `{"tool_input":{"session_id":"nested"},"tool_response":"` + strings.Repeat("x", 2*maxHookInput) + `","session_id":"s-big","cwd":"/work/demo"}`
	if _, _, err := runCLIWithInput(t, payload, "hooks", "agent", "claude-code", "stop"); err != nil {
		t.Fatal(err)
	}
	events, _ := spooledHookEvents(t, paths)
	if len(events) != 1 || events[0].Session == nil || events[0].Session.ID != "s-big" || events[0].Session.Cwd != "/work/demo" {
		t.Fatalf("events = %+v", events)
	}
}
