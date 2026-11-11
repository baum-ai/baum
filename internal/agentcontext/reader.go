package agentcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// maxRead bounds the body of one read.
const maxRead = 8 << 20

// unreadable are the routes no tool may read, whatever it declares: settings,
// credentials and connections, diagnostics, plan usage, the event stream and
// the MCP route itself. Each is a plain string prefix, so /api/github covers
// /api/github-auth and /api/github/.
var unreadable = []string{"/api/settings", "/api/debug", "/api/github", "/api/diagnostics", "/api/plan-usage", "/api/events", "/api/mcp"}

// refreshFlags are query parameters that make a GET route do work beyond
// reading (refresh a cache, fetch more from GitHub); no read sends them.
var refreshFlags = []string{"refresh", "more"}

// binding is how a route names the repository it serves.
type binding int

const (
	// byRepository: the handler reads the repository= parameter.
	byRepository binding = iota + 1
	// byClone: the handler reads clone=, a registered clone resolved to its
	// repository.
	byClone
	// byPath: the path holds {owner} and {name}.
	byPath
	// byResponse: the route finds its data by id, so the repository is read
	// from the response (actionRoute.check) and nothing is returned on a
	// mismatch.
	byResponse
)

// actionRoute is a route an action may read and how it is bound to the
// action's repository.
type actionRoute struct {
	// pattern is the path, with {name} standing for one whole segment.
	pattern string
	bind    binding
	// params are the query parameters allowed besides the binding one; any
	// other is refused.
	params []string
	// check reads the repository from a byResponse route's body.
	check func(body []byte) (string, error)
}

// actionRoutes are the only routes an action reads; any other is refused to
// it. An entry binds the route the way its handler finds what it serves.
var actionRoutes = []actionRoute{
	// The jobs of one pull request: the handler reads repository and pr.
	{pattern: "/api/jobs", bind: byRepository, params: []string{"pr"}},
	// One job, by id.
	{pattern: "/api/jobs/{id}", bind: byResponse, check: repositoryField},
}

// repositoryField reads a JSON object's top-level "repository".
func repositoryField(body []byte) (string, error) {
	var v struct {
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", err
	}
	return v.Repository, nil
}

// Reader reads the daemon's local API in process, as the app does: the same
// routes, below the app server's authentication, with no credentials. It
// reads only through GET routes on its allowlist, without refresh flags; it
// has no way to send another method.
//
// A tool's reader reads only the route prefixes the tool declared
// (Tool.Reads), never the unreadable ones. An action's reader also reads only
// the routes in actionRoutes, each bound to the action's repository the way
// its handler finds what it serves, with no query parameter the entry does
// not list.
type Reader struct {
	api http.Handler
	// clone resolves a registered clone's id to its repository.
	clone      func(ctx context.Context, id string) (string, error)
	allow      []string
	repository string
}

// NewReader reads through api, the LocalAPI handler; clone resolves clone
// ids for an action's reader (nil refuses clone= for actions). It reads
// nothing until within gives it routes.
func NewReader(api http.Handler, clone func(ctx context.Context, id string) (string, error)) *Reader {
	return &Reader{api: api, clone: clone}
}

// within returns a reader of the given route prefixes, bound to repository
// when it is not empty.
func (r *Reader) within(allow []string, repository string) *Reader {
	return &Reader{api: r.api, clone: r.clone, allow: allow, repository: repository}
}

// Response is what one read returned.
type Response struct {
	Status      int
	ContentType string
	Body        []byte
}

var (
	errReadTooLarge    = errors.New("response too large")
	errNotReadable     = errors.New("not a route this tool reads")
	errRefreshFlag     = errors.New("reads never ask for a refresh")
	errNotActionRoute  = errors.New("not a route this agent reads")
	errOtherRepository = errors.New("this agent may read only its own pull request's repository")
)

