package baumcli

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/baum-ai/baum/hosted/agenthooks"
	"github.com/baum-ai/baum/hosted/apiclient"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/sessions/collector"
)

// forwardTimeout bounds forwarding one session: finding, redacting and
// uploading it.
const forwardTimeout = 10 * time.Minute

// SessionForwarder runs in the daemon and forwards hook events to the hosted
// service the user is logged in to. The service has one way to receive
// sessions, the upload `baum upload` makes, so forwarding is that upload,
// unchanged, of the session an agent hook reports ended: the same endpoint,
// archive, local redaction and size bound, the same refusal of a repository
// not enabled in baum, and, as there, one attempt whose failure is reported
// (logged) rather than retried. Nothing is sent without a login: a token from
// BAUM_TOKEN in the daemon's environment or stored by `baum login`, sent only
// to the server it was issued for. Every event stays in the daemon's local
// record either way.
type SessionForwarder struct {
	Log *slog.Logger
	// loggedOut is set once the forwarder has logged that it sends nothing
	// without a login, so the log says it once rather than per event.
	loggedOut bool
}

// Run forwards the records it receives until ctx ends or records closes, one
// at a time.
func (f *SessionForwarder) Run(ctx context.Context, records <-chan hookevents.Record) {
	for {
		select {
		case <-ctx.Done():
			return
		case record, ok := <-records:
			if !ok {
				return
			}
			f.forward(ctx, record.Event)
		}
	}
}

// Forwarded reports whether the forwarder acts on an event: an agent hook's
// session end that names its session.
func Forwarded(event hookevents.Event) bool {
	return event.Kind == hookevents.KindAgent && event.Name == agenthooks.SessionEndEvent &&
		event.Session != nil && (event.Session.ID != "" || event.Session.TranscriptPath != "")
}

func (f *SessionForwarder) log() *slog.Logger {
	if f.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return f.Log
}

func (f *SessionForwarder) forward(ctx context.Context, event hookevents.Event) {
	if !Forwarded(event) {
		return
	}
	client, _, err := authenticatedClient("")
	if err != nil {
		if !f.loggedOut {
			f.loggedOut = true
			f.log().Info("not logged in to a baum server; hook events stay on this machine", "reason", err.Error())
		}
		return
	}
	f.loggedOut = false
	ctx, cancel := context.WithTimeout(ctx, forwardTimeout)
	defer cancel()
	log := f.log().With("agent", event.Agent, "event", event.ID)
	dir := event.Session.Cwd
	if dir == "" {
		dir = event.Dir
	}
	root, err := collector.RepoRoot(dir)
	if err != nil {
		log.Info("ended session not forwarded: not in a Git repository")
		return
	}
	fullName, err := originRepository(ctx, root)
	if err != nil {
		log.Info("ended session not forwarded", "reason", err.Error())
		return
	}
	log = log.With("repository", fullName)
	repository, err := client.FindGitHubRepository(ctx, fullName)
	if errors.Is(err, apiclient.ErrUnauthenticated) {
		log.Warn("ended session not forwarded: the baum token is expired or revoked; run `baum login`")
		return
	}
	if err != nil {
		log.Warn("ended session not forwarded", "reason", err.Error())
		return
	}
	if !repository.Enabled {
		log.Info("ended session not forwarded: the repository is not enabled in baum")
		return
	}
	sessions, _, err := collector.FindSessions(root, collector.FindOptions{ExcludeNestedClones: true})
	if err != nil {
		log.Warn("ended session not forwarded", "reason", err.Error())
		return
	}
	ended := endedSessions(sessions, event.Session)
	if len(ended) == 0 {
		log.Info("ended session not forwarded: it was not found among the repository's local sessions")
		return
	}
	// Without a root inventory: an inventory states the repository's
	// checkouts, which one session's roots do not.
	if err = postRepositoryTraces(ctx, client, repository, ended, nil, io.Discard, io.Discard); err != nil {
		log.Warn("ended session not forwarded", "reason", err.Error())
		return
	}
	log.Info("forwarded the ended session", "sessions", len(ended))
}

// endedSessions picks the sessions an event names, by id or transcript.
func endedSessions(sessions []collector.Session, ended *hookevents.Session) []collector.Session {
	var matched []collector.Session
	for _, session := range sessions {
		if (ended.ID != "" && session.SessionID == ended.ID) ||
			(ended.TranscriptPath != "" && filepath.Clean(session.Path) == filepath.Clean(ended.TranscriptPath)) {
			matched = append(matched, session)
		}
	}
	return matched
}
