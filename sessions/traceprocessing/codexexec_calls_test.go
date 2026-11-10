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
		"// {cmd:\"commented\"}\n"
	want := []CodexExecCall{
		{Command: "git commit -q -m x", CommandStatic: true, Workdir: "/a", HasWorkdir: true, WorkdirStatic: true},
		{Command: "ls", CommandStatic: true, Workdir: "/b", HasWorkdir: true, WorkdirStatic: true},
		{Command: "inner", CommandStatic: true},
		{CommandStatic: false, HasWorkdir: true},
		{Command: "make", CommandStatic: true},
	}
	got := CodexExecCalls(source)
	if len(got) != len(want) {
		t.Fatalf("calls = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCodexExecCallsStayLinear(t *testing.T) {
	source := strings.Repeat("{", 200000)
	start := time.Now()
	CodexExecCalls(source)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("scanning %d bytes took %s", len(source), elapsed)
	}
}