// Get reads target, an /api/ path with an optional query.
func (r *Reader) Get(ctx context.Context, target string) (Response, error) {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Fragment != "" ||
		!strings.HasPrefix(u.Path, "/api/") || path.Clean(u.Path) != u.Path {
		return Response{}, fmt.Errorf("read %q: not a local API path", target)
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Response{}, fmt.Errorf("read %s: malformed query", u.Path)
	}
	for _, flag := range refreshFlags {
		if query.Has(flag) {
			return Response{}, fmt.Errorf("read %s: %w", u.Path, errRefreshFlag)
		}
	}
	if !readable(u.Path, r.allow) {
		return Response{}, fmt.Errorf("read %s: %w", u.Path, errNotReadable)
	}
	var route *actionRoute
	if r.repository != "" {
		if route, err = r.bind(ctx, u.Path, query); err != nil {
			return Response{}, fmt.Errorf("read %s: %w", u.Path, err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, (&url.URL{Path: u.Path, RawQuery: u.RawQuery}).String(), nil)
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Accept", "application/json")
	w := &recorder{header: http.Header{}}
	r.api.ServeHTTP(w, req)
	if w.overflow {
		return Response{}, fmt.Errorf("read %s: %w (over %d bytes)", u.Path, errReadTooLarge, maxRead)
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if route != nil && route.bind == byResponse {
		// A miss and another repository's record look the same.
		repository, err := route.check(w.body.Bytes())
		if w.status != http.StatusOK || err != nil || !strings.EqualFold(repository, r.repository) {
			return Response{}, fmt.Errorf("read %s: %w", u.Path, errOtherRepository)
		}
	}
	return Response{Status: w.status, ContentType: w.header.Get("Content-Type"), Body: w.body.Bytes()}, nil
}

// readable reports whether p is under one of the allowed prefixes, a whole
// segment at a time, and under none of the unreadable ones.
func readable(p string, allow []string) bool {
	for _, prefix := range unreadable {
		if strings.HasPrefix(p, prefix) {
			return false
		}
	}
	for _, prefix := range allow {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// checkReads refuses route prefixes a tool may not declare.
func checkReads(reads []string) error {
	for _, prefix := range reads {
		if !strings.HasPrefix(prefix, "/api/") || path.Clean(prefix) != prefix {
			return fmt.Errorf("read prefix %q: not a local API path", prefix)
		}
		for _, hidden := range unreadable {
			if strings.HasPrefix(prefix, hidden) || strings.HasPrefix(hidden, prefix) {
				return fmt.Errorf("read prefix %q: %s is never readable", prefix, hidden)
			}
		}
	}
	return nil
}

// match reports whether p fits pattern, and the segments its wildcards took.
func match(pattern, p string) (map[string]string, bool) {
	want, got := strings.Split(pattern, "/"), strings.Split(p, "/")
	if len(want) != len(got) {
		return nil, false
	}
	vars := map[string]string{}
	for i, segment := range want {
		if name, ok := strings.CutPrefix(segment, "{"); ok && strings.HasSuffix(name, "}") {
			if got[i] == "" {
				return nil, false
			}
			vars[strings.TrimSuffix(name, "}")] = got[i]
		} else if segment != got[i] {
			return nil, false
		}
	}
	return vars, true
}

// bind finds an action's route for p and checks that its request names the
// action's repository and carries no parameter the route does not list. A
// byResponse route is checked again once it has answered.
func (r *Reader) bind(ctx context.Context, p string, query url.Values) (*actionRoute, error) {
	for i := range actionRoutes {
		route := &actionRoutes[i]
		vars, ok := match(route.pattern, p)
		if !ok {
			continue
		}
		allowed := map[string]bool{}
		for _, name := range route.params {
			allowed[name] = true
		}
		var named string
		switch route.bind {
		case byRepository:
			allowed["repository"] = true
			if len(query["repository"]) != 1 {
				return nil, errOtherRepository
			}
			named = query.Get("repository")
		case byClone:
			allowed["clone"] = true
			if len(query["clone"]) != 1 || r.clone == nil {
				return nil, errOtherRepository
			}
			repository, err := r.clone(ctx, query.Get("clone"))
			if err != nil {
				return nil, errOtherRepository
			}
			named = repository
		case byPath:
			named = vars["owner"] + "/" + vars["name"]
		case byResponse:
			named = r.repository
		}
		for name := range query {
			if !allowed[name] {
				return nil, fmt.Errorf("%w: parameter %s", errNotActionRoute, name)
			}
		}
		if !strings.EqualFold(named, r.repository) {
			return nil, errOtherRepository
		}
		return route, nil
	}
	return nil, errNotActionRoute
}

// recorder is the ResponseWriter a read hands the API: it keeps at most
// maxRead bytes and fails the read beyond that.
type recorder struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (w *recorder) Header() http.Header { return w.header }
func (w *recorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *recorder) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	if w.overflow || w.body.Len()+len(b) > maxRead {
		w.overflow = true
		return 0, errReadTooLarge
	}
	return w.body.Write(b)
}
