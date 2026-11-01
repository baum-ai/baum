package cli

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/store"
)

// view --pr answers another saved version's patches from the store, and a
// version the store does not hold is 404, not a failure.
func TestViewPRAnswersPatchesOfOtherSavedVersions(t *testing.T) {
	bundleEnv(t)
	root := filepath.Join(t.TempDir(), "store")
	versions, err := store.Open("", root, true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: "acme/demo", Number: 7}
	previous := store.Version{}
	for n, path := range []string{"old.go", "new.go"} {
		document := fmt.Appendf(nil, `{"schema":"baum.review/1","review":{"repository":"acme/demo","pull_request":7,"version":%d,"head":"%s","base":"%s"},"index":{"hunks":[{"id":"h","path":%q}]}}`,
			n+1, strings.Repeat("a", 40), strings.Repeat("b", 40), path)
		next := store.Version{Metadata: store.Metadata{Version: n + 1}, Document: document}
		if err := versions.Put(context.Background(), key, previous, next); err != nil {
			t.Fatal(err)
		}
		previous = next
	}

	_, stop := stubViewSeams(t)
	var out, errOut lockedBuffer
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_ = Run([]string{"internal", "view", "--pr", "7", "--repo", "acme/demo", "--store", root, "--no-open"}, strings.NewReader(""), &out, &errOut)
	}()
	t.Cleanup(func() {
		stop()
		select {
		case <-exited:
		case <-time.After(30 * time.Second):
			t.Error("view did not stop")
		}
	})
	var url string
	for deadline := time.Now().Add(30 * time.Second); url == ""; time.Sleep(5 * time.Millisecond) {
		url = strings.TrimSpace(out.String())
		if time.Now().After(deadline) {
			t.Fatalf("no URL printed; stderr %q", errOut.String())
		}
	}
	for _, c := range []struct {
		query string
		code  int
	}{
		{"path=new.go", http.StatusOK},
		{"path=old.go&version=1", http.StatusOK},
		{"path=new.go&version=1", http.StatusNotFound},
		{"path=old.go&version=3", http.StatusNotFound},
	} {
		resp, err := http.Get(url + "api/patch?" + c.query)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != c.code {
			t.Errorf("%s: status %d, want %d; stderr %q", c.query, resp.StatusCode, c.code, errOut.String())
		}
	}
}
