package agentcontext

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
)

// An operation proposal is the second kind of proposal: something the person
// does with a button in the app (merge, cancel a job, answer an agent's
// request), which an agent suggests and the person confirms. The agent never
// runs anything. Nothing runs before the person confirms; on Confirm the
// daemon runs, in process, the same route the app's button calls, with that
// route's own validation, and each confirmed operation runs once.
//
// An Operation is the definition of one: the pages that offer it, its fixed
// method and route, the arguments an agent gives, how they become the
// request, and what the person reads before confirming. Its route is fixed:
// the request a builder makes only fills the route's {name} segments, each
// one plain segment, and the writer refuses anything else.
type Operation struct {
	// Name names the operation; its tool is "propose_" + Name.
	Name        string
	Description string
	// Pages are the pages that offer it. A proposal is made, and confirmed,
	// only on one of them, and its target must belong to what the page shows.
	Pages []string
	// Method and Route are the request the app's button sends: the method,
	// and the path with {name} standing for one whole segment.
	Method string
	Route  string
	// InputSchema is the JSON Schema of the agent's arguments.
	InputSchema json.RawMessage
	// Reads are the GET route prefixes Build and Describe may read.
	Reads []string
	// Projected are the exact otherwise unreadable routes Build and Describe
	// may read, only as projectedReads allows them to the operation's tool:
	// the model and effort a paid run uses, from the settings, which only the
	// person reads.
	Projected []string
	// Paid marks an operation that starts an agent run, which costs the
	// person money; Irreversible one that cannot be undone (merge, delete,
	// remove, end).
	Paid, Irreversible bool
	// Build makes the request from the agent's arguments (already checked
	// against InputSchema) and the page, and names its target as the page
	// knows it. It reads only through c.Reader.
	Build func(ctx context.Context, c OperationContext, args json.RawMessage) (OperationRequest, error)
	// Describe checks that the target still belongs to the page, and says
	// what will happen and on what, for the person. It runs at Propose and
	// again at Confirm, against the page as it is then. It reads only through
	// c.Reader.
	Describe func(ctx context.Context, c OperationContext, req OperationRequest) (OperationSummary, error)

	// schema is InputSchema resolved, which ProposeOperation checks the
	// arguments against whoever calls it.
	schema *jsonschema.Resolved
}

// OperationContext is what an operation's Build and Describe get: the page,
// the consumer proposing, and a GET reader of the operation's Reads.
type OperationContext struct {
	Page     PageContext
	Consumer Consumer
	Reader   *Reader
}

// OperationRequest is the request an operation runs: the values of its
// route's {name} segments, its query and its JSON body. Target names what it
// acts on, as the page knows it.
type OperationRequest struct {
	Target string            `json:"target"`
	Vars   map[string]string `json:"vars,omitempty"`
	Query  url.Values        `json:"query,omitempty"`
	Body   json.RawMessage   `json:"body,omitempty"`
}

func (r OperationRequest) clone() OperationRequest {
	if r.Vars != nil {
		vars := make(map[string]string, len(r.Vars))
		for k, v := range r.Vars {
			vars[k] = v
		}
		r.Vars = vars
	}
	if r.Query != nil {
		r.Query = cloneValues(r.Query)
	}
	r.Body = bytes.Clone(r.Body)
	return r
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, values := range v {
		out[k] = slices.Clone(values)
	}
	return out
}

// OperationSummary is what the person reads before confirming: what will
// happen, on what, and the operation's flags. Action is baum's own sentence,
// naming things only by identifiers (a repository and number, an agent and
// session id, a job id); everything else is a field (Details), and text that
// is not baum's (a title, what a tool asked, what the agent wrote) is a
// quoted field, shown in full and never written into the sentence. For a
// paid run it names the harness, model and effort from the settings. It is
// never sent to the agent.
type OperationSummary struct {
	Action       string         `json:"action"`
	Details      []SummaryField `json:"details,omitempty"`
	Model        string         `json:"model,omitempty"`
	Effort       string         `json:"effort,omitempty"`
	Paid         bool           `json:"paid"`
	Irreversible bool           `json:"irreversible"`
}

