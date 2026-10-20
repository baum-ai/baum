// Package shellenv reads the environment a user's login shell sets up, so a
// background process started from a desktop launcher (with a bare PATH) or
// from a coding agent (with the agent's environment) sees the same tools and
// settings as the user's terminal.
//
// The shell runs as an interactive login shell in its own session, without a
// terminal, with a timeout. It is given only the session's own variables (see
// Session) and a default PATH, so its output depends on the user's startup
// files rather than on whoever started baum. A program printed between two
// random markers reports the resulting environment as JSON, so whatever the
// startup files print is ignored. Values are never logged.
//
// Windows has no login shell. There the result is the inherited environment
// with the PATH a new logon would get: the system Path followed by the
// user's, from the registry. A tool installed after the starting process was
// (winget, an installer that edits the user Path) is found without signing
// out, and so is one only the user's Path names when an agent started baum.
package shellenv

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// DefaultTimeout bounds one resolution; slow startup files fall back to the
// inherited environment.
const DefaultTimeout = 10 * time.Second

// ResolvingVariable is set to 1 in the shell's environment, so startup files
// can skip slow or interactive work while baum reads the environment.
const ResolvingVariable = "BAUM_RESOLVING_ENVIRONMENT"

// defaultPath is the PATH the shell starts with, as login(1) would give it.
const defaultPath = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

// maxOutput bounds what the shell may print.
const maxOutput = 1 << 20

// ErrUnsupported is returned where there is neither a login shell nor a
// registry to ask.
var ErrUnsupported = errors.New("login shell environment is not supported on this platform")

// Options configure Resolve.
type Options struct {
	// Shell is the login shell; empty uses $SHELL from Environ, then the
	// user database.
	Shell string
	// Printer is the program, and its leading arguments, that prints the
	// environment with PrintEnv; Resolve appends the marker. Empty runs this
	// executable as "daemon print-env".
	Printer []string
	// Timeout bounds the shell; zero means DefaultTimeout.
	Timeout time.Duration
	// Environ is the inherited environment the session variables are taken
	// from; nil uses os.Environ().
	Environ []string
}

// Result is a resolved environment and the shell that produced it.
type Result struct {
	Shell string
	Env   map[string]string
}

// Resolve runs the login shell and returns the environment it set up. A shell
// that is slow, fails or prints no environment is an error; callers fall back
// to the inherited environment.
func Resolve(ctx context.Context, opts Options) (Result, error) {
	environ := opts.Environ
	if environ == nil {
		environ = os.Environ()
	}
	if runtime.GOOS == "windows" {
		path, err := registryPath()
		if err != nil {
			return Result{}, err
		}
		return Result{Env: withPath(environ, path)}, nil
	}
	shell := opts.Shell
	if shell == "" {
		shell = detectShell(lookup(environ, "SHELL"))
	}
	if !filepath.IsAbs(shell) {
		return Result{}, errors.New("no login shell found")
	}
	printer := opts.Printer
	if len(printer) == 0 {
		self, err := os.Executable()
		if err != nil {
			return Result{Shell: shell}, err
		}
		printer = []string{self, "daemon", "print-env"}
	}
	marker, err := newMarker()
	if err != nil {
		return Result{Shell: shell}, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := quoteAll(append(append([]string{}, printer...), marker))
	cmd := exec.CommandContext(ctx, shell, append(Arguments(shell), command)...)
	cmd.Env = shellEnvironment(environ, shell)
	cmd.Dir = lookup(cmd.Env, "HOME")
	if cmd.Dir == "" {
		cmd.Dir = "/"
	}
	out := &limited{max: maxOutput}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	detach(cmd)
	cmd.WaitDelay = time.Second
	runErr := cmd.Run()
	// Startup files may leave background processes behind (an ssh-agent, a
	// version manager's daemon); none of them belongs to baum.
	killGroup(cmd)
	if ctx.Err() != nil {
		return Result{Shell: shell}, fmt.Errorf("login shell %s did not finish within %s", shell, timeout)
	}
	env, parseErr := parse(out.Bytes(), marker)
	if parseErr != nil {
		if runErr != nil {
			return Result{Shell: shell}, fmt.Errorf("login shell %s failed: %w", shell, runErr)
		}
		return Result{Shell: shell}, fmt.Errorf("login shell %s: %w", shell, parseErr)
	}
	// Startup files may exit non-zero after the environment was printed.
	return Result{Shell: shell, Env: env}, nil
}

// Arguments are the flags that make shell an interactive login shell running
// one command, which the caller appends.
func Arguments(shell string) []string {
	switch filepath.Base(shell) {
	case "csh", "tcsh":
		// csh accepts -l only on its own; -i reads .cshrc, which is where csh
		// users set their environment.
		return []string{"-i", "-c"}
	default:
		// bash, zsh, fish, ksh, dash and sh.
		return []string{"-i", "-l", "-c"}
	}
}

// Session lists the inherited variables the shell is given: where the user's
// files are and how to reach the desktop session's agents. Everything else
// comes from the shell's startup files.
var Session = []string{
	"HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "LANG", "TZ",
	"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR",
	"SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS", "DISPLAY", "WAYLAND_DISPLAY",
}

func shellEnvironment(environ []string, shell string) []string {
	env := []string{"PATH=" + defaultPath, "SHELL=" + shell, ResolvingVariable + "=1", "TERM=dumb"}
	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "SHELL" || name == "PATH" || name == "TERM" {
			continue
		}
		if strings.HasPrefix(name, "LC_") || slices.Contains(Session, name) {
			env = append(env, entry)
		}
	}
	return env
}

// PrintEnv writes this process's environment as a JSON object between two
// copies of marker. It is what the shell runs.
func PrintEnv(w io.Writer, marker string) error {
	env := map[string]string{}
	for _, entry := range os.Environ() {
		if name, value, ok := strings.Cut(entry, "="); ok && name != "" {
			env[name] = value
		}
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s%s%s\n", marker, data, marker)
	return err
}

func parse(out []byte, marker string) (map[string]string, error) {
	_, rest, ok := bytes.Cut(out, []byte(marker))
	if !ok {
		return nil, errors.New("no environment printed")
	}
	body, _, ok := bytes.Cut(rest, []byte(marker))
	if !ok {
		return nil, errors.New("environment output was cut short")
	}
	var env map[string]string
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, errors.New("environment output is not valid")
	}
	return env, nil
}

func newMarker() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "__baum_env_" + hex.EncodeToString(b[:]) + "__", nil
}

// quoteAll single-quotes each word for POSIX shells and fish. A quote inside a
// word closes the quoted text, adds an escaped quote and reopens it, which
// both kinds of shell read the same way.
func quoteAll(words []string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// withPath is environ with its PATH, in whatever case it is spelled,
// replaced by path. Entries without a name, such as Windows' per-drive
// directories ("=C:=C:\work"), are not variables and are left out.
func withPath(environ []string, path string) map[string]string {
	env := map[string]string{}
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" || strings.EqualFold(name, "PATH") {
			continue
		}
		env[name] = value
	}
	env["Path"] = path
	return env
}

func lookup(environ []string, name string) string {
	for i := len(environ) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(environ[i], "="); ok && k == name {
			return v
		}
	}
	return ""
}

// limited keeps at most max bytes and drops the rest.
type limited struct {
	buf bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}
func (l *limited) Bytes() []byte { return l.buf.Bytes() }
