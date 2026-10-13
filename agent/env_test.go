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
		"OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_API_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_PROFILE",
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
	if got := localEnvironment(true, environ, nil); len(got) != 0 {
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
	got := localEnvironment(false, append(environ, "ANTHROPIC_API_KEY=x", "malformed"), nil)
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

func TestLocalEnvironmentWindows(t *testing.T) {
	environ := []string{"Path=C:\\bin", "SystemRoot=C:\\Windows", "userprofile=C:\\u", "APPDATA=a", "=C:=C:\\", "Anthropic_Api_Key=x", "GitHub_Token=y"}
	if got := localEnvironment(true, environ, nil); !slices.Equal(names(got), []string{"APPDATA", "Path", "SystemRoot", "userprofile"}) {
		t.Errorf("windows kept %v", got)
	}
	// Elsewhere the Windows names and other cases are not special.
	if got := localEnvironment(false, environ, nil); len(got) != 0 {
		t.Errorf("non-windows kept %v", got)
	}
}
