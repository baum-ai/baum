package agentcontext

import (
	"context"
	"encoding/json"
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
		if r.URL.Path == "/api/prs/missing" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	reader := NewReader(api, nil).within([]string{"/api/prs"}, "")
	// Get is the reader's only method: there is no way to name another.
	if typ := reflect.TypeFor[*Reader](); typ.NumMethod() != 1 || typ.Method(0).Name != "Get" {
		t.Fatalf("Reader has methods beyond Get: %d", typ.NumMethod())
	}
	response, err := reader.Get(context.Background(), "/api/prs?repo=acme%2Fweb&n=1")
	if err != nil || response.Status != 200 || string(response.Body) != `{"ok":true}` || response.ContentType != "application/json" {
		t.Fatalf("%+v %v", response, err)
	}
	if response, err := reader.Get(context.Background(), "/api/prs/missing"); err != nil || response.Status != 404 {
		t.Fatalf("%+v %v", response, err)
	}
	for _, target := range []string{
		"/api/../launch.js", "/api/./prs", "/launch.js", "/s/ns/api/prs", "http://127.0.0.1:1/api/prs",
		"//evil.example/api/prs", "/api/mcp", "api/prs", "/api/prs#frag", "/api//prs", "/api/prsx", "/api/repos",
	} {
		if _, err := reader.Get(context.Background(), target); err == nil {
			t.Fatalf("%s was read", target)
		}
	}
	if strings.Join(seen, "|") != "GET /api/prs?repo=acme%2Fweb&n=1 |GET /api/prs/missing " {
		t.Fatalf("requests %q", seen)
	}
	if _, err := NewReader(api, nil).Get(context.Background(), "/api/prs"); !errors.Is(err, errNotReadable) {
		t.Fatal("a reader with no routes read one:", err)
	}
}

func TestUnreadableRoutes(t *testing.T) {
	reached := false
	api := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })
	// Even a reader allowed all of /api/ never reads these.
	reader := NewReader(api, nil).within([]string{"/api"}, "")
	hidden := []string{
		"/api/settings", "/api/settings/time-limits", "/api/debug/stats", "/api/debug/pprof/heap",
		"/api/github-auth", "/api/github-auth/device", "/api/diagnostics", "/api/diagnostics/x",
		"/api/plan-usage", "/api/github/repositories", "/api/events", "/api/mcp",
	}
	for _, target := range hidden {
		if _, err := reader.Get(context.Background(), target); !errors.Is(err, errNotReadable) {
			t.Fatalf("%s: %v", target, err)
		}
	}
	if reached {
		t.Fatal("an unreadable route reached the API")
	}
	s := newServer(t)
	call := func(context.Context, CallContext, json.RawMessage) (string, error) { return "", nil }
	for _, reads := range [][]string{{"/api"}, {"/api/"}, {"/api/settings"}, {"/api/github-auth"}, {"/api/debug/stats"}, {"/api/github"}, {"/api/events"}, {"/api/mcp"}, {"/api/prs/"}, {"prs"}} {
		if err := s.RegisterTool(Tool{Name: "t", Call: call, Reads: reads}); err == nil {
			t.Fatalf("a tool declared %v", reads)
		}
	}
	if err := s.RegisterTool(Tool{Name: "t", Call: call, Reads: []string{"/api/prs", "/api/groups"}}); err != nil {
		t.Fatal(err)
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
	}), nil).within([]string{"/api/huge"}, "")
	if _, err := reader.Get(context.Background(), "/api/huge"); !errors.Is(err, errReadTooLarge) {
		t.Fatal(err)
	}
}