// SummaryField is one labelled part of a summary. Quoted marks text that is
// not baum's: read from a route (made plain: no control, format or
// bidirectional characters, one line unless Multiline) or, with ByAgent,
// written by the agent and sent as it is (the agent's text is refused when it
// holds such characters, so the field is exactly what runs). The app shows a
// quoted field as data, apart from baum's words.
type SummaryField struct {
	Label     string `json:"label"`
	Value     string `json:"value"`
	Quoted    bool   `json:"quoted,omitempty"`
	Multiline bool   `json:"multiline,omitempty"`
	ByAgent   bool   `json:"by_agent,omitempty"`
}

func (s OperationSummary) clone() OperationSummary {
	s.Details = slices.Clone(s.Details)
	return s
}

// equal reports whether two summaries say the same.
// revision identifies what a summary says: the hex SHA-256 of its JSON. The
// app shows a summary with its revision and confirms with it.
func (s OperationSummary) revision() string {
	raw, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// OperationProposal is what an operation proposal holds: the operation's
// name, the request Confirm runs, and the summary the person reads.
type OperationProposal struct {
	Name    string           `json:"name"`
	Request OperationRequest `json:"request"`
	Summary OperationSummary `json:"summary"`
	// Revision is the summary's revision, which Confirm takes back: the
	// app confirms the summary it showed, and nothing else.
	Revision string `json:"revision"`
}

func (o *OperationProposal) clone() *OperationProposal {
	if o == nil {
		return nil
	}
	return &OperationProposal{Name: o.Name, Request: o.Request.clone(), Summary: o.Summary.clone(), Revision: o.Revision}
}

// OperationOutcome is what a confirmed operation's route answered: its
// status and a short message from its body. It goes to the app, never to the
// agent.
type OperationOutcome struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
}

// OK reports whether the route accepted the request.
func (o OperationOutcome) OK() bool { return o.Status >= 200 && o.Status < 300 }

var (
	ErrUnknownOperation = errors.New("unknown operation")
	// ErrNotAnOperation is Confirm on a diff, ErrNotADiff Save on an
	// operation.
	ErrNotAnOperation = errors.New("this proposal is a change to save, not an operation to confirm")
	ErrNotADiff       = errors.New("this proposal is an operation to confirm, not a change to save")
	// errOwnRequest is an answer to a request of the chat's own session.
	errOwnRequest = errors.New("a chat cannot answer its own session's requests: the person answers them")
	// errOtherRoute is a request that is not the operation's own route.
	errOtherRoute = errors.New("not the operation's route")
	// ErrOperationChanged is a confirm of an operation whose summary reads
	// differently from the one the person read: nothing ran, and the
	// proposal now holds the new summary.
	ErrOperationChanged = errors.New("what the operation would do changed since it was shown: nothing ran; read it again and confirm")
	// errNoSession is a chat whose agent session is not known, which may not
	// answer requests: it could be answering its own.
	errNoSession = errors.New("this chat's own session is not known, so it cannot answer agents' requests or act on sessions: the person does that in the app")
)

// operationMethods are the methods an operation may send.
var operationMethods = []string{http.MethodPost, http.MethodDelete}

// routeSegment is the shape of a value that fills one {name} segment of an
// operation's route: letters, digits and ._~@+:- (no '/', '%', '?', '#',
// '\' or space), of at most 256 bytes, and never "." or "..". Ids,
// repository owners and names (".github" too), agents and session ids all
// fit.
var routeSegment = regexp.MustCompile(`^[A-Za-z0-9._~@+:-]{1,256}$`)

func validSegment(value string) bool {
	return routeSegment.MatchString(value) && value != "." && value != ".."
}

