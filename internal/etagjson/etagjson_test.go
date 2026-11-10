package etagjson

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServeRevalidates(t *testing.T) {
	b, err := Encode(map[string]int{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	get := func(inm string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/", nil)
		if inm != "" {
			req.Header.Set("If-None-Match", inm)
		}
		rec := httptest.NewRecorder()
		Serve(rec, req, b)
		return rec
	}
	first := get("")
	if first.Code != 200 || first.Body.String() != "{\"a\":1}\n" || first.Header().Get("ETag") != b.ETag || first.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("first = %d %q %v", first.Code, first.Body.String(), first.Header())
	}
	for _, inm := range []string{b.ETag, "W/" + b.ETag, `"x", ` + b.ETag, "*"} {
		rec := get(inm)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || rec.Header().Get("ETag") != b.ETag {
			t.Fatalf("If-None-Match %q = %d %q", inm, rec.Code, rec.Body.String())
		}
	}
	if rec := get(`"other"`); rec.Code != 200 {
		t.Fatalf("mismatch = %d", rec.Code)
	}
}
