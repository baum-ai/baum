package agent

import (
	"runtime"
	"slices"
	"strings"
)

var allowedEnv = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "TERM", "LANG", "TZ",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
}

var allowedWindowsEnv = []string{
	"USERPROFILE", "APPDATA", "LOCALAPPDATA", "SystemRoot", "PATHEXT", "COMSPEC", "TEMP", "TMP", "HOMEDRIVE", "HOMEPATH",
	"SystemDrive", "WINDIR", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "ProgramData", "USERNAME",
	"NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE",
}

// LocalEnvironment returns the variables of environ a local CLI backend may
// pass to its child: a fixed allowlist, plus the names in extra, which the
// calling backend trusts (for example CLAUDE_CONFIG_DIR). Provider credentials
// and configuration, cloud and GitHub tokens, Git settings and the SSH agent
// never pass, even when extra names them. Each element of environ is
// "NAME=value".
//
// This covers environment variables only. HOME and the XDG directories stay,
// so the child can still read credentials kept in files under them.
func LocalEnvironment(environ []string, extra ...string) []string {
	return localEnvironment(runtime.GOOS == "windows", environ, extra)
}

func localEnvironment(windows bool, environ, extra []string) []string {
	allowed := map[string]bool{}
	add := func(names []string) {
		for _, name := range names {
			if windows {
				name = strings.ToUpper(name)
			}
			allowed[name] = true
		}
	}
	add(allowedEnv)
	if windows {
		add(allowedWindowsEnv)
	}
	add(extra)
	var kept []string
	for _, entry := range environ {
		name, _, found := strings.Cut(entry, "=")
		// Windows keeps drive-relative entries such as "=C:=C:\" with an empty name.
		if !found || name == "" {
			continue
		}
		if windows {
			name = strings.ToUpper(name)
		}
		if denied(name) {
			continue
		}
		if allowed[name] || strings.HasPrefix(name, "LC_") {
			kept = append(kept, entry)
		}
	}
	return kept
}

var deniedPrefixes = []string{"ANTHROPIC_", "OPENAI_", "AWS_", "GOOGLE_", "AZURE_", "GH_", "GIT_"}

var deniedNames = []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CODEX_API_KEY", "GITHUB_TOKEN", "SSH_AUTH_SOCK"}

// denied reports a provider or source-host credential or setting. name is
// already upper case on Windows; elsewhere names are case-sensitive.
func denied(name string) bool {
	return slices.Contains(deniedNames, name) || slices.ContainsFunc(deniedPrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
}
