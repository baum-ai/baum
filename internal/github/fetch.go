package github

import (
	"context"
	"errors"
	"fmt"

	graphql "github.com/cli/shurcooL-graphql"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/githubapi"
)

// The bundle sources a fetch records, one per part read from GitHub. A part
// is true when GitHub returned all of it, even when that is nothing.
// Review comments are read with the review threads, so they have no source
// of their own: when the threads fail, Unavailable says so under
// UnavailableReviewComments.
const (
	SourcePullRequest        = "pull_request"
	SourceRequestedReviewers = "requested_reviewers"
	SourceCommits            = "commits"
	SourceComments           = "comments"
	SourceReviews            = "reviews"
	SourceReviewThreads      = "review_threads"
	SourceChecks             = "checks"
	SourceRequiredChecks     = "required_checks"

	UnavailableReviewComments = "review_comments"
)

// ErrHeadMoved reports a pull request whose head changed while it was being
// read, every time it was read.
var ErrHeadMoved = errors.New("the pull request's head changed while it was being read from GitHub")

var errInvalidPagination = errors.New("github returned an invalid page cursor")

// fetchAttempts bounds how often Fetch starts over after the head moved.
const fetchAttempts = 3

// maxPages bounds one connection's pagination.
const maxPages = 1000

// FetchOptions adjust what Fetch flags.
type FetchOptions struct {
	// BotReportMarkers are the bot-report markers of the base branch's
	// REVIEW.md marker registry. The registry is not built yet, so callers
	// pass none.
	BotReportMarkers []string
}

// Evidence is the GitHub-sourced part of a generation bundle for one pull
// request at its current head.
type Evidence struct {
	// Host is the GitHub host the evidence was read from.
	Host string
	// Repository is "owner/name" as GitHub spells it.
	Repository   string
	PullRequest  bundle.PullRequest
	Checks       []bundle.Check
	Conversation []bundle.ConversationItem
	Threads      []bundle.Thread
	// Images are the images GitHub rendered from the description, for the
	// attachment step to download. Their FetchURL is signed: it is never
	// stored or logged.
	Images  []bundle.RenderedImage
	Sources bundle.Sources
	// Unavailable says why each part recorded false in Sources could not be
	// read.
	Unavailable map[string]error
}

// Apply copies the evidence into b, merging its sources into b's.
func (e Evidence) Apply(b *bundle.Bundle) {
	b.Repository = e.Repository
	b.PullRequest = e.PullRequest
	b.Checks, b.Conversation, b.Threads = e.Checks, e.Conversation, e.Threads
	if b.Sources == nil {
		b.Sources = bundle.Sources{}
	}
	for name, available := range e.Sources {
		b.Sources[name] = available
	}
}

// Fetch reads pull request number of repo: its header, commits, checks and
// statuses at the head with whether each is required, and its comments,
// reviews and review threads. A part GitHub refuses is left empty, recorded
// false in Sources and explained in Unavailable; a missing pull request, a
// rate limit the client does not wait out and cancellation fail the fetch.
// When the head moves while the parts are read, the fetch starts over, so
// every part describes the same head.
func (c *Client) Fetch(ctx context.Context, repo Repository, number int, opts FetchOptions) (Evidence, error) {
	if number <= 0 {
		return Evidence{}, fmt.Errorf("pull request number must be positive, got %d", number)
	}
	for attempt := 1; ; attempt++ {
		evidence, err := c.fetchOnce(ctx, repo, number, opts)
		if !errors.Is(err, ErrHeadMoved) || attempt == fetchAttempts {
			return evidence, err
		}
	}
}

// rawEvidence is what one fetch read, before mapping.
type rawEvidence struct {
	repository string
	header     pullRequestHeader
	reviewers  []reviewRequest
	commits    []commitNode
	comments   []issueComment
	reviews    []review
	threads    []reviewThread
	suites     []checkSuite
	statuses   []statusContext
	// required are the required status check contexts of the base branch;
	// nil when they could not be read.
	required map[string]bool
}

