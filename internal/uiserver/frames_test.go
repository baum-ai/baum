package uiserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/reviewv2"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/tilestest"
)

// frameMetadata is what a browser sends when the app's page loads a frame.
var frameMetadata = map[string]string{"Sec-Fetch-Dest": "iframe", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Site": "same-origin"}

// variant returns a synthetic custom tile's definition under another title,
// so its digest is its own.
func variant(t *testing.T, id, title string) tiles.TypeDefinition {
	t.Helper()
	def := tileviewtest.CustomDefinition(id)
	def.Title, def.Digest = title, ""
	var sealed tiles.TypeDefinition
	if err := json.Unmarshal(tilestest.SealDefinition(def), &sealed); err != nil {
		t.Fatal(err)
	}
	return sealed
}

// registeredFrame registers a synthetic custom tile's frame document as the
// tile routes do and returns its definition and path.
func registeredFrame(t *testing.T, id string) (tiles.TypeDefinition, string) {
	t.Helper()
	return register(t, tileviewtest.CustomDefinition(id))
}

func register(t *testing.T, def tiles.TypeDefinition) (tiles.TypeDefinition, string) {
	t.Helper()
	digest, err := tileview.FrameRegistry().Add(def)
	if err != nil {
		t.Fatal(err)
	}
	return def, framePrefix + strings.TrimPrefix(digest, "sha256:")
}

// frameRequest serves a request through the server's handler, with the
// browser's frame metadata changed by headers (an empty value removes one).
func frameRequest(t *testing.T, s *Server, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, s.Origin()+target, nil)
	for name, value := range frameMetadata {
		r.Header.Set(name, value)
	}
	for name, value := range headers {
		if value == "" {
			r.Header.Del(name)
		} else {
			r.Header.Set(name, value)
		}
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// The frame document is the kit's composition of the type's HTML, with
// exactly these headers: the frame's own policy in place of the app's.
func TestFrameRouteServesTheDocumentWithItsHeaders(t *testing.T) {
	s := testServer(t)
	def, path := registeredFrame(t, tileviewtest.WidgetHealth)
	want, err := kit.Compose(def.Kit, def.Source.HTML)
	if err != nil {
		t.Fatal(err)
	}
	w := frameRequest(t, s, "GET", path, nil)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), want) {
		t.Fatalf("%d, %d bytes", w.Code, w.Body.Len())
	}
	golden := http.Header{
		"Content-Security-Policy":      {"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; form-action 'none'; base-uri 'none'; frame-ancestors 'self'; sandbox allow-scripts"},
		"Content-Type":                 {"text/html; charset=utf-8"},
		"Content-Length":               {strconv.Itoa(len(want))},
		"X-Content-Type-Options":       {"nosniff"},
		"Referrer-Policy":              {"no-referrer"},
		"Cache-Control":                {"no-store"},
		"Cross-Origin-Resource-Policy": {"same-origin"},
		"Vary":                         {"Sec-Fetch-Dest, Sec-Fetch-Mode, Sec-Fetch-Site"},
	}
	if !reflect.DeepEqual(w.Header(), golden) {
		t.Fatalf("headers\n%v\nwant\n%v", w.Header(), golden)
	}
	if kit.CSP != golden.Get("Content-Security-Policy") {
		t.Fatal("kit.CSP changed: review the frame's policy and update the golden")
	}

	// HEAD: the same headers, no body.
	head := frameRequest(t, s, "HEAD", path, nil)
	if head.Code != 200 || head.Body.Len() != 0 || !reflect.DeepEqual(head.Header(), golden) {
		t.Fatalf("HEAD %d, %d bytes, %v", head.Code, head.Body.Len(), head.Header())
	}
	// The query is ignored, and so is Range: the whole document is served.
	for _, tc := range []struct {
		target  string
		headers map[string]string
	}{
		{path + "?v=2&clone=x", nil},
		{path + "?", nil},
		{path, map[string]string{"Range": "bytes=0-9"}},
		{path, map[string]string{"Cookie": "baum_ui=whatever", "Authorization": "Bearer nope"}},
	} {
		w := frameRequest(t, s, "GET", tc.target, tc.headers)
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), want) || w.Header().Get("Content-Range") != "" {
			t.Errorf("%s %v: %d, %d bytes", tc.target, tc.headers, w.Code, w.Body.Len())
		}
	}
}

