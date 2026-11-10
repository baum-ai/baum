package traceprocessing

import (
	"strings"
	"testing"
	"time"
)

func TestCodexExecCallsPairEachCommandWithItsOwnWorkdir(t *testing.T) {
	source := "await tools.exec_command({cmd:\"git commit -q -m x\", workdir:\"/a\"});\n" +
		"await tools.exec_command({\"cmd\":'ls', \"workdir\":\"/b\", opts:{cmd:\"inner\"}});\n" +
		"await tools.exec_command({cmd:`git commit ${msg}`, workdir:dir});\n" +
		"await tools.exec_command({command:[\"bash\",\"-lc\",\"make\"]});\n" +
		"const steps = [{cmd:\"git commit -m listed\"}];\n" +
		"// tools.exec_command({cmd:\"commented\"})\n"
	want := []CodexExecCall{
		{Command: "git commit -q -m x", CommandStatic: true, Workdir: "/a", HasWorkdir: true, WorkdirStatic: true},
		{Command: "ls", CommandStatic: true, Workdir: "/b", HasWorkdir: true, WorkdirStatic: true},
		{CommandStatic: false, HasWorkdir: true},
		{Command: "make", CommandStatic: true},
	}
	assertCalls(t, CodexExecCalls(source), want)
}

func TestCodexExecCallsDoNotGuessAtWorkdirsTheyCannotSee(t *testing.T) {
	source := "await tools.exec_command({cmd:\"git commit -m x\", workdir});\n" +
		"await tools.exec_command({...opts, cmd:\"git commit -m y\"});\n" +
		"await tools.exec_command({[key]: 1, cmd:\"git commit -m z\"});\n" +
		"await run({cmd:\"git commit -m other\"});\n" +
		"await exec_command(args);\n"
	want := []CodexExecCall{
		{Command: "git commit -m x", CommandStatic: true, HasWorkdir: true},
		{Command: "git commit -m y", CommandStatic: false},
		{Command: "git commit -m z", CommandStatic: false},
	}
	assertCalls(t, CodexExecCalls(source), want)
}

// A real call, as the rollout holds it after JSON decoding: the newlines and
// quotes inside the cmd string are JavaScript escapes.
func TestCodexExecCallsDecodeJavaScriptEscapes(t *testing.T) {
	source := `text(await tools.exec_command({cmd:"git add a b\n git diff --cached --check\n git commit -m 'Subject' -m 'Body text.'","workdir":"/abs/wt","sandbox_permissions":"require_escalated","justification":"May I commit?","max_output_tokens":2500,"yield_time_ms":1000}));` + "\n" +
		`text(await tools.mcp__codex_apps__github_get_user_login({}));` + "\n"
	want := []CodexExecCall{{
		Command:       "git add a b\n git diff --cached --check\n git commit -m 'Subject' -m 'Body text.'",
		CommandStatic: true, Workdir: "/abs/wt", HasWorkdir: true, WorkdirStatic: true,
	}}
	assertCalls(t, CodexExecCalls(source), want)

	escaped := `await tools.exec_command({cmd:"echo \"a\\b\"\txA",workdir:"/w"})`
	assertCalls(t, CodexExecCalls(escaped), []CodexExecCall{
		{Command: "echo \"a\\b\"\txA", CommandStatic: true, Workdir: "/w", HasWorkdir: true, WorkdirStatic: true},
	})
}

func assertCalls(t *testing.T, got, want []CodexExecCall) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("calls = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCodexExecCallsStayLinear(t *testing.T) {
	for name, unit := range map[string]string{
		"braces":        "{",
		"unclosed call": "tools.exec_command({cmd:",
	} {
		source := strings.Repeat(unit, 200000)
		start := time.Now()
		CodexExecCalls(source)
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("%s: scanning %d bytes took %s", name, len(source), elapsed)
		}
	}
}
