package uiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/viewserver"
)

func decisionsRegistry(t *testing.T) (*repos.Registry, repos.Clone) {
	t.Helper()
	registry, err := repos.Open(filepath.Join(t.TempDir(), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://github.com/owner/repo"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(b), err)
		}
	}
	clone, err := registry.Add(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return registry, clone
}

// A pull request's linked sessions are those its saved review's sessions list
// holds, by the hooks' agent names, with when the review was generated.
func TestLinkedSessionsFollowTheSavedReview(t *testing.T) {
	ctx := context.Background()
	registry, clone := decisionsRegistry(t)
	versions, err := store.Open(clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	document := []byte(`{"review":{"repository":"owner/repo","pull_request":7,"version":1,"head":"1111111111111111111111111111111111111111","base":"2222222222222222222222222222222222222222","generated_at":"2026-10-20T09:00:00Z"}}`)
	if err = versions.Put(ctx, store.Key{Repository: "owner/repo", Number: 7}, store.Version{}, store.Version{Metadata: store.Metadata{Version: 1}, Document: document}); err != nil {
		t.Fatal(err)
	}
	cache := newDocumentCache()
	defer cache.close()
	asked := 0
	cache.resolve = func(_ context.Context, r locallink.Request) (locallink.Result, error) {
		asked++
		if r.Dir != clone.Path || r.Head != "1111111111111111111111111111111111111111" {
			t.Errorf("resolve asked %+v", r)
		}
		return locallink.Result{Available: true, Sessions: []locallink.Session{
			{ID: "s1", Vendor: "claude_code", Title: "Port the parser"},
			{ID: "s2", Vendor: "codex"},
			{ID: "s3", Vendor: "someone_else"},
		}}, nil
	}
	linked := LocalAPI{Registry: registry}.linkedSessions(cache)
	sessions, generated, err := linked(ctx, "Owner/Repo", 7)
	if err != nil || !generated.Equal(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)) || len(sessions) != 3 ||
		sessions[0].ID != "s1" || sessions[0].Title != "Port the parser" || sessions[1].Vendor != "codex" {
		t.Fatalf("%+v %v %v", sessions, generated, err)
	}
	// The PR page's sessions list shares the computation.
	if _, _, err := linked(ctx, "owner/repo", 7); err != nil || asked != 1 {
		t.Fatalf("asked %d times, %v", asked, err)
	}
	sessions, generated, err = linked(ctx, "owner/repo", 8)
	if err != nil || sessions != nil || !generated.IsZero() {
		t.Fatalf("without a review: %+v %v %v", sessions, generated, err)
	}
}

func TestDecisionSettingsAndRoute(t *testing.T) {
	registry, _ := decisionsRegistry(t)
	var linked LinkedSessions
	changes := 0
	handler := LocalAPI{Registry: registry, Config: config.Default(), Decisions: func(l LinkedSessions) http.Handler {
		linked = l
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) })
	}, DecisionModel: func(_ context.Context, p repos.Preferences) DecisionModel {
		if p.Decisions.Model != nil && *p.Decisions.Model == "claude-haiku-5-5" {
			return DecisionModel{Backend: "codex", Model: "gpt-6-luna", Ignored: true}
		}
		return DecisionModel{Backend: "codex", Model: "gpt-6-luna"}
	}, DecisionsChanged: func() { changes++ }}.Handler()
	defer handler.Close()
	serve := func(method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, "http://local"+path, strings.NewReader(body)))
		return w.Code, w.Body.String()
	}
	if code, body := serve("GET", "/api/decisions?agent=a&session=s", ""); code != 200 || body != `{"ok":true}` || linked == nil {
		t.Fatalf("decisions route %d %s", code, body)
	}
	settings := func() decisionSettings {
		t.Helper()
		code, body := serve("GET", "/api/settings/decisions", "")
		var got struct{ Decisions decisionSettings }
		if code != 200 || json.Unmarshal([]byte(body), &got) != nil {
			t.Fatalf("%d %s", code, body)
		}
		return got.Decisions
	}
	if got := settings(); !got.Enabled || got.Model != "" || got.DailyCap != nil {
		t.Fatalf("defaults %+v", got)
	}
	// GET /api/settings, which the app reads on every reload, leaves them out.
	if _, body := serve("GET", "/api/settings", ""); strings.Contains(body, "decisions") {
		t.Fatalf("settings carry decisions: %s", body)
	}
	if changes != 0 {
		t.Fatalf("a read published %d changes", changes)
	}
	if code, body := serve("PUT", "/api/settings/decisions", `{"enabled":false,"model":" gpt-6-luna ","daily_cap":1.5}`); code != 200 ||
		body != `{"decisions":{"enabled":false,"model":"gpt-6-luna","daily_cap":1.5,"backend":"codex","backend_undetermined":false,"effective_model":"gpt-6-luna","model_ignored":false}}`+"\n" {
		t.Fatalf("put %d %q", code, body)
	}
	if got := settings(); got.Enabled || got.Model != "gpt-6-luna" || got.DailyCap == nil || *got.DailyCap != 1.5 {
		t.Fatalf("after put %+v", got)
	}
	// Saving the settings, or a repository's, changes what capture does.
	if changes != 1 {
		t.Fatalf("changes after a put: %d", changes)
	}
	if code, _ := serve("POST", "/api/repositories", `{"repository":"owner/other"}`); code != 200 || changes != 2 {
		t.Fatalf("a repository change: %d, %d changes", code, changes)
	}
	// The settings' own route also says what capture makes of them.
	if code, body := serve("PUT", "/api/settings/decisions", `{"enabled":true,"model":"claude-haiku-5-5","daily_cap":null}`); code != 200 ||
		!strings.Contains(body, `"effective_model":"gpt-6-luna","model_ignored":true`) {
		t.Fatalf("put an ignored model %d %s", code, body)
	}
	if code, body := serve("GET", "/api/settings/decisions", ""); code != 200 ||
		body != `{"decisions":{"enabled":true,"model":"claude-haiku-5-5","daily_cap":null,"backend":"codex","backend_undetermined":false,"effective_model":"gpt-6-luna","model_ignored":true}}`+"\n" {
		t.Fatalf("get %d %s", code, body)
	}
	for _, body := range []string{`{"enabled":true,"daily_cap":-1}`, `{"model":"x"}`, `{"enabled":true,"other":1}`} {
		if code, _ := serve("PUT", "/api/settings/decisions", body); code != http.StatusBadRequest {
			t.Errorf("%s answered %d", body, code)
		}
	}
	// A whole replacement: on, the default model and no cap.
	if code, _ := serve("PUT", "/api/settings/decisions", `{"enabled":true,"model":"","daily_cap":null}`); code != 200 {
		t.Fatal(code)
	}
	prefs, _ := registry.Preferences(context.Background())
	if prefs.Decisions != (repos.DecisionPreferences{}) {
		t.Fatalf("stored %+v", prefs.Decisions)
	}
}

