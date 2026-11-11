package agentcontext

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestReaderOnlyGets(t *testing.T) {
	var seen []string
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization")+r.Header.Get("Cookie"))
		if r.URL.Path == "/api/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	reader := NewReader(api)
	// Get is the reader's only method: there is no way to name another.
	if typ := reflect.TypeFor[*Reader](); typ.NumMethod() != 1 || typ.Method(0).Name != "Get" {
		t.Fatalf("Reader has methods beyond Get: %d", typ.NumMethod())
	}
	response, err := reader.Get(context.Background(), "/api/prs?repo=acme%2Fweb&n=1")
	if err != nil || response.Status != 200 || string(response.Body) != `{"ok":true}` || response.ContentType != "application/json" {
		t.Fatalf("%+v %v", response, err)
	}
	if response, err := reader.Get(context.Background(), "/api/missing"); err != nil || response.Status != 404 {
		t.Fatalf("%+v %v", response, err)
	}
	for _, target := range []string{
		"/api/../launch.js", "/api/./prs", "/launch.js", "/s/ns/api/prs", "http://127.0.0.1:1/api/prs",
		"//evil.example/api/prs", "/api/mcp", "api/prs", "/api/prs#frag", "/api//prs",
	} {
		if _, err := reader.Get(context.Background(), target); err == nil {
			t.Fatalf("%s was read", target)
		}
	}
	if strings.Join(seen, "|") != "GET /api/prs?repo=acme%2Fweb&n=1 |GET /api/missing " {
		t.Fatalf("requests %q", seen)
	}
}

func TestReaderBoundsTheBody(t *testing.T) {
	reader := NewReader(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 1<<20)
		for range 9 {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	if _, err := reader.Get(context.Background(), "/api/huge"); !errors.Is(err, errReadTooLarge) {
		t.Fatal(err)
	}
}
