//go:build unix

package codex

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lastpersonlabs/baum/agent"
)

const successEvents = `{"type":"turn.started"}
{"type":"item.completed","item":{"type":"agent_message","text":"{\"ok\":true}"}}
{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`

// fakeCodex writes an executable that answers --version with version and
// otherwise runs run in the directory the backend chose.
func fakeCodex(t *testing.T, version, run string) string {
	t.Helper()
	return fakeCodexSandbox(t, version, workingSandbox, run)
}

// workingSandbox answers the preflight as an enforced codex sandbox would: the
// read succeeds and the shell reports its write was refused.
const workingSandbox = `for last; do :; done; case "$*" in *"-- sh -c"*) echo ` + writeBlocked + `; exit 0;; esac; cat "$last"; exit 0;`

// writableSandbox lets the write probe succeed, as a sandbox that is not
// enforced would.
const writableSandbox = `for last; do :; done; case "$*" in *"-- sh -c"*) echo x > "$last"; echo ` + writeAllowed + `; exit 0;; esac; cat "$last"; exit 0;`

// fakeCodexSandbox is fakeCodex with its own answer to the sandbox preflight.
func fakeCodexSandbox(t *testing.T, version, sandbox, run string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	body := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '" + version + "'; exit 0; fi\n" +
		"if [ \"$1\" = sandbox ]; then " + sandbox + " fi\n" + run
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// recording captures the arguments, the environment and the output schema
// (its path and its contents at the time of the run), then succeeds.
const recording = `cat >/dev/null
printf '%s\n' "$@" > arguments.txt
env > environment.txt
next=0
for a in "$@"; do
  if [ "$next" = 1 ]; then echo "$a" > schema-path.txt; cat "$a" > schema-copy.txt; next=0; fi
  if [ "$a" = --output-schema ]; then next=1; fi
done
cat <<'EVENTS'
` + successEvents + `
EVENTS
`

func readLines(t *testing.T, dir, name string) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(body)), "\n")
}

func environmentOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	env := map[string]string{}
	for _, line := range readLines(t, dir, "environment.txt") {
		name, value, _ := strings.Cut(line, "=")
		env[name] = value
	}
	return env
}

