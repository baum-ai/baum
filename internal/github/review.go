package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// The events a review can be submitted with, as GitHub's REST API names them.
const (
	ReviewEventApprove        = "APPROVE"
	ReviewEventComment        = "COMMENT"
	ReviewEventRequestChanges = "REQUEST_CHANGES"
)

// The sides of a diff a review comment can sit on.
const (
	SideRight = "RIGHT"
	SideLeft  = "LEFT"
)

// ReviewComment is one inline comment on a diff line. StartLine and StartSide
// are set for a comment on a range of lines and omitted otherwise.
type ReviewComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine int    `json:"start_line,omitempty"`
	StartSide string `json:"start_side,omitempty"`
	Body      string `json:"body"`
}

// ReviewSubmission is a review the user wrote. CommitID is the head commit the
// reviewer read, so comments land on the lines they saw.
type ReviewSubmission struct {
	CommitID string          `json:"commit_id"`
	Body     string          `json:"body,omitempty"`
	Event    string          `json:"event"`
	Comments []ReviewComment `json:"comments,omitempty"`
}

// ReviewResult is the submitted review.
type ReviewResult struct {
	URL string `json:"url"`
}

// Review failure codes, stable for the app to branch on.
const (
	ReviewForbidden   = "forbidden"
	ReviewNotFound    = "not_found"
	ReviewOwnPull     = "own_pull_request"
	ReviewInvalidLine = "invalid_line"
	ReviewFailed      = "failed"
)

// ReviewError is a refusal from GitHub, worded for the person who clicked
// Submit.
type ReviewError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ReviewError) Error() string { return e.Message }

const reviewUnreachable = "baum could not reach GitHub. Check your connection and try again."

// SubmitReview submits one pull request review with its inline comments. It is
// sent once and never retried: a second attempt could post the review twice.
func (c *Client) SubmitReview(ctx context.Context, repository Repository, number int, review ReviewSubmission) (ReviewResult, error) {
	reader, err := c.REST(ctx, repository)
	if err != nil {
		return ReviewResult{}, &ReviewError{ReviewFailed, reviewUnreachable}
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews", repository.Owner, repository.Name, number)
	status, body, err := reader.Write(ctx, http.MethodPost, path, review)
	if err != nil {
		return ReviewResult{}, &ReviewError{ReviewFailed, reviewUnreachable}
	}
	if status == http.StatusOK || status == http.StatusCreated {
		var reply struct {
			HTMLURL string `json:"html_url"`
		}
		_ = json.Unmarshal(body, &reply)
		return ReviewResult{URL: reply.HTMLURL}, nil
	}
	return ReviewResult{}, reviewError(status, body)
}

// reviewError words GitHub's refusal of a review. Its messages are for
// developers, so only the recognized causes are rephrased; others are quoted.
func reviewError(status int, body []byte) error {
	var reply struct {
		Message string
		Errors  []json.RawMessage
	}
	_ = json.Unmarshal(body, &reply)
	parts := []string{strings.TrimSpace(reply.Message)}
	for _, raw := range reply.Errors {
		var text string
		var object struct{ Message, Field, Code string }
		switch {
		case json.Unmarshal(raw, &text) == nil:
			parts = append(parts, text)
		case json.Unmarshal(raw, &object) == nil:
			parts = append(parts, object.Message, object.Field, object.Code)
		}
	}
	text := strings.TrimSpace(strings.Join(parts, " "))
	lower := strings.ToLower(text)
	switch {
	case status == http.StatusUnauthorized:
		return &ReviewError{ReviewForbidden, "GitHub rejected the saved connection. Reconnect GitHub in Settings and try again."}
	case status == http.StatusForbidden && strings.Contains(lower, "rate limit"):
		return &ReviewError{ReviewFailed, "GitHub is limiting requests right now. Try again in a minute."}
	case status == http.StatusForbidden:
		return &ReviewError{ReviewForbidden, "Your GitHub account is not allowed to review this pull request."}
	case status == http.StatusNotFound:
		return &ReviewError{ReviewNotFound, "GitHub could not find this pull request, or your account cannot see it."}
	case status == http.StatusUnprocessableEntity && strings.Contains(lower, "own pull request"):
		return &ReviewError{ReviewOwnPull, "GitHub does not let you approve or request changes on your own pull request. Submit it as a comment instead."}
	case status == http.StatusUnprocessableEntity && (strings.Contains(lower, "diff") || strings.Contains(lower, "line") || strings.Contains(lower, "position") || strings.Contains(lower, "path")):
		return &ReviewError{ReviewInvalidLine, "GitHub could not place some inline comments because their lines are not part of the pull request's diff. The pull request may have changed; reload it and move those comments."}
	case text == "":
		return &ReviewError{ReviewFailed, fmt.Sprintf("GitHub refused the review (HTTP %d).", status)}
	}
	return &ReviewError{ReviewFailed, "GitHub refused the review: " + text}
}
