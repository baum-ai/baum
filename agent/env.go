package agent

import (
	"runtime"
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
}

// LocalEnvironment returns the variables of environ a local CLI backend may
// pass to its child: a fixed allowlist, plus the names in extra, which the
// calling backend trusts (for example CLAUDE_CONFIG_DIR). Provider credentials
// and configuration, cloud and GitHub tokens and the SSH agent are never in
// the allowlist, so they do not reach the child unless a backend names them.
// Each element of environ is "NAME=value".
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
		if allowed[name] || strings.HasPrefix(name, "LC_") {
			kept = append(kept, entry)
		}
	}
	return kept
}
