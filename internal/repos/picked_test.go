package repos

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPickWithoutAClone(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	v, e := r.Pick(ctx, "Owner/Repo", &Settings{Automatic: new(false)})
	if e != nil || v.Repository != "owner/repo" || len(v.Clones) != 0 || v.Clones == nil {
		t.Fatalf("%#v %v", v, e)
	}
	// Picking again keeps the settings unless new ones are given.
	if v, e = r.Pick(ctx, "owner/repo", nil); e != nil || v.Settings.Automatic == nil || *v.Settings.Automatic {
		t.Fatalf("%#v %v", v, e)
	}
	list, e := r.Repositories(ctx)
	if e != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, e)
	}
	data, _ := json.Marshal(list)
	if !strings.Contains(string(data), `"clones":[]`) {
		t.Fatal(string(data))
	}
	s, ok, e := r.RepositorySettings(ctx, "OWNER/repo")
	if e != nil || !ok || s.Automatic == nil || *s.Automatic {
		t.Fatalf("%#v %v %v", s, ok, e)
	}
	if clones, e := r.List(ctx); e != nil || len(clones) != 0 {
		t.Fatalf("clones %v %v", clones, e)
	}
	if e = r.UpdateRepository(ctx, "owner/repo", Settings{Automatic: new(true)}); e != nil {
		t.Fatal(e)
	}
	if s, _, _ = r.RepositorySettings(ctx, "owner/repo"); !explicitlyOn(s.Automatic) {
		t.Fatalf("%#v", s)
	}
	if e = r.UpdateRepository(ctx, "owner/other", Settings{}); !errors.Is(e, ErrNotFound) {
		t.Fatalf("unpicked update %v", e)
	}
	if list, _ = r.Repositories(ctx); len(list) != 1 {
		t.Fatalf("failed pick registered %v", list)
	}
}

func TestPickRejectsInvalidNames(t *testing.T) {
	r := registry(t)
	for _, name := range []string{"", "a", "a/b/c", "../x", "owner/..", "git.example.com/owner/repo", "ow ner/repo"} {
		if _, e := r.Pick(context.Background(), name, nil); !errors.Is(e, ErrInvalidRepository) {
			t.Errorf("%q: %v", name, e)
		}
	}
	if list, _ := r.Repositories(context.Background()); len(list) != 0 {
		t.Fatal(list)
	}
}

func TestRemoveKeepsPickedAndUnpickRemovesClones(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	c, e := r.Add(ctx, clone(t, "https://github.com/owner/repo"), &Settings{Automatic: new(false)})
	if e != nil {
		t.Fatal(e)
	}
	list, e := r.Repositories(ctx)
	if e != nil || len(list) != 1 || len(list[0].Clones) != 1 || !list[0].Clones[0].Available {
		t.Fatalf("%v %v", list, e)
	}
	if e = r.Remove(ctx, c.ID); e != nil {
		t.Fatal(e)
	}
	list, e = r.Repositories(ctx)
	if e != nil || len(list) != 1 || len(list[0].Clones) != 0 {
		t.Fatalf("remove unpicked %v %v", list, e)
	}
	if _, ok, _ := r.RepositorySettings(ctx, "owner/repo"); !ok {
		t.Fatal("not registered after remove")
	}
	if _, e = r.Add(ctx, c.Path, nil); e != nil {
		t.Fatal(e)
	}
	if e = r.Unpick(ctx, "Owner/Repo"); e != nil {
		t.Fatal(e)
	}
	if list, _ = r.Repositories(ctx); len(list) != 0 {
		t.Fatal(list)
	}
	if clones, _ := r.List(ctx); len(clones) != 0 {
		t.Fatal(clones)
	}
	if _, ok, _ := r.RepositorySettings(ctx, "owner/repo"); ok {
		t.Fatal("registered after unpick")
	}
	if e = r.Unpick(ctx, "owner/repo"); !errors.Is(e, ErrNotFound) {
		t.Fatalf("second unpick %v", e)
	}
	if e = r.Unpick(ctx, "owner/unknown"); !errors.Is(e, ErrNotFound) {
		t.Fatalf("unknown %v", e)
	}
	// The settings survive a re-pick.
	v, e := r.Pick(ctx, "owner/repo", nil)
	if e != nil || v.Settings.Automatic == nil || *v.Settings.Automatic {
		t.Fatalf("%#v %v", v, e)
	}
}

func TestRepositoriesHTTP(t *testing.T) {
	r := registry(t)
	h := r.Handler()
	request := func(method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	if code, body := request(http.MethodGet, "/api/repositories", ""); code != 200 || body != "[]" {
		t.Fatal(code, body)
	}
	for _, body := range []string{`{"repository":"a"}`, `{"repository":"a/b/c"}`, `{"repository":""}`, `{}`, `{"repository":"owner/repo","unknown":1}`, `{"repository":"owner/repo","settings":{"other":""}}`, `{"repository":"owner/repo"} {}`} {
		if code, _ := request("POST", "/api/repositories", body); code != 400 {
			t.Fatal(body, code)
		}
	}
	code, body := request("POST", "/api/repositories", `{"repository":"Owner/Repo","settings":null}`)
	if code != 200 || body != `{"repository":"owner/repo","settings":{"automatic":null},"clones":[]}` {
		t.Fatal(code, body)
	}
	if code, body = request(http.MethodGet, "/api/repositories", ""); code != 200 || !strings.HasPrefix(body, `[{"repository":"owner/repo"`) || !strings.Contains(body, `"clones":[]`) {
		t.Fatal(code, body)
	}
	for _, body := range []string{`{"daily_cap":null}`, `{"unknown":1,"automatic":null}`} {
		if code, _ = request("PUT", "/api/repositories/owner/repo/settings", body); code != 400 {
			t.Fatal(body, code)
		}
	}
	// An older app still sends the daily cap baum no longer has, and the
	// repository harness and model that are global now: they are ignored, not
	// refused.
	if code, _ = request("PUT", "/api/repositories/owner/repo/settings", `{"automatic":false,"daily_cap":1.5,"backend":"codex","model":"m"}`); code != 200 {
		t.Fatal(code)
	}
	if s, ok, _ := r.RepositorySettings(context.Background(), "owner/repo"); !ok || s.Automatic == nil || *s.Automatic {
		t.Fatalf("%#v %v", s, ok)
	}
	if code, _ = request("PUT", "/api/repositories/owner/unknown/settings", `{"automatic":null,"daily_cap":null}`); code != 404 {
		t.Fatal(code)
	}
	if code, _ = request("DELETE", "/api/repositories/owner/repo", ""); code != 204 {
		t.Fatal(code)
	}
	if code, _ = request("DELETE", "/api/repositories/owner/repo", ""); code != 404 {
		t.Fatal(code)
	}
}
