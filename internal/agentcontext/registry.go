package agentcontext

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Kind is what a token was minted for.
type Kind string

const (
	// Chat is the app's chat: it may read any page and propose changes to the
	// page the user has open.
	Chat Kind = "chat"
	// Action is an agent working on one pull request: it only reads, and only
	// that pull request's repository.
	Action Kind = "action"
)

// Consumer is the holder of one MCP token.
type Consumer struct {
	// ID names the consumer without being its token; Mint sets it.
	ID   string `json:"-"`
	Kind Kind   `json:"kind"`
	// Repository ("owner/name") and PullRequest bind an Action.
	Repository  string `json:"repository,omitempty"`
	PullRequest int    `json:"pull_request,omitempty"`
	// Session is the agent session a chat runs in, as "agent:id" (the
	// agent, such as claude-code or codex, and its session id), set at Mint
	// or once later (Registry.SetSession). A chat never proposes answering a
	// request of its own session (approving its own tool call), and while its
	// session is unknown it answers none; nor does it message, end or bring
	// forward its own session.
	Session string `json:"session,omitempty"`
}

// sessionAgents are the agents a chat runs in, as livesessions and
// agentrequests name them.
var sessionAgents = []string{"claude-code", "codex"}

// SessionKey is the Consumer.Session of an agent's session.
func SessionKey(agent, id string) string { return agent + ":" + id }

// The pages a consumer can be on: the app's sidebar pages, then the pages
// that show one thing.
const (
	PageInbox        = "inbox"
	PagePulls        = "pulls"
	PageTimeline     = "timeline"
	PageSessions     = "sessions"
	PageAnalytics    = "analytics"
	PageJobs         = "jobs"
	PageRepositories = "repositories"
	PageSettings     = "settings"
	PagePullRequest  = "pull_request"
	PageGroup        = "group"
	PageSession      = "session"
	PageJob          = "job"
	PageFeature      = "feature"
)

var pageNames = map[string]bool{
	PageInbox: true, PagePulls: true, PageTimeline: true, PageSessions: true, PageAnalytics: true,
	PageJobs: true, PageRepositories: true, PageSettings: true, PagePullRequest: true,
	PageGroup: true, PageSession: true, PageJob: true, PageFeature: true,
}

// PageContext is what the user has open in the app: the page, the thing it
// shows and the filters on it. Section is the open section of the Settings
// page (settingsSections) or of a repository's page, the Repositories page
// with a repository (repositorySections); no other page has one. Focus is the
// part of the page the user picked, as the page describes it.
type PageContext struct {
	Page        string            `json:"page"`
	Repository  string            `json:"repository,omitempty"`
	PullRequest int               `json:"pull_request,omitempty"`
	Group       string            `json:"group,omitempty"`
	Session     string            `json:"session,omitempty"`
	Job         string            `json:"job,omitempty"`
	Feature     string            `json:"feature,omitempty"`
	Section     string            `json:"section,omitempty"`
	Filters     map[string]string `json:"filters,omitempty"`
	Focus       json.RawMessage   `json:"focus,omitempty"`
}

const (
	maxFilters  = 32
	maxPageText = 1 << 10
	maxFocus    = 16 << 10
)

func (p PageContext) validate() error {
	if !pageNames[p.Page] {
		return fmt.Errorf("unknown page %q", p.Page)
	}
	if len(p.Filters) > maxFilters {
		return fmt.Errorf("at most %d filters", maxFilters)
	}
	for _, text := range []string{p.Repository, p.Group, p.Session, p.Job, p.Feature, p.Section} {
		if len(text) > maxPageText {
			return errors.New("page field too long")
		}
	}
	if err := checkSection(p.Page, p.Repository, p.Section); err != nil {
		return err
	}
	for key, value := range p.Filters {
		if len(key) > maxPageText || len(value) > maxPageText {
			return errors.New("filter too long")
		}
	}
	if len(p.Focus) > maxFocus || (len(p.Focus) > 0 && !json.Valid(p.Focus)) {
		return fmt.Errorf("focus must be JSON of at most %d bytes", maxFocus)
	}
	return nil
}

var (
	// ErrUnknownConsumer is a consumer id or token that names no live
	// consumer: never minted, or revoked.
	ErrUnknownConsumer = errors.New("unknown or revoked MCP consumer")
	errActionPage      = errors.New("an action's page is its pull request")
)

// Registry holds the MCP tokens of the running daemon. Tokens live only in
// memory: they are never logged, persisted or sent anywhere but to the agent
// they were minted for, and a new daemon starts with none.
type Registry struct {
	mu sync.Mutex
	// byHash is keyed by the token's SHA-256, so a lookup's timing says
	// nothing about the token; the hash is then compared in constant time.
	byHash map[[sha256.Size]byte]*holder
	byID   map[string]*holder
	forget []func(id string)
}

type holder struct {
	hash     [sha256.Size]byte
	consumer Consumer
	page     *PageContext
}

func NewRegistry() *Registry {
	return &Registry{byHash: map[[sha256.Size]byte]*holder{}, byID: map[string]*holder{}}
}