// compile checks an operation's definition.
func (op *Operation) compile() error {
	if op.Name == "" || op.Build == nil || op.Describe == nil {
		return errors.New("an operation needs a name, a build and a describe")
	}
	if !slices.Contains(operationMethods, op.Method) {
		return fmt.Errorf("operation %s: method %q is not one an operation sends", op.Name, op.Method)
	}
	if !strings.HasPrefix(op.Route, "/api/") || path.Clean(op.Route) != op.Route || strings.ContainsAny(op.Route, "?#%\\") {
		return fmt.Errorf("operation %s: route %q is not a local API path", op.Name, op.Route)
	}
	for _, hidden := range unreadable {
		if strings.HasPrefix(op.Route, hidden) {
			return fmt.Errorf("operation %s: route %s is not one an operation may call", op.Name, op.Route)
		}
	}
	for _, segment := range strings.Split(op.Route[1:], "/") {
		if strings.ContainsAny(segment, "{}") && !(strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") && len(segment) > 2 && !strings.ContainsAny(segment[1:len(segment)-1], "{}")) {
			return fmt.Errorf("operation %s: route %s: a wildcard is one whole segment", op.Name, op.Route)
		}
	}
	if len(op.Pages) == 0 {
		return fmt.Errorf("operation %s: offered on no page", op.Name)
	}
	for _, page := range op.Pages {
		if !pageNames[page] {
			return fmt.Errorf("operation %s: unknown page %q", op.Name, page)
		}
	}
	if err := checkReads(op.Reads); err != nil {
		return fmt.Errorf("operation %s: %w", op.Name, err)
	}
	if err := checkProjected("propose_"+op.Name, ScopeGlobal, op.Projected); err != nil {
		return fmt.Errorf("operation %s: %w", op.Name, err)
	}
	return nil
}

// readerFor is the GET reader of an operation's Build and Describe: its
// reads and its projected routes. It reads nothing else and sends no other
// method.
func (p *Proposals) readerFor(op *Operation) *Reader {
	r := p.reader.within(op.Reads, "")
	r.projected = op.Projected
	return r
}

// Path is the route with vars filled in. Every {name} of the route must have
// a value and every value a {name}; each value is one plain segment
// (routeSegment). The result is checked to be the route again.
func (op *Operation) Path(vars map[string]string) (string, error) {
	segments := strings.Split(op.Route[1:], "/")
	used := 0
	for i, segment := range segments {
		name, ok := strings.CutPrefix(segment, "{")
		if !ok {
			continue
		}
		name = strings.TrimSuffix(name, "}")
		value, ok := vars[name]
		if !ok || !validSegment(value) {
			return "", fmt.Errorf("%w: %s is not one path segment", errOtherRoute, name)
		}
		segments[i] = value
		used++
	}
	if used != len(vars) {
		return "", fmt.Errorf("%w: a value for no segment of the route", errOtherRoute)
	}
	p := "/" + strings.Join(segments, "/")
	if _, ok := match(op.Route, p); !ok || path.Clean(p) != p {
		return "", errOtherRoute
	}
	return p, nil
}

// maxOutcome bounds what is kept of a confirmed operation's answer, and
// maxMessage the message taken from it.
const (
	maxOutcome = 1 << 20
	maxMessage = 300
)

// writer runs one operation's request through the LocalAPI handler, in
// process, as the app's button sends it. It sends only the operation's own
// method to its own route.
type writer struct{ api http.Handler }

