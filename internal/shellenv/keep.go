package shellenv

import (
	"runtime"
	"slices"
	"sort"
	"strings"
)

// Locations decide where baum keeps its files: the daemon's lock, socket and
// job journal, the registry, caches and stores. They always come from the
// starting process, never from the login shell, because the daemon resolved
// its lock and socket from them before reading the shell; a location only the
// shell sets would put its job journal somewhere another daemon could also
// use.
var Locations = []string{
	"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR",
	"TMPDIR", "USERPROFILE", "APPDATA", "LOCALAPPDATA",
}

// Kept lists the variables a background baum process keeps, besides BAUM_*
// and LC_*: the session, the tools' PATH, network settings, GitHub tokens and
// the backend CLIs' configuration directories. Nothing else is passed on.
var Kept = []string{
	"USER", "LOGNAME", "SHELL", "LANG", "TZ",
	"DBUS_SESSION_BUS_ADDRESS", "DISPLAY", "WAYLAND_DISPLAY",
	"PATH",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "no_proxy", "all_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
	"GITHUB_TOKEN", "GH_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", "GH_CONFIG_DIR", "GH_PATH",
	"ANTHROPIC_API_KEY", "CLAUDE_CONFIG_DIR", "CODEX_HOME",
}

// keptWindows are the system variables Windows programs need.
var keptWindows = []string{
	"SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP", "HOMEDRIVE", "HOMEPATH",
	"PROGRAMFILES", "PROGRAMFILES(X86)", "PROGRAMW6432", "PROGRAMDATA", "USERNAME", "USERDOMAIN",
	"NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE",
}

// Merge builds a background process's environment. The Locations only ever
// come from inherited, and stay unset when it lacks them. With a resolved
// login shell environment, every other kept variable comes from it, and one
// only the inherited environment has is dropped, so the result does not
// depend on who started the process. Without one (resolved is nil), kept
// variables come from inherited.
func Merge(inherited []string, resolved map[string]string) []string {
	windows := runtime.GOOS == "windows"
	return merge(windows, inherited, resolved)
}

func merge(windows bool, inherited []string, resolved map[string]string) []string {
	fold := func(name string) string {
		if windows {
			return strings.ToUpper(name)
		}
		return name
	}
	kept := func(name string) bool {
		name = fold(name)
		if name == "" || name == fold(ResolvingVariable) {
			return false
		}
		if strings.HasPrefix(name, "BAUM_") || strings.HasPrefix(name, "LC_") {
			return true
		}
		match := func(list []string) bool {
			return slices.ContainsFunc(list, func(v string) bool { return fold(v) == name })
		}
		return match(Locations) || match(Kept) || (windows && match(keptWindows))
	}
	location := func(name string) bool {
		return slices.ContainsFunc(Locations, func(v string) bool { return fold(v) == fold(name) })
	}
	out := map[string]string{}
	names := map[string]string{} // folded name to the spelling kept
	put := func(name, value string) {
		names[fold(name)] = name
		out[fold(name)] = value
	}
	for name, value := range resolved {
		if kept(name) && !location(name) {
			put(name, value)
		}
	}
	for _, entry := range inherited {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !kept(name) {
			continue
		}
		if resolved == nil || location(name) {
			put(name, value)
		}
	}
	env := make([]string, 0, len(out))
	for key, value := range out {
		env = append(env, names[key]+"="+value)
	}
	sort.Strings(env)
	return env
}

// Names returns the variable names of env, for logs that must not hold values.
func Names(env []string) []string {
	names := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		names = append(names, name)
	}
	return names
}
