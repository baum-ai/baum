package uistate

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPValidationAndConflict(t *testing.T) {
	s := store(t)
	h := s.Handler()
	call := func(method, path, body string, want int) string {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "clients") {
			t.Fatal("mutation ledger exposed")
		}
		return w.Body.String()
	}
	path := "/api/ui-state/walkthrough?key=acme%2Fdemo%237%40v1"
	call("GET", path, "", 200)
	body := fmt.Sprintf(`{"revision":0,"mutation_id":"mutation-00000001:1","value":%s}`, draft)
	call("PUT", path, body, 200)
	call("PUT", path, strings.Replace(body, "00000001", "00000002", 1), 409)
	for _, body := range []string{`{}`, `{"revision":0,"mutation_id":"short","value":{}}`, `{"revision":-1,"mutation_id":"mutation-00000002:1","value":null}`, strings.Repeat("x", 513<<10), body + " {}", `{"revision":1,"mutation_id":"mutation-00000002:1","value":{"seen":[],"notes":{},"note":null}}`, `{"revision":1,"mutation_id":"mutation-00000002:1","value":{"seen":[],"notes":{}}}`, `{"revision":1,"mutation_id":"mutation-00000002:1","value":{"seen":[],"notes":{"one":null},"note":""}}`} {
		call("PUT", path, body, 400)
	}
	for _, path := range []string{"/api/ui-state/unknown", "/api/ui-state/walkthrough?key=../../config", "/api/ui-state/inbox?key=other"} {
		call("GET", path, "", 400)
	}
	call("POST", path, "", 405)
}
