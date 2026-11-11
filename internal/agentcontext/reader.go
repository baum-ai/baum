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

// Reader reads the daemon's local API in process, as the app does: the same
// routes, below the app server's authentication, with no credentials. It
// has no way to send anything but GET, so a tool can never change state
// through it.
type Reader struct{ api http.Handler }

// NewReader reads through api, the LocalAPI handler.
func NewReader(api http.Handler) *Reader { return &Reader{api: api} }

// Response is what one read returned.
type Response struct {
	Status      int
	ContentType string
	Body        []byte
}

var errReadTooLarge = errors.New("response too large")

// Get reads target, an /api/ path with an optional query.
func (r *Reader) Get(ctx context.Context, target string) (Response, error) {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Fragment != "" {
		return Response{}, fmt.Errorf("read %q: not a local API path", target)
	}
	if !strings.HasPrefix(u.Path, "/api/") || path.Clean(u.Path) != u.Path || u.Path == "/api/mcp" {
		return Response{}, fmt.Errorf("read %q: not a local API path", target)
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
