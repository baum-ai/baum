// Package facts serves a pull request's facts (review.Facts) to the desktop
// app's PR page while the pull request has no saved review: its change size,
// checks, sessions, commits, conversation and the agent reviews posted on it,
// read the way generation reads them and with no model.
//
// Facts are built only when a page asks for them, never in the background,
// and are kept in memory only: they are never a version in the store. While
// the page keeps asking about the same head, GitHub is read again at most
// once per window, and never while the host's rate limit is paused.
package facts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/review"
)

const (
	// defaultWindow is the background refresh's default interval.
	defaultWindow = time.Minute
	// failureWindow is how long a failed build stands, shorter than the
	// window so that a retry soon after reads again.
	failureWindow = 10 * time.Second
	// buildTimeout bounds one build, which outlives the request that started
	// it so that a page reopened at once shares it.
	buildTimeout = 2 * time.Minute
	// maxEntries bounds the pull requests whose facts are kept.
	maxEntries = 32
)

// Built is one pull request's facts.
type Built struct {
	// Document is the encoded review.Facts.
	Document []byte
	// Head and Base are the commits the facts describe.
	Head, Base string
}

// Builder reads a pull request's facts from GitHub, its clone and the
// sessions on this machine.
type Builder func(ctx context.Context, clone repos.Clone, number int) (Built, error)

// Local builds facts with the sources generation assembles its bundle from:
// the GitHub client, the clone (fetching a pull request commit it lacks, as
// generation does) and the local session stores.
type Local struct {
	Client assemble.GitHub
	Config config.Config
}

// Build assembles the pull request's bundle in facts mode and derives its
// facts.
func (l Local) Build(ctx context.Context, clone repos.Clone, number int) (Built, error) {
	ctx, cancel := config.GitHubAuth.IdentityContext(ctx)
	defer cancel()
	repo, err := github.ParseRepository(clone.Repository)
	if err != nil {
		return Built{}, err
	}
	caps, err := l.Config.EffectiveCaps()
	if err != nil {
		return Built{}, err
	}
	git, err := gitlocal.Open(ctx, clone.Path, gitlocal.Options{})
	if err != nil {
		return Built{}, err
	}
	defer func() { _ = git.Close() }()
	assembler := &assemble.Assembler{GitHub: l.Client, Git: git, Fetcher: git, Sessions: assemble.LocalSessions{Dir: git.Dir(), Warn: io.Discard}, Warn: io.Discard}
	result, err := assembler.Assemble(ctx, assemble.Request{Repository: repo, Number: number, Limits: assemble.LimitsFrom(caps), Facts: true})
	if err != nil {
		return Built{}, err
	}
	encoded, err := json.Marshal(review.BuildFacts(&result.Bundle, time.Now()))
	if err != nil {
		return Built{}, err
	}
	return Built{Document: encoded, Head: result.Bundle.PullRequest.HeadSHA, Base: result.Bundle.PullRequest.BaseSHA}, nil
}

// Service builds facts on demand and keeps the latest of each pull request,
// per clone, repository and pull request; a new head or base replaces them.
type Service struct {
	Build Builder
	// Window is how long facts stand before a request about the same head
	// reads GitHub again; zero is the background refresh's default interval.
	// A failed build stands for ten seconds at most, a rate-limited one for
	// the whole window.
	Window time.Duration
	// Paused is when the host's GitHub rate limit lets reads resume, as the
	// background refresh keeps it; nil never pauses.
	Paused func(host string) time.Time
	// Limited records a rate limit a build hit, so the background refresh
	// waits it out too.
	Limited func(host string, until time.Time)
	Now     func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
	flight  singleflight.Group
}

type entry struct {
	// built is the latest facts, nil before the first build succeeds.
	built *Built
	// asked is the head the latest build was asked about, which GitHub may
	// not report yet.
	asked string
	// tried is when the latest build finished, and err its failure.
	tried time.Time
	err   error
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) window() time.Duration {
	if s.Window > 0 {
		return s.Window
	}
	return defaultWindow
}

