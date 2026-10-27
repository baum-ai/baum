// Package hookevents carries what coding-agent and Git hooks report to the
// user's baum daemon: the event a hook sends over the daemon's control socket,
// the private spool a hook falls back to when the daemon cannot take it, and
// the bounded local record the daemon keeps of every event it received, which
// code inside the daemon reads and subscribes to.
//
// A hook must never slow the agent or Git noticeably, so Sender bounds what it
// adds (Budget) and never fails: an event the daemon does not take within the
// bound goes to the spool, which the daemon drains when it starts and
// periodically while it runs.
package hookevents

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"
)

// Version is the event format a hook sends and the daemon reads. Adding a
// field does not change it: the daemon ignores fields it does not know.
// Raise it only for a change an older daemon would misread; such a daemon
// refuses the event (CodeEventVersion) and the hook spools it for a newer
// one.
const Version = 1

// Method is the control request that delivers one event; its params are the
// Event.
const Method = "event"

// CodeEventVersion is the error code of a daemon that refuses an event of a
// newer Version.
const CodeEventVersion = "event_version"

// Kinds of event.
const (
	KindAgent = "agent"
	KindGit   = "git"
)

// Bounds on what one event carries, so an encoded event stays well below the
// control protocol's 64 KiB line.
const (
	maxField = 2048
	// maxMessage bounds a notification's text: the app shows one line of it.
	maxMessage   = 512
	maxGitLines  = 64
	maxGitLine   = 256
	maxGitArgs   = 4
	maxEventSize = 48 << 10
)

// Event is one hook run.
type Event struct {
	// V is the Version the hook wrote.
	V int `json:"v"`
	// ID is random; the daemon records an event once however often it
	// arrives (a late answer and the spool can both deliver it).
	ID string `json:"id"`
	// Time is when the hook ran.
	Time time.Time `json:"time"`
	// Kind is KindAgent or KindGit.
	Kind string `json:"kind"`
	// Agent is the coding agent's slug for an agent hook (claude-code,
	// codex, cursor).
	Agent string `json:"agent,omitempty"`
	// Name is the agent hook event (session-start, stop, session-end, ...)
	// or the Git hook (post-commit, pre-push, ...).
	Name string `json:"name"`
	// Dir is the hook's working directory.
	Dir string `json:"dir,omitempty"`
	// Exit is the status the hook exited with, which the agent or Git acts
	// on: a pre-push that exits non-zero stops the push.
	Exit int `json:"exit,omitempty"`
	// Client is the version of the baum that ran the hook.
	Client string `json:"client,omitempty"`
	// Session holds the identifiers an agent hook's payload carries.
	Session *Session `json:"session,omitempty"`
	// Git describes a Git hook run.
	Git *Git `json:"git,omitempty"`
}

// Session is the part of an agent hook's payload an event keeps: identifiers
// and paths, and for a notification the line the agent showed its user.
// Prompts, tool input and output, and anything else the payload carries are
// never kept.
type Session struct {
	// ID is the agent's session id (Cursor's conversation id).
	ID string `json:"id,omitempty"`
	// TranscriptPath is the session's transcript file, when the agent names
	// it.
	TranscriptPath string `json:"transcript_path,omitempty"`
	// Cwd is the agent's working directory.
	Cwd string `json:"cwd,omitempty"`
	// HookEvent is the agent's own name for the event (SessionEnd, ...).
	HookEvent string `json:"hook_event,omitempty"`
	// Source says how a session started (startup, resume, ...) and Reason
	// why it ended, where the agent reports them.
	Source string `json:"source,omitempty"`
	Reason string `json:"reason,omitempty"`
	// NotificationType and Message are a Claude Code notification's
	// (permission_prompt, idle_prompt, ...) and the text it showed, such as
	// "Claude needs your permission to use Bash", cut to a few hundred
	// bytes. No other event carries them.
	NotificationType string `json:"notification_type,omitempty"`
	Message          string `json:"message,omitempty"`
}

