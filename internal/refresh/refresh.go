// Package refresh updates the evidence of a saved local review without generation.
package refresh

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/review"
)

// Git reads only the reviewed commit range. Refresh never exports a checkout.
type Git interface {
	Commits(context.Context, string, string) ([]gitlocal.Commit, error)
}

type Refresher struct {
	GitHub   assemble.GitHub
	Git      Git
	Sessions assemble.Sessions
}

// Refresh returns a validated replacement document. Callers publish it with
// Store.RefreshCurrent so a newer generation or refresh cannot be overwritten.
func (r Refresher) Refresh(ctx context.Context, repository github.Repository, number int, saved store.Version, caps bundle.Caps, now time.Time) ([]byte, error) {
	validator, err := review.NewDocumentValidator()
	if err != nil {
		return nil, err
	}
	if findings := validator.ValidateStored(saved.Document, nil); len(findings) != 0 {
		return nil, fmt.Errorf("invalid stored review: %v", findings)
	}
	var stored struct {
		Review struct {
			Repository  string `json:"repository"`
			PullRequest int    `json:"pull_request"`
			Version     int    `json:"version"`
			Head        string `json:"head"`
			Base        string `json:"base"`
			MergeBase   string `json:"merge_base"`
		} `json:"review"`
		Index struct {
			Hunks []struct {
				ID       string `json:"id"`
				Path     string `json:"path"`
				Change   string `json:"change"`
				Role     string `json:"role"`
				NewRange []int  `json:"new_range"`
			} `json:"hunks"`
		} `json:"index"`
	}
	if err := json.Unmarshal(saved.Document, &stored); err != nil {
		return nil, err
	}
	if stored.Review.Head != saved.Metadata.Head || stored.Review.Base != saved.Metadata.Base || stored.Review.MergeBase != saved.Metadata.MergeBase || stored.Review.Version != saved.Metadata.Version || !strings.EqualFold(stored.Review.Repository, repository.Owner+"/"+repository.Name) || stored.Review.PullRequest != number {
		return nil, fmt.Errorf("stored review identity does not match metadata")
	}
	evidence, err := r.GitHub.Fetch(ctx, repository, number, github.FetchOptions{})
	if err != nil {
		return nil, err
	}
	var b bundle.Bundle
	evidence.Apply(&b)
	// Findings are located against the reviewed diff, not a newly fetched head.
	for _, h := range stored.Index.Hunks {
		hunk := bundle.Hunk{ID: h.ID, Path: h.Path, Change: h.Change, Role: h.Role}
		if len(h.NewRange) == 2 {
			hunk.StartLine, hunk.LineCount = h.NewRange[0], h.NewRange[1]
		}
		b.Diff = append(b.Diff, hunk)
	}
	b.PullRequest.MergeBaseSHA = stored.Review.MergeBase
	commits, err := r.Git.Commits(ctx, stored.Review.MergeBase, stored.Review.Head)
	if err != nil {
		return nil, fmt.Errorf("reading reviewed commits: %w", err)
	}
	if r.Sessions != nil {
		b.Sessions, err = r.Sessions.Evidence(ctx, commits, caps.MaxPromptsPerSession, caps.MaxCommandEventsPerSession)
		if err != nil {
			return nil, fmt.Errorf("reading local session evidence: %w", err)
		}
	}
	b.Sources["sessions"] = len(b.Sessions) > 0
	var paths []string
	for _, h := range stored.Index.Hunks {
		paths = append(paths, h.Path)
	}
	for _, commit := range commits {
		paths = append(paths, commit.Files...)
	}
	bundle.KeepKnownCommandTargets(b.Sessions, bundle.KnownPaths(paths...))
	redactor, err := bundle.NewRedactor()
	if err != nil {
		return nil, err
	}
	bundle.ApplyCaps(&b, caps)
	if err := bundle.Redact(ctx, &b, redactor); err != nil {
		return nil, err
	}
	if err := bundle.Fit(&b, caps); err != nil {
		return nil, err
	}
	document, err := validator.Refresh(saved.Document, &b, now)
	if err != nil {
		return nil, err
	}
	// Generation provenance is frozen; currency follows both fetched revisions.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(document, &doc); err != nil {
		return nil, err
	}
	var header map[string]json.RawMessage
	if err := json.Unmarshal(doc["review"], &header); err != nil {
		return nil, err
	}
	state := "outdated"
	if stored.Review.Head == evidence.PullRequest.HeadSHA && stored.Review.Base == evidence.PullRequest.BaseSHA {
		state = "current"
	}
	header["generation_state"], _ = json.Marshal(state)
	doc["review"], err = json.Marshal(header)
	if err != nil {
		return nil, err
	}
	document, err = json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if findings := validator.ValidateStored(document, nil); len(findings) != 0 {
		return nil, fmt.Errorf("invalid refreshed review: %v", findings)
	}
	return document, nil
}
