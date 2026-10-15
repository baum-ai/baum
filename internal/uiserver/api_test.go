package uiserver

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

func TestLocalAPISelectorsAndSecretFreeSettings(t *testing.T) {
	ctx := context.Background()
	registry, err := repos.Open(filepath.Join(t.TempDir(), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"remote", "add", "origin", "https://github.com/owner/repo"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(string(b), err)
		}
	}
	clone, err := registry.Add(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := store.Open(clone.CommonDir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	const id = "att_0123456789abcdef0123456789abcdef"
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
	document := []byte(fmt.Sprintf(`{"review":{"repository":"owner/repo","pull_request":7,"version":1},"index":{"attachments":[{"id":%q}]}}`, id))
	if err = versions.Put(ctx, store.Key{Repository: "owner/repo", Number: 7}, 0, store.Version{Metadata: store.Metadata{Version: 1}, Document: document, Attachments: []store.Attachment{{ID: id, ContentType: "image/png", Bytes: png}}}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.GitHubToken = "secret-github"
	cfg.AnthropicAPIKey = "secret-model"
	handler := LocalAPI{Registry: registry, Config: cfg}.Handler()
	defer handler.Close()
	selector := "?clone=" + clone.ID + "&pr=7"
	for _, tt := range []struct {
		path string
		code int
	}{{"/api/repos", 200}, {"/api/settings", 200}, {"/api/diagnostics", 200}, {"/api/document" + selector, 200}, {"/api/versions" + selector, 200}, {"/api/sessions" + selector, 200}, {"/api/attachments/" + id + selector, 200}, {"/api/document?clone=/tmp&pr=7", 404}, {"/api/document?clone=" + clone.ID + "&pr=8", 404}, {"/api/document?pr=7", 400}, {"/api/attachments/att_ffffffffffffffffffffffffffffffff" + selector, 404}, {"/api/jobs", 200}} {
		t.Run(tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("GET", "http://local"+tt.path, nil))
			if w.Code != tt.code {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "secret-github") || strings.Contains(w.Body.String(), "secret-model") {
				t.Fatal("credentials exposed")
			}
			if strings.HasPrefix(tt.path, "/api/attachments/"+id) && w.Header().Get("Content-Type") != "image/png" {
				t.Fatal("wrong media type")
			}
		})
	}
	// Registry writes are still rejected by the enclosing server before routing.
	s, err := New(Options{API: handler})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.listener.Close() }()
	w := request(t, s, "DELETE", "/api/repos/"+clone.ID, "", "https://evil.example", "application/json", "Bearer "+s.launch, "{}")
	if w.Code != http.StatusForbidden {
		t.Fatal(w.Code)
	}
	list, err := registry.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatal("CSRF removed registry record", err)
	}
}
