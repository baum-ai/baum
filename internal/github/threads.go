package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The review threads a fix addresses (internal/fix). baum reads a pull
// request's unresolved threads for the agent and, after the fix's push, replies
// on each thread the agent addressed and resolves it. These are, with merging,
// the client's writes to GitHub, made once each and never retried.

// MaxThreadComments is how many comments of a thread a fix reads: the first
// ones, which state what the reviewer asked.
const MaxThreadComments = 10

// threadPages bounds how many pages of 50 threads a fix reads.
const threadPages = 4

const fixThreadsQuery = `query FixReviewThreads($owner: String!, $name: String!, $number: Int!, $cursor: String) {
 repository(owner: $owner, name: $name) {
  pullRequest(number: $number) {
   headRefOid viewerDidAuthor
   reviewThreads(first: 50, after: $cursor) {
    pageInfo { hasNextPage endCursor }
    nodes {
     id path line originalLine isResolved isOutdated viewerCanReply viewerCanResolve
     comments(first: 10) { totalCount nodes { state authorAssociation body author { __typename login } } }
    }
   }
  }
 }
}`

const threadStateQuery = `query ReviewThreadState($thread: ID!) {
 node(id: $thread) {
  ... on PullRequestReviewThread { id isResolved viewerCanReply viewerCanResolve pullRequest { number repository { nameWithOwner } } }
 }
}`

const replyMutation = `mutation ReplyToReviewThread($thread: ID!, $body: String!) {
 addPullRequestReviewThreadReply(input: {pullRequestReviewThreadId: $thread, body: $body}) { comment { id } }
}`

const resolveMutation = `mutation ResolveReviewThread($thread: ID!) {
 resolveReviewThread(input: {threadId: $thread}) { thread { id isResolved } }
}`

// ThreadComment is one submitted comment of a review thread. Body is the
// reviewer's text, untrusted.
type ThreadComment struct {
	Author string
	// Association is GitHub's author association (OWNER, MEMBER,
	// COLLABORATOR, CONTRIBUTOR, NONE…); Bot marks an app's account.
	Association string
	Bot         bool
	Body        string
}

// ReviewThread is an unresolved review thread as a fix reads it.
type ReviewThread struct {
	// ID is the thread's GraphQL node id, which baum alone uses.
	ID   string
	Path string
	// Line is the thread's line, or its original line once outdated; 0 for
	// a comment on the whole file.
	Line     int
	Outdated bool
	// CanReply and CanResolve are the signed-in account's.
	CanReply, CanResolve bool
	// Comments are the first MaxThreadComments submitted comments; Total
	// counts them all.
	Comments []ThreadComment
	Total    int
}

// ReviewThreads is what a fix reads of a pull request's review threads.
type ReviewThreads struct {
	HeadSHA string
	// ViewerDidAuthor: the signed-in account opened the pull request.
	ViewerDidAuthor bool
	// Unresolved are the unresolved threads in GitHub's order.
	Unresolved []ReviewThread
	// Capped: the pull request has more threads than baum read.
	Capped bool
}

// document sends one GraphQL document once and decodes its data into out. GitHub's
// own error text is not kept: these errors may reach a job's record.
func (c *Client) document(ctx context.Context, repository Repository, query string, variables map[string]any, out any) error {
	reply, err := c.post(ctx, repository, query, variables)
	if err != nil {
		return err
	}
	if len(reply.Errors) > 0 {
		return fmt.Errorf("GitHub refused the request (%s)", errorType(reply.Errors[0].Type))
	}
	if json.Unmarshal(reply.Data, out) != nil {
		return errors.New("GitHub's answer could not be read")
	}
	return nil
}

// errorType keeps a GraphQL error type when it is one of GitHub's
// upper-case codes.
func errorType(kind string) string {
	if kind == "" || len(kind) > 32 || strings.Trim(kind, "ABCDEFGHIJKLMNOPQRSTUVWXYZ_") != "" {
		return "error"
	}
	return kind
}

