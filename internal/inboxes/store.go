package inboxes

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/internal/appdb"
)

// Limits and the allowed values of the enumerated fields.
const (
	MaxInboxes      = 20
	MaxNameLength   = 40
	MaxOwners       = 50
	MaxRepositories = 50
	MaxQueryLength  = 500
)

var (
	whats  = []string{"any", "review", "merge", "agent", "mine"}
	risks  = []string{"any", "3", "4"}
	colors = []string{"blue", "green", "red", "amber", "purple", "grey"}

	ownerPattern      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?/[A-Za-z0-9._-]{1,100}$`)
	// idPattern is the shape of newID.
	idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Error codes of the API.
const (
	CodeInvalidRequest = "invalid_request"
	CodeInvalidInbox   = "invalid_inbox"
	CodeTooMany        = "too_many_inboxes"
	CodeLastInbox      = "last_inbox"
	CodeDuplicateName  = "duplicate_name"
	CodeDuplicateID    = "duplicate_id"
	CodeStale          = "stale_revision"
	CodeUnreadable     = "inboxes_unreadable"
)

// Inbox is a saved filter over the app's Up next queue. The daemon stores it
// and never evaluates it.
type Inbox struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Color        string   `json:"color"`
	What         string   `json:"what"`
	Owners       []string `json:"owners"`
	Risk         string   `json:"risk"`
	Repositories []string `json:"repositories"`
	Query        string   `json:"query"`
	NoBots       bool     `json:"no_bots"`
	Badge        bool     `json:"badge"`
}

// State is the stored list and the revision it is at.
type State struct {
	Revision int     `json:"revision"`
	Inboxes  []Inbox `json:"inboxes"`
	// Initialized is set once the default inbox has been created, so that
	// deleting it later is not undone.
	Initialized bool `json:"initialized"`
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

// DefaultName is the name of the inbox every installation starts with.
const DefaultName = "Default"

// Read returns stored state. The first read of an installation creates the
// default inbox.
func (s *Store) Read() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensure()
}

// ensure initializes the document when it has not been, and returns the
// stored state. Initialized documents are only read. Unreadable ones are
// reported and never written.
func (s *Store) ensure() (State, error) {
	// Any read failure falls through to the transaction: a database another
	// store has created but not yet set up fails a read-only open, and the
	// transaction waits for it. One that is really unreadable fails there
	// before anything is written.
	data, err := appdb.Read(s.path, "state", "inboxes")
	if err == nil {
		state, err := decode(data)
		if err != nil || state.Initialized {
			return state, err
		}
	}
	var state State
	entered := false
	err = appdb.Update(s.path, "state", "inboxes", func(raw []byte) ([]byte, error) {
		entered = true
		out, st, err := initialize(raw)
		state = st
		return out, err
	})
	if err != nil && !entered {
		return State{}, &UnreadableError{err}
	}
	return state, err
}

// initialize adds the default inbox to a document that has none of the
// marker. Stored entries are carried over untouched. Twenty inboxes leave no
// room, so none is added.
func initialize(raw []byte) ([]byte, State, error) {
	current, err := decode(raw)
	if err != nil || current.Initialized {
		return raw, current, err
	}
	doc := map[string]json.RawMessage{}
	if raw != nil {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, State{}, &UnreadableError{err}
		}
	}
	items := []json.RawMessage{}
	if stored, ok := doc["inboxes"]; ok {
		if err := json.Unmarshal(stored, &items); err != nil {
			return nil, State{}, &UnreadableError{err}
		}
		if items == nil {
			items = []json.RawMessage{}
		}
	}
	if len(items) < MaxInboxes {
		def, err := json.Marshal(Inbox{
			ID: newID(), Name: freeName(current.Inboxes), Color: "blue", What: "any",
			Owners: []string{}, Risk: "any", Repositories: []string{}, Badge: true,
		})
		if err != nil {
			return nil, State{}, err
		}
		items = append(items, def)
	}
	for key, value := range map[string]any{"inboxes": items, "initialized": true, "revision": current.Revision + 1} {
		if doc[key], err = json.Marshal(value); err != nil {
			return nil, State{}, err
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, State{}, err
	}
	state, err := decode(out)
	return out, state, err
}

// freeName is "Default", or "Default 2", "Default 3", ... when the name is
// taken ignoring case.
func freeName(existing []Inbox) string {
	taken := func(name string) bool {
		for _, inbox := range existing {
			if strings.EqualFold(strings.TrimSpace(inbox.Name), name) {
				return true
			}
		}
		return false
	}
	name := DefaultName
	for n := 2; taken(name); n++ {
		name = fmt.Sprintf("%s %d", DefaultName, n)
	}
	return name
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
	return state, nil
}

// Replace saves the whole list, based on the revision the caller read. Entries
// without an id get one; an id in the daemon's shape is kept, stored or not.
func (s *Store) Replace(revision int, inboxes []Inbox) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A client still at revision 0 gets the initialized list with a stale
	// answer, which must not be rolled back with the refused save.
	if _, err := s.ensure(); err != nil {
		return State{}, err
	}
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
		next = State{Revision: current.Revision + 1, Inboxes: clean, Initialized: true}
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
	if len(in) == 0 {
		return nil, invalid(CodeLastInbox, "Keep at least one inbox.")
	}
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
		if !oneOf(inbox.Risk, risks) {
			return nil, invalid(CodeInvalidInbox, "Inbox %q: risk must be one of %s.", inbox.Name, strings.Join(risks, ", "))
		}
		var err error
		if inbox.Owners, err = cleanList(inbox.Owners, MaxOwners, ownerPattern, inbox.Name, "owners", "a GitHub user or organization login"); err != nil {
			return nil, err
		}
		if inbox.Repositories, err = cleanList(inbox.Repositories, MaxRepositories, repositoryPattern, inbox.Name, "repositories", "owner/name"); err != nil {
			return nil, err
		}
		inbox.Query = strings.TrimSpace(inbox.Query)
		if !ValidQuery(inbox.Query) {
			return nil, invalid(CodeInvalidInbox, "Inbox %q: the query must be one line of at most %d characters.", inbox.Name, MaxQueryLength)
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

// ValidQuery reports whether a filter query is one line of valid UTF-8 text
// of at most MaxQueryLength characters. The daemon never parses it: the app
// does.
func ValidQuery(query string) bool {
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > MaxQueryLength {
		return false
	}
	return strings.IndexFunc(query, unicode.IsControl) < 0
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
func cleanList(in []string, max int, pattern *regexp.Regexp, inbox, field, shape string) ([]string, error) {
	if len(in) > max {
		return nil, invalid(CodeInvalidInbox, "Inbox %q: at most %d %s are allowed.", inbox, max, field)
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if !pattern.MatchString(v) || strings.HasSuffix(v, "/.") || strings.HasSuffix(v, "/..") {
			return nil, invalid(CodeInvalidInbox, "Inbox %q: %q in %s is not %s.", inbox, v, field, shape)
		}
		if key := strings.ToLower(v); !seen[key] {
			seen[key] = true
			out = append(out, v)
		}
	}
	return out, nil
}