func (c *Client) fetchOnce(ctx context.Context, repo Repository, number int, opts FetchOptions) (Evidence, error) {
	raw, err := c.pullRequest(ctx, repo, number)
	if err != nil {
		return Evidence{}, err
	}
	f := partReader{client: c, repo: repo, number: number, head: raw.header.HeadRefOID}
	sources := bundle.Sources{SourcePullRequest: true}
	unavailable := map[string]error{}
	parts := []struct {
		name string
		read func() error
	}{
		{SourceRequestedReviewers, func() (err error) { raw.reviewers, err = f.reviewRequests(ctx); return err }},
		{SourceCommits, func() (err error) { raw.commits, err = f.commits(ctx); return err }},
		{SourceComments, func() (err error) { raw.comments, err = f.comments(ctx); return err }},
		{SourceReviews, func() (err error) { raw.reviews, err = f.reviews(ctx); return err }},
		{SourceReviewThreads, func() (err error) { raw.threads, err = f.threads(ctx); return err }},
		{SourceChecks, func() (err error) { raw.suites, raw.statuses, err = f.checks(ctx); return err }},
		{SourceRequiredChecks, func() (err error) { raw.required, err = f.requiredChecks(ctx, raw.header.BaseRefName); return err }},
	}
	for _, part := range parts {
		err := part.read()
		switch {
		case err == nil:
			sources[part.name] = true
		case fatal(err):
			return Evidence{}, err
		default:
			// Each reader returns nothing alongside an error, so a failed part
			// is empty, never a partial list.
			sources[part.name] = false
			unavailable[part.name] = err
		}
	}
	if threadsErr := unavailable[SourceReviewThreads]; threadsErr != nil {
		unavailable[UnavailableReviewComments] = fmt.Errorf("review comments are read with the review threads: %w", threadsErr)
	}
	evidence := mapEvidence(raw, sources, opts)
	evidence.Host, evidence.Unavailable = repo.Host, unavailable
	return evidence, nil
}

// fatal reports an error that fails the whole fetch rather than one part.
func fatal(err error) bool {
	var limited *RateLimitedError
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &limited) || errors.Is(err, ErrHeadMoved)
}

func (c *Client) pullRequest(ctx context.Context, repo Repository, number int) (rawEvidence, error) {
	var q pullRequestQuery
	variables := repoVariables(repo)
	variables["number"] = graphql.Int(number)
	err := c.query(ctx, repo.Host, "PullRequest", &q, variables)
	if err != nil {
		return rawEvidence{}, fmt.Errorf("reading pull request %s#%d: %w", repo, number, err)
	}
	if q.Repository == nil || q.Repository.PullRequest == nil {
		return rawEvidence{}, fmt.Errorf("%w: pull request %s#%d", ErrNotFound, repo, number)
	}
	return rawEvidence{repository: q.Repository.NameWithOwner, header: *q.Repository.PullRequest}, nil
}

// partReader reads the parts of one pull request at one head.
type partReader struct {
	client *Client
	repo   Repository
	number int
	head   string
}

func (f partReader) variables(cursor *string) map[string]any {
	variables := repoVariables(f.repo)
	variables["number"], variables["cursor"] = graphql.Int(f.number), (*graphql.String)(cursor)
	return variables
}

// repoVariables are the variables naming repo. The GraphQL library declares
// each variable's type from its Go type, so variables use its scalar types.
func repoVariables(repo Repository) map[string]any {
	return map[string]any{"owner": graphql.String(repo.Owner), "name": graphql.String(repo.Name)}
}

func (f partReader) notFound() error {
	return fmt.Errorf("%w: pull request %s#%d", ErrNotFound, f.repo, f.number)
}

func (f partReader) reviewRequests(ctx context.Context) ([]reviewRequest, error) {
	return paginate(nil, func(cursor *string) ([]reviewRequest, pageInfo, error) {
		var q reviewRequestsQuery
		if err := f.client.query(ctx, f.repo.Host, "PullRequestReviewRequests", &q, f.variables(cursor)); err != nil {
			return nil, pageInfo{}, err
		}
		pr := q.Repository.PullRequest
		if pr == nil {
			return nil, pageInfo{}, f.notFound()
		}
		if pr.HeadRefOID != f.head {
			return nil, pageInfo{}, ErrHeadMoved
		}
		return pr.ReviewRequests.Nodes, pr.ReviewRequests.PageInfo, nil
	})
}

