package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFixEnvironmentDropsTokensAndMovesCaches(t *testing.T) {
	environ := []string{"PATH=/bin", "HOME=/home/user", "GITHUB_TOKEN=a", "GH_TOKEN=b", "GH_HOST=c", "SSH_AUTH_SOCK=/s", "GIT_DIR=/r", "GIT_ASKPASS=/p",
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
		"XDG_CACHE_HOME=" + filepath.Join("/fix/tmp", "cache"), "GIT_LFS_SKIP_SMUDGE=1"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing %s in %q", want, got)
		}
	}
	if slices.Contains(got, "GOCACHE=/old") {
		t.Error("the old cache stayed")
	}
}

func TestFixCredentialPaths(t *testing.T) {
	// abs makes a slash path absolute on this OS: "/cfg" is relative on
	// Windows, which needs a volume.
	abs := func(p string) string { return filepath.VolumeName(os.TempDir()) + filepath.FromSlash(p) }
	paths := FixCredentialPaths(abs("/home/user"), []string{"CLAUDE_CONFIG_DIR=" + abs("/cfg/claude"), "CODEX_HOME=" + abs("/cfg/codex"), "CODEX_HOME=relative",
		"XDG_RUNTIME_DIR=" + abs("/run/user/1000")})
	for _, want := range []string{"/home/user/.ssh", "/home/user/.aws", "/home/user/.config/gcloud", "/home/user/.config/gh", "/home/user/.netrc", "/home/user/.git-credentials",
		"/home/user/.docker/config.json", "/home/user/.npmrc", "/home/user/.pypirc", "/home/user/.kube", "/home/user/.gnupg", "/home/user/.codex/auth.json",
		"/home/user/.claude/.credentials.json", "/cfg/claude/.credentials.json", "/cfg/codex/auth.json", "/run/user/1000"} {
		if !slices.Contains(paths, abs(want)) {
			t.Errorf("missing %s in %q", want, paths)
		}
	}
	if slices.ContainsFunc(paths, func(p string) bool { return strings.Contains(p, "relative") }) {
		t.Error("a relative CODEX_HOME was used")
	}
	if Home([]string{"HOME=" + abs("/a"), "HOME=" + abs("/b")}) != abs("/b") {
		t.Error("the last HOME does not win")
	}
}
