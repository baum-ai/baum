package inboxes

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/appdb"
)

// Limits and the allowed values of the enumerated fields.
const (
	MaxInboxes      = 20
	MaxNameLength   = 40
	MaxOwners       = 50
	MaxRepositories = 50
)

var (
	whats       = []string{"any", "review", "merge", "agent", "mine"}
	risks       = []string{"any", "3", "4"}
	colors      = []string{"blue", "green", "red", "amber", "purple", "grey"}
	involvement = []string{"assigned", "requested", "authored"}
	reviews     = []string{"approved", "changes_requested", "none"}
	checks      = []string{"passing", "failing", "running"}
	merges      = []string{"ready", "conflict"}

	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	// A GitHub Enterprise repository carries its host: host/owner/name.
	repositoryPattern = regexp.MustCompile(`^(?:[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?(?::[0-9]{1,5})?/)?[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?/[A-Za-z0-9._-]{1,100}$`)
	// idPattern is the shape of newID.
	idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Error codes of the API.
const (
	CodeInvalidRequest = "invalid_request"
	CodeInvalidInbox   = "invalid_inbox"
	CodeTooMany        = "too_many_inboxes"
	CodeDuplicateName  = "duplicate_name"
	CodeDuplicateID    = "duplicate_id"
	CodeStale          = "stale_revision"
	CodeUnreadable     = "inboxes_unreadable"
)

// Criteria narrow pull requests. Saved inboxes and the app's quick filters
// share them; every list is empty for any, and a list's values combine with or.
type Criteria struct {
	Owners       []string `json:"owners"`
	Risk         string   `json:"risk"`
	Repositories []string `json:"repositories"`
	Involvement  []string `json:"involvement"`
	NoDrafts     bool     `json:"no_drafts"`
	Review       []string `json:"review"`
	Checks       []string `json:"checks"`
	Merge        []string `json:"merge"`
}

// Inbox is a saved filter over the app's Up next queue. The daemon stores it
// and never evaluates it.
type Inbox struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
	What  string `json:"what"`
	Criteria
	NoBots bool `json:"no_bots"`
	Badge  bool `json:"badge"`
}

// State is the stored list and the revision it is at.
type State struct {
	Revision int     `json:"revision"`
	Inboxes  []Inbox `json:"inboxes"`
}

// InvalidError is a refused request: a stable code and a message for a person.
type InvalidError struct {
	Code    string
	Message string
}

func (e *InvalidError) Error() string { return e.Message }

