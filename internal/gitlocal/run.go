package gitlocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/proc"
	"github.com/baum-ai/baum/internal/textclean"
)

const (
	// maxStderrBytes bounds the stderr git may write; the rest is dropped.
	maxStderrBytes = 64 << 10
	// maxStderrSnippet bounds the stderr text an error carries.
	maxStderrSnippet = 512
	// waitDelay bounds how long a killed git's pipes may stay open.
	waitDelay = 5 * time.Second
)

// strippedEnv are variables that would redirect git to another repository,
// change how it reads pathspecs, objects or history, trace it to a file or
// to the stderr an error carries, or run something interactive. A command
// in the clone (the fetch, and finding the repository) runs without them;
// a read runs without any GIT_ variable at all.
var strippedEnv = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true,
	"GIT_COMMON_DIR": true, "GIT_OBJECT_DIRECTORY": true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_NAMESPACE": true,
	"GIT_QUARANTINE_PATH": true, "GIT_EXTERNAL_DIFF": true, "GIT_PAGER": true,
	"GIT_EDITOR": true, "GIT_SEQUENCE_EDITOR": true, "GIT_CONFIG_COUNT": true,
	"GIT_CONFIG_PARAMETERS": true, "GIT_CONFIG": true, "PAGER": true,
	"GIT_DIFF_OPTS": true, "GIT_PREFIX": true, "GIT_CEILING_DIRECTORIES": true,
	"GIT_LITERAL_PATHSPECS": true, "GIT_GLOB_PATHSPECS": true,
	"GIT_NOGLOB_PATHSPECS": true, "GIT_ICASE_PATHSPECS": true,
	"GIT_GRAFT_FILE": true, "GIT_SHALLOW_FILE": true, "GIT_REPLACE_REF_BASE": true,
	"GIT_NO_REPLACE_OBJECTS": true, "GIT_ATTR_SOURCE": true, "GIT_DEFAULT_HASH": true,
	"GIT_CURL_VERBOSE": true, "GIT_REF_PARANOIA": true,
}

var strippedEnvPrefixes = []string{"GIT_TRACE", "GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_", "GIT_TEST_"}

// fixedEnv disables prompts, pagers, editors and optional locks, and fixes
// the locale of git's messages.
var fixedEnv = []string{
	"GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "PAGER=cat", "GIT_EDITOR=true",
	"GIT_OPTIONAL_LOCKS=0", "LC_ALL=C",
}

// userEnv is the environment of a command in the user's clone. It keeps the
// user's configuration, so credential helpers, URL rewrites, SSH settings
// and safe.directory work as they do for the user.
func userEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if strippedEnv[upper] || slices.ContainsFunc(strippedEnvPrefixes, func(p string) bool { return strings.HasPrefix(upper, p) }) {
			continue
		}
		env = append(env, kv)
	}
	return append(env, fixedEnv...)
}

// mirrorEnv is the environment of a read in the private mirror at dir: no
// GIT_ variable from the caller, no system or global configuration or
// attributes, no replace refs, and a home directory with nothing in it.
func mirrorEnv(dir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if (strings.HasPrefix(upper, "GIT_") && upper != "GIT_EXEC_PATH") || strippedEnv[upper] ||
			upper == "HOME" || upper == "XDG_CONFIG_HOME" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, fixedEnv...)
	return append(env,
		"GIT_DIR="+dir,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(dir, emptyConfigName),
		"GIT_ATTR_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1",
		// The private repository has no remote, so git has nowhere to fetch a
		// missing object from; this makes the refusal explicit.
		"GIT_NO_LAZY_FETCH=1",
		"HOME="+dir, "XDG_CONFIG_HOME="+filepath.Join(dir, "xdg"),
	)
}

// Error is a failed git command. It names the subcommand and carries a
// bounded, sanitised stderr snippet, never the arguments or full output.
type Error struct {
	Command string
	Stderr  string
	Err     error
}

func (e *Error) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("git %s: %v", e.Command, e.Err)
	}
	return fmt.Sprintf("git %s: %v: %s", e.Command, e.Err, e.Stderr)
}

func (e *Error) Unwrap() error { return e.Err }

var userinfo = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/\s@]*@`)

// sanitize makes stderr safe to put in an error: credentials in URLs are
// masked, control characters dropped, and the text bounded.
func sanitize(stderr []byte) string {
	text := textclean.SafeText(strings.ReplaceAll(string(stderr), "\x00", ""))
	text = userinfo.ReplaceAllString(text, "${1}***@")
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > maxStderrSnippet {
		text = strings.ToValidUTF8(text[:maxStderrSnippet], "") + "..."
	}
	return text
}

type limitedWriter struct {
	buf       bytes.Buffer
	remaining int64
	discard   bool
	exceeded  bool
	cancel    context.CancelFunc
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.discard {
		return len(p), nil
	}
	if int64(len(p)) > w.remaining {
		w.exceeded = true
		w.cancel()
		return 0, errors.New("output limit reached")
	}
	w.remaining -= int64(len(p))
	return w.buf.Write(p)
}

// capWriter keeps at most max bytes and silently drops the rest.
type capWriter struct {
	buf bytes.Buffer
	max int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		w.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

type runSpec struct {
	// dir is the working directory.
	dir string
	// env is the complete environment.
	env []string
	// limit caps stdout; zero discards it.
	limit   int64
	timeout time.Duration
	// config are extra -c settings, applied before the subcommand.
	config []string
}

// run executes git without a shell, under a timeout, with stdout and stderr
// capped. Exceeding the stdout cap kills git and returns ErrOutputTooLarge
// (from the bundle package, via the caller's sentinel).
func run(ctx context.Context, spec runSpec, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("git: no subcommand")
	}
	ctx, cancel := context.WithTimeout(ctx, spec.timeout)
	defer cancel()
	var full []string
	for _, c := range spec.config {
		full = append(full, "-c", c)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	proc.Configure(cmd)
	cmd.Dir = spec.dir
	cmd.Env = spec.env
	cmd.WaitDelay = waitDelay
	stdout := &limitedWriter{remaining: spec.limit, discard: spec.limit <= 0, cancel: cancel}
	stderr := &capWriter{max: maxStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if stdout.exceeded {
		return nil, errOutputTooLarge
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
			err = fmt.Errorf("%w: %w", err, ctxErr)
		}
		return nil, &Error{Command: args[0], Stderr: sanitize(stderr.buf.Bytes()), Err: err}
	}
	return stdout.buf.Bytes(), nil
}
