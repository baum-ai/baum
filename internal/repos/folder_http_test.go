package repos

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFolderHTTPRegistersMatchingUpstream(t *testing.T) {
	r := registry(t)
	path := clone(t, "https://github.com/me/fork")
	git(t, path, "remote", "add", "upstream", "https://github.com/owner/repo")
	for _, tc := range []struct {
		name   string
		status int
	}{{"owner/mismatch", 400}, {"owner/repo", 200}} {
		body, _ := json.Marshal(map[string]string{"path": path, "repository": tc.name})
		rec := httptest.NewRecorder()
		r.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/repos", strings.NewReader(string(body))))
		if rec.Code != tc.status {
			t.Fatalf("%s = %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	list, err := r.Repositories(context.Background())
	if err != nil || len(list) != 1 || list[0].Repository != "owner/repo" || len(list[0].Clones) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
}