func schemaRequest(dir string, endpoint *agent.Endpoint) agent.Request {
	return agent.Request{Directory: dir, Prompt: "p", Schema: []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`), MaxCalls: 2, MaxTokens: 100, Endpoint: endpoint}
}

func TestLocalEnvironmentUsesTheUsersLoginAndNoKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "/user/codex-home")
	for _, name := range []string{"CODEX_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "AWS_SECRET_ACCESS_KEY",
		"GITHUB_TOKEN", "GH_TOKEN", "GIT_ASKPASS", "SSH_AUTH_SOCK", "DATABASE_URL"} {
		t.Setenv(name, "ambient")
	}
	script := fakeCodex(t, "codex-cli 0.162.1", recording)
	backend := &Backend{Binary: script, Model: "m"}

	dir := t.TempDir()
	if _, err := backend.Run(context.Background(), schemaRequest(dir, nil)); err != nil {
		t.Fatal(err)
	}
	env := environmentOf(t, dir)
	for name := range env {
		if strings.HasPrefix(name, "OPENAI_") || strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "AWS_") || strings.HasPrefix(name, "GH") ||
			slices.Contains([]string{"CODEX_API_KEY", "GITHUB_TOKEN", "SSH_AUTH_SOCK", "DATABASE_URL", "GIT_ASKPASS"}, name) {
			t.Errorf("%s reached the process", name)
		}
	}
	if env["HOME"] != home || env["CODEX_HOME"] != "/user/codex-home" {
		t.Errorf("HOME=%q CODEX_HOME=%q, want the user's", env["HOME"], env["CODEX_HOME"])
	}
	args := strings.Join(readLines(t, dir, "arguments.txt"), "\n")
	if strings.Contains(args, "openai_base_url") {
		t.Errorf("a base URL without an endpoint: %s", args)
	}

	dir = t.TempDir()
	endpoint := &agent.Endpoint{BaseURL: "https://proxy.example/openai", Key: "endpoint-key"}
	if _, err := backend.Run(context.Background(), schemaRequest(dir, endpoint)); err != nil {
		t.Fatal(err)
	}
	env = environmentOf(t, dir)
	if env["CODEX_API_KEY"] != "endpoint-key" || env["HOME"] != home || env["CODEX_HOME"] != "/user/codex-home" {
		t.Errorf("endpoint not applied: %v", env)
	}
	if !slices.Contains(readLines(t, dir, "arguments.txt"), `openai_base_url="https://proxy.example/openai"`) {
		t.Errorf("base URL missing from %v", readLines(t, dir, "arguments.txt"))
	}
}

// The local run never writes into the user's CODEX_HOME, which holds their
// login, and leaves no schema file behind.
func TestLocalSchemaIsPrivateAndRemoved(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	script := fakeCodex(t, "codex-cli 0.162.1", recording)
	dir := t.TempDir()
	if _, err := (&Backend{Binary: script, Model: "m"}).Run(context.Background(), schemaRequest(dir, nil)); err != nil {
		t.Fatal(err)
	}
	path := readLines(t, dir, "schema-path.txt")[0]
	if strings.HasPrefix(path, codexHome) || strings.Contains(path, "/.codex/") {
		t.Errorf("schema written under CODEX_HOME: %s", path)
	}
	if copied, _ := os.ReadFile(filepath.Join(dir, "schema-copy.txt")); !strings.Contains(string(copied), `"additionalProperties":false`) {
		t.Errorf("schema at run time = %s", copied)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("schema file left behind: %v", err)
	}
	if entries, _ := os.ReadDir(codexHome); len(entries) != 0 {
		t.Errorf("CODEX_HOME was written to: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(dir, "home")); err == nil {
		t.Error("the local run created a home directory")
	}
}

func TestContainerRefusesWithoutEndpoint(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	script := fakeCodex(t, "codex-cli 0.162.1", "touch "+marker+"\n")
	_, err := (&Backend{Binary: script, Profile: Container}).Run(context.Background(), agent.Request{Directory: t.TempDir(), MaxCalls: 2, MaxTokens: 100})
	if err == nil || !strings.Contains(err.Error(), "needs a provider endpoint") {
		t.Fatalf("error %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the process ran")
	}
}

// The hosted review agent must keep the exact flags and environment it has
// always had.
func TestContainerArgumentsAndEnvironmentAreStable(t *testing.T) {
	script := fakeCodex(t, "codex-cli 0.0.1", recording)
	dir := t.TempDir()
	endpoint := &agent.Endpoint{BaseURL: "https://proxy.example/openai", Key: "run-token"}
	if _, err := (&Backend{Binary: script, Model: "gpt-test", Profile: Container}).Run(context.Background(), schemaRequest(dir, endpoint)); err != nil {
		t.Fatal(err)
	}
	want := []string{"exec", "--json", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"--disable", "hooks", "--disable", "apps", "--disable", "multi_agent",
		"--sandbox", "danger-full-access", "-c", `approval_policy="never"`, "-c", `web_search="disabled"`,
		"--skip-git-repo-check", "--model", "gpt-test",
		"-c", `openai_base_url="https://proxy.example/openai"`,
		"--output-schema", filepath.Join(dir, "home", ".codex", "output-schema.json"), "-"}
	if got := readLines(t, dir, "arguments.txt"); !slices.Equal(got, want) {
		t.Errorf("arguments\n got %q\nwant %q", got, want)
	}
	env := environmentOf(t, dir)
	// The shell adds PWD, SHLVL and _ of its own.
	for _, shell := range []string{"PWD", "SHLVL", "_", "OLDPWD"} {
		delete(env, shell)
	}
	wantEnv := map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": filepath.Join(dir, "home"),
		"CODEX_HOME": filepath.Join(dir, "home", ".codex"), "CODEX_API_KEY": "run-token"}
	if len(env) != len(wantEnv) {
		t.Errorf("environment = %v, want %v", env, wantEnv)
	}
	for name, value := range wantEnv {
		if env[name] != value {
			t.Errorf("%s = %q, want %q", name, env[name], value)
		}
	}
	// The container profile writes the schema where it always has.
	if _, err := os.Stat(filepath.Join(dir, "home", ".codex", "output-schema.json")); err != nil {
		t.Error(err)
	}
}

// The local run is read-only and never asks for the unsandboxed mode.
func TestLocalArgumentsAreReadOnly(t *testing.T) {
	script := fakeCodex(t, "codex-cli 0.162.1", recording)
	dir := t.TempDir()
	if _, err := (&Backend{Binary: script, Model: "m"}).Run(context.Background(), schemaRequest(dir, nil)); err != nil {
		t.Fatal(err)
	}
	args := readLines(t, dir, "arguments.txt")
	at := slices.Index(args, "--sandbox")
	if at < 0 || args[at+1] != "read-only" {
		t.Errorf("arguments %q lack --sandbox read-only", args)
	}
	for _, arg := range args {
		if strings.Contains(arg, "danger-full-access") || strings.Contains(arg, "dangerously") {
			t.Errorf("unsandboxed argument %q", arg)
		}
	}
	for _, flag := range []string{"--ephemeral", "--ignore-user-config", "--ignore-rules", "--json", "--skip-git-repo-check"} {
		if !slices.Contains(args, flag) {
			t.Errorf("missing %s in %q", flag, args)
		}
	}
	if at := slices.Index(args, "--model"); at < 0 || args[at+1] != "m" {
		t.Errorf("arguments %q lack --model m", args)
	}

	// With no model, Codex chooses its default: no empty --model.
	dir = t.TempDir()
	if _, err := (&Backend{Binary: script}).Run(context.Background(), schemaRequest(dir, nil)); err != nil {
		t.Fatal(err)
	}
	if args := readLines(t, dir, "arguments.txt"); slices.Contains(args, "--model") {
		t.Errorf("an empty Model passed --model: %q", args)
	}
}

func TestLocalRefusesOldCodexOnce(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "versions")
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "codex")
	body := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo x >> " + counter + "; echo 'codex-cli 0.100.0'; exit 0; fi\ntouch " + marker + "\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	backend := &Backend{Binary: script}
	_, err := backend.Run(context.Background(), request(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "codex 0.100.0 is older than "+minimumVersion) || !strings.Contains(err.Error(), "upgrade Codex") {
		t.Fatalf("error %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the old codex ran")
	}

	// A passing check is cached, so a later run does not ask again.
	good := fakeCodex(t, "codex-cli 0.162.1", "cat >/dev/null\necho x >> "+counter+"\ncat <<'EVENTS'\n"+successEvents+"\nEVENTS\n")
	backend = &Backend{Binary: good}
	for range 2 {
		if _, err := backend.Run(context.Background(), request(t.TempDir())); err != nil {
			t.Fatal(err)
		}
	}
	lines, _ := os.ReadFile(counter)
	if got := strings.Count(string(lines), "x\n"); got != 3 {
		t.Fatalf("counter lines %d, want 1 version of the old + 2 runs", got)
	}
}

func TestLocalRefusesUnreadableVersion(t *testing.T) {
	script := fakeCodex(t, "not a version", "cat >/dev/null\n")
	if _, err := (&Backend{Binary: script}).Run(context.Background(), request(t.TempDir())); err == nil || !strings.Contains(err.Error(), "upgrade Codex") {
		t.Fatalf("error %v", err)
	}
}

func TestLocalMapsUnknownOptionToUpgrade(t *testing.T) {
	script := fakeCodex(t, "codex-cli 0.162.1", "cat >/dev/null\necho 'Usage: codex exec [OPTIONS]' >&2\necho \"error: unexpected argument '--ignore-rules' found\" >&2\nexit 2\n")
	_, err := (&Backend{Binary: script}).Run(context.Background(), request(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "upgrade Codex to "+minimumVersion) || !strings.Contains(err.Error(), "unexpected argument '--ignore-rules'") || strings.Contains(err.Error(), "Usage:") {
		t.Fatalf("error %v", err)
	}
	// The container profile reports the failure as it always has.
	_, err = (&Backend{Binary: script, Profile: Container}).Run(context.Background(), request(t.TempDir()))
	if err == nil || !strings.HasPrefix(err.Error(), "codex failed: ") || strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("error %v", err)
	}
	// An unrelated failure is not a version problem.
	script = fakeCodex(t, "codex-cli 0.162.1", "cat >/dev/null\necho 'Error: unknown error' >&2\nexit 1\n")
	_, err = (&Backend{Binary: script}).Run(context.Background(), request(t.TempDir()))
	if err == nil || strings.Contains(err.Error(), "upgrade Codex") || !strings.HasPrefix(err.Error(), "codex failed: ") {
		t.Fatalf("error %v", err)
	}
}

// A local failure carries the first line of stderr; a hosted one never does.
func TestGenericFailureDetailIsLocalOnly(t *testing.T) {
	script := fakeCodex(t, "codex-cli 0.162.1", "cat >/dev/null\necho >&2\necho 'stream closed: private repository text' >&2\necho 'second line' >&2\nexit 1\n")
	_, err := (&Backend{Binary: script}).Run(context.Background(), request(t.TempDir()))
	if err == nil || !strings.HasPrefix(err.Error(), "codex failed: ") || !strings.HasSuffix(err.Error(), "(stream closed: private repository text)") {
		t.Fatalf("local error %v", err)
	}
	_, err = (&Backend{Binary: script, Profile: Container}).Run(context.Background(), request(t.TempDir()))
	if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "stream closed") {
		t.Fatalf("container error %v", err)
	}
}

func TestUnsetCodexHomeStaysUnset(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	if err := os.Unsetenv("CODEX_HOME"); err != nil {
		t.Fatal(err)
	}
	script := fakeCodex(t, "codex-cli 0.162.1", recording)
	dir := t.TempDir()
	if _, err := (&Backend{Binary: script, Model: "m"}).Run(context.Background(), schemaRequest(dir, nil)); err != nil {
		t.Fatal(err)
	}
	if value, present := environmentOf(t, dir)["CODEX_HOME"]; present {
		t.Errorf("CODEX_HOME = %q reached the process", value)
	}
}

// The preflight runs a read-only command under Codex's read-only profile once,
// and a machine where the sandbox cannot start is refused before any run.
func TestLocalPreflightsTheReadOnlySandbox(t *testing.T) {
	counter := filepath.Join(t.TempDir(), "preflights")
	script := fakeCodexSandbox(t, "codex-cli 0.162.1", `echo "$@" >> `+counter+`; `+workingSandbox,
		"cat >/dev/null\ncat <<'EVENTS'\n"+successEvents+"\nEVENTS\n")
	backend := &Backend{Binary: script}
	for range 2 {
		if _, err := backend.Run(context.Background(), request(t.TempDir())); err != nil {
			t.Fatal(err)
		}
	}
	lines := readLines(t, filepath.Dir(counter), "preflights")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "sandbox -P :read-only -- cat ") || !strings.HasPrefix(lines[1], "sandbox -P :read-only -- sh -c ") {
		t.Fatalf("preflight calls %q", lines)
	}

	marker := filepath.Join(t.TempDir(), "ran")
	script = fakeCodexSandbox(t, "codex-cli 0.162.1", "echo 'bwrap: No permissions to create a new namespace' >&2; echo more >&2; exit 1;", "touch "+marker+"\n")
	_, err := (&Backend{Binary: script}).Run(context.Background(), request(t.TempDir()))
	want := "codex could not start its read-only sandbox on this machine: bwrap: No permissions to create a new namespace; baum will not run codex without it"
	if err == nil || err.Error() != want {
		t.Fatalf("error %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("codex ran without its sandbox")
	}
	// A sandbox that succeeds without running the command is not a pass, and a
	// failed preflight is repeated rather than cached.
	script = fakeCodexSandbox(t, "codex-cli 0.162.1", "exit 0;", "touch "+marker+"\n")
	failing := &Backend{Binary: script}
	for range 2 {
		if _, err := failing.Run(context.Background(), request(t.TempDir())); err == nil || !strings.Contains(err.Error(), "read-only sandbox") {
			t.Fatalf("error %v", err)
		}
	}
}

// The preflight passes only when a write under the read-only profile fails and
// leaves nothing behind.
func TestLocalPreflightRefusesAnUnenforcedSandbox(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	script := fakeCodexSandbox(t, "codex-cli 0.162.1", writableSandbox, "touch "+marker+"\n")
	_, err := (&Backend{Binary: script}).Run(context.Background(), request(t.TempDir()))
	want := "codex ran a command without enforcing its read-only sandbox on this machine; baum will not run codex"
	if err == nil || err.Error() != want {
		t.Fatalf("error %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("codex ran without an enforced sandbox")
	}
	// The same fake with the write blocked passes (workingSandbox).
	script = fakeCodex(t, "codex-cli 0.162.1", "cat >/dev/null\ncat <<'EVENTS'\n"+successEvents+"\nEVENTS\n")
	if _, err := (&Backend{Binary: script}).Run(context.Background(), request(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	// A sandbox that fails the write but still leaves the file is refused too.
	leaving := `for last; do :; done; case "$*" in *"-- sh -c"*) echo x > "$last"; echo ` + writeBlocked + `; exit 0;; esac; cat "$last"; exit 0;`
	script = fakeCodexSandbox(t, "codex-cli 0.162.1", leaving, "touch "+marker+"\n")
	if _, err := (&Backend{Binary: script}).Run(context.Background(), request(t.TempDir())); err == nil || !strings.Contains(err.Error(), "without enforcing") {
		t.Fatalf("error %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("codex ran without an enforced sandbox")
	}
}

// A write check whose shell never ran (no report back) proves nothing, even
// though the write failed and left no file.
func TestLocalPreflightRefusesAWriteCheckThatNeverRan(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	silent := `for last; do :; done; case "$*" in *"-- sh -c"*) echo "sh: not found" >&2; exit 127;; esac; cat "$last"; exit 0;`
	script := fakeCodexSandbox(t, "codex-cli 0.162.1", silent, "touch "+marker+"\n")
	_, err := (&Backend{Binary: script}).Run(context.Background(), request(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "could not run its read-only sandbox check") || !strings.Contains(err.Error(), "sh: not found") {
		t.Fatalf("error %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("codex ran after a write check that never ran")
	}
}

func TestSandboxIsNotVerifiedOnWindows(t *testing.T) {
	script := fakeCodex(t, "codex-cli 0.162.1", "")
	err := checkSandbox(context.Background(), script, "windows")
	if err == nil || !strings.Contains(err.Error(), "cannot verify") || !sandboxPlatform("linux") || !sandboxPlatform("darwin") || sandboxPlatform("windows") {
		t.Fatalf("error %v", err)
	}
}

func TestUnknownProfileIsRefused(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	script := fakeCodex(t, "codex-cli 0.162.1", "touch "+marker+"\n")
	_, err := (&Backend{Binary: script, Profile: 7}).Run(context.Background(), request(t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "unknown profile 7") {
		t.Fatalf("error %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the process ran")
	}
}
