package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

const inboxQuery = `query LocalInbox($owner: String!, $name: String!, $cursor: String) {
 viewer { login }
 rateLimit { cost remaining resetAt }
 repository(owner: $owner, name: $name) {
  viewerPermission
  pullRequests(states: OPEN, first: 50, after: $cursor) {
   pageInfo { hasNextPage endCursor }
   nodes {
    number title headRefOid headRefName isDraft updatedAt author { __typename login }
    headRepository { databaseId nameWithOwner } baseRepository { databaseId nameWithOwner }
    reviewRequests(first: 100) { nodes { requestedReviewer { __typename ... on User { login } ... on Team { databaseId } } } pageInfo { hasNextPage } }
    reviewDecision latestOpinionatedReviews(first: 100) { nodes { state } pageInfo { hasNextPage } }
    latestReviews: reviews(last: 100) { nodes { state } pageInfo { hasPreviousPage } }
    commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
    mergeable mergeStateStatus
    reviewThreads(first: 100) { nodes { isResolved } pageInfo { hasNextPage } }
   }
  }
 }
}`

type pageInfo struct {
	HasNextPage bool
	EndCursor   string
}
type gqlRepository struct {
	DatabaseID    int64
	NameWithOwner string
}
type gqlPR struct {
	Number                         int
	Title, HeadRefOid, HeadRefName string
	IsDraft                        bool
	// UpdatedAt feeds the probe's fingerprint and the snapshot; it is left
	// out of the unchanged comparison so the backoff is unaffected.
	UpdatedAt      time.Time `json:",omitzero"`
	Author         actor
	HeadRepository *gqlRepository
	BaseRepository gqlRepository
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer struct {
				Typename   string `json:"__typename"`
				Login      string
				DatabaseID int64
			}
		}
		PageInfo pageInfo
	}
	LatestReviews *struct {
		Nodes    []struct{ State string }
		PageInfo struct{ HasPreviousPage bool }
	}
	ReviewDecision           *string
	LatestOpinionatedReviews *struct {
		Nodes    []struct{ State string }
		PageInfo pageInfo
	}
	Commits *struct {
		Nodes []struct {
			Commit struct{ StatusCheckRollup *struct{ State string } }
		}
	}
	Mergeable, MergeStateStatus string
	ReviewThreads               *struct {
		Nodes    []struct{ IsResolved bool }
		PageInfo pageInfo
	}
}

func (pr gqlPR) legacy() apiPR {
	var out apiPR
	out.Number, out.Title, out.Draft, out.User = pr.Number, pr.Title, pr.IsDraft, pr.Author
	out.Head.SHA, out.Head.Ref = pr.HeadRefOid, pr.HeadRefName
	if pr.HeadRepository != nil {
		out.Head.Repo = &struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
		}{pr.HeadRepository.DatabaseID, pr.HeadRepository.NameWithOwner}
	}
	out.Base.Repo.ID, out.Base.Repo.FullName = pr.BaseRepository.DatabaseID, pr.BaseRepository.NameWithOwner
	for _, node := range pr.ReviewRequests.Nodes {
		reviewer := node.RequestedReviewer
		if reviewer.Typename == "User" {
			out.RequestedReviewers = append(out.RequestedReviewers, actor{Login: reviewer.Login})
		}
		if reviewer.Typename == "Team" {
			out.RequestedTeams = append(out.RequestedTeams, team{ID: reviewer.DatabaseID})
		}
	}
	return out
}

// behind reports a head branch out of date with its base that Update branch
// can fix: GitHub says BEHIND, and it is neither a draft nor conflicting.
func (pr gqlPR) behind() bool {
	return !pr.IsDraft && pr.Mergeable != "CONFLICTING" && pr.MergeStateStatus == "BEHIND"
}