func (f partReader) commits(ctx context.Context) ([]commitNode, error) {
	return paginate(nil, func(cursor *string) ([]commitNode, pageInfo, error) {
		var q commitsQuery
		if err := f.client.query(ctx, f.repo.Host, "PullRequestCommits", &q, f.variables(cursor)); err != nil {
			return nil, pageInfo{}, err
		}
		pr := q.Repository.PullRequest
		if pr == nil {
			return nil, pageInfo{}, f.notFound()
		}
		if pr.HeadRefOID != f.head {
			return nil, pageInfo{}, ErrHeadMoved
		}
		nodes := make([]commitNode, 0, len(pr.Commits.Nodes))
		for _, node := range pr.Commits.Nodes {
			nodes = append(nodes, node.Commit)
		}
		return nodes, pr.Commits.PageInfo, nil
	})
}

func (f partReader) comments(ctx context.Context) ([]issueComment, error) {
	return paginate(nil, func(cursor *string) ([]issueComment, pageInfo, error) {
		var q commentsQuery
		if err := f.client.query(ctx, f.repo.Host, "PullRequestComments", &q, f.variables(cursor)); err != nil {
			return nil, pageInfo{}, err
		}
		pr := q.Repository.PullRequest
		if pr == nil {
			return nil, pageInfo{}, f.notFound()
		}
		if pr.HeadRefOID != f.head {
			return nil, pageInfo{}, ErrHeadMoved
		}
		return pr.Comments.Nodes, pr.Comments.PageInfo, nil
	})
}

func (f partReader) reviews(ctx context.Context) ([]review, error) {
	return paginate(nil, func(cursor *string) ([]review, pageInfo, error) {
		var q reviewsQuery
		if err := f.client.query(ctx, f.repo.Host, "PullRequestReviews", &q, f.variables(cursor)); err != nil {
			return nil, pageInfo{}, err
		}
		pr := q.Repository.PullRequest
		if pr == nil {
			return nil, pageInfo{}, f.notFound()
		}
		if pr.HeadRefOID != f.head {
			return nil, pageInfo{}, ErrHeadMoved
		}
		return pr.Reviews.Nodes, pr.Reviews.PageInfo, nil
	})
}

// threads reads every review thread and, for a thread with more comments
// than its first page, the rest of its comments.
func (f partReader) threads(ctx context.Context) ([]reviewThread, error) {
	threads, err := paginate(nil, func(cursor *string) ([]reviewThread, pageInfo, error) {
		var q reviewThreadsQuery
		if err := f.client.query(ctx, f.repo.Host, "PullRequestReviewThreads", &q, f.variables(cursor)); err != nil {
			return nil, pageInfo{}, err
		}
		pr := q.Repository.PullRequest
		if pr == nil {
			return nil, pageInfo{}, f.notFound()
		}
		if pr.HeadRefOID != f.head {
			return nil, pageInfo{}, ErrHeadMoved
		}
		return pr.ReviewThreads.Nodes, pr.ReviewThreads.PageInfo, nil
	})
	if err != nil {
		return nil, err
	}
	for i := range threads {
		thread := &threads[i]
		if !thread.Comments.PageInfo.HasNextPage {
			continue
		}
		if thread.ID == "" {
			return nil, errors.New("github returned a review thread without its id")
		}
		rest, err := paginate(thread.Comments.PageInfo.EndCursor, func(cursor *string) ([]reviewComment, pageInfo, error) {
			var q threadCommentsQuery
			if err := f.client.query(ctx, f.repo.Host, "ReviewThreadComments", &q, map[string]any{"id": graphql.ID(thread.ID), "cursor": (*graphql.String)(cursor)}); err != nil {
				return nil, pageInfo{}, err
			}
			if q.Node == nil || q.Node.Thread.ID != thread.ID {
				return nil, pageInfo{}, fmt.Errorf("github did not return review thread %s", thread.ID)
			}
			return q.Node.Thread.Comments.Nodes, q.Node.Thread.Comments.PageInfo, nil
		})
		if err != nil {
			return nil, err
		}
		thread.Comments.Nodes = append(thread.Comments.Nodes, rest...)
		thread.Comments.PageInfo = pageInfo{}
	}
	return threads, nil
}

