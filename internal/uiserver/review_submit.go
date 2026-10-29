package uiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
)

// PullRequestReviewer is the GitHub write of submitting a pull request review
// the user wrote. *github.Client implements it.
type PullRequestReviewer interface {
	SubmitReview(ctx context.Context, repository github.Repository, number int, review github.ReviewSubmission) (github.ReviewResult, error)
}

const (
	maxReviewBody     = 256 << 10
	maxReviewComments = 100
)

// ReviewHandler serves POST /api/prs/review for picked repositories only.
func ReviewHandler(registry *repos.Registry, reviewer PullRequestReviewer, changed func()) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/prs/review", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxReviewBody)
		var body struct {
			Repository string `json:"repository"`
			PR         int    `json:"pr"`
			HeadSHA    string `json:"head_sha"`
			Event      string `json:"event"`
			Body       string `json:"body"`
			Comments   []struct {
				Path      string `json:"path"`
				Line      int    `json:"line"`
				Side      string `json:"side"`
				StartLine int    `json:"start_line"`
				Body      string `json:"body"`
			} `json:"comments"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF {
			http.Error(w, "the review is not valid JSON of the expected shape", http.StatusBadRequest)
			return
		}
		if body.PR < 1 || body.HeadSHA == "" {
			http.Error(w, "repository, pr and head_sha are required", http.StatusBadRequest)
			return
		}
		switch body.Event {
		case github.ReviewEventApprove, github.ReviewEventComment, github.ReviewEventRequestChanges:
		default:
			http.Error(w, "event must be APPROVE, COMMENT or REQUEST_CHANGES", http.StatusBadRequest)
			return
		}
		if len(body.Comments) > maxReviewComments {
			http.Error(w, "a review can have at most 100 inline comments", http.StatusBadRequest)
			return
		}
		if body.Event != github.ReviewEventApprove && body.Body == "" && len(body.Comments) == 0 {
			http.Error(w, "this review needs a summary or at least one inline comment", http.StatusBadRequest)
			return
		}
		submission := github.ReviewSubmission{CommitID: body.HeadSHA, Body: body.Body, Event: body.Event}
		for _, c := range body.Comments {
			if c.Side == "" {
				c.Side = github.SideRight
			}
			if c.Path == "" || c.Body == "" || c.Line < 1 || (c.Side != github.SideRight && c.Side != github.SideLeft) || (c.StartLine != 0 && (c.StartLine < 1 || c.StartLine >= c.Line)) {
				http.Error(w, "each inline comment needs a path, a body, a line of 1 or more, side RIGHT or LEFT and a start_line before line", http.StatusBadRequest)
				return
			}
			comment := github.ReviewComment{Path: c.Path, Line: c.Line, Side: c.Side, Body: c.Body}
			if c.StartLine != 0 {
				comment.StartLine, comment.StartSide = c.StartLine, c.Side
			}
			submission.Comments = append(submission.Comments, comment)
		}
		name, err := pickedRepository(r.Context(), registry, body.Repository)
		if err != nil {
			apiError(w, err)
			return
		}
		repo, err := github.ParseRepository(name)
		if err != nil {
			apiError(w, err)
			return
		}
		result, err := reviewer.SubmitReview(r.Context(), repo, body.PR, submission)
		if err != nil {
			reviewFailure(w, err)
			return
		}
		if changed != nil {
			changed()
		}
		writeJSON(w, result)
	})
	return mux
}

// reviewFailure answers with GitHub's refusal in words the app shows as is.
func reviewFailure(w http.ResponseWriter, err error) {
	var refusal *github.ReviewError
	if !errors.As(err, &refusal) {
		refusal = &github.ReviewError{Code: github.ReviewFailed, Message: "baum could not reach GitHub. Check your connection and try again."}
	}
	status := http.StatusBadGateway
	switch refusal.Code {
	case github.ReviewForbidden:
		status = http.StatusForbidden
	case github.ReviewNotFound:
		status = http.StatusNotFound
	case github.ReviewOwnPull, github.ReviewInvalidLine:
		status = http.StatusUnprocessableEntity
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(refusal)
}
