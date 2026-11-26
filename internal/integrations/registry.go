// Package integrations exposes the desktop daemon's connected services.
package integrations

import (
	"context"
	"encoding/json"
	"net/http"
)

type Connection struct {
	State   string `json:"state"`
	Account string `json:"account,omitempty"`
	Message string `json:"message,omitempty"`
}

type Provider interface {
	ID() string
	Name() string
	Status(context.Context) Connection
	Connect(context.Context) (string, error)
	Disconnect(context.Context) error
}

type Issue struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	State string `json:"state"`
	URL   string `json:"url"`
}

// An IssueResolver recognizes its own references in a pull request description.
type IssueResolver interface {
	Issues(context.Context, string) []Issue
}

type entry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Connection
}

func Handler(providers ...Provider) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/integrations", func(w http.ResponseWriter, r *http.Request) {
		entries := make([]entry, 0, len(providers))
		for _, p := range providers {
			entries = append(entries, entry{p.ID(), p.Name(), p.Status(r.Context())})
		}
		reply(w, entries)
	})
	for _, p := range providers {
		path := "/api/integrations/" + p.ID()
		mux.HandleFunc("POST "+path+"/connect", func(w http.ResponseWriter, r *http.Request) {
			url, err := p.Connect(r.Context())
			if err != nil {
				http.Error(w, "Could not start connection", http.StatusServiceUnavailable)
				return
			}
			reply(w, map[string]string{"url": url})
		})
		mux.HandleFunc("POST "+path+"/disconnect", func(w http.ResponseWriter, r *http.Request) {
			if err := p.Disconnect(r.Context()); err != nil {
				http.Error(w, "Could not disconnect account", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	mux.HandleFunc("POST /api/integrations/issues", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Description string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "Invalid description", http.StatusBadRequest)
			return
		}
		issues := []Issue{}
		for _, p := range providers {
			if resolver, ok := p.(IssueResolver); ok {
				issues = append(issues, resolver.Issues(r.Context(), req.Description)...)
			}
		}
		reply(w, issues)
	})
	return mux
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
