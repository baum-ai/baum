package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockedBuffer is written by the serving goroutine while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// stubViewSeams replaces the browser opener and the signal context, and returns
// the URLs the command tried to open and the function that stops the server.
func stubViewSeams(t *testing.T) (opened *[]string, stop context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var urls []string
	oldOpen, oldContext := openBrowser, viewContext
	openBrowser = func(url string) error {
		urls = append(urls, url)
		return nil
	}
	viewContext = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	t.Cleanup(func() {
		cancel()
		openBrowser, viewContext = oldOpen, oldContext
	})
	return &urls, cancel
}

func TestViewServesSampleAndStopsOnCancel(t *testing.T) {
	opened, stop := stubViewSeams(t)
	var out, errOut lockedBuffer
	finished := make(chan error, 1)
	go func() {
		finished <- Run([]string{"view", "--sample"}, strings.NewReader(""), &out, &errOut)
	}()

	var url string
	deadline := time.Now().Add(5 * time.Second)
	for url == "" {
		url = strings.TrimSpace(out.String())
		if time.Now().After(deadline) {
			t.Fatalf("no URL printed; stderr %q", errOut.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("URL = %q", url)
	}
	resp, err := http.Get(url + "api/document")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "baum.review/1") {
		t.Errorf("document = %.80q", body)
	}

	stop()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("view: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("view did not stop")
	}
	if len(*opened) != 1 || (*opened)[0] != url {
		t.Errorf("opened %v, want [%s]", *opened, url)
	}
}

func TestViewNoOpenPrintsURLWithoutOpening(t *testing.T) {
	opened, stop := stubViewSeams(t)
	stop() // the server stops as soon as it starts
	stdout, _, err := run(t, "view", "--sample", "--no-open")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if !strings.HasPrefix(stdout, "http://127.0.0.1:") {
		t.Errorf("stdout = %q", stdout)
	}
	if len(*opened) != 0 {
		t.Errorf("opened %v with --no-open", *opened)
	}
}

func TestViewBrowserFailureIsOnlyAWarning(t *testing.T) {
	_, stop := stubViewSeams(t)
	stop()
	openBrowser = func(string) error { return errors.New("no display") }
	_, stderr, err := run(t, "view", "--sample")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if !strings.Contains(stderr, "could not open a browser: no display") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestViewPRNeedsTheVersionStore(t *testing.T) {
	stubViewSeams(t)
	for _, args := range [][]string{{"view", "--pr", "1"}, {"view", "--pr", "0"}} {
		stdout, _, err := run(t, args...)
		if err == nil || !strings.Contains(err.Error(), "needs the local version store, which is not in this build") {
			t.Errorf("%v: err = %v", args, err)
		}
		if stdout != "" {
			t.Errorf("%v: stdout = %q", args, stdout)
		}
	}
}

func TestViewArgumentsAreExclusive(t *testing.T) {
	stubViewSeams(t)
	path := filepath.Join(t.TempDir(), "review.json")
	writeFile(t, path, "{}")
	for _, args := range [][]string{
		{"view"},
		{"view", path, "--sample"},
		{"view", path, "--pr", "1"},
		{"view", "--sample", "--pr", "1"},
		{"view", path, path},
		{"view", "--sample", "--watch"},
		{"view", "--sample", "--port", "70000"},
	} {
		if _, _, err := run(t, args...); err == nil {
			t.Errorf("%v succeeded", args)
		}
	}
}

func TestViewRejectsBadDocuments(t *testing.T) {
	stubViewSeams(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	writeFile(t, bad, "not json")
	for _, path := range []string{bad, filepath.Join(dir, "missing.json")} {
		if _, _, err := run(t, "view", path, "--no-open"); err == nil {
			t.Errorf("view %s succeeded", path)
		}
	}
}

func TestViewServesInvalidDocumentWithWarnings(t *testing.T) {
	_, stop := stubViewSeams(t)
	stop()
	path := filepath.Join(t.TempDir(), "review.json")
	writeFile(t, path, "{}")
	_, stderr, err := run(t, "view", path, "--no-open")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	_, validateErr, _ := run(t, "validate", path)
	for _, line := range strings.Split(strings.TrimSpace(validateErr), "\n") {
		if !strings.Contains(stderr, line) {
			t.Errorf("view did not print the finding %q; stderr %q", line, stderr)
		}
	}
}

func TestViewPortInUse(t *testing.T) {
	stubViewSeams(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)

	_, _, err = run(t, "view", "--sample", "--no-open", "--port", port)
	if err == nil || !strings.Contains(err.Error(), "port "+port+" is already in use") {
		t.Fatalf("err = %v", err)
	}
}