// checks reads the check suites, with all their runs, and the commit
// statuses of the head commit itself, so they describe the fetched head even
// after a later push.
func (f partReader) checks(ctx context.Context) ([]checkSuite, []statusContext, error) {
	var statuses []statusContext
	first := true
	suites, err := paginate(nil, func(cursor *string) ([]checkSuite, pageInfo, error) {
		var q headChecksQuery
		variables := repoVariables(f.repo)
		variables["oid"], variables["cursor"] = githubapi.GitObjectID(f.head), (*graphql.String)(cursor)
		if err := f.client.query(ctx, f.repo.Host, "HeadChecks", &q, variables); err != nil {
			return nil, pageInfo{}, err
		}
		if q.Repository.Object == nil || q.Repository.Object.Commit.OID != f.head {
			return nil, pageInfo{}, fmt.Errorf("github did not return head commit %s", f.head)
		}
		commit := q.Repository.Object.Commit
		if first && commit.Status != nil {
			statuses = commit.Status.Contexts
		}
		first = false
		return commit.CheckSuites.Nodes, commit.CheckSuites.PageInfo, nil
	})
	if err != nil {
		return nil, nil, err
	}
	for i := range suites {
		suite := &suites[i]
		if !suite.CheckRuns.PageInfo.HasNextPage {
			continue
		}
		if suite.ID == "" {
			return nil, nil, errors.New("github returned a check suite without its id")
		}
		rest, err := paginate(suite.CheckRuns.PageInfo.EndCursor, func(cursor *string) ([]checkRun, pageInfo, error) {
			var q suiteRunsQuery
			if err := f.client.query(ctx, f.repo.Host, "CheckSuiteRuns", &q, map[string]any{"id": graphql.ID(suite.ID), "cursor": (*graphql.String)(cursor)}); err != nil {
				return nil, pageInfo{}, err
			}
			if q.Node == nil || q.Node.CheckSuite.ID != suite.ID {
				return nil, pageInfo{}, fmt.Errorf("github did not return check suite %s", suite.ID)
			}
			return q.Node.CheckSuite.CheckRuns.Nodes, q.Node.CheckSuite.CheckRuns.PageInfo, nil
		})
		if err != nil {
			return nil, nil, err
		}
		suite.CheckRuns.Nodes = append(suite.CheckRuns.Nodes, rest...)
		suite.CheckRuns.PageInfo = pageInfo{}
	}
	return suites, statuses, nil
}

// requiredChecks reads the status check contexts the base branch's
// protection requires, from its classic protection rule and its rulesets.
// Protection that cannot be read in full is an error, so required stays
// unknown rather than reading as "not required".
func (f partReader) requiredChecks(ctx context.Context, baseRef string) (map[string]bool, error) {
	var q requiredChecksQuery
	variables := repoVariables(f.repo)
	variables["ref"] = graphql.String("refs/heads/" + baseRef)
	if err := f.client.query(ctx, f.repo.Host, "RequiredChecks", &q, variables); err != nil {
		return nil, err
	}
	ref := q.Repository.Ref
	if ref == nil {
		return nil, fmt.Errorf("base branch %s not found", baseRef)
	}
	if ref.Rules.PageInfo.HasNextPage {
		return nil, fmt.Errorf("base branch %s has more rules than one page", baseRef)
	}
	required := map[string]bool{}
	if ref.RefUpdateRule != nil {
		for _, context := range ref.RefUpdateRule.RequiredStatusCheckContexts {
			if context != nil && *context != "" {
				required[*context] = true
			}
		}
	}
	for _, rule := range ref.Rules.Nodes {
		if rule.Parameters == nil {
			continue
		}
		for _, check := range rule.Parameters.RequiredStatusChecks.RequiredStatusChecks {
			if check.Context != "" {
				required[check.Context] = true
			}
		}
	}
	return required, nil
}

// paginate reads a connection page by page from cursor (nil for the start)
// until GitHub reports no next page. It returns nothing on any error.
func paginate[T any](cursor *string, page func(cursor *string) ([]T, pageInfo, error)) ([]T, error) {
	var all []T
	for range maxPages {
		nodes, info, err := page(cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, nodes...)
		if !info.HasNextPage {
			return all, nil
		}
		if info.EndCursor == nil || (cursor != nil && *cursor == *info.EndCursor) {
			return nil, errInvalidPagination
		}
		cursor = info.EndCursor
	}
	return nil, fmt.Errorf("github returned more than %d pages", maxPages)
}
