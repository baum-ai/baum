package gitlocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/lastpersonlabs/baum/internal/textclean"
)

const (
	// maxStderrBytes bounds the stderr git may write; the rest is dropped.
	maxStderrBytes = 64 << 10
	// maxStderrSnippet bounds the stderr text an error carries.
	maxStderrSnippet = 512
	// waitDelay bounds how long a killed git's pipes may stay open.
	waitDelay = 5 * time.Second
)

// envMode selects how much of the user's git configuration a command sees.
type envMode int

const (
	// envHermetic ignores system and global configuration and normalises the
	// diff settings, so output depends only on the repository's objects.
	envHermetic envMode = iota
	// envUser keeps the user's configuration (credential helpers, URL
	// rewrites, SSH settings) for commands that talk to a remote.
	envUser
)

// hermeticConfig overrides repository-local settings that would change diff
// output, so a clone's own configuration cannot make local output differ from
// the output of a bare mirror with default configuration.
var hermeticConfig = []string{
	"color.ui=false",
	"core.attributesFile=/dev/null",
	"core.quotepath=true",
	"diff.algorithm=myers",
	"diff.context=3",
	"diff.dstPrefix=b/",
	"diff.interHunkContext=0",
	"diff.mnemonicPrefix=false",
	"diff.noprefix=false",
	"diff.srcPrefix=a/",
}

// emptyTreeOID makes git read no .gitattributes from the working tree, as in
// a bare mirror, so a file's own attributes cannot hide it as binary.
const emptyTreeOID = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// strippedEnv are variables that would redirect git to another repository or
// run something interactive or user-chosen.
var strippedEnv = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true,
	"GIT_COMMON_DIR": true, "GIT_OBJECT_DIRECTORY": true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_NAMESPACE": true,
	"GIT_EXTERNAL_DIFF": true, "GIT_PAGER": true, "GIT_EDITOR": true,
	"GIT_SEQUENCE_EDITOR": true, "GIT_CONFIG_COUNT": true,
	"GIT_CONFIG_PARAMETERS": true, "GIT_CONFIG": true, "PAGER": true,
	"GIT_DIFF_OPTS": true, "GIT_PREFIX": true, "GIT_CEILING_DIRECTORIES": true,
}

func gitEnv(mode envMode) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strippedEnv[name] || strings.HasPrefix(name, "GIT_CONFIG_KEY_") || strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
			continue
		}
		if mode == envHermetic && (name == "GIT_CONFIG_GLOBAL" || name == "GIT_CONFIG_SYSTEM" || name == "GIT_CONFIG_NOSYSTEM" || name == "GIT_ATTR_NOSYSTEM") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat", "PAGER=cat", "GIT_EDITOR=true",
		"GIT_OPTIONAL_LOCKS=0", "LC_ALL=C",
	)
	if mode == envHermetic {
		env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_ATTR_NOSYSTEM=1")
	}
	return env
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
	// dir is the working directory (the repository).
	dir string
	// limit caps stdout; zero discards it.
	limit   int64
	mode    envMode
	timeout time.Duration
	// config are extra -c settings, applied before the subcommand.
	config []string
	// global are extra options placed before the subcommand.
	global []string
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
	full = append(full, spec.global...)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = spec.dir
	cmd.Env = gitEnv(spec.mode)
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
