package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// The merge methods GitHub names in its GraphQL schema.
const (
	MergeMethodMerge  = "MERGE"
	MergeMethodSquash = "SQUASH"
	MergeMethodRebase = "REBASE"
)

// MergeOptions is what the repository allows and the pull request's current
// state, read just before the user confirms a merge.
type MergeOptions struct {
	// Methods lists the allowed merge methods in a fixed order.
	Methods []string `json:"methods"`
	// Default is the method GitHub preselects for this user, or empty.
	Default          string `json:"default"`
	State            string `json:"state"`
	Draft            bool   `json:"draft"`
	HeadSHA          string `json:"head_sha"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"merge_state_status"`
	id               string
}

// Merge failure codes, stable for the app to branch on.
const (
	MergeHeadMoved = "head_moved"
	MergeConflict  = "conflict"
	MergeBlocked   = "blocked"
	MergeForbidden = "forbidden"
	MergeNotOpen   = "not_open"
	MergeMethod    = "method_not_allowed"
	MergeFailed    = "failed"
)

// MergeError is a refusal from GitHub, worded for the person who clicked Merge.
type MergeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *MergeError) Error() string { return e.Message }

const mergeOptionsQuery = `query MergeOptions($owner: String!, $name: String!, $number: Int!) {
 repository(owner: $owner, name: $name) {
  mergeCommitAllowed squashMergeAllowed rebaseMergeAllowed viewerDefaultMergeMethod
  pullRequest(number: $number) { id state isDraft headRefOid mergeable mergeStateStatus }
 }
}`

const mergeMutation = `mutation MergePullRequest($input: MergePullRequestInput!) {
 mergePullRequest(input: $input) { pullRequest { merged state } }
}`

type graphQLReply struct {
	Data   json.RawMessage
	Errors []struct{ Type, Message string }
}

// post sends one GraphQL document once: a merge is never retried.
func (c *Client) post(ctx context.Context, repository Repository, query string, variables map[string]any) (graphQLReply, error) {
	reader, err := c.REST(ctx, repository)
	if err != nil {
		return graphQLReply{}, err
	}
	status, _, body, err := reader.GraphQL(ctx, query, variables)
	if err != nil {
		return graphQLReply{}, err
	}
	var reply graphQLReply
	parseErr := json.Unmarshal(body, &reply)
	switch {
	case status == 401:
		return reply, &MergeError{MergeForbidden, "GitHub rejected the saved connection. Reconnect GitHub in Settings and try again."}
	case (status == 403 || status == 429) && len(reply.Errors) == 0:
		return reply, &MergeError{MergeFailed, "GitHub is limiting requests right now. Try again in a minute."}
	case status != 200 && len(reply.Errors) == 0:
		return reply, &MergeError{MergeFailed, fmt.Sprintf("GitHub answered with HTTP %d.", status)}
	case parseErr != nil:
		return reply, &MergeError{MergeFailed, "GitHub's answer could not be read."}
	}
	return reply, nil
}

// MergeOptions reads the allowed merge methods and the pull request's head and
// mergeability.
func (c *Client) MergeOptions(ctx context.Context, repository Repository, number int) (MergeOptions, error) {
	reply, err := c.post(ctx, repository, mergeOptionsQuery, map[string]any{"owner": repository.Owner, "name": repository.Name, "number": number})
	if err != nil {
		return MergeOptions{}, err
	}
	if len(reply.Errors) > 0 {
		return MergeOptions{}, mergeError(reply.Errors[0].Type, reply.Errors[0].Message)
	}
	var data struct {
		Repository *struct {
			MergeCommitAllowed, SquashMergeAllowed, RebaseMergeAllowed bool
			ViewerDefaultMergeMethod                                   string
			PullRequest                                                *struct {
				ID, State, HeadRefOid, Mergeable, MergeStateStatus string
				IsDraft                                            bool
			}
		}
	}
	if json.Unmarshal(reply.Data, &data) != nil || data.Repository == nil || data.Repository.PullRequest == nil {
		return MergeOptions{}, &MergeError{MergeFailed, "GitHub could not find this pull request."}
	}
	repo, pr := data.Repository, data.Repository.PullRequest
	out := MergeOptions{Default: repo.ViewerDefaultMergeMethod, State: pr.State, Draft: pr.IsDraft, HeadSHA: pr.HeadRefOid, Mergeable: pr.Mergeable, MergeStateStatus: pr.MergeStateStatus, id: pr.ID}
	for _, m := range []struct {
		name    string
		allowed bool
	}{{MergeMethodMerge, repo.MergeCommitAllowed}, {MergeMethodSquash, repo.SquashMergeAllowed}, {MergeMethodRebase, repo.RebaseMergeAllowed}} {
		if m.allowed {
			out.Methods = append(out.Methods, m.name)
		}
	}
	return out, nil
}