func (w writer) run(ctx context.Context, op *Operation, req OperationRequest) (OperationOutcome, error) {
	p, err := op.Path(req.Vars)
	if err != nil {
		return OperationOutcome{}, err
	}
	if len(req.Body) > 0 && !json.Valid(req.Body) {
		return OperationOutcome{}, errors.New("the operation's body is not JSON")
	}
	target := url.URL{Path: p}
	if len(req.Query) > 0 {
		target.RawQuery = req.Query.Encode()
	}
	r, err := http.NewRequestWithContext(ctx, op.Method, target.String(), bytes.NewReader(req.Body))
	if err != nil {
		return OperationOutcome{}, err
	}
	// The request is checked as the handler will see it: the method and the
	// path are the operation's own, and no segment was escaped.
	if r.Method != op.Method || r.URL.Path != p || r.URL.RawPath != "" {
		return OperationOutcome{}, errOtherRoute
	}
	if _, ok := match(op.Route, r.URL.Path); !ok {
		return OperationOutcome{}, errOtherRoute
	}
	if len(req.Body) > 0 {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Accept", "application/json")
	rec := &recorder{header: http.Header{}, limit: maxOutcome}
	w.api.ServeHTTP(rec, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return OperationOutcome{Status: rec.status, Message: outcomeMessage(rec.status, rec.body.Bytes())}, nil
}

// outcomeMessage is a short message from a route's answer: its JSON error,
// message or code, else its text, cut at maxMessage bytes; else the status's
// own text.
func outcomeMessage(status int, body []byte) string {
	var fields struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	text := ""
	if json.Unmarshal(body, &fields) == nil {
		switch e := fields.Error.(type) {
		case string:
			text = e
		case map[string]any:
			if m, ok := e["message"].(string); ok {
				text = m
			}
		}
		if text == "" {
			text = fields.Message
		}
		if text == "" {
			text = fields.Code
		}
	} else if !json.Valid(body) {
		text = strings.TrimSpace(string(body))
	}
	if text == "" {
		text = http.StatusText(status)
	}
	if len(text) > maxMessage {
		cut := maxMessage
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "…"
	}
	return text
}

// operationTool is the propose_* tool of an operation. It is global, so an
// action neither lists nor calls it, and it answers a receipt: never the
// summary, the request or anything the route answers.
func operationTool(op *Operation) Tool {
	description := op.Description + " The person reads what will happen and confirms it in the app; nothing runs until they do, and you get a receipt, never the outcome."
	if op.Paid {
		description += " Confirming starts a paid agent run."
	}
	if op.Irreversible {
		description += " It cannot be undone."
	}
	return Tool{
		Name:        "propose_" + op.Name,
		Description: description,
		InputSchema: op.InputSchema,
		// The tool itself reads nothing: its operation's Build and Describe
		// read through Proposals.readerFor.
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			if !c.HasPage {
				return "", errors.New("no page is open: the person has not opened a page in the baum app for this conversation")
			}
			receipt, err := c.ProposeOperation(ctx, op.Name, args)
			if err != nil {
				return "", err
			}
			out, err := json.Marshal(proposed{receipt, operationStatus})
			return string(out), err
		},
	}
}

const operationStatus = "proposed: the person reads what will happen and confirms or dismisses it in the app; nothing runs until they confirm"

// ProposeOperation records an operation proposal for the consumer with this
// id: only a chat, only on a page that offers the operation, and only for a
// target that belongs to that page (Build and Describe refuse others).
// Nothing runs.
func (p *Proposals) ProposeOperation(ctx context.Context, consumer, name string, args json.RawMessage) (Proposal, error) {
	c, ok := p.registry.Consumer(consumer)
	if !ok {
		return Proposal{}, ErrUnknownConsumer
	}
	if c.Kind != Chat {
		return Proposal{}, ErrReadOnly
	}
	op := p.operation(name)
	if op == nil {
		return Proposal{}, ErrUnknownOperation
	}
	page, ok := p.registry.Page(consumer)
	if !ok || !slices.Contains(op.Pages, page.Page) {
		return Proposal{}, ErrNotCurrentPage
	}
	// The arguments are checked here, not only by the tool, so no caller
	// reaches Build with arguments the schema refuses.
	args, err := (&Tool{schema: op.schema}).arguments(args)
	if err != nil {
		return Proposal{}, err
	}
	oc := OperationContext{Page: page, Consumer: c, Reader: p.readerFor(op)}
	req, err := op.Build(ctx, oc, args)
	if err != nil {
		return Proposal{}, err
	}
	if _, err := op.Path(req.Vars); err != nil {
		return Proposal{}, err
	}
	summary, err := op.Describe(ctx, oc, req)
	if err != nil {
		return Proposal{}, err
	}
	summary.Paid, summary.Irreversible = op.Paid, op.Irreversible
	proposal := Proposal{ID: random(), Kind: ProposalOperation, Page: page.Page, Target: req.Target, Context: page,
		Operation: &OperationProposal{Name: op.Name, Request: req.clone(), Summary: summary, Revision: summary.revision()}}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.registry.live(consumer) {
		return Proposal{}, ErrUnknownConsumer
	}
	if len(p.open[consumer]) >= maxProposals {
		return Proposal{}, errTooManyProposals
	}
	p.open[consumer] = append(p.open[consumer], proposal)
	return proposal.clone(), nil
}

// Confirm runs an operation proposal the person confirmed, when the consumer
// is live, the person still has the page it was made on open, showing the
// same thing, and its target still belongs to that page (Describe again,
// against the page as it is now). A proposal refused so stays open. seen is
// the revision of the summary the app showed the person
// (OperationProposal.Revision): under the store's lock it must be the stored
// summary's revision, and that must be the revision of the summary Describe
// just wrote. When either differs (another title, harness, model or effort,
// or a summary another Confirm replaced meanwhile), nothing runs, Confirm
// returns ErrOperationChanged and the proposal stays open holding the new
// summary and its revision, for the person to read and confirm again.
//
// The proposal is then taken out of the store, under the lock a revoke and
// every other Confirm take, before anything runs: of two Confirms only one
// takes it, and a revoke either comes first and nothing runs, or comes after
// the person's Confirm took it. Its request then runs once, through the
// operation's own route, with the route's own validation; Confirm returns
// what the route answered. An error means nothing ran; an outcome whose
// status is not 2xx is the route's own refusal.
func (p *Proposals) Confirm(ctx context.Context, consumer, id, seen string) (OperationOutcome, error) {
	proposal, ok := p.find(consumer, id)
	if !ok {
		return OperationOutcome{}, ErrUnknownProposal
	}
	if proposal.Kind != ProposalOperation || proposal.Operation == nil {
		return OperationOutcome{}, ErrNotAnOperation
	}
	op := p.operation(proposal.Operation.Name)
	if op == nil {
		return OperationOutcome{}, ErrUnknownOperation
	}
	c, ok := p.registry.Consumer(consumer)
	if !ok {
		return OperationOutcome{}, ErrUnknownConsumer
	}
	page, ok := p.registry.Page(consumer)
	if !ok || !samePage(page, proposal.Context) || !slices.Contains(op.Pages, page.Page) {
		return OperationOutcome{}, ErrNotCurrentPage
	}
	req := proposal.Operation.Request
	summary, err := op.Describe(ctx, OperationContext{Page: page, Consumer: c, Reader: p.readerFor(op)}, req)
	if err != nil {
		return OperationOutcome{}, err
	}
	summary.Paid, summary.Irreversible = op.Paid, op.Irreversible
	p.mu.Lock()
	if !p.registry.live(consumer) {
		p.mu.Unlock()
		return OperationOutcome{}, ErrUnknownConsumer
	}
	i := slices.IndexFunc(p.open[consumer], func(open Proposal) bool { return open.ID == id && open.Operation != nil })
	if i < 0 {
		p.mu.Unlock()
		return OperationOutcome{}, ErrUnknownProposal
	}
	// What the person read must be what runs: the summary the app showed
	// (seen) must be the one stored now, which another Confirm may have
	// replaced, and the one Describe just wrote. Otherwise nothing runs, and
	// the proposal stays open holding the new summary, for the person to
	// read and confirm again.
	stored := p.open[consumer][i].Operation
	if fresh := summary.revision(); seen == "" || seen != stored.Revision || fresh != stored.Revision {
		stored.Summary, stored.Revision = summary.clone(), fresh
		p.mu.Unlock()
		return OperationOutcome{}, ErrOperationChanged
	}
	p.removeLocked(consumer, id)
	p.mu.Unlock()
	// Taken, it runs to the end: the person confirmed it, and a run cut off
	// halfway would leave its outcome unknown.
	return p.writer.run(context.WithoutCancel(ctx), op, req)
}