// Every refusal is the same 404, whatever failed: it never says whether a
// digest is registered.
func TestFrameRouteRefusesWithOneAnswer(t *testing.T) {
	s := testServer(t)
	_, path := registeredFrame(t, tileviewtest.WidgetHealth)
	// A type of this test's own: a hung mark lasts for the process.
	hung, hungPath := register(t, variant(t, tileviewtest.WidgetHealth, "Hung widgets"))
	hex := strings.TrimPrefix(path, framePrefix)
	unknown := framePrefix + strings.Repeat("0", 64)
	reference := frameRequest(t, s, "GET", unknown, nil)
	if reference.Code != 404 {
		t.Fatalf("unknown digest: %d", reference.Code)
	}
	// A refusal carries the server's own policy, never the frame's.
	if policy := request(t, s, "GET", "/", "", "", "", "", "").Header().Values("Content-Security-Policy"); !reflect.DeepEqual(reference.Header().Values("Content-Security-Policy"), policy) ||
		strings.Contains(policy[0], "sandbox") || !strings.Contains(policy[0], "frame-ancestors 'none'") {
		t.Fatalf("a refusal's policy %q, the server's %q", reference.Header().Values("Content-Security-Policy"), policy)
	}
	tileview.FrameRegistry().MarkHung(hung.Digest)
	refused := []struct {
		name, method, target string
		headers              map[string]string
	}{
		{"hung", "GET", hungPath, nil},
		{"uppercase", "GET", framePrefix + strings.ToUpper(hex), nil},
		{"63 digits", "GET", path[:len(path)-1], nil},
		{"65 digits", "GET", path + "0", nil},
		{"sha256 prefix", "GET", framePrefix + "sha256:" + hex, nil},
		{"trailing slash", "GET", path + "/", nil},
		{"extra segment", "GET", path + "/x", nil},
		{"empty segment", "GET", framePrefix + "/" + hex, nil},
		{"encoded digit", "GET", framePrefix + escaped(hex), nil},
		{"encoded slash", "GET", path + "%2F", nil},
		{"dot segments", "GET", framePrefix + "../tile-frames/" + hex, nil},
		{"encoded dots", "GET", framePrefix + "%2e%2e/" + hex, nil},
		{"bare prefix", "GET", framePrefix, nil},
		{"long path", "GET", path + strings.Repeat("a", 64<<10), nil},
		{"POST", "POST", path, nil},
		{"PUT", "PUT", path, nil},
		{"DELETE", "DELETE", path, nil},
		{"OPTIONS", "OPTIONS", path, nil},
		{"no metadata", "GET", path, map[string]string{"Sec-Fetch-Dest": "", "Sec-Fetch-Mode": "", "Sec-Fetch-Site": ""}},
		{"no dest", "GET", path, map[string]string{"Sec-Fetch-Dest": ""}},
		{"no mode", "GET", path, map[string]string{"Sec-Fetch-Mode": ""}},
		{"no site", "GET", path, map[string]string{"Sec-Fetch-Site": ""}},
		{"typed address", "GET", path, map[string]string{"Sec-Fetch-Dest": "document", "Sec-Fetch-Site": "none"}},
		{"top-level from the app", "GET", path, map[string]string{"Sec-Fetch-Dest": "document"}},
		{"site none", "GET", path, map[string]string{"Sec-Fetch-Site": "none"}},
		{"embed", "GET", path, map[string]string{"Sec-Fetch-Dest": "embed", "Sec-Fetch-Mode": "no-cors"}},
		{"object", "GET", path, map[string]string{"Sec-Fetch-Dest": "object", "Sec-Fetch-Mode": "no-cors"}},
		{"frame", "GET", path, map[string]string{"Sec-Fetch-Dest": "frame"}},
		{"script", "GET", path, map[string]string{"Sec-Fetch-Dest": "script", "Sec-Fetch-Mode": "no-cors"}},
		{"fetch", "GET", path, map[string]string{"Sec-Fetch-Dest": "empty", "Sec-Fetch-Mode": "cors"}},
		{"no-cors iframe", "GET", path, map[string]string{"Sec-Fetch-Mode": "no-cors"}},
		{"nested navigate", "GET", path, map[string]string{"Sec-Fetch-Mode": "nested-navigate"}},
		{"case", "GET", path, map[string]string{"Sec-Fetch-Dest": "IFrame"}},
	}
	for _, tc := range refused {
		w := frameRequest(t, s, tc.method, tc.target, tc.headers)
		if w.Code != reference.Code || w.Body.String() != reference.Body.String() || !reflect.DeepEqual(w.Header(), reference.Header()) {
			t.Errorf("%s: %d %q %v, want the unknown digest's %q %v", tc.name, w.Code, w.Body.String(), w.Header(), reference.Body.String(), reference.Header())
		}
		if len(w.Result().Cookies()) != 0 {
			t.Errorf("%s: set a cookie", tc.name)
		}
	}
	// Another site's request is refused before any route, the same for every
	// digest, registered or not (the server's cross-site refusal).
	for _, site := range []string{"same-site", "cross-site"} {
		for _, target := range []string{path, framePrefix + strings.Repeat("0", 64)} {
			w := frameRequest(t, s, "GET", target, map[string]string{"Sec-Fetch-Site": site})
			if w.Code != http.StatusForbidden || strings.TrimSpace(w.Body.String()) != "cross-site request refused" || len(w.Result().Cookies()) != 0 {
				t.Errorf("%s %s: %d %q", site, target, w.Code, w.Body.String())
			}
		}
	}
	// A request without any fetch metadata is counted, for a webview that
	// omits it; one that sends some is not.
	before := framesWithoutMetadata.Load()
	frameRequest(t, s, "GET", path, map[string]string{"Sec-Fetch-Dest": "", "Sec-Fetch-Mode": "", "Sec-Fetch-Site": ""})
	frameRequest(t, s, "GET", path, map[string]string{"Sec-Fetch-Site": "cross-site"})
	if got := framesWithoutMetadata.Load() - before; got != 1 {
		t.Errorf("%d requests counted without fetch metadata", got)
	}
	// The Host check still comes first.
	r := httptest.NewRequest("GET", s.Origin()+path, nil)
	r.Host = "attacker.example"
	for name, value := range frameMetadata {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 || bytes.Contains(w.Body.Bytes(), []byte("<html")) {
		t.Fatalf("wrong host: %d", w.Code)
	}
	// The namespace never leads to frames.
	if w := frameRequest(t, s, "GET", s.appPath()+strings.TrimPrefix(path, "/"), map[string]string{"Authorization": "Bearer " + s.launch}); w.Code == 200 && bytes.Contains(w.Body.Bytes(), []byte("baum-kit")) {
		t.Fatal("a frame document was served under the namespace")
	}
}

// escaped is the first character of s percent-encoded, then the rest.
func escaped(s string) string { return fmt.Sprintf("%%%02X", s[0]) + s[1:] }

// Requests as they arrive on the wire, where the server, not a test helper,
// parses the request target.
func TestFrameRouteOnTheWire(t *testing.T) {
	s := testServer(t)
	_, path := registeredFrame(t, tileviewtest.WidgetHealth)
	hex := strings.TrimPrefix(path, framePrefix)
	send := func(target string) int {
		t.Helper()
		conn, err := net.Dial("tcp", strings.TrimPrefix(s.Origin(), "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		request := "GET " + target + " HTTP/1.1\r\nHost: " + strings.TrimPrefix(s.Origin(), "http://") +
			"\r\nSec-Fetch-Dest: iframe\r\nSec-Fetch-Mode: navigate\r\nSec-Fetch-Site: same-origin\r\nConnection: close\r\n\r\n"
		if _, err := io.WriteString(conn, request); err != nil {
			t.Fatal(err)
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}
	if got := send(path); got != 200 {
		t.Fatalf("the frame: %d", got)
	}
	if got := send(path + "?x=%2F..%2F"); got != 200 {
		t.Fatalf("with a query: %d", got)
	}
	for _, target := range []string{
		"/tile-frames/../tile-frames/" + hex,
		"/tile-frames/./" + hex,
		"/tile-frames/%2e%2e/tile-frames/" + hex,
		"/tile-frames/" + hex + "%2f",
		"/tile-frames/" + hex + "/",
		"/tile-frames//" + hex,
		"/tile-frames/" + strings.ToUpper(hex),
		"/tile-frames/" + escaped(hex),
		"/x/../tile-frames/" + hex,
		"http://" + strings.TrimPrefix(s.Origin(), "http://") + "/tile-frames/" + hex + "/..",
	} {
		if got := send(target); got == 200 {
			t.Errorf("%s: %d", target, got)
		}
	}
}

// End to end: the tile route hands a custom tile to the app ready, after
// which its frame loads; an unavailable tile's frame and a digest never
// handed out do not.
func TestCustomTileFromTheTilesRouteLoadsItsFrame(t *testing.T) {
	t.Cleanup(tileview.SetFixture(func(doc *tiles.Document) {
		if doc.Facts.Title == "Custom tiles" {
			tileviewtest.AddCustomTiles(doc)
		}
	}))
	f := newTilesFixture(t)
	f.put(t, titled(t, "Custom tiles"))
	s, err := New(Options{API: f.handler})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Listen(0); err != nil {
		t.Fatal(err)
	}
	// A type no route has handed out.
	never := variant(t, tileviewtest.WidgetHealth, "Never served")
	if w := frameRequest(t, s, "GET", framePrefix+strings.TrimPrefix(never.Digest, "sha256:"), nil); w.Code != 404 {
		t.Fatalf("a digest never handed out: %d", w.Code)
	}

	r := httptest.NewRequest("GET", s.Origin()+"/api/tiles?clone="+f.clone.ID+"&pr=42", nil)
	r.Header.Set("Authorization", "Bearer "+s.launch)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var body struct {
		Document reviewv2.Document `json:"document"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("baum.tile(")) {
		t.Fatal("the tile route sent a tile's HTML")
	}
	for _, id := range tileviewtest.CustomTileIDs {
		var tile *reviewv2.Tile
		for i := range body.Document.Tiles {
			if body.Document.Tiles[i].ID == id {
				tile = &body.Document.Tiles[i]
			}
		}
		if tile == nil {
			t.Fatalf("%s missing", id)
		}
		var served struct {
			Digest string `json:"digest"`
		}
		if err := json.Unmarshal(tile.Definition, &served); err != nil {
			t.Fatal(err)
		}
		def := tileviewtest.CustomDefinition(id)
		if served.Digest != def.Digest {
			t.Fatalf("%s: served digest %s, checked %s", id, served.Digest, def.Digest)
		}
		frame := frameRequest(t, s, "GET", framePrefix+strings.TrimPrefix(served.Digest, "sha256:"), nil)
		switch id {
		case tileviewtest.NewerKit:
			if tile.State != tiles.StateUnavailable || frame.Code != 404 {
				t.Errorf("%s: %s, frame %d", id, tile.State, frame.Code)
			}
		default:
			want, err := kit.Compose(def.Kit, def.Source.HTML)
			if err != nil {
				t.Fatal(err)
			}
			if tile.State != tiles.StateReady || frame.Code != 200 || !bytes.Equal(frame.Body.Bytes(), want) {
				t.Errorf("%s: %s %v, frame %d", id, tile.State, tile.Problems, frame.Code)
			}
		}
	}
}
