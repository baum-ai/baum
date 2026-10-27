package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Reader reads one relative REST path with an optional ETag; *RESTReader is
// one.
type Reader interface {
	Read(ctx context.Context, path, etag string) (int, http.Header, []byte, error)
}

// ListedPull is a pull request as the REST pull request list gives it.
type ListedPull struct {
	Number int
	Title  string
	Body   string
	// State is "open" or "closed".
	State string
	Draft bool
	// Author is the opener's login; Bot is set when GitHub reports the
	// account as a bot.
	Author               string
	Bot                  bool
	HeadRef, HeadSHA     string
	BaseRef, BaseSHA     string
	MergeCommitSHA       string
	CreatedAt, UpdatedAt time.Time
	MergedAt, ClosedAt   *time.Time
}

// PullList is what ListPullRequests read.
type PullList struct {
	// Pulls are the pull requests updated at or after the cursor, most
	// recently updated first.
	Pulls []ListedPull
	// ETag is the first page's, for the next call.
	ETag string
	// NotModified is set when the first page is unchanged since ETag.
	NotModified bool
	// Calls counts the requests made.
	Calls int
}

// maxPullPages bounds one listing at 10,000 pull requests.
const maxPullPages = 100

// ListPullRequests lists the repository's pull requests of every state,
// most recently updated first, 100 a page, until one was last updated before
// since. The first page is read with etag; when GitHub answers that it is
// unchanged nothing else is read.
func ListPullRequests(ctx context.Context, reader Reader, repository Repository, etag string, since time.Time) (PullList, error) {
	var list PullList
	for page := 1; page <= maxPullPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/pulls?state=all&sort=updated&direction=desc&per_page=100&page=%d",
			url.PathEscape(repository.Owner), url.PathEscape(repository.Name), page)
		send := ""
		if page == 1 {
			send = etag
		}
		status, header, body, err := reader.Read(ctx, path, send)
		list.Calls++
		if err != nil {
			return list, err
		}
		if page == 1 && status == http.StatusNotModified {
			list.NotModified, list.ETag = true, etag
			return list, nil
		}
		if status != http.StatusOK {
			if status == http.StatusForbidden || status == http.StatusTooManyRequests {
				return list, fmt.Errorf("listing pull requests: GitHub answered %d (rate limit remaining %q)", status, header.Get("X-RateLimit-Remaining"))
			}
			return list, fmt.Errorf("listing pull requests: GitHub answered %d", status)
		}
		if page == 1 {
			list.ETag = header.Get("ETag")
		}
		var rows []struct {
			Number         int        `json:"number"`
			Title          string     `json:"title"`
			Body           *string    `json:"body"`
			State          string     `json:"state"`
			Draft          bool       `json:"draft"`
			MergeCommitSHA *string    `json:"merge_commit_sha"`
			CreatedAt      time.Time  `json:"created_at"`
			UpdatedAt      time.Time  `json:"updated_at"`
			MergedAt       *time.Time `json:"merged_at"`
			ClosedAt       *time.Time `json:"closed_at"`
			User           *struct {
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"user"`
			Head, Base struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			}
		}
		if err := json.Unmarshal(body, &rows); err != nil {
			return list, fmt.Errorf("listing pull requests: %w", err)
		}
		for _, row := range rows {
			if row.UpdatedAt.Before(since) {
				return list, nil
			}
			pull := ListedPull{
				Number: row.Number, Title: row.Title, State: row.State, Draft: row.Draft,
				HeadRef: row.Head.Ref, HeadSHA: row.Head.SHA, BaseRef: row.Base.Ref, BaseSHA: row.Base.SHA,
				CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, MergedAt: row.MergedAt, ClosedAt: row.ClosedAt,
			}
			if row.Body != nil {
				pull.Body = *row.Body
			}
			if row.MergeCommitSHA != nil {
				pull.MergeCommitSHA = *row.MergeCommitSHA
			}
			if row.User != nil {
				pull.Author, pull.Bot = row.User.Login, row.User.Type == "Bot"
			}
			list.Pulls = append(list.Pulls, pull)
		}
		if len(rows) < 100 {
			return list, nil
		}
	}
	return list, nil
}