func invalid(code, format string, args ...any) error {
	return &InvalidError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// StaleError is a replace based on an older revision than the stored one.
type StaleError struct{ Current State }

func (*StaleError) Error() string { return "The inboxes changed since they were read." }

// UnreadableError reports stored state that cannot be read. It is never
// overwritten, so what it holds is not lost silently.
type UnreadableError struct{ Err error }

func (e *UnreadableError) Error() string { return "the inbox state cannot be read: " + e.Err.Error() }
func (e *UnreadableError) Unwrap() error { return e.Err }

// Store keeps inbox definitions in the application database.
type Store struct {
	path string
	mu   sync.Mutex
}

// Open returns a store backed by the application database at path.
func Open(path string) *Store { return &Store{path: path} }

// Read returns stored state; a missing document is revision 0 with no inboxes.
func (s *Store) Read() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

func (s *Store) read() (State, error) {
	data, err := appdb.Read(s.path, "state", "inboxes")
	if errors.Is(err, os.ErrNotExist) {
		return State{Inboxes: []Inbox{}}, nil
	}
	if err != nil {
		return State{}, &UnreadableError{err}
	}
	return decode(data)
}

func decode(data []byte) (State, error) {
	if data == nil {
		return State{Inboxes: []Inbox{}}, nil
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, &UnreadableError{err}
	}
	if state.Revision < 0 {
		return State{}, &UnreadableError{errors.New("negative revision")}
	}
	if state.Inboxes == nil {
		state.Inboxes = []Inbox{}
	}
	// Inboxes saved before a list existed read it as empty.
	for i := range state.Inboxes {
		state.Inboxes[i].Criteria.fill()
	}
	return state, nil
}

// Replace saves the whole list, based on the revision the caller read. Entries
// without an id get one; an id in the daemon's shape is kept, stored or not.
func (s *Store) Replace(revision int, inboxes []Inbox) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var next State
	read := false
	err := appdb.Update(s.path, "state", "inboxes", func(raw []byte) ([]byte, error) {
		read = true
		current, err := decode(raw)
		if err != nil {
			return nil, err
		}
		if revision != current.Revision {
			return nil, &StaleError{current}
		}
		known := map[string]bool{}
		for _, inbox := range current.Inboxes {
			known[inbox.ID] = true
		}
		clean, err := normalize(inboxes, known)
		if err != nil {
			return nil, err
		}
		next = State{Revision: current.Revision + 1, Inboxes: clean}
		return json.Marshal(next)
	})
	if err != nil && !read {
		return State{}, &UnreadableError{err}
	}
	return next, err
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func normalize(in []Inbox, known map[string]bool) ([]Inbox, error) {
	if len(in) > MaxInboxes {
		return nil, invalid(CodeTooMany, "At most %d inboxes are allowed.", MaxInboxes)
	}
	out := make([]Inbox, 0, len(in))
	ids, names := map[string]bool{}, map[string]bool{}
	for i, inbox := range in {
		inbox.Name = strings.TrimSpace(inbox.Name)
		if inbox.Name == "" || !utf8.ValidString(inbox.Name) || utf8.RuneCountInString(inbox.Name) > MaxNameLength {
			return nil, invalid(CodeInvalidInbox, "Inbox %d: the name must be 1 to %d characters.", i+1, MaxNameLength)
		}
		key := strings.ToLower(inbox.Name)
		if names[key] {
			return nil, invalid(CodeDuplicateName, "Inbox %d: the name %q is used twice.", i+1, inbox.Name)
		}
		names[key] = true
		if !oneOf(inbox.Color, colors) {
			return nil, invalid(CodeInvalidInbox, "Inbox %q: color must be one of %s.", inbox.Name, strings.Join(colors, ", "))
		}
		if !oneOf(inbox.What, whats) {
			return nil, invalid(CodeInvalidInbox, "Inbox %q: what must be one of %s.", inbox.Name, strings.Join(whats, ", "))
		}
		if err := inbox.Criteria.Clean(fmt.Sprintf("Inbox %q", inbox.Name)); err != nil {
			return nil, err
		}
		// An id the list no longer holds is kept when it has the daemon's
		// shape: it is an inbox deleted and brought back by Undo, whose links
		// should still lead to it.
		switch {
		case inbox.ID == "":
			for inbox.ID = newID(); ids[inbox.ID] || known[inbox.ID]; inbox.ID = newID() {
			}
		case !idPattern.MatchString(inbox.ID):
			return nil, invalid(CodeInvalidInbox, "Inbox %q has an id the daemon did not assign.", inbox.Name)
		case ids[inbox.ID]:
			return nil, invalid(CodeDuplicateID, "Inbox %q repeats the id of another inbox.", inbox.Name)
		}
		ids[inbox.ID] = true
		out = append(out, inbox)
	}
	return out, nil
}

// Clean checks the criteria and normalizes their lists in place; subject
// names what holds them in an error's message.
func (c *Criteria) Clean(subject string) error {
	if !oneOf(c.Risk, risks) {
		return invalid(CodeInvalidInbox, "%s: risk must be one of %s.", subject, strings.Join(risks, ", "))
	}
	var err error
	if c.Owners, err = cleanList(c.Owners, MaxOwners, ownerPattern, subject, "owners", "a GitHub user or organization login"); err != nil {
		return err
	}
	if c.Repositories, err = cleanList(c.Repositories, MaxRepositories, repositoryPattern, subject, "repositories", "owner/name"); err != nil {
		return err
	}
	for _, list := range []struct {
		values  *[]string
		field   string
		allowed []string
	}{{&c.Involvement, "involvement", involvement}, {&c.Review, "review", reviews}, {&c.Checks, "checks", checks}, {&c.Merge, "merge", merges}} {
		out := []string{}
		for _, v := range *list.values {
			if !oneOf(v, list.allowed) {
				return invalid(CodeInvalidInbox, "%s: %s must be among %s.", subject, list.field, strings.Join(list.allowed, ", "))
			}
			if !oneOf(v, out) {
				out = append(out, v)
			}
		}
		*list.values = out
	}
	return nil
}

func (c *Criteria) fill() {
	for _, list := range []*[]string{&c.Owners, &c.Repositories, &c.Involvement, &c.Review, &c.Checks, &c.Merge} {
		if *list == nil {
			*list = []string{}
		}
	}
}

func oneOf(v string, allowed []string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// cleanList trims the entries, rejects ones that do not match pattern and
// drops repeats ignoring case, keeping the first spelling.
func cleanList(in []string, max int, pattern *regexp.Regexp, subject, field, shape string) ([]string, error) {
	if len(in) > max {
		return nil, invalid(CodeInvalidInbox, "%s: at most %d %s are allowed.", subject, max, field)
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if !pattern.MatchString(v) || strings.HasSuffix(v, "/.") || strings.HasSuffix(v, "/..") {
			return nil, invalid(CodeInvalidInbox, "%s: %q in %s is not %s.", subject, v, field, shape)
		}
		if key := strings.ToLower(v); !seen[key] {
			seen[key] = true
			out = append(out, v)
		}
	}
	return out, nil
}
