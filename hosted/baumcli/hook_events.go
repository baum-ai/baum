package baumcli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/hookevents"
)

// Seams for tests: where the daemon is, and how a hook starts it (nil runs
// this executable as `baum daemon run`).
var (
	hookDaemonPaths = daemon.DefaultPaths
	hookDaemonSpawn daemon.Spawner
)

// sendHookEvent hands a hook's event to the user's daemon, starting it when it
// is not running, within hookevents.Budget. It never fails the hook and writes
// nothing to the hook's output: what it spools or drops goes to the hook log.
func sendHookEvent(ctx context.Context, event hookevents.Event) {
	paths, err := hookDaemonPaths()
	if err != nil {
		return
	}
	sender := &hookevents.Sender{Paths: paths, Version: version(), Spawn: hookDaemonSpawn}
	sender.Send(context.WithoutCancel(ctx), event)
}

// newHookEvent starts the event of a hook run in the working directory.
func newHookEvent(kind, agent, name string) hookevents.Event {
	dir, _ := os.Getwd()
	return hookevents.New(kind, agent, name, dir, version())
}

// exitStatus is the status a hook's result makes the process exit with.
func exitStatus(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := errors.AsType[*ExitError](err); ok {
		return exitErr.Code
	}
	return 1
}

// Bounds on reading a hook's standard input for its event.
const (
	// maxHookInput is the most of it kept; the rest still reaches Entire.
	maxHookInput = 256 << 10
	// hookInputWait is how long a hook waits, once Entire is done, for its
	// input to end. Agents and Git close it right after writing, so it has
	// ended by then; an input left open is not waited for.
	hookInputWait = 20 * time.Millisecond
)

// hookInput keeps a bounded copy of a hook's standard input for its event
// while Entire, which owns the hook protocol, reads it unchanged.
type hookInput struct {
	source io.Reader
	mu     sync.Mutex
	data   []byte
	done   chan struct{}
}

// newHookInput captures in, unless it is a terminal or a device such as
// /dev/null, which carry no payload and are passed on untouched.
func newHookInput(in io.Reader) *hookInput {
	if in == nil {
		return nil
	}
	if file, ok := in.(*os.File); ok {
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice != 0 {
			return nil
		}
	}
	return &hookInput{source: in, done: make(chan struct{})}
}

// pipe returns the read end of a pipe the delegated command reads in place of
// the input, and copies the input into it. The caller closes the read end
// once the command has started. Entire then sees the same bytes, through an
// ordinary file descriptor, as it did when it inherited the input.
func (h *hookInput) pipe() (*os.File, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	go func() {
		h.copy(w)
		_ = w.Close()
		close(h.done)
	}()
	return r, nil
}

// read captures the input when no command reads it.
func (h *hookInput) read() {
	if h == nil {
		return
	}
	go func() {
		h.copy(nil)
		close(h.done)
	}()
}

func (h *hookInput) copy(w *os.File) {
	buf := make([]byte, 32<<10)
	for {
		n, err := h.source.Read(buf)
		if n > 0 {
			h.mu.Lock()
			if room := maxHookInput - len(h.data); room > 0 {
				h.data = append(h.data, buf[:min(n, room)]...)
			}
			h.mu.Unlock()
			// A command that exits without reading everything closes the
			// pipe; the input is still read for the copy.
			if w != nil {
				if _, werr := w.Write(buf[:n]); werr != nil {
					w = nil
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// payload is what the input carried, once it ended or hookInputWait passed.
func (h *hookInput) payload() []byte {
	if h == nil {
		return nil
	}
	select {
	case <-h.done:
	case <-time.After(hookInputWait):
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return bytes.Clone(h.data)
}

// agentSession reads the identifiers an agent hook's payload carries. Only
// these string fields are kept, never prompts or tool input and output.
func agentSession(payload []byte) *hookevents.Session {
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return nil
	}
	text := func(names ...string) string {
		for _, name := range names {
			var value string
			if json.Unmarshal(fields[name], &value) == nil && value != "" {
				return value
			}
		}
		return ""
	}
	session := &hookevents.Session{
		ID:             text("session_id", "conversation_id"),
		TranscriptPath: text("transcript_path"),
		Cwd:            text("cwd"),
		HookEvent:      text("hook_event_name"),
		Source:         text("source"),
		Reason:         text("reason"),
	}
	if *session == (hookevents.Session{}) {
		return nil
	}
	return session
}

// gitHookEvent describes a Git hook run: its arguments, except a pre-push
// hook's remote URL, which can carry credentials, and the lines Git wrote to
// its standard input.
func gitHookEvent(args []string, input []byte) *hookevents.Git {
	git := &hookevents.Git{}
	if len(args) > 1 {
		git.Args = append(git.Args, args[1:]...)
		if args[0] == "pre-push" {
			git.Args = git.Args[:1]
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(input))
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			git.Lines = append(git.Lines, line)
		}
	}
	if len(git.Args) == 0 && len(git.Lines) == 0 {
		return nil
	}
	return git
}
