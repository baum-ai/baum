package agentcontext

import (
	"bytes"
	"context"
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

// Reader reads the daemon's local API in process, as the app does: the same
// routes, below the app server's authentication, with no credentials. It
// has no way to send anything but GET, so a tool can never change state
// through it.
//
// A tool's reader reads only the route prefixes the tool declared
// (Tool.Reads), never the unreadable ones. An action's reader also reads only
// requests that name its repository, by a repository= or repo= parameter, a
// clone= parameter of one of its clones, or a /api/repositories/{owner}/{name}
// path.
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
	errOtherRepository = errors.New("this agent may read only its own pull request's repository")
)

// Get reads target, an /api/ path with an optional query.
func (r *Reader) Get(ctx context.Context, target string) (Response, error) {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Fragment != "" ||
		!strings.HasPrefix(u.Path, "/api/") || path.Clean(u.Path) != u.Path {
		return Response{}, fmt.Errorf("read %q: not a local API path", target)
	}
	if !readable(u.Path, r.allow) {
		return Response{}, fmt.Errorf("read %s: %w", u.Path, errNotReadable)
	}
	if r.repository != "" {
		if err := r.names(ctx, u); err != nil {
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

// names checks that a request names the reader's repository, and no other.
func (r *Reader) names(ctx context.Context, u *url.URL) error {
	q := u.Query()
	named := append(append([]string(nil), q["repository"]...), q["repo"]...)
	for _, id := range q["clone"] {
		if r.clone == nil {
			return errOtherRepository
		}
		repository, err := r.clone(ctx, id)
		if err != nil {
			return errOtherRepository
		}
		named = append(named, repository)
	}
	if rest, ok := strings.CutPrefix(u.Path, "/api/repositories/"); ok {
		segments := strings.SplitN(rest, "/", 3)
		if len(segments) >= 2 {
			named = append(named, segments[0]+"/"+segments[1])
		}
	}
	if len(named) == 0 {
		return errors.New("an action may read only requests that name its repository")
	}
	for _, repository := range named {
		if !strings.EqualFold(strings.TrimSpace(repository), r.repository) {
			return errOtherRepository
		}
	}
	return nil
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
