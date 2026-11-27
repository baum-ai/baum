package agent

import (
	"slices"
	"strings"
	"testing"
)

func names(environ []string) []string {
	var out []string
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

func TestLocalEnvironmentDropsCredentials(t *testing.T) {
	denied := []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
		"OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_API_KEY", "CURSOR_API_KEY", "CURSOR_AUTH_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_PROFILE",
		"GOOGLE_APPLICATION_CREDENTIALS", "AZURE_CLIENT_SECRET", "GITHUB_TOKEN", "GH_TOKEN", "GIT_ASKPASS", "GIT_SSH_COMMAND",
		"SSH_AUTH_SOCK", "CLAUDE_CONFIG_DIR", "CODEX_HOME", "SECRET",
	}
	var environ []string
	for _, name := range denied {
		environ = append(environ, name+"=x")
	}
	if got := LocalEnvironment(environ); len(got) != 0 {
		t.Errorf("kept %v", got)
	}
	if got := localEnvironment(true, environ, nil, nil); len(got) != 0 {
		t.Errorf("windows kept %v", got)
	}
}

func TestLocalEnvironmentKeepsAllowlisted(t *testing.T) {
	allowed := []string{
		"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TERM", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
	}
	var environ []string
	for _, name := range allowed {
		environ = append(environ, name+"=v=1")
	}
	got := localEnvironment(false, append(environ, "ANTHROPIC_API_KEY=x", "malformed"), nil, nil)
	slices.Sort(allowed)
	if !slices.Equal(names(got), allowed) {
		t.Errorf("kept %v, want %v", names(got), allowed)
	}
	if !slices.Contains(got, "HOME=v=1") {
		t.Error("value changed")
	}
}

func TestLocalEnvironmentExtraIsTrusted(t *testing.T) {
	got := LocalEnvironment([]string{"CLAUDE_CONFIG_DIR=/c", "CODEX_HOME=/x", "PATH=/bin"}, "CLAUDE_CONFIG_DIR")
	if !slices.Equal(names(got), []string{"CLAUDE_CONFIG_DIR", "PATH"}) {
		t.Errorf("kept %v", got)
	}
}

func TestLocalEnvironmentExtraCannotAdmitCredentials(t *testing.T) {
	environ := []string{"ANTHROPIC_API_KEY=x", "OPENAI_API_KEY=x", "CODEX_API_KEY=x", "CURSOR_API_KEY=x", "CURSOR_AUTH_TOKEN=x", "AWS_PROFILE=x", "GOOGLE_API_KEY=x", "AZURE_X=x",
		"GITHUB_TOKEN=x", "GH_TOKEN=x", "GIT_ASKPASS=x", "SSH_AUTH_SOCK=x", "CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_USE_VERTEX=1", "CODEX_HOME=/x"}
	extra := []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CODEX_API_KEY", "CURSOR_API_KEY", "CURSOR_AUTH_TOKEN", "AWS_PROFILE", "GOOGLE_API_KEY", "AZURE_X", "GITHUB_TOKEN",
		"GH_TOKEN", "GIT_ASKPASS", "SSH_AUTH_SOCK", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CODEX_HOME"}
	for _, windows := range []bool{false, true} {
		if got := localEnvironment(windows, environ, extra, nil); !slices.Equal(names(got), []string{"CODEX_HOME"}) {
			t.Errorf("windows=%v kept %v", windows, got)
		}
	}
}

func TestLocalEnvironmentWindows(t *testing.T) {
	environ := []string{"Path=C:\\bin", "SystemRoot=C:\\Windows", "userprofile=C:\\u", "APPDATA=a", "=C:=C:\\", "Anthropic_Api_Key=x", "GitHub_Token=y"}
	if got := localEnvironment(true, environ, nil, nil); !slices.Equal(names(got), []string{"APPDATA", "Path", "SystemRoot", "userprofile"}) {
		t.Errorf("windows kept %v", got)
	}
	// Elsewhere the Windows names and other cases are not special.
	if got := localEnvironment(false, environ, nil, nil); len(got) != 0 {
		t.Errorf("non-windows kept %v", got)
	}
}

func TestWithUserEnvSetsTheUsersVariablesOverTheInherited(t *testing.T) {
	inherited := []string{"PATH=/bin", "HOME=/u/ada", "LANG=C"}
	got := WithUserEnv(inherited, []string{"GIT_CONFIG_GLOBAL=/u/ada/agent.gitconfig", "LANG=en_GB.UTF-8"})
	want := []string{"PATH=/bin", "HOME=/u/ada", "GIT_CONFIG_GLOBAL=/u/ada/agent.gitconfig", "LANG=en_GB.UTF-8"}
	if !slices.Equal(got, want) {
		t.Fatalf("WithUserEnv = %q, want %q", got, want)
	}
	if !slices.Equal(inherited, []string{"PATH=/bin", "HOME=/u/ada", "LANG=C"}) {
		t.Fatal("WithUserEnv changed its input")
	}
	if got := WithUserEnv(inherited, nil); !slices.Equal(got, inherited) {
		t.Fatalf("no user variables changed the environment: %q", got)
	}
}

// A person's own session keeps the SSH agent, the GitHub CLI's variables and
// Git's settings, but not the Git variables that point at another repository,
// the provider credentials, or anything off the allowlist.
func TestConverseEnvironmentKeepsGitAndGitHub(t *testing.T) {
	kept := []string{"PATH", "HOME", "SSH_AUTH_SOCK", "GITHUB_TOKEN", "GH_TOKEN", "GH_HOST", "GH_CONFIG_DIR", "GIT_SSH_COMMAND", "GIT_ASKPASS",
		"GIT_AUTHOR_NAME", "GIT_CONFIG_GLOBAL"}
	stripped := []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR",
		"GIT_NAMESPACE", "GIT_PREFIX", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "OPENAI_API_KEY", "CODEX_API_KEY", "CURSOR_API_KEY",
		"AWS_SECRET_ACCESS_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "AZURE_CLIENT_SECRET", "CLAUDE_CODE_USE_BEDROCK", "DATABASE_URL", "SECRET"}
	var environ []string
	for _, name := range append(slices.Clone(kept), stripped...) {
		environ = append(environ, name+"=x")
	}
	want := slices.Clone(kept)
	slices.Sort(want)
	for _, windows := range []bool{false, true} {
		if got := names(localEnvironment(windows, environ, nil, sessionKept)); !slices.Equal(got, want) {
			t.Errorf("windows %v: got %q\nwant %q", windows, got, want)
		}
	}
	// LocalEnvironment, which every other run gets, is unchanged.
	if got := names(LocalEnvironment(environ)); !slices.Equal(got, []string{"HOME", "PATH"}) {
		t.Errorf("LocalEnvironment kept %q", got)
	}
}
