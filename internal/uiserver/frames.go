package uiserver

import (
	"net/http"
	"regexp"
	"strconv"

	"github.com/baum-ai/baum/internal/tileview"
	"github.com/baum-ai/baum/tiles/kit"
)

// framePrefix is the route of custom tile frame documents.
const framePrefix = "/tile-frames/"

// framePath is the whole path of a frame document, as the request wrote it:
// the 64 lowercase hex digits of a definition digest and nothing else.
var framePath = regexp.MustCompile(`^/tile-frames/[0-9a-f]{64}$`)

// serveFrame serves GET and HEAD /tile-frames/<hex>: the frame document of a
// custom tile type the tile routes handed to the app (tileview.FrameRegistry),
// which the app loads into <iframe sandbox="allow-scripts">. It is the one
// document route outside the session namespace and without the cookie, so a
// tile's frame never learns the namespace and its document request carries no
// credential; the Host check has already run. The document holds only HTML
// the authenticated app was given, but the route checks, in this order:
//
//   - The method is GET or HEAD: nothing else reads a document.
//   - Sec-Fetch-Dest is iframe and Sec-Fetch-Mode navigate: the request
//     loads a frame. A script's fetch, an <object> or <embed>, a top-level
//     page or a client that sends no fetch metadata (anything but a browser
//     loading a frame) is refused, so the document is never a top-level page
//     on the app's origin, never read as data by another page, and never
//     handed to a local process that did not authenticate. The document's own
//     CSP sandbox makes it opaque-origin wherever it is loaded; these checks
//     keep it from being loaded anywhere else at all.
//   - Sec-Fetch-Site is same-origin: a document of the daemon's own origin
//     started the navigation. Under the Host check those are the app and the
//     launch and status pages; a tile's frame is opaque, so its requests are
//     cross-site. That refuses a tile navigating itself or a frame to another
//     tile's document, another loopback service (same-site) or any web page
//     (cross-site) framing a tile or probing which digests are registered, and
//     an address typed into the address bar (none).
//   - The path is exactly the prefix and 64 lowercase hex digits, as sent: no
//     percent-encoding, no further segment, no trailing slash. The query is
//     never read.
//   - The digest is registered and not marked hung.
//
// Every refusal is the same plain 404, so a response never says whether a
// digest is registered, or which check failed.
//
// The response carries the frame's own CSP (kit.CSP), which replaces the
// app's: default-src 'none' with inline script and style only, sandbox
// allow-scripts and frame-ancestors 'self'. It is no-store, as every daemon
// answer is: a cached copy would outlive the registry (an evicted or hung
// type would still load) and skip these checks, and composing again is free.
// It sets no cookie, and Range is ignored.
func serveFrame(w http.ResponseWriter, r *http.Request) {
	document, ok := frameDocument(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", kit.CSP)
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(document)))
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Vary", "Sec-Fetch-Dest, Sec-Fetch-Mode, Sec-Fetch-Site")
	if r.Method != http.MethodHead {
		_, _ = w.Write(document)
	}
}

// frameDocument applies serveFrame's checks and returns the document.
func frameDocument(r *http.Request) ([]byte, bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return nil, false
	}
	if r.Header.Get("Sec-Fetch-Dest") != "iframe" || r.Header.Get("Sec-Fetch-Mode") != "navigate" || r.Header.Get("Sec-Fetch-Site") != "same-origin" {
		return nil, false
	}
	path := r.URL.EscapedPath()
	if !framePath.MatchString(path) {
		return nil, false
	}
	return tileview.FrameRegistry().Get("sha256:" + path[len(framePrefix):])
}
