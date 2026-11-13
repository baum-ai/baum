package decisionlog

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/repos"
)

// LocalRelevancePatch opens only the picked repository's clone and reads one
// bounded PR diff. It neither scans sessions nor builds a review bundle.
func LocalRelevancePatch(registry *repos.Registry) func(context.Context, RelevanceContext) (string, error) {
	return func(ctx context.Context, p RelevanceContext) (string, error) {
		clones, err := registry.List(ctx)
		if err != nil {
			return "", err
		}
		for _, c := range clones {
			if !c.Available || !strings.EqualFold(c.Repository, p.Repository) {
				continue
			}
			git, err := gitlocal.Open(ctx, c.Path, gitlocal.Options{MaxPatchBytes: relevancePatchLimit, CommandTimeout: 20 * time.Second, FetchTimeout: 30 * time.Second})
			if err != nil {
				return "", err
			}
			defer func() { _ = git.Close() }()
			remotes, err := git.RemoteURLs(ctx)
			if err != nil {
				return "", err
			}
			remote := ""
			for _, candidate := range remotes {
				if gitlocal.SameRepository(candidate.URL, p.Repository) {
					remote = candidate.Name
					break
				}
			}
			if remote == "" {
				return "", errors.New("no remote names the PR repository")
			}
			pull, err := git.ResolvePull(ctx, gitlocal.PullRequest{Number: p.Number, BaseSHA: p.Base, HeadSHA: p.Head, BaseRef: p.BaseRef, Remote: remote})
			if err != nil {
				return "", err
			}
			patch, err := git.PullPatch(ctx, pull)
			if err != nil {
				return "", err
			}
			return string(patch), nil
		}
		return "", errors.New("no available clone for PR decision relevance")
	}
}

func (r *Relevance) patch(ctx context.Context, p RelevanceContext) (string, error) {
	revision := relevanceHash([]string{p.Base, p.Head})
	var patch string
	var failure string
	var retry int64
	err := r.Store.db.QueryRowContext(ctx, `SELECT patch, failure, retry_at FROM pr_relevance_patch WHERE repository=? AND number=? AND revision=?`, p.Repository, p.Number, revision).Scan(&patch, &failure, &retry)
	if err == nil {
		if failure == "oversized" {
			return "", errRelevanceContextLarge
		}
		if failure == "" {
			return patch, nil
		}
		if r.now().Before(time.UnixMilli(retry)) {
			return "", errors.New("PR diff read is backing off")
		}
	}
	if err != nil && !missingRelevanceContext(err) {
		return "", err
	}
	if r.Patch == nil {
		return "", errors.New("PR diff unavailable")
	}
	patch, err = r.Patch(ctx, p)
	failure, retry = "", 0
	if errors.Is(err, bundle.ErrOutputTooLarge) || len(patch) > relevancePatchLimit {
		err = errRelevanceContextLarge
	}
	if err == nil {
		patch, err = r.mask(ctx, patch)
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		failure, retry, patch = "unavailable", r.now().Add(relevanceRetry).UnixMilli(), ""
		if errors.Is(err, errRelevanceContextLarge) {
			failure = "oversized"
		}
	}
	_, saveErr := r.Store.db.ExecContext(ctx, `INSERT INTO pr_relevance_patch(repository, number, revision, patch, failure, retry_at) VALUES(?,?,?,?,?,?)
ON CONFLICT(repository, number) DO UPDATE SET revision=excluded.revision, patch=excluded.patch, failure=excluded.failure, retry_at=excluded.retry_at`, p.Repository, p.Number, revision, patch, failure, retry)
	if saveErr != nil {
		return "", saveErr
	}
	return patch, err
}
