package entiresession

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/linkage/sessioncorrelate"
	"github.com/baum-ai/baum/sessions/session"
)

// Entire's compact transcript keeps Codex's code-mode `exec` call as
// {"input": "<JavaScript program>"}, with the shell command nested in a
// tools.exec_command call, and exec_command as {"cmd": ...}. The mapper gives
// the program form the documented "command" field.
func TestHostedCompactCodexCommandInputShapesYieldCommandText(t *testing.T) {
	programs := map[string]string{
		"unquoted key":       `const r = await tools.exec_command({cmd:"git add -A && git commit -m fix",workdir:"/repo",yield_time_ms:10000});` + "\ntext(r.output);",
		"quoted key":         `const r = await tools.exec_command({"cmd":"git commit -m fix","workdir":"/repo"}); text(r.output)`,
		"unparsable sibling": `await tools.exec_command({cmd:"echo 'oops"}); await tools.exec_command({cmd:"git commit -m fix"});`,
		"multi-line":         "const r = await tools.exec_command({\n  cmd: \"git commit -m fix\",\n  workdir: \"/repo\"\n});\ntext(r.output);",
	}
	wrappers := map[string]func(string) string{
		"object": func(program string) string { return `{"input":` + program + `}` },
		"string": func(program string) string { return program },
	}
	for name, program := range programs {
		for wrapperName, wrap := range wrappers {
			t.Run(name+"/"+wrapperName, func(t *testing.T) {
				hostedTestCompactProgram(t, program, wrap)
			})
		}
	}
}

func hostedTestCompactProgram(t *testing.T, program string, wrap func(string) string) {
	programJSON, _ := json.Marshal(program)
	compact := strings.Join([]string{
		`{"v":1,"agent":"codex","cli_version":"0.9.0","type":"user","content":[{"text":"commit it"}]}`,
		`{"v":1,"agent":"codex","cli_version":"0.9.0","type":"assistant","content":[` +
			`{"type":"tool_use","name":"exec","input":` + wrap(string(programJSON)) + `,"result":{"output":"[main 3fa9b21] fix","status":"success"}}]}`,
	}, "\n")
	graph := hostedCompactCodexGraph(t, compact)
	use := graph.ToolUses[0]
	if use.Verb != session.VerbExecute {
		t.Fatalf("verb = %q", use.Verb)
	}
	var input struct {
		Input   string `json:"input"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(use.Input, &input); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input.Command, "git commit -m fix") || input.Input != program {
		t.Fatalf("input = %s", use.Input)
	}
	refs := sessioncorrelate.Correlate(graph, "codex").CommitRefs
	if len(refs) != 1 || refs[0].SHA != "3fa9b21" || refs[0].Role != sessioncorrelate.RoleCreated {
		t.Fatalf("commit refs = %+v", refs)
	}
}

func TestHostedCompactCodexExecCommandKeepsCmd(t *testing.T) {
	compact := strings.Join([]string{
		`{"v":1,"agent":"codex","cli_version":"0.9.0","type":"user","content":[{"text":"commit it"}]}`,
		`{"v":1,"agent":"codex","cli_version":"0.9.0","type":"assistant","content":[` +
			`{"type":"tool_use","name":"exec_command","input":{"cmd":"git commit -m fix","workdir":"/repo"},"result":{"output":"[main 3fa9b21] fix","status":"success"}},` +
			`{"type":"tool_use","name":"exec","input":{"input":"text(await tools.web__run({search_query:[{q:\"x\"}]}))"}}]}`,
	}, "\n")
	graph := hostedCompactCodexGraph(t, compact)
	if got := string(graph.ToolUses[0].Input); got != `{"cmd":"git commit -m fix","workdir":"/repo"}` {
		t.Fatalf("exec_command input rewritten: %s", got)
	}
	// A program that runs no shell command is stored as it arrived.
	if got := string(graph.ToolUses[1].Input); !strings.HasPrefix(got, `{"input":"text(await tools.web__run`) || strings.Contains(got, `"command"`) {
		t.Fatalf("non-shell exec input = %s", got)
	}
	refs := sessioncorrelate.Correlate(graph, "codex").CommitRefs
	if len(refs) != 1 || refs[0].SHA != "3fa9b21" {
		t.Fatalf("commit refs = %+v", refs)
	}
}

func hostedCompactCodexGraph(t *testing.T, compact string) *session.Graph {
	t.Helper()
	results, report, err := hostedBuild(hostedSyntheticCheckpoint(t, "Codex", compact, nil, false))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Balanced() {
		t.Fatalf("report = %+v", report)
	}
	if err := session.Validate(results[0].Graph); err != nil {
		t.Fatal(err)
	}
	return results[0].Graph
}