// MergePullRequest merges one pull request with GitHub's own checks. GitHub
// refuses when the head is no longer expectedHead, so the user merges only the
// commit they confirmed.
func (c *Client) MergePullRequest(ctx context.Context, repository Repository, number int, method, expectedHead string) error {
	options, err := c.MergeOptions(ctx, repository, number)
	if err != nil {
		return err
	}
	allowed := false
	for _, m := range options.Methods {
		allowed = allowed || m == method
	}
	switch {
	case options.State != "OPEN":
		return &MergeError{MergeNotOpen, "This pull request is no longer open."}
	case !allowed:
		return &MergeError{MergeMethod, "This repository does not allow that merge method."}
	case options.HeadSHA != expectedHead:
		return &MergeError{MergeHeadMoved, "The pull request changed since you opened it. Review the new commits and try again."}
	}
	reply, err := c.post(ctx, repository, mergeMutation, map[string]any{"input": map[string]any{"pullRequestId": options.id, "mergeMethod": method, "expectedHeadOid": expectedHead}})
	if err != nil {
		return err
	}
	if len(reply.Errors) > 0 {
		return mergeError(reply.Errors[0].Type, reply.Errors[0].Message)
	}
	var data struct {
		MergePullRequest *struct{ PullRequest struct{ Merged bool } }
	}
	if json.Unmarshal(reply.Data, &data) != nil || data.MergePullRequest == nil || !data.MergePullRequest.PullRequest.Merged {
		return &MergeError{MergeFailed, "GitHub did not confirm the merge. Check the pull request on GitHub."}
	}
	return nil
}

// mergeError words one GraphQL error. GitHub's messages are for developers, so
// only the recognized causes are rephrased; others are quoted.
func mergeError(kind, message string) error {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "head branch was modified") || strings.Contains(lower, "expected head"):
		return &MergeError{MergeHeadMoved, "The pull request changed since you opened it. Review the new commits and try again."}
	case strings.Contains(lower, "conflict") || strings.Contains(lower, "not mergeable"):
		return &MergeError{MergeConflict, "This pull request has conflicts with its base branch. Resolve them before merging."}
	case strings.Contains(lower, "status check") || strings.Contains(lower, "checks"):
		return &MergeError{MergeBlocked, "Required checks have not passed yet. Wait for them, then try again."}
	case strings.Contains(lower, "review") || strings.Contains(lower, "protect") || strings.Contains(lower, "policy"):
		return &MergeError{MergeBlocked, "The base branch's protection rules do not allow this merge yet: " + strings.TrimSpace(message)}
	case kind == "FORBIDDEN" || strings.Contains(lower, "permission") || strings.Contains(lower, "not authorized"):
		return &MergeError{MergeForbidden, "Your GitHub account is not allowed to merge this pull request."}
	case strings.Contains(lower, "not allowed") || strings.Contains(lower, "merge method"):
		return &MergeError{MergeMethod, "This repository does not allow that merge method."}
	case kind == "NOT_FOUND":
		return &MergeError{MergeFailed, "GitHub could not find this pull request."}
	}
	if strings.TrimSpace(message) == "" {
		return &MergeError{MergeFailed, "GitHub refused the merge."}
	}
	return &MergeError{MergeFailed, "GitHub refused the merge: " + strings.TrimSpace(message)}
}