// standing is how long the entry's latest build stands.
func (s *Service) standing(e *entry) time.Duration {
	if _, limited := errors.AsType[*github.RateLimitedError](e.err); e.err == nil || limited {
		return s.window()
	}
	return min(failureWindow, s.window())
}

func key(clone repos.Clone, number int) string {
	return fmt.Sprintf("%s\x00%s\x00%d", clone.ID, strings.ToLower(clone.Repository), number)
}

// covers says whether the entry answers for head: the head its facts
// describe, or the one its latest build was asked about. An empty head is
// any.
func (e *entry) covers(head string) bool {
	return head == "" || head == e.asked || (e.built != nil && head == e.built.Head)
}

// Facts answers the pull request's facts at head, the head the page shows (""
// for whichever is current). It builds them when it has none for that head,
// or when the latest build is older than the window; concurrent requests
// share one build whichever head they ask about, as a build reads the head
// GitHub reports. A paused rate limit answers the facts it has for that
// head, else a *github.RateLimitedError.
func (s *Service) Facts(ctx context.Context, clone repos.Clone, number int, head string) ([]byte, error) {
	k := key(clone, number)
	now := s.now()
	s.mu.Lock()
	current := s.entries[k]
	var held *entry
	if current != nil {
		copied := *current
		held = &copied
	}
	s.mu.Unlock()
	if held != nil && held.covers(head) && now.Before(held.tried.Add(s.standing(held))) {
		if held.built != nil {
			return held.built.Document, nil
		}
		return nil, held.err
	}
	repo, err := github.ParseRepository(clone.Repository)
	if err != nil {
		return nil, err
	}
	if s.Paused != nil {
		if resume := s.Paused(repo.Host); resume.After(now) {
			if held != nil && held.built != nil && (head == "" || head == held.built.Head) {
				return held.built.Document, nil
			}
			return nil, &github.RateLimitedError{ResumeAt: resume}
		}
	}
	result := s.flight.DoChan(k, func() (any, error) {
		buildCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), buildTimeout)
		defer cancel()
		built, err := s.Build(buildCtx, clone, number)
		if limited, ok := errors.AsType[*github.RateLimitedError](err); ok && s.Limited != nil {
			s.Limited(repo.Host, limited.ResumeAt)
		}
		s.record(k, head, built, err)
		if err != nil {
			return nil, err
		}
		return built.Document, nil
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case answer := <-result:
		if answer.Err == nil {
			return answer.Val.([]byte), nil
		}
		// The facts built before for this head still answer for it.
		s.mu.Lock()
		defer s.mu.Unlock()
		if e := s.entries[k]; e != nil && e.built != nil {
			return e.built.Document, nil
		}
		return nil, answer.Err
	}
}

// record keeps a build's outcome. A failure keeps the facts built before for
// the same head, which still answer for it.
func (s *Service) record(k, head string, built Built, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = map[string]*entry{}
	}
	next := &entry{asked: head, tried: s.now(), err: err}
	if err == nil {
		next.built = &built
	} else if previous := s.entries[k]; previous != nil && previous.built != nil && (head == "" || head == previous.built.Head) {
		next.built = previous.built
	}
	s.entries[k] = next
	for len(s.entries) > maxEntries {
		oldest := ""
		for candidate, e := range s.entries {
			if oldest == "" || e.tried.Before(s.entries[oldest].tried) {
				oldest = candidate
			}
		}
		delete(s.entries, oldest)
	}
}

// Cached is the latest facts built for the pull request and the head they
// describe, nil when there are none. It never reads GitHub: a page's patches
// and sessions are read against the facts it was shown.
func (s *Service) Cached(clone repos.Clone, number int) ([]byte, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.entries[key(clone, number)]; e != nil && e.built != nil {
		return e.built.Document, e.built.Head
	}
	return nil, ""
}
