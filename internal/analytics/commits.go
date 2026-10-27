package analytics

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/sessions/entirecheckpoint"
)

// commitsTTL is how long one clone's commit list serves.
const commitsTTL = 5 * time.Minute

// trailerAgents maps a session's vendor to the agent its Co-Authored-By
// trailer names.
var trailerAgents = map[string]string{"claude_code": "claude", "codex": "codex", "cursor": "cursor"}

// branchCommit is one of the person's commits as the share needs it.
type branchCommit struct {
	OID string
	At  time.Time
	// Agents are those its Co-Authored-By trailers name.
	Agents []string
	// Checkpoint is an Entire-Checkpoint trailer: Entire recorded an agent
	// session for it, whose agent the commit does not say.
	Checkpoint bool
}

type cloneCommits struct {
	at      time.Time
	commits []branchCommit
	// reason is why the clone's commits could not be read, in words.
	reason string
}

// commitReader reads and caches each clone's commits of the last Retention.
type commitReader struct {
	now func() time.Time

	mu     sync.Mutex
	clones map[string]cloneCommits
}

// read returns the clone's commits by its configured author, or why there
// are none to count.
func (c *commitReader) read(ctx context.Context, clone repos.Clone) cloneCommits {
	c.mu.Lock()
	if cached, ok := c.clones[clone.ID]; ok && c.now().Sub(cached.at) < commitsTTL {
		c.mu.Unlock()
		return cached
	}
	c.mu.Unlock()
	result := cloneCommits{at: c.now()}
	repo, err := gitlocal.Open(ctx, clone.Path, gitlocal.Options{})
	if err != nil {
		result.reason = "baum could not read the clone of " + clone.Repository + " with git."
	} else {
		defer func() { _ = repo.Close() }()
		email, err := repo.UserEmail(ctx)
		switch {
		case err != nil:
			result.reason = "baum could not read the clone of " + clone.Repository + " with git."
		case email == "":
			result.reason = "The clone of " + clone.Repository + " has no git user.email, so baum cannot tell your commits."
		default:
			list, err := repo.AuthoredCommits(ctx, c.now().Add(-Retention), email)
			if err != nil {
				result.reason = "baum could not list the commits in the clone of " + clone.Repository + "."
			}
			for _, commit := range list {
				at, err := time.Parse(time.RFC3339, commit.CommittedAt)
				if err != nil {
					continue
				}
				ids, _ := entirecheckpoint.ParseMessage(commit.Message)
				result.commits = append(result.commits, branchCommit{
					OID: strings.ToLower(commit.OID), At: at.UTC(), Agents: locallink.CoauthoredAgents(commit.Trailers), Checkpoint: len(ids) > 0,
				})
			}
		}
	}
	if ctx.Err() != nil {
		return result
	}
	c.mu.Lock()
	if c.clones == nil {
		c.clones = map[string]cloneCommits{}
	}
	c.clones[clone.ID] = result
	c.mu.Unlock()
	return result
}

// CommitShare is the agent-written share of the person's commits.
type CommitShare struct {
	// Available is false when no clone in scope could be read; Reasons say
	// why, and also name clones left out of an available share.
	Available bool     `json:"available"`
	Reasons   []string `json:"reasons"`
	Commits   int      `json:"commits"`
	// Agent counts the commits linked to an agent; each commit once, under
	// its strongest evidence: a session that recorded creating it, then an
	// Entire checkpoint, then a co-author trailer.
	Agent      int               `json:"agent_commits"`
	BySession  int               `json:"by_session"`
	ByCheck    int               `json:"by_checkpoint"`
	ByTrailer  int               `json:"by_trailer"`
	Share      *float64          `json:"share"`
	Repository []RepositoryShare `json:"by_repository"`
}

// RepositoryShare is one repository's part of the share.
type RepositoryShare struct {
	Repository string   `json:"repository"`
	Commits    int      `json:"commits"`
	Agent      int      `json:"agent_commits"`
	Share      *float64 `json:"share"`
}

// share counts the commits of the clones in scope committed at or after
// start, each commit once per repository, against the sessions entries
// (which hold the commits a session recorded creating). With agent set, only
// that agent's sessions and trailers count toward the agent's commits; a
// checkpoint, which names no agent, then does not, but its commit stays in
// the denominator.
func share(commitsByClone map[string]cloneCommits, clones []repos.Clone, entries []Entry, start time.Time, agent string) CommitShare {
	out := CommitShare{Reasons: []string{}, Repository: []RepositoryShare{}}
	created := map[string][]string{} // repository → commit prefixes sessions created
	for _, e := range entries {
		if e.Generation || (agent != "" && e.Source != agent) {
			continue
		}
		created[e.Repository] = append(created[e.Repository], e.Commits...)
	}
	byRepository := map[string]*RepositoryShare{}
	var order []string
	seen := map[string]bool{}
	for _, clone := range clones {
		result, ok := commitsByClone[clone.ID]
		if !ok {
			continue
		}
		if result.reason != "" {
			out.Reasons = append(out.Reasons, result.reason)
			if result.commits == nil {
				continue
			}
		}
		out.Available = true
		repository := strings.ToLower(clone.Repository)
		row := byRepository[repository]
		if row == nil {
			row = &RepositoryShare{Repository: repository}
			byRepository[repository] = row
			order = append(order, repository)
		}
		for _, commit := range result.commits {
			key := repository + "@" + commit.OID
			if commit.At.Before(start) || seen[key] {
				continue
			}
			seen[key] = true
			out.Commits++
			row.Commits++
			switch {
			case createdBy(created[repository], commit.OID):
				out.BySession++
			case commit.Checkpoint && agent == "":
				out.ByCheck++
			case trailerNames(commit.Agents, agent):
				out.ByTrailer++
			default:
				continue
			}
			out.Agent++
			row.Agent++
		}
	}
	if out.Commits > 0 {
		out.Share = ratio(out.Agent, out.Commits)
	}
	for _, name := range order {
		row := *byRepository[name]
		if row.Commits > 0 {
			row.Share = ratio(row.Agent, row.Commits)
		}
		out.Repository = append(out.Repository, row)
	}
	return out
}

func createdBy(prefixes []string, oid string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(oid, p) {
			return true
		}
	}
	return false
}

// trailerNames reports whether the trailers name an agent: any, or the one
// the filter asks for.
func trailerNames(agents []string, filter string) bool {
	if filter == "" {
		return len(agents) > 0
	}
	want := trailerAgents[filter]
	for _, a := range agents {
		if a == want {
			return true
		}
	}
	return false
}

func ratio(n, d int) *float64 {
	if d == 0 {
		return nil
	}
	r := float64(n) / float64(d)
	return &r
}
