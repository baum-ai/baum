package agent

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFixEnvironmentDropsTokensAndMovesCaches(t *testing.T) {
	environ := []string{"PATH=/bin", "HOME=/home/u", "GITHUB_TOKEN=a", "GH_TOKEN=b", "GH_HOST=c", "SSH_AUTH_SOCK=/s", "GIT_DIR=/r", "GIT_ASKPASS=/p",
		"NPM_TOKEN=d", "MY_SERVICE_TOKEN=e", "DB_PASSWORD=f", "STRIPE_SECRET_KEY=g", "SOME_API_KEY=h",
		"ANTHROPIC_API_KEY=keep", "CLAUDE_CODE_OAUTH_TOKEN=keep", "AWS_SECRET_ACCESS_KEY=keep", "OPENAI_API_KEY=keep",
		"GIT_CONFIG_GLOBAL=/g", "GOCACHE=/old", "LANG=C"}
	got := FixEnvironment(environ, "/fix/tmp")
	for _, entry := range got {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "GITHUB_TOKEN", "GH_TOKEN", "GH_HOST", "SSH_AUTH_SOCK", "GIT_DIR", "GIT_ASKPASS", "NPM_TOKEN", "MY_SERVICE_TOKEN", "DB_PASSWORD", "STRIPE_SECRET_KEY", "SOME_API_KEY":
			t.Errorf("kept %s", entry)
		}
	}
	for _, want := range []string{"PATH=/bin", "LANG=C", "ANTHROPIC_API_KEY=keep", "CLAUDE_CODE_OAUTH_TOKEN=keep", "AWS_SECRET_ACCESS_KEY=keep", "OPENAI_API_KEY=keep", "GIT_CONFIG_GLOBAL=/g",
		"GOCACHE=" + filepath.Join("/fix/tmp", "cache", "go-build"), "GOMODCACHE=" + filepath.Join("/fix/tmp", "cache", "go-mod"), "GOPATH=" + filepath.Join("/fix/tmp", "cache", "go"),
		"npm_config_cache=" + filepath.Join("/fix/tmp", "cache", "npm"), "PIP_CACHE_DIR=" + filepath.Join("/fix/tmp", "cache", "pip"),
		"XDG_CACHE_HOME=" + filepath.Join("/fix/tmp", "cache")} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %s in %q", want, got)
		}
	}
	if slices.Contains(got, "GOCACHE=/old") {
		t.Error("the old cache stayed")
	}
}

func TestFixCredentialPaths(t *testing.T) {
	paths := FixCredentialPaths("/home/u", []string{"CLAUDE_CONFIG_DIR=/cfg/claude", "CODEX_HOME=/cfg/codex", "CODEX_HOME=relative"})
	for _, want := range []string{"/home/u/.ssh", "/home/u/.aws", "/home/u/.config/gcloud", "/home/u/.config/gh", "/home/u/.netrc", "/home/u/.git-credentials",
		"/home/u/.docker/config.json", "/home/u/.npmrc", "/home/u/.pypirc", "/home/u/.kube", "/home/u/.gnupg", "/home/u/.codex/auth.json",
		"/home/u/.claude/.credentials.json", "/cfg/claude/.credentials.json", "/cfg/codex/auth.json"} {
		if !slices.Contains(paths, filepath.FromSlash(want)) {
			t.Errorf("missing %s", want)
		}
	}
	if slices.ContainsFunc(paths, func(p string) bool { return strings.Contains(p, "relative") }) {
		t.Error("a relative CODEX_HOME was used")
	}
	if Home([]string{"HOME=/a", "HOME=/b"}) != "/b" {
		t.Error("the last HOME does not win")
	}
}
