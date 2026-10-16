package background

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/refresh"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

type Local struct {
	Client assemble.GitHub
	Config config.Config
}

func (l Local) Refresh(ctx context.Context, clone repos.Clone, number int) (discovery.Observation, error) {
	repo, err := github.ParseRepository(clone.Repository)
	if err != nil {
		return discovery.Observation{}, err
	}
	versions, err := store.Open(clone.CommonDir, "", false)
	if err != nil {
		return discovery.Observation{}, err
	}
	key := store.Key{Repository: repo.String(), Number: number}
	saved, err := versions.Latest(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		evidence, err := l.Client.Fetch(ctx, repo, number, github.FetchOptions{MetadataOnly: true})
		if err != nil {
			return discovery.Observation{}, err
		}
		return observe(evidence), nil
	}
	if err != nil {
		return discovery.Observation{}, err
	}
	git, err := gitlocal.Open(ctx, clone.Path, gitlocal.Options{})
	if err != nil {
		return discovery.Observation{}, err
	}
	defer func() { _ = git.Close() }()
	caps, err := l.Config.EffectiveCaps()
	if err != nil {
		return discovery.Observation{}, err
	}
	capture := &capturingClient{client: l.Client}
	updater := refresh.Refresher{GitHub: capture, Git: git, Sessions: assemble.LocalSessions{Dir: git.Dir(), Warn: io.Discard}}
	document, err := updater.Refresh(ctx, repo, number, saved, assemble.LimitsFrom(caps).Caps, time.Now().UTC())
	// Remote metadata is useful even when reviewed git objects are no longer
	// present or a local session store cannot be read. Never discard it because
	// the document refresh failed; surface the latter separately to the host.
	observation := observe(capture.evidence)
	moved := observation.HeadSHA != saved.Metadata.Head
	if moved {
		observation.Checks = "unknown"
	}
	if err != nil {
		return observation, err
	}
	// Every fallible network read happens before publication, so a failed
	// refresh (including a rate-limited one the worker retries) never commits.
	if moved {
		current, err := l.Client.Fetch(ctx, repo, number, github.FetchOptions{MetadataOnly: true})
		if err != nil {
			return observation, err
		}
		observation = observe(current)
	}
	if err := versions.RefreshCurrent(ctx, key, saved, document); err != nil {
		return observation, err
	}
	return observation, nil
}

type capturingClient struct {
	client   assemble.GitHub
	evidence github.Evidence
}

func (c *capturingClient) Fetch(ctx context.Context, repo github.Repository, number int, options github.FetchOptions) (github.Evidence, error) {
	evidence, err := c.client.Fetch(ctx, repo, number, options)
	c.evidence = evidence
	return evidence, err
}
func observe(evidence github.Evidence) discovery.Observation {
	pr := evidence.PullRequest
	state := strings.ToUpper(pr.State)
	if pr.MergedSHA != "" {
		state = "MERGED"
	}
	checks := "unknown"
	if evidence.Sources[github.SourceChecks] {
		checks = "none"
		if len(evidence.Checks) > 0 {
			checks = "success"
		}
		for _, check := range evidence.Checks {
			switch check.Conclusion {
			case "success", "neutral", "skipped":
			case "", "pending":
				if checks != "failure" {
					checks = "pending"
				}
			default:
				checks = "failure"
			}
		}
	}
	return discovery.Observation{Number: pr.Number, HeadSHA: pr.HeadSHA, Title: pr.Title, State: state, Draft: pr.Draft, FetchedAt: time.Now().UTC(), Checks: checks}
}
