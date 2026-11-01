package uiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// Each saved version's patches are read between that version's own commits,
// for the paths that version cites, whichever version is the latest.
func TestPatchesOfEachSavedVersion(t *testing.T) {
	ctx := context.Background()
	registry, err := repos.Open(filepath.Join(appdbtest.TempDir(t), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	dir := appdbtest.TempDir(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Ada", "-c", "user.email=ada@example.com"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(string(out), err)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(name, content string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", "-A")
		git("commit", "-q", "-m", name)
		return git("rev-parse", "HEAD")
	}
	git("init", "-q", "-b", "main")
	git("remote", "add", "origin", "https://github.com/owner/repo")
	base := commit("a.txt", "a\n")
	first := commit("b.txt", "b\n")
	second := commit("b.txt", "b\nb2\n")
	clone, err := registry.Add(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := store.Open(clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: "owner/repo", Number: 7}
	previous := store.Version{}
	for n, head := range []string{first, second} {
		document := fmt.Appendf(nil, `{"review":{"repository":"owner/repo","pull_request":7,"version":%d,"head":%q,"base":%q,"merge_base":%q},"index":{"hunks":[{"id":"h","path":"b.txt","role":"docs"}]}}`, n+1, head, base, base)
		next := store.Version{Metadata: store.Metadata{Version: n + 1}, Document: document}
		if err := versions.Put(ctx, key, previous, next); err != nil {
			t.Fatal(err)
		}
		previous = next
	}
	handler := LocalAPI{Registry: registry, Config: config.Default()}.Handler()
	defer handler.Close()
	patch := func(query string) (int, *string) {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "http://local/api/patch?clone="+clone.ID+"&pr=7"+query, nil))
		if w.Code != 200 {
			return w.Code, nil
		}
		var body struct {
			Patch *string `json:"patch"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return w.Code, body.Patch
	}
	for _, c := range []struct{ query, want string }{
		{"&path=b.txt", "@@ -0,0 +1,2 @@\n+b\n+b2\n"},
		{"&version=2&path=b.txt", "@@ -0,0 +1,2 @@\n+b\n+b2\n"},
		{"&version=1&path=b.txt", "@@ -0,0 +1 @@\n+b\n"},
	} {
		if code, got := patch(c.query); code != 200 || got == nil || *got != c.want {
			t.Errorf("%s: %d %v, want %q", c.query, code, got, c.want)
		}
	}
	for _, query := range []string{"&version=1&path=a.txt", "&version=3&path=b.txt"} {
		if code, _ := patch(query); code != 404 {
			t.Errorf("%s: %d, want 404", query, code)
		}
	}
}
