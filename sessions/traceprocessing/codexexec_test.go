package traceprocessing

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCodexExecCommand(t *testing.T) {
	tests := []struct {
		name, source, want string
	}{
		{"unquoted key", `const r = await tools.exec_command({cmd:"go test ./...",workdir:"/work"}); text(r.output);`, "go test ./..."},
		{"quoted key", `const r = await tools.exec_command({"cmd":"git status --short","workdir":"/work"});`, "git status --short"},
		{"multi-line object", "const r = await tools.exec_command({\n  cmd: \"sed -n '1,20p' a.go\",\n  workdir: \"/work\"\n});", "sed -n '1,20p' a.go"},
		{"single quotes", `tools.exec_command({cmd: 'ls -la', yield_time_ms: 1000})`, "ls -la"},
		{"template literal", "tools.exec_command({cmd: `git commit -m \"x\"`})", `git commit -m "x"`},
		{"escapes", `tools.exec_command({cmd:"printf 'a\tb\n' && echo \"q\" é \x41 \\"})`, "printf 'a\tb\n' && echo \"q\" é A \\"},
		{"command key", `tools.shell({command: "make build"})`, "make build"},
		{"several calls", `const a = await tools.exec_command({cmd:"git add -A"}); const b = await tools.exec_command({cmd:"git commit -m wip"});`, "git add -A\ngit commit -m wip"},
		{"array of calls", `const cmds = [["a", {cmd:"go vet ./..."}], ["b", {"cmd":"go test ./..."}]];`, "go vet ./...\ngo test ./..."},
		{"cmd inside a string literal", `tools.apply_patch("x"); const body = "cmd: \"rm -rf /\""; text(body)`, ""},
		{"cmd in comment", "// cmd: \"rm -rf /\"\n/* cmd: \"x\" */ text(1)", ""},
		{"template substitution", "tools.exec_command({cmd: `echo ${name}`})", ""},
		{"variable value", `tools.exec_command({cmd: command})`, ""},
		{"no exec", `text(await tools.web__run({search_query:[{q:"x"}]}))`, ""},
		{"surrogate pair", `tools.exec_command({cmd:"echo 😀"})`, "echo \U0001F600"},
		{"concatenation is run-time", `tools.exec_command({cmd:"git commit -m " + msg}); tools.exec_command({cmd: "a" + "b"}); tools.exec_command({cmd:"ls"})`, "ls"},
		{"ternary is not a key", `const c = ok ? cmd : "rm -rf /"; text(c)`, ""},
		{"case label is not a key", `switch (x) { case "cmd": "rm -rf /"; break; }`, ""},
		{"key after comma", `tools.exec_command({workdir:"/w", cmd:"ls"})`, "ls"},
		{"quote inside a regex literal", "const s = text.replace(/'/g, \"\"); tools.exec_command({cmd:\"git status\"})", "git status"},
		{"regex with a class and a slash", `const m = x.match(/[/"]+/); tools.exec_command({cmd:"pwd"})`, "pwd"},
		{"division is not a regex", `const r = a / b; const q = c / d; tools.exec_command({cmd:"ls"})`, "ls"},
		{"cmd inside a regex", `const re = /cmd: "x"/; text(re)`, ""},
		{"argv inside a program", `tools.shell({command:["bash","-lc","git commit -m fix"]})`, "git commit -m fix"},
		{"argv without a shell", `tools.shell({command:["go","test","./..."]})`, "go test ./..."},
		{"argv with a variable", `tools.shell({command:["go", arg]})`, ""},
		{"unterminated unicode brace", `tools.exec_command({cmd:"echo \u{12"})`, "echo u{12"},
		{"line continuation", "tools.exec_command({cmd:\"echo a\\\nb\"})", "echo ab"},
		{"unparsable command is dropped", `tools.exec_command({cmd:"echo 'oops"}); tools.exec_command({cmd:"git commit -m fix"})`, "git commit -m fix"},
		{"heredoc stays whole", "tools.exec_command({cmd:\"cat > a.md <<'EOF'\\ngit commit -m x\\nEOF\"}); tools.exec_command({cmd:\"ls\"})", "cat > a.md <<'EOF'\ngit commit -m x\nEOF\nls"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CodexExecCommand(tt.source); got != tt.want {
				t.Fatalf("CodexExecCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeCodexExecInput(t *testing.T) {
	source := `const r = await tools.exec_command({cmd:"git commit -m fix"}); text(r.output)`
	sourceJSON, _ := json.Marshal(source)
	wrapped := json.RawMessage(`{"input":` + string(sourceJSON) + `}`)
	for name, input := range map[string]json.RawMessage{"string": sourceJSON, "object": wrapped} {
		t.Run(name, func(t *testing.T) {
			var got struct {
				Input   string `json:"input"`
				Command string `json:"command"`
			}
			if err := json.Unmarshal(NormalizeCodexExecInput("exec", input), &got); err != nil {
				t.Fatal(err)
			}
			if got.Command != "git commit -m fix" || got.Input != source {
				t.Fatalf("normalized = %+v", got)
			}
		})
	}
	unchanged := map[string]json.RawMessage{
		"other tool":   sourceJSON,
		"no command":   json.RawMessage(`"text(1)"`),
		"has command":  json.RawMessage(`{"command":"ls","input":"x"}`),
		"not a string": json.RawMessage(`{"input":3}`),
	}
	for name, input := range unchanged {
		name := name
		t.Run("unchanged "+name, func(t *testing.T) {
			toolName := "exec"
			if name == "other tool" {
				toolName = "apply_patch"
			}
			if got := NormalizeCodexExecInput(toolName, input); string(got) != string(input) {
				t.Fatalf("changed %s to %s", input, got)
			}
		})
	}
}

func TestCodexExecCommandLongInputsStayLinear(t *testing.T) {
	// Unterminated constructs repeated many times must not rescan the rest of
	// the program from each one.
	for name, unit := range map[string]string{
		"unicode brace": `"\u{`,
		"regex":         `= /`,
	} {
		source := strings.Repeat(unit, 200000)
		start := time.Now()
		CodexExecCommand(source)
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("%s: scanning %d bytes took %s", name, len(source), elapsed)
		}
	}
}
