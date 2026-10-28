package codexsession

import (
	"encoding/json"
	"testing"

	"github.com/baum-ai/baum/linkage/sessioncorrelate"
	"github.com/baum-ai/baum/sessions/session"
)

// Codex spells a shell command three ways in a rollout. exec_command carries
// {"cmd": ...} and shell_command {"command": ...} in the arguments JSON; the
// code-mode `exec` wrapper is a custom tool call whose input is a JavaScript
// program with the command nested in a tools.exec_command call. The mapper
// stores the program and the command lines it runs under "command".
func TestHostedCodexCommandInputShapesYieldCommandText(t *testing.T) {
	program := `const r = await tools.exec_command({cmd:"git add -A && git commit -m \"fix\"",workdir:"/repo",yield_time_ms:10000});` + "\n" + `text(r.output);`
	programJSON, _ := json.Marshal(program)
	cases := []struct {
		name, call, output string
	}{
		{
			"exec_command", `{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"git add -A && git commit -m fix\",\"workdir\":\"/repo\"}","call_id":"c"}`,
			`{"type":"function_call_output","call_id":"c","output":"[main 3fa9b21] fix"}`,
		},
		{
			"shell_command", `{"type":"function_call","name":"shell_command","arguments":"{\"command\":\"git add -A && git commit -m fix\",\"workdir\":\"/repo\"}","call_id":"c"}`,
			`{"type":"function_call_output","call_id":"c","output":"[main 3fa9b21] fix"}`,
		},
		{
			"shell argv", `{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"git add -A && git commit -m fix\"]}","call_id":"c"}`,
			`{"type":"function_call_output","call_id":"c","output":"[main 3fa9b21] fix"}`,
		},
		{
			"exec code mode", `{"type":"custom_tool_call","name":"exec","input":` + string(programJSON) + `,"call_id":"c","status":"completed"}`,
			`{"type":"custom_tool_call_output","call_id":"c","output":"[main 3fa9b21] fix"}`,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			lines := append(hostedBasicStart(),
				hostedEnvelope("2026-08-01T12:00:04Z", "response_item", tt.call),
				hostedEnvelope("2026-08-01T12:00:05Z", "response_item", tt.output),
				hostedEnvelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"exec_command_end","call_id":"c","status":"completed","exit_code":0,"stdout":"[main 3fa9b21] fix"}`),
				hostedEnvelope("2026-08-01T12:00:07Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
			)
			graph, _ := hostedBuildLines(t, lines...)
			use := hostedToolForCall(t, graph, "c")
			if use.Verb != session.VerbExecute {
				t.Fatalf("verb = %q", use.Verb)
			}
			var input struct {
				Command json.RawMessage `json:"command"`
				Cmd     string          `json:"cmd"`
			}
			if err := json.Unmarshal(use.Input, &input); err != nil {
				t.Fatalf("input %s is not an object: %v", use.Input, err)
			}
			if len(input.Command) == 0 && input.Cmd == "" {
				t.Fatalf("input has no command text: %s", use.Input)
			}
			if graph.Root().Outcome != session.OutcomeCommitted {
				t.Fatalf("outcome = %q", graph.Root().Outcome)
			}
			refs := sessioncorrelate.Correlate(graph, "codex").CommitRefs
			if len(refs) != 1 || refs[0].SHA != "3fa9b21" || refs[0].Role != sessioncorrelate.RoleCreated {
				t.Fatalf("commit refs = %+v", refs)
			}
			if err := session.Validate(graph); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHostedCodexExecInputKeepsProgramBesideCommand(t *testing.T) {
	program := `text(await tools.exec_command({cmd:"go test ./..."}))`
	programJSON, _ := json.Marshal(program)
	lines := append(hostedBasicStart(),
		hostedEnvelope("2026-08-01T12:00:04Z", "response_item", `{"type":"custom_tool_call","name":"exec","input":`+string(programJSON)+`,"call_id":"c"}`),
		hostedEnvelope("2026-08-01T12:00:05Z", "response_item", `{"type":"custom_tool_call","name":"exec","input":"text(1)","call_id":"plain"}`),
		hostedEnvelope("2026-08-01T12:00:06Z", "event_msg", `{"type":"task_complete","turn_id":"turn"}`),
	)
	graph, _ := hostedBuildLines(t, lines...)
	var got map[string]string
	if err := json.Unmarshal(hostedToolForCall(t, graph, "c").Input, &got); err != nil {
		t.Fatal(err)
	}
	if got["command"] != "go test ./..." || got["input"] != program {
		t.Fatalf("input = %+v", got)
	}
	// A program that runs no shell command is stored as it arrived.
	if raw := string(hostedToolForCall(t, graph, "plain").Input); raw != `"text(1)"` {
		t.Fatalf("plain input = %s", raw)
	}
}
