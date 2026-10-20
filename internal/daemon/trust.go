package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// TrustedExecutables are further executables a daemon may restart into at a
// newer client's request, beside its own executable and the baum on its login
// shell's PATH. Entries are absolute paths, compared after resolving symbolic
// links, and are set before Run. It is empty: the desktop app's bundled baum
// belongs here once the app ships one.
var TrustedExecutables []string

// executablePolicy decides whether the daemon may run an executable a client
// named as the newer version to restart into. Any process of this user can
// reach the control socket, including an agent hook run from a sandboxed
// command that can write files in a workspace but not run them unsandboxed,
// so the executable must be an installed baum, not merely a file of this
// user: after resolving symbolic links it must be one of the allowed
// executables (this daemon's own, for an upgrade in place; the baum on the
// daemon's PATH, which is the login shell's; or TrustedExecutables), a
// regular file, and on Unix executable, owned by this user or root and not
// group- or world-writable, in directories that are all, up to the root, as
// well (so not under a sticky, world-writable directory such as /tmp). On
// Windows owners and permission bits do not describe its ACLs, so only the
// allowed executables are checked there.
type executablePolicy struct {
	// self is this daemon's executable; nil is os.Executable.
	self func() (string, error)
	// lookPath finds baum on the PATH; nil is exec.LookPath.
	lookPath func(file string) (string, error)
	// extra are further allowed executables; nil is TrustedExecutables.
	extra []string
	// top, set by tests only, ends the walk over the parent directories
	// below the root.
	top string
}

// trustedExecutable checks path with the default policy.
func trustedExecutable(path string) (string, error) { return executablePolicy{}.trusted(path) }

// trusted returns path with its symbolic links resolved if the daemon may run
// it, and why not otherwise.
func (p executablePolicy) trusted(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("the executable is not an absolute path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a file", resolved)
	}
	if !p.allowed(resolved) {
		return "", fmt.Errorf("%s is neither this daemon's executable nor the baum on its PATH", resolved)
	}
	if err = secure(resolved, info, p.top); err != nil {
		return "", err
	}
	return resolved, nil
}

// allowed reports whether resolved is one of the allowed executables.
func (p executablePolicy) allowed(resolved string) bool {
	self, lookPath, extra := p.self, p.lookPath, p.extra
	if self == nil {
		self = os.Executable
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if extra == nil {
		extra = TrustedExecutables
	}
	candidates := append([]string{}, extra...)
	if exe, err := self(); err == nil {
		candidates = append(candidates, exe)
	}
	if exe, err := lookPath("baum"); err == nil {
		candidates = append(candidates, exe)
	}
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) {
			continue
		}
		if r, err := filepath.EvalSymlinks(candidate); err == nil && samePath(r, resolved) {
			return true
		}
	}
	return false
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