func (pr gqlPR) states() (review, checks, merge string, unresolved *int, capped bool) {
	if pr.ReviewDecision != nil {
		switch *pr.ReviewDecision {
		case "APPROVED":
			review = "approved"
		case "CHANGES_REQUESTED":
			review = "changes_requested"
		case "REVIEW_REQUIRED":
			review = "unreviewed"
		}
	}
	if pr.ReviewDecision == nil || review == "unreviewed" {
		if pr.LatestOpinionatedReviews != nil && !pr.LatestOpinionatedReviews.PageInfo.HasNextPage {
			fallback := "unreviewed"
			for _, node := range pr.LatestOpinionatedReviews.Nodes {
				switch node.State {
				case "CHANGES_REQUESTED":
					fallback = "changes_requested"
				case "APPROVED":
					if fallback != "changes_requested" {
						fallback = "approved"
					}
				case "COMMENTED":
					if fallback == "unreviewed" {
						fallback = "commented"
					}
				}
			}
			if review == "" || fallback == "commented" {
				review = fallback
			}
		}
	}
	if review == "unreviewed" && pr.LatestReviews != nil {
		for _, node := range pr.LatestReviews.Nodes {
			if node.State == "COMMENTED" {
				review = "commented"
				break
			}
		}
	}
	if pr.Commits != nil && len(pr.Commits.Nodes) > 0 {
		rollup := pr.Commits.Nodes[0].Commit.StatusCheckRollup
		if rollup == nil {
			checks = "none"
		} else {
			switch rollup.State {
			case "SUCCESS":
				checks = "passing"
			case "FAILURE", "ERROR":
				checks = "failing"
			case "PENDING", "EXPECTED":
				checks = "running"
			}
		}
	}
	switch {
	case pr.IsDraft:
		merge = "draft"
	case pr.Mergeable == "CONFLICTING" || pr.MergeStateStatus == "DIRTY":
		merge = "conflict"
	case pr.MergeStateStatus == "CLEAN" || pr.MergeStateStatus == "HAS_HOOKS":
		merge = "ready"
	case pr.MergeStateStatus == "BLOCKED" || pr.MergeStateStatus == "BEHIND" || pr.MergeStateStatus == "UNSTABLE":
		merge = "blocked"
	}
	if pr.ReviewThreads != nil {
		count := 0
		for _, node := range pr.ReviewThreads.Nodes {
			if !node.IsResolved {
				count++
			}
		}
		unresolved = &count
		capped = pr.ReviewThreads.PageInfo.HasNextPage
	}
	return
}

// openPulls reads the open pull requests, the viewer and whether the viewer
// may push to the repository (nil when GitHub did not say).
func (p *poll) openPulls(ctx context.Context, repository github.Repository) ([]gqlPR, actor, *bool, error) {
	result, viewer, permission, err := p.openPullPages(ctx, repository)
	if err != nil {
		return nil, viewer, nil, err
	}
	var push *bool
	switch permission {
	case "ADMIN", "MAINTAIN", "WRITE":
		push = new(true)
	case "READ", "TRIAGE":
		push = new(false)
	}
	return result, viewer, push, nil
}

type graphQLError struct {
	Type string
	Path []any
}

