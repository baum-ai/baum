package livesessions

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/hookevents"
)

// activities is the list's activity of each session, by agent and id, as the
// JSON the list serves.
func activities(t *testing.T, f *transcriptFixture) map[string]*Activity {
	t.Helper()
	w := httptest.NewRecorder()
	f.live.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/live-sessions", nil))
	var body struct {
		Sessions []struct {
			Agent    string    `json:"agent"`
			ID       string    `json:"session_id"`
			Activity *Activity `json:"activity"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: %v", w.Body.String(), err)
	}
	out := map[string]*Activity{}
	for _, s := range body.Sessions {
		out[s.Agent+"/"+s.ID] = s.Activity
	}
	return out
}

// toolUse is an assistant line of Claude Code with one tool call.
func toolUse(at, name, input string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"sessionId":"s1","message":{"role":"assistant","content":[{"type":"tool_use","id":"t","name":%q,"input":%s}]}}`, at, name, input)
}

func TestActivityStepsNameTheirTargetsMaskedAndRelativeToTheWorkingDirectory(t *testing.T) {
	f := newTranscriptFixture(t)
	f.tracker.Mask = func(s string) string { return strings.ReplaceAll(s, plantedDSN, "[masked]") }
	appendLines(t, f.path,
		toolUse("2026-10-27T18:01:00.000Z", "Read", `{"file_path":"/work/demo/oss/app/README.md"}`),
		toolUse("2026-10-27T18:01:01.000Z", "Edit", `{"file_path":"/elsewhere/x.go","old_string":"a","new_string":"b"}`),
		toolUse("2026-10-27T18:01:02.000Z", "Bash", `{"command":"psql `+plantedDSN+`\nselect 1"}`),
		toolUse("2026-10-27T18:01:03.000Z", "Grep", `{"pattern":"func main","path":"oss"}`),
		toolUse("2026-10-27T18:01:04.000Z", "Grep", `{"pattern":"TODO"}`),
		toolUse("2026-10-27T18:01:05.000Z", "mcp__tracker__get_item", `{"id":"item-1"}`),
		toolUse("2026-10-27T18:01:06.000Z", "Glob", `{"pattern":"**/*.go"}`),
	)
	a := activities(t, f)["claude-code/s1"]
	if a == nil {
		t.Fatal("no activity")
	}
	want := []Step{
		{"Read", "oss/app/README.md", "2026-10-27T18:01:00Z"},
		{"Edit", "/elsewhere/x.go", "2026-10-27T18:01:01Z"},
		{"Bash", "psql [masked]", "2026-10-27T18:01:02Z"},
		{"Grep", `"func main" in oss`, "2026-10-27T18:01:03Z"},
		{"Grep", `"TODO"`, "2026-10-27T18:01:04Z"},
		{"tracker: get_item", "", "2026-10-27T18:01:05Z"},
		{"Glob", "**/*.go", "2026-10-27T18:01:06Z"},
	}
	if len(a.Steps) != len(want) {
		t.Fatalf("steps %+v", a.Steps)
	}
	for i := range want {
		if a.Steps[i] != want[i] {
			t.Fatalf("step %d: %+v, want %+v", i, a.Steps[i], want[i])
		}
	}
	if a.FilesTouched != 1 || a.LastMessage != "Connected." {
		t.Fatalf("activity %+v", a)
	}
}

func TestActivityKeepsTheLatestTwelveStepsAndTheFilesTouchedAcrossPasses(t *testing.T) {
	f := newTranscriptFixture(t)
	for i := range 10 {
		appendLines(t, f.path, toolUse("2026-10-27T18:00:00Z", "Write", fmt.Sprintf(`{"file_path":"/work/demo/f%d.go"}`, i)))
	}
	if a := activities(t, f)["claude-code/s1"]; a.FilesTouched != 10 {
		t.Fatalf("files touched %d", a.FilesTouched)
	}
	// A second pass: three more writes, one of a file already written.
	appendLines(t, f.path,
		toolUse("2026-10-27T18:00:00Z", "Edit", `{"file_path":"/work/demo/f0.go"}`),
		toolUse("2026-10-27T18:00:00Z", "Edit", `{"file_path":"/work/demo/g.go"}`),
		toolUse("2026-10-27T18:00:00Z", "MultiEdit", `{"file_path":"/work/demo/h.go"}`),
	)
	f.clock.advance(TitleRecheckEvery)
	a := activities(t, f)["claude-code/s1"]
	if a.FilesTouched != 12 {
		t.Fatalf("files touched %d", a.FilesTouched)
	}
	if len(a.Steps) != 12 || a.Steps[0].Target != "f1.go" || a.Steps[11].Tool != "MultiEdit" || a.Steps[11].Target != "h.go" {
		t.Fatalf("steps %+v", a.Steps)
	}
}

func TestActivityLastMessageIsTheLatestAssistantTextCutAndMasked(t *testing.T) {
	f := newTranscriptFixture(t)
	f.tracker.Mask = func(s string) string { return strings.ReplaceAll(s, plantedDSN, "[masked]") }
	long := "\n# First line  \r\n\n\n\n  second " + plantedDSN + " " + strings.Repeat("word ", 400)
	appendLines(t, f.path, fmt.Sprintf(`{"type":"assistant","timestamp":"2026-10-27T18:00:00Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":%q}]}}`, long))
	a := activities(t, f)["claude-code/s1"]
	if !strings.HasPrefix(a.LastMessage, "# First line\n\n  second [masked] word word") {
		t.Fatalf("message %q", a.LastMessage)
	}
	if n := len([]rune(a.LastMessage)); n != 1200 || !strings.HasSuffix(a.LastMessage, "…") {
		t.Fatalf("message of %d characters: %q", n, a.LastMessage)
	}
}

func TestActivityIsOmittedWithoutToolCallsOrAssistantText(t *testing.T) {
	f := newTranscriptFixture(t)
	write(t, f.path, `{"type":"user","timestamp":"2026-10-27T18:00:00Z","message":{"role":"user","content":"hello"}}`+"\n")
	if a := activities(t, f)["claude-code/s1"]; a != nil {
		t.Fatalf("activity %+v", a)
	}
	w := httptest.NewRecorder()
	f.live.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/live-sessions", nil))
	if strings.Contains(w.Body.String(), `"activity"`) {
		t.Fatalf("served %s", w.Body.String())
	}
}

func TestCodexActivityNamesShellCommandsAndPatchedFiles(t *testing.T) {
	f := newTranscriptFixture(t)
	const id = "019f0000-0000-7000-8000-000000000003"
	write(t, filepath.Join(f.home, ".codex", "sessions", "2026", "10", "20", "rollout-2026-10-20T09-00-00-"+id+".jsonl"),
		`{"timestamp":"2026-10-20T09:00:00.000Z","type":"session_meta","payload":{"id":"`+id+`","cwd":"/work/demo"}}
{"timestamp":"2026-10-20T09:00:09.000Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"rg -n users db\\nls\",\"workdir\":\"/work/demo\"}","call_id":"c1"}}
{"timestamp":"2026-10-20T09:00:10.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"go test ./...\"]}","call_id":"c2"}}
{"timestamp":"2026-10-20T09:00:14.000Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","input":"*** Begin Patch\n*** Update File: /work/demo/db/a.sql\n@@\n+x\n*** Add File: /work/demo/db/b.sql\n+y\n*** End Patch","call_id":"c3"}}
{"timestamp":"2026-10-20T09:00:18.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Added the index."}]}}
`)
	e := hookevents.New(hookevents.KindAgent, "codex", "user-prompt-submit", "/work/demo", "test")
	e.Time = f.clock.now
	e.Session = &hookevents.Session{ID: id, Cwd: "/work/demo"}
	if !apply(f.tracker, e) {
		t.Fatal("not tracked")
	}
	a := activities(t, f)["codex/"+id]
	if a == nil || len(a.Steps) != 3 {
		t.Fatalf("activity %+v", a)
	}
	want := []Step{
		{"Bash", "rg -n users db", "2026-10-20T09:00:09Z"},
		{"Bash", "go test ./...", "2026-10-20T09:00:10Z"},
		{"Edit", "db/a.sql", "2026-10-20T09:00:14Z"},
	}
	for i := range want {
		if a.Steps[i] != want[i] {
			t.Fatalf("step %d: %+v, want %+v", i, a.Steps[i], want[i])
		}
	}
	if a.FilesTouched != 2 || a.LastMessage != "Added the index." {
		t.Fatalf("activity %+v", a)
	}
}
