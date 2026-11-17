package github

import (
	"context"
	"fmt"
	"time"
)

// timelinePages bounds how many pages of 100 timeline items are read.
const timelinePages = 5

// referenceSelection selects a pull request or an issue as a reference. The
// states are aliased because GraphQL refuses one name for two enums.
const referenceSelection = `__typename
      ... on PullRequest { number title prState: state url repository { nameWithOwner } }
      ... on Issue { number title issueState: state url repository { nameWithOwner } }`

const timelineQuery = `query PullRequestTimeline($owner: String!, $name: String!, $number: Int!, $cursor: String) {
 repository(owner: $owner, name: $name) {
  pullRequest(number: $number) {
   timelineItems(first: 100, after: $cursor, itemTypes: [PULL_REQUEST_COMMIT, HEAD_REF_FORCE_PUSHED_EVENT, BASE_REF_FORCE_PUSHED_EVENT, PULL_REQUEST_REVIEW, ISSUE_COMMENT, LABELED_EVENT, UNLABELED_EVENT, ASSIGNED_EVENT, UNASSIGNED_EVENT, REVIEW_REQUESTED_EVENT, REVIEW_REQUEST_REMOVED_EVENT, READY_FOR_REVIEW_EVENT, CONVERT_TO_DRAFT_EVENT, MERGED_EVENT, CLOSED_EVENT, REOPENED_EVENT, HEAD_REF_DELETED_EVENT, BASE_REF_CHANGED_EVENT, RENAMED_TITLE_EVENT, CROSS_REFERENCED_EVENT, REFERENCED_EVENT]) {
    pageInfo { hasNextPage endCursor }
    nodes {
     __typename
     ... on PullRequestCommit { prCommit: commit { oid messageHeadline committedDate author { user { login } } } }
     ... on HeadRefForcePushedEvent { createdAt actor { login } beforeCommit { oid } afterCommit { oid } }
     ... on BaseRefForcePushedEvent { createdAt actor { login } beforeCommit { oid } afterCommit { oid } }
     ... on PullRequestReview { createdAt submittedAt author { login } reviewState: state body url }
     ... on IssueComment { createdAt author { login } body url }
     ... on LabeledEvent { createdAt actor { login } label { name } }
     ... on UnlabeledEvent { createdAt actor { login } label { name } }
     ... on AssignedEvent { createdAt actor { login } assignee { ... on User { login } ... on Bot { login } ... on Mannequin { login } } }
     ... on UnassignedEvent { createdAt actor { login } assignee { ... on User { login } ... on Bot { login } ... on Mannequin { login } } }
     ... on ReviewRequestedEvent { createdAt actor { login } requestedReviewer { ... on User { login } ... on Bot { login } ... on Mannequin { login } ... on Team { name } } }
     ... on ReviewRequestRemovedEvent { createdAt actor { login } requestedReviewer { ... on User { login } ... on Bot { login } ... on Mannequin { login } ... on Team { name } } }
     ... on ReadyForReviewEvent { createdAt actor { login } }
     ... on ConvertToDraftEvent { createdAt actor { login } }
     ... on MergedEvent { createdAt actor { login } mergeCommit { oid } }
     ... on ClosedEvent { createdAt actor { login } }
     ... on ReopenedEvent { createdAt actor { login } }
     ... on HeadRefDeletedEvent { createdAt actor { login } }
     ... on BaseRefChangedEvent { createdAt actor { login } previousRefName currentRefName }
     ... on RenamedTitleEvent { createdAt actor { login } previousTitle currentTitle }
     ... on CrossReferencedEvent { createdAt actor { login } willCloseTarget source { ` + referenceSelection + ` } }
     ... on ReferencedEvent { createdAt actor { login } refCommit: commit { oid } commitRepository { nameWithOwner } }
    }
   }
  }
 }
}`

// IssueReference is a pull request or issue that mentions, or is linked to,
// another. Kind is "pull_request" or "issue"; Title is untrusted.
type IssueReference struct {
	Kind       string
	Repository string
	Number     int
	Title      string
	State      string
	URL        string
}