func random() string {
	var b [32]byte
	// crypto/rand.Read never returns an error; it ends the program instead.
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	namePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// validRepository reports whether repository is "owner/name" as GitHub
// spells them.
func validRepository(repository string) bool {
	owner, name, ok := strings.Cut(repository, "/")
	return ok && ownerPattern.MatchString(owner) && namePattern.MatchString(name) && name != "." && name != ".."
}

func (c Consumer) validate() error {
	switch c.Kind {
	case Chat:
		if c.Repository != "" || c.PullRequest != 0 {
			return errors.New("a chat is bound to no repository or pull request")
		}
		if c.Session != "" {
			// The agent is named as the live sessions and agent requests
			// name it, so a session spelled otherwise can never fail to
			// match the chat's own requests.
			agent, id, ok := strings.Cut(c.Session, ":")
			if !ok || !slices.Contains(sessionAgents, agent) || !validSegment(id) {
				return fmt.Errorf(`a chat's session is "agent:id", the agent one of %s`, strings.Join(sessionAgents, ", "))
			}
		}
	case Action:
		if !validRepository(c.Repository) || c.PullRequest <= 0 {
			return errors.New("an action needs an owner/name repository and a pull request number")
		}
		if c.Session != "" {
			return errors.New("an action names no session: it proposes nothing")
		}
	default:
		return fmt.Errorf("unknown consumer kind %q", c.Kind)
	}
	return nil
}

// Mint returns a new token for c, the consumer's id, and the function that
// revokes the token. c must be a chat, or an action bound to a repository
// and pull request. An action starts on its pull request's page and stays
// there.
func (r *Registry) Mint(c Consumer) (token, id string, revoke func(), err error) {
	if err := c.validate(); err != nil {
		return "", "", nil, err
	}
	token, id = random(), random()
	c.ID = id
	h := &holder{hash: sha256.Sum256([]byte(token)), consumer: c}
	if c.Kind == Action {
		h.page = &PageContext{Page: PagePullRequest, Repository: c.Repository, PullRequest: c.PullRequest}
	}
	r.mu.Lock()
	r.byHash[h.hash] = h
	r.byID[id] = h
	r.mu.Unlock()
	return token, id, func() { r.revoke(id) }, nil
}

func (r *Registry) revoke(id string) {
	r.mu.Lock()
	h, ok := r.byID[id]
	if ok {
		delete(r.byID, id)
		delete(r.byHash, h.hash)
	}
	forget := r.forget
	r.mu.Unlock()
	if ok {
		for _, f := range forget {
			f(id)
		}
	}
}

// onRevoke registers f to drop what a revoked consumer left behind.
func (r *Registry) onRevoke(f func(id string)) {
	r.mu.Lock()
	r.forget = append(r.forget, f)
	r.mu.Unlock()
}

// Authenticate returns the live consumer token belongs to.
func (r *Registry) Authenticate(token string) (Consumer, bool) {
	if token == "" {
		return Consumer{}, false
	}
	sum := sha256.Sum256([]byte(token))
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.byHash[sum]
	if !ok || subtle.ConstantTimeCompare(h.hash[:], sum[:]) != 1 {
		return Consumer{}, false
	}
	return h.consumer, true
}

// live reports whether the consumer with this id is live.
func (r *Registry) live(id string) bool {
	_, ok := r.Consumer(id)
	return ok
}

// Consumer returns the live consumer with this id.
func (r *Registry) Consumer(id string) (Consumer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.byID[id]
	if !ok {
		return Consumer{}, false
	}
	return h.consumer, true
}

// SetSession records the agent session ("agent:id") the chat with this id
// runs in, for a panel that learns it after minting the token (Codex reports
// its session id late). It is set once: setting the same session again is
// accepted, another one refused, so a chat's session cannot be swapped for
// the one whose requests it would answer.
func (r *Registry) SetSession(id, session string) error {
	if session == "" {
		return errors.New(`a chat's session is "agent:id"`)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.byID[id]
	switch {
	case !ok:
		return ErrUnknownConsumer
	case h.consumer.Kind != Chat:
		return errors.New("only a chat runs in a session")
	case h.consumer.Session == session:
		return nil
	case h.consumer.Session != "":
		return errors.New("the chat's session is set already")
	}
	c := h.consumer
	c.Session = session
	if err := c.validate(); err != nil {
		return err
	}
	h.consumer = c
	return nil
}

// SetPage records the page the consumer with this id has open. An action's
// page cannot change.
func (r *Registry) SetPage(id string, p PageContext) error {
	// The repository is trimmed first, so the section is checked against the
	// value kept and a blank one is no repository everywhere.
	p.Repository = strings.TrimSpace(p.Repository)
	if err := p.validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.byID[id]
	switch {
	case !ok:
		return ErrUnknownConsumer
	case h.consumer.Kind == Action:
		return errActionPage
	}
	p = p.clone()
	h.page = &p
	return nil
}

func (p PageContext) clone() PageContext {
	p.Filters = maps.Clone(p.Filters)
	p.Focus = bytes.Clone(p.Focus)
	return p
}

// Page returns the page the consumer with this id has open, if one was set.
func (r *Registry) Page(id string) (PageContext, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.byID[id]
	if !ok || h.page == nil {
		return PageContext{}, false
	}
	return h.page.clone(), true
}
