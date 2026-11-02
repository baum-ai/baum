package viewserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/locallink/linktest"
)

type fileAnswer struct {
	Path      string  `json:"path"`
	Content   *string `json:"content"`
	Truncated bool    `json:"truncated"`
}

func fetchFile(t *testing.T, base, path string) (*http.Response, fileAnswer) {
	t.Helper()
	resp, body := get(t, base+"/api/file?path="+url.QueryEscape(path))
	var decoded fileAnswer
	if resp.StatusCode != http.StatusOK {
		return resp, decoded
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("/api/file: %v\n%s", err, body)
	}
	return resp, decoded
}

// fileDocument cites a context reference, a hunk and a renamed hunk's old
// path, and a file anchor inside a section.
func fileDocument(head string) string {
	return fmt.Sprintf(`{"schema":"baum.review/1","review":{"repository":"acme/widgets","pull_request":7,"head":%q,"base":%q},`+
		`"summary":{"text":"","context":{"area":{"text":"","refs":[{"path":"pkg/ctx.go","symbol":"Run"},{"path":"pkg","symbol":null}]}}},`+
		`"sections":[{"id":"s1","anchors":[{"type":"file","path":"docs/anchor.md"},{"type":"commit","sha":"abc","path":"not/a/file.go"}]}],`+
		`"index":{"hunks":[{"id":"h0","path":"src/new.go","old_path":"src/old.go"}]}}`+"\n", head, base0)
}

func TestFileIsServedOnlyForAPathTheDocumentCites(t *testing.T) {
	var calls atomic.Int32
	var asked []string
	_, base := start(t, Options{Document: []byte(fileDocument(headA)), File: func(_ context.Context, review Review, path string) (locallink.FileResult, error) {
		calls.Add(1)
		asked = append(asked, path)
		if review.Head != headA {
			t.Errorf("read at %q, want the document's head", review.Head)
		}
		return locallink.FileResult{Available: true, Content: "package " + path + "\n"}, nil
	}})
	for _, path := range []string{"pkg/ctx.go", "pkg", "src/new.go", "src/old.go", "docs/anchor.md"} {
		resp, answer := fetchFile(t, base, path)
		if resp.StatusCode != http.StatusOK || answer.Path != path || answer.Content == nil || *answer.Content != "package "+path+"\n" {
			t.Errorf("%s: status %d, answer %+v", path, resp.StatusCode, answer)
		}
		if resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s: headers %v", path, resp.Header)
		}
	}
	fetchFile(t, base, "pkg/ctx.go")
	if n := calls.Load(); n != 5 {
		t.Errorf("calls = %d, want 5: an available file is kept", n)
	}
	for _, path := range []string{"pkg/secret.go", "not/a/file.go", "../pkg/ctx.go", "pkg/../pkg/ctx.go", "/pkg/ctx.go", "-p", ""} {
		if resp, _ := fetchFile(t, base, path); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%q: status %d, want 404", path, resp.StatusCode)
		}
	}
	if n := calls.Load(); n != 5 {
		t.Errorf("calls = %d: git ran for a path the document does not cite (%v)", n, asked)
	}
}

func TestFileWithoutAFunctionOrForTheSampleIsNull(t *testing.T) {
	for _, opts := range []Options{
		{Document: []byte(fileDocument(headA))},
		{Document: []byte(fileDocument(headA)), Sample: true, File: func(context.Context, Review, string) (locallink.FileResult, error) {
			t.Error("the sample read a file")
			return locallink.FileResult{}, nil
		}},
	} {
		_, base := start(t, opts)
		if resp, answer := fetchFile(t, base, "pkg/ctx.go"); resp.StatusCode != http.StatusOK || answer.Content != nil {
			t.Errorf("status %d, answer %+v", resp.StatusCode, answer)
		}
	}
}

// The whole path, with the real reader: a clone holding the document's head,
// with a text file, a directory, a binary file and a long file at it.
func TestFileEndToEndFromAClone(t *testing.T) {
	f := linktest.NewWithFiles(t, "pkg/util.go")
	linktest.Git(t, f.Repo, "checkout", "-q", "feature")
	if err := os.WriteFile(filepath.Join(f.Repo, "image.bin"), []byte("PNG\x00\x01\x02"), 0o644); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("é", locallink.MaxFileBytes) // two bytes each
	if err := os.WriteFile(filepath.Join(f.Repo, "long.txt"), []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	linktest.Git(t, f.Repo, "add", "-A")
	linktest.Git(t, f.Repo, "commit", "-q", "-m", "Add more files")
	head := linktest.Git(t, f.Repo, "rev-parse", "HEAD")
	linktest.Git(t, f.Repo, "checkout", "-q", "main")

	refs := []string{}
	for _, path := range []string{"a.txt", "pkg/util.go", "pkg", "image.bin", "long.txt", "gone.txt"} {
		refs = append(refs, fmt.Sprintf(`{"path":%q,"symbol":null}`, path))
	}
	document := fmt.Sprintf(`{"schema":"baum.review/1","review":{"repository":%q,"pull_request":7,"head":%q,"base":%q},"summary":{"text":"","context":{"area":{"text":"","refs":[%s]}}},"index":{"hunks":[{"id":"h0","path":"b.txt"}]}}`+"\n",
		linktest.Repository, head, f.Base, strings.Join(refs, ","))
	_, base := start(t, Options{Document: []byte(document), File: func(ctx context.Context, review Review, path string) (locallink.FileResult, error) {
		return locallink.ReadFile(ctx, locallink.FileRequest{Dir: f.Repo, Repository: review.Repository, Head: review.Head, Path: path})
	}})

	// A file the pull request does not change, read at the head although
	// the clone has main checked out; and one it does change.
	for path, want := range map[string]string{"a.txt": "a\n", "pkg/util.go": "pkg/util.go\n", "b.txt": "b\n"} {
		if resp, answer := fetchFile(t, base, path); resp.StatusCode != http.StatusOK || answer.Content == nil || *answer.Content != want || answer.Truncated {
			t.Errorf("%s: status %d, answer %+v", path, resp.StatusCode, answer)
		}
	}
	// A directory, a binary file and a path the head lacks have no text.
	for _, path := range []string{"pkg", "image.bin", "gone.txt"} {
		if resp, answer := fetchFile(t, base, path); resp.StatusCode != http.StatusOK || answer.Content != nil {
			t.Errorf("%s: status %d, answer %+v, want null content", path, resp.StatusCode, answer)
		}
	}
	// A long file is cut at the bound, on a character.
	resp, answer := fetchFile(t, base, "long.txt")
	if resp.StatusCode != http.StatusOK || answer.Content == nil {
		t.Fatalf("long.txt: status %d, answer %+v", resp.StatusCode, answer)
	}
	if text := *answer.Content; !answer.Truncated || len(text) > locallink.MaxFileBytes || len(text) < locallink.MaxFileBytes-1 || !strings.HasPrefix(long, text) {
		t.Errorf("long.txt: truncated %v, %d bytes", answer.Truncated, len(text))
	}
	if resp, _ := fetchFile(t, base, "c.txt"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("c.txt, which the document does not cite: status %d", resp.StatusCode)
	}
}
