package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// Tool is one MCP tool.
type Tool struct {
	Name        string
	Description string
	// InputSchema is the JSON Schema of the tool's arguments, an object.
	InputSchema json.RawMessage
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
type CallContext struct {
	Consumer Consumer
	// Page is the consumer's page; HasPage is false until one is set.
	Page    PageContext
	HasPage bool
	// Reader reads the local API.
	Reader *Reader
	// Proposals records changes a chat proposes (Propose).
	Proposals *Proposals
}

var errOtherRepository = errors.New("this agent may read only its own pull request's repository")

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

// Propose records a change for the user to save or dismiss.
func (c CallContext) Propose(ctx context.Context, p Proposal) (Proposal, error) {
	return c.Proposals.Propose(ctx, c.Consumer.ID, p)
}

func (t *Tool) compile() error {
	if t.Name == "" || t.Call == nil {
		return errors.New("a tool needs a name and a call")
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

// builtinTools are the tools every consumer has, whatever its page.
func (s *Server) builtinTools() []Tool {
	return []Tool{{
		Name:        "current_page",
		Description: "The page the user has open in the baum app, the pull request, group, session, job or feature it shows, its filters and focus, and the tools that read it.",
		Call: func(_ context.Context, c CallContext, _ json.RawMessage) (string, error) {
			if !c.HasPage {
				return "No page is set: the user has not opened a page in the baum app for this conversation.", nil
			}
			out := struct {
				PageContext
				Tools []string `json:"tools,omitempty"`
			}{PageContext: c.Page}
			if spec, ok := s.page(c.Page.Page); ok {
				out.Tools = spec.Tools
			}
			b, err := json.MarshalIndent(out, "", "  ")
			return string(b), err
		},
	}, {
		Name:        "read_more",
		Description: "The next part of a tool result that was cut, given the cursor the result ended with. Each cursor gives its part once.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"cursor":{"type":"string","description":"The cursor the cut result named."}},"required":["cursor"],"additionalProperties":false}`),
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
