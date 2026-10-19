package repos

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/internal/config"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("git %v: %s %v", args, out, e)
	}
}
func clone(t *testing.T, remote string) string {
	t.Helper()
	p := t.TempDir()
	git(t, p, "init")
	git(t, p, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "init")
	git(t, p, "remote", "add", "origin", remote)
	return p
}
func registry(t *testing.T) *Registry {
	t.Helper()
	r, e := Open(filepath.Join(t.TempDir(), "registry.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}
func TestClonesAndSettings(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	p := clone(t, "https://github.com/Owner/Repo.git")
	cap := 1.25
	b := config.BackendCodex
	m := "model"
	s := Settings{Automatic: new(true), DailyCap: &cap, Backend: &b, Model: &m}
	c, e := r.Add(ctx, p, &s)
	if e != nil {
		t.Fatal(e)
	}
	wt := filepath.Join(t.TempDir(), "linked")
	git(t, p, "worktree", "add", "-b", "linked", wt)
	w, e := r.Add(ctx, wt, nil)
	if e != nil {
		t.Fatal(e)
	}
	other, e := r.Add(ctx, clone(t, "https://github.com/owner/repo"), nil)
	if e != nil {
		t.Fatal(e)
	}
	if w.ID == c.ID || other.ID == c.ID || w.CommonDir != c.CommonDir || other.CommonDir == c.CommonDir || w.Repository != "owner/repo" || !explicitlyOn(w.Settings.Automatic) {
		t.Fatalf("clones %#v %#v %#v", c, w, other)
	}
	again, e := r.Add(ctx, p, nil)
	if e != nil || !explicitlyOn(again.Settings.Automatic) {
		t.Fatalf("idempotent %#v %v", again, e)
	}
	global := config.Default()
	global.Model = "global"
	if got := s.Resolve(global); got.Backend != b || got.Model != m {
		t.Fatal(got)
	}
	if got := (Settings{}).Resolve(global); got.Model != "global" {
		t.Fatal(got)
	}
	git(t, p, "remote", "set-url", "origin", "https://github.com/owner/replaced")
	list, e := r.List(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range list {
		if (v.ID == c.ID || v.ID == w.ID) && v.Available {
			t.Fatal("changed remote available")
		}
	}
	if e = os.RemoveAll(other.Path); e != nil {
		t.Fatal(e)
	}
	list, e = r.List(ctx)
	if e != nil || len(list) != 3 {
		t.Fatalf("missing retained %v %v", list, e)
	}
	for _, v := range list {
		if v.ID == other.ID && v.Available {
			t.Fatal("missing available")
		}
	}
	if e = r.Remove(ctx, other.Path); e != nil {
		t.Fatal(e)
	}
	if e = r.Update(ctx, c.ID, Settings{}); e != nil {
		t.Fatal(e)
	}
	if e = r.Remove(ctx, w.ID); e != nil {
		t.Fatal(e)
	}
}
func TestRejectAndFallback(t *testing.T) {
	r := registry(t)
	ctx := context.Background()
	for _, p := range []string{filepath.Join(t.TempDir(), "gone"), t.TempDir(), clone(t, "https://gitlab.com/owner/repo")} {
		if _, e := r.Add(ctx, p, nil); e == nil {
			t.Fatal("accepted", p)
		}
	}
	p := clone(t, "https://gitlab.com/owner/repo")
	git(t, p, "remote", "add", "upstream", "https://token:secret@github.com/owner/repo")
	c, e := r.Add(ctx, p, nil)
	if e != nil || c.Settings.Automatic != nil || c.Repository != "owner/repo" {
		t.Fatalf("%#v %v", c, e)
	}
	t.Setenv("GH_HOST", "git.example.com")
	enterprise, e := r.Add(ctx, clone(t, "https://git.example.com/owner/repo"), nil)
	if e != nil || enterprise.Repository == c.Repository {
		t.Fatalf("enterprise %#v %v", enterprise, e)
	}
	negative := -1.0
	if _, e = r.Add(ctx, p, &Settings{DailyCap: &negative}); e == nil {
		t.Fatal("negative cap accepted")
	}
}
func TestPermissionsAndConcurrentProcesses(t *testing.T) {
	if os.Getenv("BAUM_REGISTRY_CHILD") != "" {
		r, e := Open(os.Getenv("BAUM_REGISTRY_DB"))
		if e != nil {
			t.Fatal(e)
		}
		defer func() { _ = r.Close() }()
		if _, e = r.Add(context.Background(), os.Getenv("BAUM_REGISTRY_CLONE"), nil); e != nil {
			t.Fatal(e)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "registry.sqlite")
	if e := os.WriteFile(path, nil, 0644); e != nil {
		t.Fatal(e)
	}
	r, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close() }()
	info, e := os.Stat(path)
	if e != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v %v", info, e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		p := clone(t, fmt.Sprintf("https://github.com/owner/repo%d", i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestPermissionsAndConcurrentProcesses$")
			cmd.Env = append(os.Environ(), "BAUM_REGISTRY_CHILD=1", "BAUM_REGISTRY_DB="+path, "BAUM_REGISTRY_CLONE="+p)
			if out, e := cmd.CombinedOutput(); e != nil {
				t.Errorf("child: %s %v", out, e)
			}
		}()
	}
	wg.Wait()
	list, e := r.List(context.Background())
	if e != nil || len(list) != 4 {
		t.Fatalf("lost writes %d %v", len(list), e)
	}
}
func TestHTTP(t *testing.T) {
	r := registry(t)
	h := r.Handler()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	p := clone(t, "https://github.com/owner/repo")
	body, _ := json.Marshal(map[string]string{"path": p})
	w := request("POST", "/api/repos", string(body))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var c Clone
	if e := json.Unmarshal(w.Body.Bytes(), &c); e != nil {
		t.Fatal(e)
	}
	for _, body := range []string{`{"unknown":1}`, `{"daily_cap":-1}`, `{"backend":"no"}`, `{} {}`, `{"model":""}`} {
		if w = request("PUT", "/api/repos/"+c.ID+"/settings", body); w.Code != 400 {
			t.Fatal(body, w.Code)
		}
	}
	if w = request("PUT", "/api/repos/"+c.ID+"/settings", `{"automatic":true,"backend":null,"model":null,"daily_cap":0}`); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = request(http.MethodGet, "/api/repos", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"automatic":true`) {
		t.Fatal(w.Code, w.Body)
	}
	if w = request("DELETE", "/api/repos/"+c.ID, ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w = request("DELETE", "/api/repos/"+c.ID, ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestSpecialDatabasePath(t *testing.T) {
	name := "with?#space "
	if runtime.GOOS == "windows" {
		name = "with# space" // Windows forbids '?' and trailing spaces in directory names.
	}
	p := filepath.Join(t.TempDir(), name, "repos.sqlite")
	r, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close() }()
	if _, e = r.Add(context.Background(), clone(t, "https://github.com/owner/repo"), nil); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(p); e != nil {
		t.Fatal(e)
	}
}

func TestConcurrentSettingsPatches(t *testing.T) {
	p := clone(t, "https://github.com/owner/repo")
	path := filepath.Join(t.TempDir(), "repos.sqlite")
	r, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = r.Close() }()
	other, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = other.Close() }()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, db := range []*Registry{r, other} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, e := db.AddPatch(context.Background(), p, func(s Settings) (Settings, error) {
				if i == 0 {
					s.Automatic = new(true)
				} else {
					v := 7.5
					s.DailyCap = &v
				}
				return s, nil
			})
			if e != nil {
				t.Error(e)
			}
		}()
	}
	close(start)
	wg.Wait()
	list, e := r.List(context.Background())
	if e != nil || len(list) != 1 || !explicitlyOn(list[0].Settings.Automatic) || list[0].Settings.DailyCap == nil || *list[0].Settings.DailyCap != 7.5 {
		t.Fatalf("lost settings %#v %v", list, e)
	}
}
