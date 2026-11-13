package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// Scope is what a tool reads, which decides who may call it.
type Scope int

const (
	// ScopeGlobal tools read across repositories or the app's own state.
	// Actions never see or call them. It is the zero value, so a tool that
	// names no scope is kept from actions.
	ScopeGlobal Scope = iota
	// ScopeRepository tools read one repository, the one CallContext.Target
	// returns. Actions may call them.
	ScopeRepository
)

// Tool is one MCP tool.
type Tool struct {
	Name        string
	Description string
	// InputSchema is the JSON Schema of the tool's arguments, an object.
	InputSchema json.RawMessage
	Scope       Scope
	// Reads are the local API route prefixes the tool's Reader may read, a
	// whole path segment at a time. The routes listed in unreadable can never
	// be declared.
	Reads []string
	// Projected are the exact routes, otherwise unreadable, this tool may
	// read in process, and only if projectedReads names them for this tool.
	// The tool returns a typed projection of them, never their text.
	Projected []string
	// Proposes marks a tool that records a proposal for the user to review. The
	// default, false, is a read-only tool; tools/list reports it in the tool's
	// annotations so a client can approve read-only tools unasked.
	Proposes bool
	// Call runs the tool. Its text, or its error's message, goes back to the
	// agent through the envelope: masked, cut and fenced.
	Call func(ctx context.Context, c CallContext, args json.RawMessage) (string, error)

	schema *jsonschema.Resolved
	// enveloped marks a call whose text has been through the envelope.
	enveloped bool
}

// PageSpec says what a page offers agents: the tools that read it, and the
// proposer that takes changes to it (nil when nothing on it can be proposed).
type PageSpec struct {
	Name     string
	Tools    []string
	Proposer Proposer
}

// CallContext is what a tool call runs with.
//
// A tool takes the repository and pull request it reads only from Target's
// return value, never from its arguments or the page directly: Target is
// what keeps an action to its own repository. An action's tools default to
// its own pull request and may read others in that repository. As a
// backstop, an action's Reader reads only the routes in actionRoutes, each
// bound to the action's repository the way its handler finds what it
// serves: by the parameter the handler reads, by the path, or by checking
// the repository in the response.
type CallContext struct {
	Consumer Consumer
	// Page is the consumer's page; HasPage is false until one is set.
	Page    PageContext
	HasPage bool
	// Reader reads the routes the tool declared (Tool.Reads); an action's
	// reads only its repository's, through actionRoutes.
	Reader *Reader
	// proposals is the store behind Propose; tools reach it only through
	// Propose, which never gives them a proposal's Before.
	proposals *Proposals
}

// Target resolves the repository and pull request a tool reads: the ones
// asked for, else the action's own, else those of the page the user has open.
// An action is refused any other repository.
func (c CallContext) Target(repository string, number int) (string, int, error) {
	repository = strings.TrimSpace(repository)
	if c.Consumer.Kind == Action {
		if repository != "" && !strings.EqualFold(repository, c.Consumer.Repository) {
			return "", 0, errOtherRepository
		}
		if number == 0 {
			number = c.Consumer.PullRequest
		}
		return c.Consumer.Repository, number, nil
	}
	if repository == "" && c.HasPage {
		repository = c.Page.Repository
		if number == 0 {
			number = c.Page.PullRequest
		}
	}
	if repository == "" {
		return "", 0, errors.New("name a repository: no page with one is open")
	}
	return repository, number, nil
}

// Receipt is what an agent learns of a proposal it made: never the target's
// current value, which only the person sees, and never what an operation
// would do or, once confirmed, what it answered.
type Receipt struct {
	ID     string       `json:"id"`
	Page   string       `json:"page"`
	Target string       `json:"target"`
	Kind   ProposalKind `json:"kind"`
}

func receipt(p Proposal) Receipt {
	return Receipt{ID: p.ID, Page: p.Page, Target: p.Target, Kind: p.Kind}
}

// Propose records a change for the user to save or dismiss.
func (c CallContext) Propose(ctx context.Context, p Proposal) (Receipt, error) {
	recorded, err := c.proposals.Propose(ctx, c.Consumer.ID, p)
	if err != nil {
		return Receipt{}, err
	}
	return receipt(recorded), nil
}

