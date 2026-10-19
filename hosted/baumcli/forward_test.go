//go:build !windows

package baumcli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/uiserver"
)

// forwardWorld is a repository with two local Codex sessions, one of which
// carries a secret, and a fake hosted server that records what it receives.
type forwardWorld struct {
	repo   string
	server *httptest.Server
	mu     sync.Mutex
	// requests counts every request; uploads holds each uploaded archive.
	requests int
	uploads  [][]byte
}

func newForwardWorld(t *testing.T) *forwardWorld {
	t.Helper()
	setTempConfigHome(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	w := &forwardWorld{repo: filepath.Join(t.TempDir(), "demo")}
	runGitTest(t, w.repo, "init")
	runGitTest(t, w.repo, "remote", "add", "origin", "https://github.com/acme/demo.git")
	rollout := filepath.Join(home, ".codex", "sessions", "2026", "10", "19", "rollout-ended.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"timestamp":"2026-10-19T12:00:00Z","type":"session_meta","payload":{"id":"codex-ended","session_id":"codex-ended","cwd":` + quoteJSON(w.repo) + `,"source":"cli"}}` + "\n" +
		`{"timestamp":"2026-10-19T12:00:05Z","type":"event_msg","payload":{"type":"user_message","message":"deploy with ` + fakeGitHubToken + `"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	writeCodexRollout(t, home, "other", w.repo)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /github/repositories/find", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, http.StatusOK, map[string]any{"id": "repo-1", "full_name": "acme/demo", "enabled": true})
	})
	mux.HandleFunc("POST /github/repositories/repo-1/trace-uploads", func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.mu.Lock()
		w.uploads = append(w.uploads, body)
		w.mu.Unlock()
		rw.WriteHeader(http.StatusAccepted)
	})
	w.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.mu.Lock()
		w.requests++
		w.mu.Unlock()
		mux.ServeHTTP(rw, r)
	}))
	t.Cleanup(w.server.Close)
	t.Setenv(serverEnvVar, w.server.URL)
	return w
}

func (w *forwardWorld) logIn(t *testing.T) {
	t.Helper()
	if err := saveCredentials(credentials{ServerURL: w.server.URL, Token: "rcnt_forward"}); err != nil {
		t.Fatal(err)
	}
}

func (w *forwardWorld) received() (int, [][]byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.requests, append([][]byte(nil), w.uploads...)
}

func (w *forwardWorld) sessionEnd(id string) hookevents.Event {
	event := hookevents.New(hookevents.KindAgent, "codex", "session-end", w.repo, "v0.2.0")
	event.Session = &hookevents.Session{ID: id, Cwd: w.repo}
	return event
}

func TestForwarderSendsNothingWithoutALogin(t *testing.T) {
	w := newForwardWorld(t)
	var log bytes.Buffer
	forwarder := &SessionForwarder{Log: slog.New(slog.NewTextHandler(&log, nil))}
	forwarder.forward(context.Background(), w.sessionEnd("codex-ended"))
	forwarder.forward(context.Background(), w.sessionEnd("codex-other"))
	if requests, _ := w.received(); requests != 0 {
		t.Errorf("%d requests reached the baum server without a login", requests)
	}
	if n := strings.Count(log.String(), "hook events stay on this machine"); n != 1 {
		t.Errorf("logged the missing login %d times:\n%s", n, log.String())
	}
}

// Forwarding is `baum upload` of the ended session alone: the same endpoint
// and archive, with the secret it held redacted on this machine.
func TestForwarderUploadsTheEndedSessionRedacted(t *testing.T) {
	w := newForwardWorld(t)
	w.logIn(t)
	forwarder := &SessionForwarder{}
	// Only a session end is forwarded.
	stop := w.sessionEnd("codex-ended")
	stop.Name = "stop"
	forwarder.forward(context.Background(), stop)
	if requests, _ := w.received(); requests != 0 {
		t.Fatalf("a stop event was forwarded")
	}
	forwarder.forward(context.Background(), w.sessionEnd("codex-ended"))
	_, uploads := w.received()
	if len(uploads) != 1 {
		t.Fatalf("%d uploads, want 1", len(uploads))
	}
	if bytes.Contains(uploads[0], []byte(fakeGitHubToken)) {
		t.Error("the forwarded archive holds the secret unredacted")
	}
	manifest := uploadedManifest(t, uploads[0])
	if len(manifest.Sessions) != 1 || manifest.Sessions[0].SessionID != "codex-ended" || manifest.Repository.ID != "repo-1" {
		t.Errorf("manifest = %+v", manifest)
	}
	if manifest.Redaction.FindingsByType == nil || sumFindings(manifest.Redaction.FindingsByType) == 0 {
		t.Errorf("redaction found nothing: %+v", manifest.Redaction)
	}
	if len(manifest.RepositoryRoots) != 0 {
		t.Errorf("a one-session upload claimed the repository's roots: %+v", manifest.RepositoryRoots)
	}
}

func sumFindings(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

// runForwardingDaemon runs the daemon in this process the way the command
// line builds it: the hook event service, and the forwarder subscribed to
// session ends.
func runForwardingDaemon(t *testing.T, paths daemon.Paths) *hookevents.Service {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ready := make(chan *hookevents.Service, 1)
	go func() {
		defer close(done)
		err := daemon.Run(ctx, daemon.Options{Paths: paths, Version: version(), Start: func(context.Context, *uiserver.Server) (daemon.App, error) {
			events, err := hookevents.Open(paths, nil)
			if err != nil {
				return daemon.App{}, err
			}
			forwarded, unsubscribe := events.Store.Subscribe(16, Forwarded)
			forwarder := &SessionForwarder{}
			ready <- events
			return daemon.App{Methods: events.Methods(), Run: func(ctx context.Context) {
				var wg sync.WaitGroup
				wg.Go(func() { events.Run(ctx) })
				wg.Go(func() { forwarder.Run(ctx, forwarded) })
				wg.Wait()
			}, Close: func() { unsubscribe(); _ = events.Close() }}, nil
		}})
		if err != nil && !errors.Is(err, daemon.ErrRunning) {
			t.Error(err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	events := <-ready
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := (&daemon.Client{Paths: paths}).Hello(context.Background()); err == nil {
			return events
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// From the agent's hook to the hosted server: the hook hands the session end
// to the daemon, which records it, and forwards it only for a logged-in user.
func TestHookEventsReachTheHostedServerOnlyWhenLoggedIn(t *testing.T) {
	for _, loggedIn := range []bool{false, true} {
		name := "logged out"
		if loggedIn {
			name = "logged in"
		}
		t.Run(name, func(t *testing.T) {
			w := newForwardWorld(t)
			if loggedIn {
				w.logIn(t)
			}
			paths := useTestHookDaemon(t)
			events := runForwardingDaemon(t, paths)
			stubEntireBinary(t, "#!/bin/sh\ncat >/dev/null\n")
			t.Chdir(w.repo)
			payload := `{"session_id":"codex-ended","cwd":` + quoteJSON(w.repo) + `,"hook_event_name":"SessionEnd"}`
			start := time.Now()
			if _, _, err := runCLIWithInput(t, payload, "hooks", "agent", "codex", "session-end"); err != nil {
				t.Fatal(err)
			}
			// Forwarding happens in the daemon, after the hook returned.
			if elapsed := time.Since(start); elapsed > hookevents.Budget {
				t.Errorf("the hook took %s", elapsed)
			}
			records, err := events.Store.Recent(0)
			if err != nil || len(records) != 1 || records[0].Via != hookevents.ViaSocket || records[0].Event.Session.ID != "codex-ended" {
				t.Fatalf("records = %+v, %v", records, err)
			}
			if !loggedIn {
				time.Sleep(200 * time.Millisecond)
				if requests, _ := w.received(); requests != 0 {
					t.Errorf("%d requests reached the baum server without a login", requests)
				}
				return
			}
			deadline := time.Now().Add(20 * time.Second)
			for {
				_, uploads := w.received()
				if len(uploads) == 1 {
					if bytes.Contains(uploads[0], []byte(fakeGitHubToken)) {
						t.Error("the forwarded archive holds the secret unredacted")
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatal("the ended session was not forwarded")
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}
