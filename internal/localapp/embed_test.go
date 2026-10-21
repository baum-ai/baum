package localapp

import (
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/uibuild"
)

func TestHandlerServesFilesOnly(t *testing.T) {
	uibuild.Require(t, Assets())
	assets, err := fs.Glob(Assets(), "assets/*.js")
	if err != nil || len(assets) == 0 {
		t.Fatal("no built assets", err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	if w := get("/"); w.Code != 200 || !strings.Contains(w.Body.String(), `<div id="root">`) {
		t.Fatal("index", w.Code)
	}
	// Assets are referenced relative to the namespace, never from the root.
	if w := get("/"); strings.Contains(w.Body.String(), `"/assets/`) {
		t.Fatal("index references root-absolute assets")
	}
	if w := get("/" + assets[0]); w.Code != 200 {
		t.Fatal(assets[0], w.Code)
	}
	for _, path := range []string{"/assets/", "/assets", "/missing", "/../embed.go", "/%2e%2e/embed.go"} {
		if w := get(path); w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
}