// ProposeOperation records an operation for the user to confirm or dismiss.
func (c CallContext) ProposeOperation(ctx context.Context, name string, args json.RawMessage) (Receipt, error) {
	recorded, err := c.proposals.ProposeOperation(ctx, c.Consumer.ID, name, args)
	if err != nil {
		return Receipt{}, err
	}
	return receipt(recorded), nil
}

func (t *Tool) compile() error {
	if t.Name == "" || t.Call == nil {
		return errors.New("a tool needs a name and a call")
	}
	if t.Scope != ScopeGlobal && t.Scope != ScopeRepository {
		return fmt.Errorf("tool %s: unknown scope", t.Name)
	}
	if err := checkReads(t.Reads); err != nil {
		return fmt.Errorf("tool %s: %w", t.Name, err)
	}
	if err := checkProjected(t.Name, t.Scope, t.Projected); err != nil {
		return fmt.Errorf("tool %s: %w", t.Name, err)
	}
	schema := t.InputSchema
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
		t.InputSchema = schema
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("tool %s: %w", t.Name, err)
	}
	if s.Type != "object" {
		return fmt.Errorf("tool %s: the input schema must be an object", t.Name)
	}
	resolved, err := s.Resolve(nil)
	if err != nil {
		return fmt.Errorf("tool %s: %w", t.Name, err)
	}
	t.schema = resolved
	return nil
}

// arguments checks args against the tool's schema; absent args are {}.
func (t *Tool) arguments(args json.RawMessage) (json.RawMessage, error) {
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	var value any
	if err := json.Unmarshal(args, &value); err != nil {
		return nil, errors.New("the arguments are not JSON")
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, errors.New("the arguments must be an object")
	}
	if err := t.schema.Validate(value); err != nil {
		return nil, fmt.Errorf("the arguments do not match the tool's input schema: %w", err)
	}
	return args, nil
}

// visible reports whether consumer may list and call t.
func (t Tool) visible(consumer Consumer) bool {
	return consumer.Kind != Action || t.Scope == ScopeRepository
}

// builtinTools are the tools every consumer has, whatever its page.
func (s *Server) builtinTools() []Tool {
	return []Tool{{
		Name:        "current_page",
		Description: "The page the user has open in the baum app, the pull request, group, session, job or feature it shows, the Settings or repository section open, its filters and focus, and the tools that read it; on a page with sections, section_tools are the tools that read the open section.",
		// An action's page is its own pull request.
		Scope: ScopeRepository,
		Call: func(_ context.Context, c CallContext, _ json.RawMessage) (string, error) {
			if !c.HasPage {
				return "No page is set: the user has not opened a page in the baum app for this conversation.", nil
			}
			out := struct {
				PageContext
				Tools        []string `json:"tools,omitempty"`
				SectionTools []string `json:"section_tools,omitempty"`
			}{PageContext: c.Page}
			if spec, ok := s.page(c.Page.Page); ok {
				// The Repositories page lists a repository's tools only when it
				// shows one.
				own := repositoryPageTools()
				section := sectionTools(c.Page)
				s.mu.RLock()
				for _, name := range spec.Tools {
					if c.Page.Page == PageRepositories && c.Page.Repository == "" && slices.Contains(own, name) {
						continue
					}
					if t, ok := s.tool(name); ok && t.visible(c.Consumer) {
						out.Tools = append(out.Tools, name)
						if slices.Contains(section, name) {
							out.SectionTools = append(out.SectionTools, name)
						}
					}
				}
				s.mu.RUnlock()
			}
			b, err := json.MarshalIndent(out, "", "  ")
			return string(b), err
		},
	}, {
		Name:        "read_more",
		Description: "The next part of a tool result that was cut, given the cursor the result ended with. Each cursor gives its part once.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"cursor":{"type":"string","description":"The cursor the cut result named."}},"required":["cursor"],"additionalProperties":false}`),
		// A cursor gives only what its own consumer was given.
		Scope: ScopeRepository,
		Call: func(_ context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Cursor string `json:"cursor"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			return s.envelope.more(c.Consumer.ID, a.Cursor)
		},
		enveloped: true,
	}}
}
