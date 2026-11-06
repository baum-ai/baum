package locallink

import (
	"context"
	"slices"
	"strings"

	"github.com/baum-ai/baum/internal/commitattribution"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/linkage"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/vendors"
)

// addObservedCommits supplies creation evidence independently of transcript
// completeness. Missing transcripts retain a metadata-only session and link.
func addObservedCommits(ctx context.Context, dir string, found []sessions.Session, commits []gitlocal.Commit) ([]sessions.Session, error) {
	repo, err := gitlocal.Open(ctx, dir, gitlocal.Options{})
	if err != nil {
		return nil, err
	}
	remotes, err := repo.RemoteURLs(ctx)
	_ = repo.Close()
	if err != nil {
		return nil, err
	}
	var repositories, shas []string
	for _, remote := range remotes {
		repository, err := github.RepositoryFromRemote(remote.URL)
		if err != nil {
			continue
		}
		name := strings.ToLower(repository.String())
		if !slices.Contains(repositories, name) {
			repositories = append(repositories, name)
		}
	}
	for _, c := range commits {
		shas = append(shas, c.OID)
	}
	observations, err := (&commitattribution.Store{}).Lookup(ctx, repositories, shas)
	if err != nil {
		return nil, err
	}
	for _, o := range observations {
		if o.Kind != commitattribution.Commit {
			continue
		}
		exists := false
		for i := range found {
			s := &found[i]
			if s.ID != o.SessionID {
				continue
			}
			exists = true
			if s.Vendor == o.Agent && !slices.Contains(s.CreatedCommits, o.CommitSHA) {
				s.CreatedCommits = append(s.CreatedCommits, o.CommitSHA)
			}
		}
		if !exists {
			found = append(found, sessions.Session{ID: o.SessionID, Source: vendors.Slug(o.Agent), Vendor: o.Agent, CreatedCommits: []string{o.CommitSHA}})
		}
	}
	return found, nil
}

// A process-owned hook outranks a transcript guess about who created a commit.
// Other sessions remain eligible as editors, with their file evidence intact.
func preferObservedCreation(sha string, candidates []linkage.RunCandidate, latest map[string]snapshot) []linkage.RunCandidate {
	observed := map[string]bool{}
	for id, s := range latest {
		if slices.Contains(s.session.CreatedCommits, sha) {
			observed[id] = true
		}
	}
	if len(observed) == 0 {
		return candidates
	}
	preferred := slices.Clone(candidates)
	for i := range preferred {
		if !observed[preferred[i].RunID] {
			preferred[i].CommitRefs = slices.DeleteFunc(slices.Clone(preferred[i].CommitRefs), func(ref linkage.CommitRef) bool { return ref.Role == linkage.RoleCreated })
		}
	}
	return preferred
}
