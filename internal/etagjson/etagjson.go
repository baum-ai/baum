// Package etagjson serves JSON that is encoded once and revalidated with a
// strong ETag, so an unchanged snapshot costs neither an encode nor a body.
package etagjson

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// Body is an encoded response and its ETag, the digest of the bytes.
type Body struct {
	Data []byte
	ETag string
}

// Encode marshals v the way json.Encoder does, with its trailing newline.
func Encode(v any) (Body, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return Body{}, err
	}
	data = append(data, '\n')
	sum := sha256.Sum256(data)
	return Body{Data: data, ETag: `"` + hex.EncodeToString(sum[:16]) + `"`}, nil
}

// Serve answers 304 when the request's If-None-Match names b, and the body
// otherwise. no-cache lets the webview store the response but makes it
// revalidate each time, which is what turns an unchanged snapshot into a 304.
func Serve(w http.ResponseWriter, r *http.Request, b Body) {
	h := w.Header()
	h.Set("Cache-Control", "no-cache")
	h.Set("ETag", b.ETag)
	if matches(r.Header.Get("If-None-Match"), b.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(b.Data)))
	_, _ = w.Write(b.Data)
}

func matches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || strings.TrimPrefix(part, "W/") == etag {
			return true
		}
	}
	return false
}
