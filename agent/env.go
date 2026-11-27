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
	return localEnvironment(runtime.GOOS == "windows", environ, extra, nil)
}

// ConverseEnvironment is LocalEnvironment for a person's own session
// (Converser), which works as the person would in a terminal: it also keeps
// the SSH agent (SSH_AUTH_SOCK), GitHub CLI and token variables (GH_*,
// GITHUB_TOKEN) and Git's settings (GIT_*), so the agent can push and use gh.
// Git variables that point at another repository (repositoryGitEnv) are still
// removed, so git works on the directory the session runs in, and so are the
// provider credentials and settings.
func ConverseEnvironment(environ []string, extra ...string) []string {
	return localEnvironment(runtime.GOOS == "windows", environ, extra, sessionKept)
}

// repositoryGitEnv are the Git variables that choose a repository, working
// tree, index or object store instead of the working directory's.
var repositoryGitEnv = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_PREFIX"}

// sessionKept reports a variable ConverseEnvironment keeps although
// LocalEnvironment does not. name is upper case on Windows.
func sessionKept(name string) bool {
	switch {
	case name == "SSH_AUTH_SOCK" || name == "GITHUB_TOKEN" || strings.HasPrefix(name, "GH_"):
		return true
	case strings.HasPrefix(name, "GIT_"):
		return !slices.Contains(repositoryGitEnv, name)
	}
	return false
}

func localEnvironment(windows bool, environ, extra []string, keeps func(string) bool) []string {
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
		keep := keeps != nil && keeps(name)
		if denied(name) && !keep {
			continue
		}
		if allowed[name] || strings.HasPrefix(name, "LC_") || keep {
			kept = append(kept, entry)
		}
	}
	return kept
}

var deniedPrefixes = []string{"ANTHROPIC_", "OPENAI_", "AWS_", "GOOGLE_", "AZURE_", "GH_", "GIT_"}

var deniedNames = []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CODEX_API_KEY", "CURSOR_API_KEY", "CURSOR_AUTH_TOKEN", "GITHUB_TOKEN", "SSH_AUTH_SOCK"}

// WithUserEnv is environ with the user's own agent variables (agent_env),
// each "NAME=value", set over it. They are the user's explicit choice, so
// unlike inherited variables they may name a Git setting, such as
// GIT_CONFIG_GLOBAL. Callers append the variables a run itself needs after
// them, which then win.
func WithUserEnv(environ, user []string) []string {
	if len(user) == 0 {
		return environ
	}
	names := map[string]bool{}
	for _, entry := range user {
		name, _, _ := strings.Cut(entry, "=")
		names[envKey(name)] = true
	}
	kept := slices.DeleteFunc(slices.Clone(environ), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return names[envKey(name)]
	})
	return append(kept, user...)
}

// envKey is name as the platform compares it: upper case on Windows.
func envKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

// denied reports a provider or source-host credential or setting. name is
// already upper case on Windows; elsewhere names are case-sensitive.
func denied(name string) bool {
	return slices.Contains(deniedNames, name) || slices.ContainsFunc(deniedPrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
}

// WithMCPToken is environ with the MCP token of server set last: any entry
// of the same name, such as one from the user's agent_env, is dropped first, so
// it can neither replace nor remove the token. A nil server leaves environ
// as it is.
func WithMCPToken(environ []string, server *MCPServer) []string {
	if server == nil {
		return environ
	}
	kept := slices.DeleteFunc(slices.Clone(environ), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return envKey(name) == envKey(MCPTokenEnv)
	})
	return append(kept, MCPTokenEnv+"="+server.Token)
}
