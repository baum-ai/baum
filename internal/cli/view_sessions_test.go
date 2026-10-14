package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/schema"
)

// startView runs "baum view" with args until the test ends and returns its URL.
func startView(t *testing.T, args ...string) string {
	t.Helper()
	_, stop := stubViewSeams(t)
	var out, errOut lockedBuffer
	finished := make(chan error, 1)
	go func() {
		finished <- Run(append([]string{"view", "--no-open"}, args...), strings.NewReader(""), &out, &errOut)
	}()
	t.Cleanup(func() {
		stop()
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			t.Error("view did not stop")
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if url := strings.TrimSpace(out.String()); url != "" {
			return url
		}
		if time.Now().After(deadline) {
			t.Fatalf("no URL printed; stderr %q", errOut.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// reviewFor is the example document, moved onto the fixture's pull request.
func reviewFor(t *testing.T, f linktest.Fixture) string {
	t.Helper()
	text := string(schema.Example)
	for old, replacement := range map[string]string{
		"example-org/taskline":                     linktest.Repository,
		"d6d7a116b9babb73a1e9be4db63b94f35952ba6b": f.Head,
		"48682784a10b4ecf3a94f47a2f8f68b846cfc01b": f.Base,
	} {
		text = strings.ReplaceAll(text, old, replacement)
	}
	path := filepath.Join(t.TempDir(), "review.json")
	writeFile(t, path, text)
	return path
}

func getSessions(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url + "api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return body
}

func TestViewServesLinkedSessionsFromThisMachine(t *testing.T) {
	f := linktest.New(t)
	t.Chdir(f.Repo)
	url := startView(t, append([]string{reviewFor(t, f)}, f.Flags...)...)

	body := getSessions(t, url)
	var got struct {
		Available bool
		Sessions  []struct{ ID, Source, Title string }
		Commits   []struct {
			OID   string
			Links []struct {
				Session, Method string
				Confidence      float64
			}
		}
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	if !got.Available || len(got.Sessions) != 1 || got.Sessions[0].ID != linktest.SessionID || len(got.Commits) != 2 {
		t.Fatalf("response: %s", body)
	}
	if links := got.Commits[0].Links; len(links) != 1 || links[0].Session != linktest.SessionID || links[0].Method != "commit_ref" {
		t.Errorf("links = %+v", links)
	}
	for _, leaked := range []string{linktest.Email, linktest.Secret, "bob@example.com"} {
		if strings.Contains(string(body), leaked) {
			t.Errorf("response leaks %q: %s", leaked, body)
		}
	}
}

func TestViewSampleHasNoSessions(t *testing.T) {
	f := linktest.New(t)
	t.Chdir(f.Repo)
	url := startView(t, append([]string{"--sample"}, f.Flags...)...)
	body := string(getSessions(t, url))
	if !strings.Contains(body, `"available":false`) || !strings.Contains(body, `"code":"sample"`) {
		t.Errorf("response: %s", body)
	}
}

func TestViewOutsideTheCloneHasNoSessions(t *testing.T) {
	f := linktest.New(t)
	t.Chdir(t.TempDir())
	url := startView(t, append([]string{reviewFor(t, f)}, f.Flags...)...)
	body := string(getSessions(t, url))
	if !strings.Contains(body, `"code":"not_a_clone"`) || strings.Contains(body, f.Repo) {
		t.Errorf("response: %s", body)
	}
}