// graphQL sends one query on the shared GraphQL budget: it refuses while the
// budget is exhausted, records what the response says of the budget and turns
// a rate-limited response into errRateLimit. The caller judges the data and
// errors it returns.
func (p *poll) graphQL(ctx context.Context, query string, variables map[string]any) (json.RawMessage, []graphQLError, error) {
	if rateDeadline(p.graphRate).After(p.now) {
		return nil, nil, errRateLimit
	}
	status, headers, body, err := p.reader.GraphQL(ctx, query, variables)
	if err != nil {
		return nil, nil, err
	}
	// GraphQL response headers describe its point budget, never the REST budget.
	if n, e := strconv.Atoi(headers.Get("X-RateLimit-Remaining")); e == nil {
		p.graphRate.Remaining = &n
	}
	if n, e := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); e == nil {
		p.graphRate.Reset = time.Unix(n, 0)
	}
	var response struct {
		Data   json.RawMessage
		Errors []graphQLError
	}
	parseErr := json.Unmarshal(body, &response)
	var budget struct {
		RateLimit *struct {
			Cost, Remaining int
			ResetAt         time.Time
		}
	}
	if json.Unmarshal(response.Data, &budget) == nil && budget.RateLimit != nil {
		rate := budget.RateLimit
		p.graphRate.Cost, p.graphRate.Remaining, p.graphRate.Reset = &rate.Cost, &rate.Remaining, rate.ResetAt
	}
	limited := status == 429 || status == 403 && (headers.Get("X-RateLimit-Remaining") == "0" || headers.Get("Retry-After") != "")
	for _, issue := range response.Errors {
		limited = limited || issue.Type == "RATE_LIMITED"
	}
	if limited {
		resume := p.now.Add(time.Minute)
		if seconds, e := strconv.Atoi(headers.Get("Retry-After")); e == nil && seconds >= 0 {
			resume = p.now.Add(time.Duration(seconds) * time.Second)
		} else if at, e := http.ParseTime(headers.Get("Retry-After")); e == nil {
			resume = at
		}
		p.graphRate.RetryAt = resume
		return nil, nil, errRateLimit
	}
	if status != 200 {
		return nil, nil, fmt.Errorf("GitHub discovery HTTP %d", status)
	}
	if parseErr != nil {
		return nil, nil, errors.New("invalid GitHub discovery response")
	}
	return response.Data, response.Errors, nil
}

func (p *poll) openPullPages(ctx context.Context, repository github.Repository) ([]gqlPR, actor, string, error) {
	var result []gqlPR
	var viewer actor
	var permission string
	var cursor any
	for page := 0; page < 1000; page++ {
		data, issues, err := p.graphQL(ctx, inboxQuery, map[string]any{"owner": repository.Owner, "name": repository.Name, "cursor": cursor})
		if err != nil {
			return nil, viewer, "", err
		}
		var response struct {
			Viewer     actor
			Repository *struct {
				ViewerPermission string
				PullRequests     struct {
					Nodes    []gqlPR
					PageInfo pageInfo
				}
			}
		}
		if len(issues) > 0 || json.Unmarshal(data, &response) != nil || response.Repository == nil {
			return nil, viewer, "", errors.New("invalid GitHub discovery response")
		}
		viewer = response.Viewer
		permission = response.Repository.ViewerPermission
		pulls := response.Repository.PullRequests
		for _, pr := range pulls.Nodes {
			// Missing request pages could conceal automatic eligibility; do not publish
			// a partial inbox as if it represented all requests.
			if pr.ReviewRequests.PageInfo.HasNextPage {
				return nil, viewer, "", errors.New("GitHub review requests exceeded limit")
			}
		}
		result = append(result, pulls.Nodes...)
		if !pulls.PageInfo.HasNextPage {
			p.fingerprint = fingerprint{Open: len(result)}
			stable := append([]gqlPR(nil), result...)
			for i := range stable {
				p.fingerprint.Newest = max(p.fingerprint.Newest, stable[i].Number)
				p.fingerprint.Updated = maxTime(p.fingerprint.Updated, stable[i].UpdatedAt)
				stable[i].UpdatedAt = time.Time{}
			}
			// Compare only the typed, minimal result: no raw GraphQL payload is cached.
			sort.Slice(stable, func(i, j int) bool { return stable[i].Number < stable[j].Number })
			minimal, _ := json.Marshal(stable)
			const cacheKey = "graphql:open-pulls"
			if string(minimal) != string(p.cache.Responses[cacheKey].Body) {
				p.unchanged = false
			}
			p.cache.Responses[cacheKey] = cachedResponse{Body: minimal}
			return result, viewer, permission, nil
		}
		if pulls.PageInfo.EndCursor == "" || pulls.PageInfo.EndCursor == cursor {
			return nil, viewer, "", errors.New("invalid GitHub pagination cursor")
		}
		cursor = pulls.PageInfo.EndCursor
	}
	return nil, viewer, "", errors.New("GitHub discovery pagination exceeded limit")
}