// Git is a Git hook run.
type Git struct {
	// Args are the hook's arguments, except a pre-push hook's remote URL,
	// which can carry credentials; its remote name is kept.
	Args []string `json:"args,omitempty"`
	// Lines are what Git wrote to the hook's standard input: the refs a
	// pre-push sends, the commits a post-rewrite replaced.
	Lines []string `json:"lines,omitempty"`
}

// NewID returns a random event id: 32 lowercase hexadecimal digits.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// New starts an event of this Version with a new id, stamped now.
func New(kind, agent, name, dir, client string) Event {
	return Event{V: Version, ID: NewID(), Time: time.Now().UTC(), Kind: kind, Agent: agent, Name: name, Dir: dir, Client: client}
}

// Bound cuts the event to the event bounds, so a hook never builds an event
// the daemon would refuse for its size: every field to its own bound, then,
// while the encoding is still too large (escaping can make a field up to six
// times longer than its bytes), the Git lines from the end and the longest
// fields by half.
func (e *Event) Bound() {
	for _, field := range e.fields() {
		*field = cut(*field, maxField)
	}
	if s := e.Session; s != nil {
		s.Message = cut(s.Message, maxMessage)
	}
	if g := e.Git; g != nil {
		if len(g.Args) > maxGitArgs {
			g.Args = g.Args[:maxGitArgs]
		}
		for i := range g.Args {
			g.Args[i] = cut(g.Args[i], maxField)
		}
		if len(g.Lines) > maxGitLines {
			g.Lines = g.Lines[:maxGitLines]
		}
		for i := range g.Lines {
			g.Lines[i] = cut(g.Lines[i], maxGitLine)
		}
	}
	for e.size() > maxEventSize {
		if e.Git != nil && len(e.Git.Lines) > 0 {
			e.Git.Lines = e.Git.Lines[:len(e.Git.Lines)-1]
			continue
		}
		var longest *string
		for _, field := range e.fields() {
			if longest == nil || len(*field) > len(*longest) {
				longest = field
			}
		}
		if longest == nil || *longest == "" {
			return
		}
		*longest = cut(*longest, len(*longest)/2)
	}
}

// fields are the event's strings, Git lines excepted.
func (e *Event) fields() []*string {
	fields := []*string{&e.Agent, &e.Name, &e.Dir, &e.Client}
	if s := e.Session; s != nil {
		fields = append(fields, &s.ID, &s.TranscriptPath, &s.Cwd, &s.HookEvent, &s.Source, &s.Reason, &s.NotificationType, &s.Message)
	}
	if g := e.Git; g != nil {
		for i := range g.Args {
			fields = append(fields, &g.Args[i])
		}
	}
	return fields
}

// size is the event's length as the control request carries it, which
// escapes <, > and & too, so it is the longest encoding the event has.
func (e *Event) size() int {
	data, err := json.Marshal(e)
	if err != nil {
		return 0
	}
	return len(data)
}

// encode is the event as the spool stores it.
func (e Event) encode() ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(e); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// cut shortens s to at most n bytes, at a character boundary when the cut
// falls inside a valid character; invalid bytes elsewhere stay.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for i := n; i > 0 && i > n-utf8.UTFMax; i-- {
		if utf8.RuneStart(s[i]) {
			if r, size := utf8.DecodeRuneInString(s[i:]); (r != utf8.RuneError || size > 1) && i+size > n {
				return s[:i]
			}
			break
		}
	}
	return s[:n]
}

// errInvalid is an event no daemon can record.
var errInvalid = errors.New("invalid hook event")

// check reports whether the daemon can record e: a version it reads, an id,
// a kind and a name. A kind it does not know is recorded all the same, for
// readers that do.
func (e Event) check() error {
	switch {
	case e.V > Version:
		return errNewer
	case e.V < 1 || len(e.ID) == 0 || len(e.ID) > 64 || e.Kind == "" || e.Name == "":
		return errInvalid
	}
	return nil
}

// errNewer is an event of a Version this daemon does not read.
var errNewer = errors.New("the hook event is of a newer format than this baum daemon reads")