// TimelineEvent is one event of a pull request's timeline, with the fields
// its Type uses; the others are empty. Bodies and titles are untrusted.
type TimelineEvent struct {
	// Type is committed, force_pushed, base_force_pushed, reviewed,
	// commented, labeled, unlabeled, assigned, unassigned, review_requested,
	// review_request_removed, ready_for_review, converted_to_draft, merged,
	// closed, reopened, head_ref_deleted, base_changed, renamed,
	// cross_referenced or referenced.
	Type  string
	At    time.Time
	Actor string
	// Commit is the oid of the commit committed, merged or referenced.
	Commit   string
	Headline string
	// Before and After are the heads a force push moved between.
	Before, After string
	// State is a review's state.
	State string
	Body  string
	URL   string
	Label string
	// Assignee and Reviewer are a login, or a team's name.
	Assignee, Reviewer string
	// From and To are a base branch's or a title's change.
	From, To string
	// Source is what mentioned the pull request; WillClose: it closes it.
	Source    *IssueReference
	WillClose bool
	// Repository is where a referencing commit is.
	Repository string
}

// Timeline is a pull request's events, oldest first.
type Timeline struct {
	Events []TimelineEvent
	// Capped: the pull request has more events than baum read.
	Capped bool
}

type loginNode struct{ Login string }

type oidNode struct{ OID string }

type referenceNode struct {
	Typename   string `json:"__typename"`
	Number     int
	Title      string
	PRState    string `json:"prState"`
	IssueState string `json:"issueState"`
	URL        string
	Repository struct{ NameWithOwner string }
}

func (r *referenceNode) reference() *IssueReference {
	if r == nil {
		return nil
	}
	out := &IssueReference{Kind: "issue", Repository: r.Repository.NameWithOwner, Number: r.Number, Title: r.Title, State: r.IssueState, URL: r.URL}
	if r.Typename == "PullRequest" {
		out.Kind, out.State = "pull_request", r.PRState
	}
	return out
}

// PullRequestTimeline reads the pull request's timeline from up to five pages
// of 100 events, oldest first.
func (c *Client) PullRequestTimeline(ctx context.Context, repository Repository, number int) (Timeline, error) {
	var out Timeline
	var cursor *string
	for range timelinePages {
		var data struct {
			Repository *struct {
				PullRequest *struct {
					TimelineItems struct {
						PageInfo struct {
							HasNextPage bool
							EndCursor   *string
						}
						Nodes []timelineNode
					}
				}
			}
		}
		variables := map[string]any{"owner": repository.Owner, "name": repository.Name, "number": number, "cursor": cursor}
		if err := c.document(ctx, repository, timelineQuery, variables, &data); err != nil {
			return Timeline{}, fmt.Errorf("reading the timeline: %w", err)
		}
		if data.Repository == nil || data.Repository.PullRequest == nil {
			return Timeline{}, fmt.Errorf("%w: pull request %s#%d", ErrNotFound, repository, number)
		}
		items := data.Repository.PullRequest.TimelineItems
		for _, node := range items.Nodes {
			if event, ok := node.event(); ok {
				out.Events = append(out.Events, event)
			}
		}
		if !items.PageInfo.HasNextPage {
			return out, nil
		}
		if items.PageInfo.EndCursor == nil || (cursor != nil && *cursor == *items.PageInfo.EndCursor) {
			return Timeline{}, errInvalidPagination
		}
		cursor = items.PageInfo.EndCursor
	}
	out.Capped = true
	return out, nil
}

// timelineNode is every field any timeline item selects.
type timelineNode struct {
	Typename    string `json:"__typename"`
	CreatedAt   time.Time
	SubmittedAt *time.Time
	Actor       *loginNode
	Author      *loginNode
	PRCommit    *struct {
		OID, MessageHeadline string
		CommittedDate        time.Time
		Author               *struct{ User *loginNode }
	} `json:"prCommit"`
	BeforeCommit      *oidNode
	AfterCommit       *oidNode
	MergeCommit       *oidNode
	RefCommit         *oidNode
	ReviewState       string
	Body, URL         string
	Label             *struct{ Name string }
	Assignee          *loginNode
	RequestedReviewer *struct{ Login, Name string }
	PreviousRefName   string
	CurrentRefName    string
	PreviousTitle     string
	CurrentTitle      string
	WillCloseTarget   bool
	Source            *referenceNode
	CommitRepository  *struct{ NameWithOwner string }
}

