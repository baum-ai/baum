package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// credentialPaths are the files and directories under the home directory
// where tools keep credentials: SSH and GPG keys, cloud, container and
// cluster logins, package registry tokens, git's and the GitHub CLI's
// stored tokens, and the agent CLIs' own logins. A fix agent's commands may
// not read them (FixCredentialPaths).
var credentialPaths = []string{
	".ssh", ".gnupg", ".aws", ".azure", ".config/gcloud", ".kube", ".docker/config.json",
	".netrc", "_netrc", ".git-credentials", ".config/git/credentials", ".config/gh", ".config/hub",
	".npmrc", ".yarnrc.yml", ".pypirc", ".gem/credentials", ".cargo/credentials", ".cargo/credentials.toml",
	".terraform.d/credentials.tfrc.json", ".codex/auth.json", ".claude/.credentials.json",
}

// FixCredentialPaths are the absolute paths a fix agent's commands may not
// read: credentialPaths under home, the login files under the Claude Code
// configuration directory and Codex home of environ when they are set
// elsewhere, and the session's runtime directory (XDG_RUNTIME_DIR, else
// /run/user/<uid> on Linux), whose D-Bus socket reaches the keyring. The agent CLI itself, which signs in with them, is not confined
// by them: only the commands it runs are.
func FixCredentialPaths(home string, environ []string) []string {
	var paths []string
	if home != "" {
		for _, p := range credentialPaths {
			paths = append(paths, filepath.Join(home, filepath.FromSlash(p)))
		}
	}
	runtimeDir := ""
	if runtime.GOOS == "linux" {
		runtimeDir = filepath.Join("/run/user", strconv.Itoa(os.Getuid()))
	}
	for _, entry := range environ {
		name, value, _ := strings.Cut(entry, "=")
		if value == "" || !filepath.IsAbs(value) {
			continue
		}
		switch name {
		case "XDG_RUNTIME_DIR":
			runtimeDir = value
		case "CLAUDE_CONFIG_DIR":
			paths = append(paths, filepath.Join(value, ".credentials.json"))
		case "CODEX_HOME":
			paths = append(paths, filepath.Join(value, "auth.json"))
		}
	}
	if runtimeDir != "" {
		// The session's runtime directory holds the D-Bus socket, through
		// which the Secret Service keyring answers, and the SSH, GPG and
		// keyring agents' sockets.
		paths = append(paths, runtimeDir)
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// Home is the home directory of environ (HOME, or USERPROFILE on Windows),
// else the process's.
func Home(environ []string) string {
	for _, name := range []string{"HOME", "USERPROFILE"} {
		for i := len(environ) - 1; i >= 0; i-- {
			if value, ok := strings.CutPrefix(environ[i], name+"="); ok && filepath.IsAbs(value) {
				return value
			}
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

// fixDeniedNames and fixDeniedPrefixes are variables a fix agent's commands
// never get, even from the user's agent_env: tokens for GitHub and git
// hosts, the SSH agent and credential prompts, and Git variables that point
// git at another repository than the agent's own.
var fixDeniedNames = []string{"GITHUB_TOKEN", "GITHUB_PAT", "GITLAB_TOKEN", "BITBUCKET_TOKEN", "HF_TOKEN", "NPM_TOKEN", "NODE_AUTH_TOKEN",
	"PYPI_TOKEN", "TWINE_PASSWORD", "CARGO_REGISTRY_TOKEN", "DOCKER_PASSWORD", "SSH_AUTH_SOCK", "GPG_AGENT_INFO", "SSH_ASKPASS", "GIT_ASKPASS"}

var fixDeniedPrefixes = []string{"GH_", "GITHUB_", "NPM_CONFIG__AUTH", "npm_config__auth", "npm_config_//"}

// providerPrefixes are the agent CLIs' own provider settings, which the
// user's agent_env may set for the CLI to sign in (a Bedrock or Vertex
// login, an API key) and which therefore stay.
var providerPrefixes = []string{"ANTHROPIC_", "CLAUDE_", "OPENAI_", "CODEX_", "AWS_", "GOOGLE_", "VERTEX_", "AZURE_", "CLOUD_ML_"}

// FixEnvironment is environ, a local CLI's environment with the user's
// agent_env over it, made fit for a fix agent's commands: without the
// variables of fixDeniedNames and fixDeniedPrefixes, without any other
// variable whose name says it holds a token, secret or password (provider
// settings for the CLI's own sign-in excepted), without Git variables that
// choose a repository, and with package and build caches under scratch, so
// installs and builds work in the sandbox: GOCACHE, GOMODCACHE, GOPATH
// (whose pkg/sumdb Go writes when it verifies a module), npm_config_cache,
// YARN_CACHE_FOLDER, PIP_CACHE_DIR and XDG_CACHE_HOME, and with
// GIT_LFS_SKIP_SMUDGE=1.
// An empty scratch sets no cache.
func FixEnvironment(environ []string, scratch string) []string {
	kept := slices.DeleteFunc(slices.Clone(environ), func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return fixDenied(envKey(name))
	})
	if scratch == "" {
		return kept
	}
	cache := filepath.Join(scratch, "cache")
	caches := map[string]string{
		"GOCACHE": filepath.Join(cache, "go-build"), "GOMODCACHE": filepath.Join(cache, "go-mod"), "GOPATH": filepath.Join(cache, "go"),
		"npm_config_cache": filepath.Join(cache, "npm"), "YARN_CACHE_FOLDER": filepath.Join(cache, "yarn"),
		"PIP_CACHE_DIR": filepath.Join(cache, "pip"), "XDG_CACHE_HOME": cache,
	}
	kept = slices.DeleteFunc(kept, func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		_, set := caches[name]
		return set
	})
	for _, name := range []string{"GOCACHE", "GOMODCACHE", "GOPATH", "npm_config_cache", "YARN_CACHE_FOLDER", "PIP_CACHE_DIR", "XDG_CACHE_HOME"} {
		kept = append(kept, name+"="+caches[name])
	}
	// The agent's git repository has no remote, so Git LFS cannot download an
	// object it lacks: a merge or checkout writes the pointer instead of
	// failing, and baum's commit keeps the pointer as it is.
	kept = append(slices.DeleteFunc(kept, func(entry string) bool { return strings.HasPrefix(entry, "GIT_LFS_SKIP_SMUDGE=") }), "GIT_LFS_SKIP_SMUDGE=1")
	return kept
}

// fixDenied reports a variable FixEnvironment removes. name is upper case
// on Windows.
func fixDenied(name string) bool {
	upper := strings.ToUpper(name)
	if slices.Contains(fixDeniedNames, upper) || slices.Contains(repositoryGitEnv, upper) ||
		slices.ContainsFunc(fixDeniedPrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) || strings.HasPrefix(upper, prefix) }) {
		return true
	}
	if slices.ContainsFunc(providerPrefixes, func(prefix string) bool { return strings.HasPrefix(upper, prefix) }) {
		return false
	}
	return strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") ||
		strings.Contains(upper, "PASSWD") || strings.HasSuffix(upper, "_API_KEY") || strings.HasSuffix(upper, "_ACCESS_KEY")
}