// A saved review too large to serve links no sessions, but its time still
// marks the decisions made after it provisional.
func TestLinkedSessionsOfAnOversizedReview(t *testing.T) {
	ctx := context.Background()
	registry, clone := decisionsRegistry(t)
	versions, err := store.Open(clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	document := []byte(`{"review":{"repository":"owner/repo","pull_request":7,"version":1,"generated_at":"2026-10-20T09:00:00Z"},"pad":"` + strings.Repeat("x", viewserver.MaxDocumentBytes) + `"}`)
	if err = versions.Put(ctx, store.Key{Repository: "owner/repo", Number: 7}, store.Version{}, store.Version{Metadata: store.Metadata{Version: 1}, Document: document}); err != nil {
		t.Fatal(err)
	}
	cache := newDocumentCache()
	defer cache.close()
	cache.resolve = func(context.Context, locallink.Request) (locallink.Result, error) {
		t.Error("an oversized review was linked")
		return locallink.Result{}, nil
	}
	sessions, generated, err := LocalAPI{Registry: registry}.linkedSessions(cache)(ctx, "owner/repo", 7)
	if err != nil || sessions != nil || !generated.Equal(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v %v %v", sessions, generated, err)
	}
}

// A viewer closed while it links sessions links none, whichever way the
// computation ends (the viewer stopping, or the link failing as its context
// ends), and the review's time still dates the provisional marks.
func TestLinkedSessionsWhenTheViewerStops(t *testing.T) {
	ctx := context.Background()
	registry, clone := decisionsRegistry(t)
	versions, err := store.Open(clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	document := []byte(`{"review":{"repository":"owner/repo","pull_request":7,"version":1,"head":"1111111111111111111111111111111111111111","base":"2222222222222222222222222222222222222222","generated_at":"2026-10-20T09:00:00Z"}}`)
	if err = versions.Put(ctx, store.Key{Repository: "owner/repo", Number: 7}, store.Version{}, store.Version{Metadata: store.Metadata{Version: 1}, Document: document}); err != nil {
		t.Fatal(err)
	}
	cache := newDocumentCache()
	started := make(chan struct{})
	cache.resolve = func(ctx context.Context, _ locallink.Request) (locallink.Result, error) {
		close(started)
		<-ctx.Done()
		return locallink.Result{}, ctx.Err()
	}
	go func() {
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Error("the sessions computation did not start")
		}
		cache.close()
	}()
	sessions, generated, err := LocalAPI{Registry: registry}.linkedSessions(cache)(ctx, "owner/repo", 7)
	if err != nil || sessions != nil || !generated.Equal(time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v %v %v", sessions, generated, err)
	}
}

func TestDecisionsUnavailableWithoutCapture(t *testing.T) {
	registry, _ := decisionsRegistry(t)
	handler := LocalAPI{Registry: registry, Config: config.Default()}.Handler()
	defer handler.Close()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "http://local/api/decisions?agent=a&session=s", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"decisions_unavailable"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