func (o *oidNode) value() string {
	if o == nil {
		return ""
	}
	return o.OID
}

// event is the node as a TimelineEvent; false for a type baum does not list.
func (n timelineNode) event() (TimelineEvent, bool) {
	e := TimelineEvent{At: n.CreatedAt}
	if n.Actor != nil {
		e.Actor = n.Actor.Login
	}
	switch n.Typename {
	case "PullRequestCommit":
		if n.PRCommit == nil {
			return e, false
		}
		e.Type, e.At, e.Commit, e.Headline = "committed", n.PRCommit.CommittedDate, n.PRCommit.OID, n.PRCommit.MessageHeadline
		if n.PRCommit.Author != nil && n.PRCommit.Author.User != nil {
			e.Actor = n.PRCommit.Author.User.Login
		}
	case "HeadRefForcePushedEvent", "BaseRefForcePushedEvent":
		e.Type, e.Before, e.After = "force_pushed", n.BeforeCommit.value(), n.AfterCommit.value()
		if n.Typename == "BaseRefForcePushedEvent" {
			e.Type = "base_force_pushed"
		}
	case "PullRequestReview":
		e.Type, e.State, e.Body, e.URL = "reviewed", n.ReviewState, n.Body, n.URL
		if n.SubmittedAt != nil && !n.SubmittedAt.IsZero() {
			e.At = *n.SubmittedAt
		}
		if n.Author != nil {
			e.Actor = n.Author.Login
		}
	case "IssueComment":
		e.Type, e.Body, e.URL = "commented", n.Body, n.URL
		if n.Author != nil {
			e.Actor = n.Author.Login
		}
	case "LabeledEvent", "UnlabeledEvent":
		e.Type = "labeled"
		if n.Typename == "UnlabeledEvent" {
			e.Type = "unlabeled"
		}
		if n.Label != nil {
			e.Label = n.Label.Name
		}
	case "AssignedEvent", "UnassignedEvent":
		e.Type = "assigned"
		if n.Typename == "UnassignedEvent" {
			e.Type = "unassigned"
		}
		if n.Assignee != nil {
			e.Assignee = n.Assignee.Login
		}
	case "ReviewRequestedEvent", "ReviewRequestRemovedEvent":
		e.Type = "review_requested"
		if n.Typename == "ReviewRequestRemovedEvent" {
			e.Type = "review_request_removed"
		}
		if r := n.RequestedReviewer; r != nil {
			e.Reviewer = r.Login
			if e.Reviewer == "" {
				e.Reviewer = r.Name
			}
		}
	case "ReadyForReviewEvent":
		e.Type = "ready_for_review"
	case "ConvertToDraftEvent":
		e.Type = "converted_to_draft"
	case "MergedEvent":
		e.Type, e.Commit = "merged", n.MergeCommit.value()
	case "ClosedEvent":
		e.Type = "closed"
	case "ReopenedEvent":
		e.Type = "reopened"
	case "HeadRefDeletedEvent":
		e.Type = "head_ref_deleted"
	case "BaseRefChangedEvent":
		e.Type, e.From, e.To = "base_changed", n.PreviousRefName, n.CurrentRefName
	case "RenamedTitleEvent":
		e.Type, e.From, e.To = "renamed", n.PreviousTitle, n.CurrentTitle
	case "CrossReferencedEvent":
		if n.Source == nil {
			return e, false
		}
		e.Type, e.Source, e.WillClose = "cross_referenced", n.Source.reference(), n.WillCloseTarget
	case "ReferencedEvent":
		e.Type, e.Commit = "referenced", n.RefCommit.value()
		if n.CommitRepository != nil {
			e.Repository = n.CommitRepository.NameWithOwner
		}
	default:
		return e, false
	}
	return e, true
}