// UnresolvedThreads reads the pull request's unresolved review threads, with
// the first comments of each, from up to four pages of 50 threads. A
// pending comment (the signed-in account's unsubmitted review) is left out.
func (c *Client) UnresolvedThreads(ctx context.Context, repository Repository, number int) (ReviewThreads, error) {
	var out ReviewThreads
	var cursor *string
	for page := range threadPages {
		var data struct {
			Repository *struct {
				PullRequest *struct {
					HeadRefOid      string
					ViewerDidAuthor bool
					ReviewThreads   struct {
						PageInfo struct {
							HasNextPage bool
							EndCursor   *string
						}
						Nodes []struct {
							ID, Path               string
							Line, OriginalLine     *int
							IsResolved, IsOutdated bool
							ViewerCanReply         bool
							ViewerCanResolve       bool
							Comments               struct {
								TotalCount int
								Nodes      []struct {
									State, AuthorAssociation, Body string
									Author                         *struct {
										Typename string `json:"__typename"`
										Login    string
									}
								}
							}
						}
					}
				}
			}
		}
		variables := map[string]any{"owner": repository.Owner, "name": repository.Name, "number": number, "cursor": cursor}
		if err := c.document(ctx, repository, fixThreadsQuery, variables, &data); err != nil {
			return ReviewThreads{}, fmt.Errorf("reading the review threads: %w", err)
		}
		if data.Repository == nil || data.Repository.PullRequest == nil {
			return ReviewThreads{}, fmt.Errorf("%w: pull request %s#%d", ErrNotFound, repository, number)
		}
		pr := data.Repository.PullRequest
		if page == 0 {
			out.HeadSHA, out.ViewerDidAuthor = pr.HeadRefOid, pr.ViewerDidAuthor
		} else if pr.HeadRefOid != out.HeadSHA {
			return ReviewThreads{}, ErrHeadMoved
		}
		for _, node := range pr.ReviewThreads.Nodes {
			if node.IsResolved {
				continue
			}
			if node.ID == "" {
				return ReviewThreads{}, errors.New("github returned a review thread without its id")
			}
			thread := ReviewThread{ID: node.ID, Path: node.Path, Outdated: node.IsOutdated, CanReply: node.ViewerCanReply, CanResolve: node.ViewerCanResolve, Total: node.Comments.TotalCount}
			if node.Line != nil {
				thread.Line = *node.Line
			} else if node.OriginalLine != nil {
				thread.Line = *node.OriginalLine
			}
			for _, comment := range node.Comments.Nodes {
				if comment.State == "PENDING" {
					thread.Total--
					continue
				}
				entry := ThreadComment{Association: comment.AuthorAssociation, Body: comment.Body}
				if comment.Author != nil {
					entry.Author, entry.Bot = comment.Author.Login, comment.Author.Typename == "Bot"
				}
				thread.Comments = append(thread.Comments, entry)
			}
			if len(thread.Comments) > 0 {
				out.Unresolved = append(out.Unresolved, thread)
			}
		}
		info := pr.ReviewThreads.PageInfo
		if !info.HasNextPage {
			return out, nil
		}
		if info.EndCursor == nil || (cursor != nil && *cursor == *info.EndCursor) {
			return ReviewThreads{}, errInvalidPagination
		}
		cursor = info.EndCursor
	}
	out.Capped = true
	return out, nil
}

// ThreadState is a review thread as GitHub reports it now.
type ThreadState struct {
	// Repository is "owner/name" and Number the pull request the thread is on.
	Repository string
	Number     int
	Resolved   bool
	// CanReply and CanResolve are the signed-in account's.
	CanReply, CanResolve bool
}

// ThreadState reads one review thread by its node id.
func (c *Client) ThreadState(ctx context.Context, repository Repository, id string) (ThreadState, error) {
	var data struct {
		Node *struct {
			ID                               string
			IsResolved                       bool
			ViewerCanReply, ViewerCanResolve bool
			PullRequest                      *struct {
				Number     int
				Repository struct{ NameWithOwner string }
			}
		}
	}
	if err := c.document(ctx, repository, threadStateQuery, map[string]any{"thread": id}, &data); err != nil {
		return ThreadState{}, err
	}
	if data.Node == nil || data.Node.ID != id || data.Node.PullRequest == nil {
		return ThreadState{}, errors.New("GitHub did not return the review thread")
	}
	n := data.Node
	return ThreadState{Repository: n.PullRequest.Repository.NameWithOwner, Number: n.PullRequest.Number, Resolved: n.IsResolved, CanReply: n.ViewerCanReply, CanResolve: n.ViewerCanResolve}, nil
}

// ReplyToThread posts body as a reply in the review thread, as the signed-in
// account.
func (c *Client) ReplyToThread(ctx context.Context, repository Repository, id, body string) error {
	var data struct {
		AddPullRequestReviewThreadReply *struct {
			Comment *struct{ ID string }
		}
	}
	if err := c.document(ctx, repository, replyMutation, map[string]any{"thread": id, "body": body}, &data); err != nil {
		return err
	}
	if data.AddPullRequestReviewThreadReply == nil || data.AddPullRequestReviewThreadReply.Comment == nil {
		return errors.New("GitHub did not confirm the reply")
	}
	return nil
}

// ResolveThread marks the review thread resolved.
func (c *Client) ResolveThread(ctx context.Context, repository Repository, id string) error {
	var data struct {
		ResolveReviewThread *struct {
			Thread *struct{ IsResolved bool }
		}
	}
	if err := c.document(ctx, repository, resolveMutation, map[string]any{"thread": id}, &data); err != nil {
		return err
	}
	if data.ResolveReviewThread == nil || data.ResolveReviewThread.Thread == nil || !data.ResolveReviewThread.Thread.IsResolved {
		return errors.New("GitHub did not confirm the resolution")
	}
	return nil
}
