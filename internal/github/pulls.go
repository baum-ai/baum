package github

import (
	"context"
	"encoding/json"
	"errors"
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
	Author           string
	Bot              bool
	HeadRef, HeadSHA string
	BaseRef, BaseSHA string
	// CrossRepository is set when the head is in another repository than
	// the base (a fork's, or one deleted since).
	CrossRepository bool
	// DefaultBranch is the base repository's default branch.
	DefaultBranch        string
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
	// Truncated is set when the listing stopped at its bound with older pull
	// requests still updated at or after since unlisted.
	Truncated bool
}

// pullsPerPage is the page size a listing starts with; a page the REST
// reader refuses as too large (pull request bodies can be long) is read again
// at half the size, down to minPullsPerPage. A listing stops at the first
// page boundary at or past maxPulls.
const (
	pullsPerPage    = 64
	minPullsPerPage = 8
)

var maxPulls = 10000

// SetMaxPulls sets the listing bound and returns a function restoring it,
// for tests.
func SetMaxPulls(n int) (restore func()) {
	previous := maxPulls
	maxPulls = n
	return func() { maxPulls = previous }
}

// ListPullRequests lists the repository's pull requests of every state,
// most recently updated first, until one was last updated before since; one
// updated exactly at since is listed. The first page is read with etag; when
// GitHub answers that it is unchanged nothing else is read. The ETag is kept
// only for a first page read at the default size. A listing that
// reaches maxPulls with more still to list stops there and is Truncated.
func ListPullRequests(ctx context.Context, reader Reader, repository Repository, etag string, since time.Time) (PullList, error) {
	var list PullList
	perPage := pullsPerPage
	for offset := 0; ; {
		path := fmt.Sprintf("/repos/%s/%s/pulls?state=all&sort=updated&direction=desc&per_page=%d&page=%d",
			url.PathEscape(repository.Owner), url.PathEscape(repository.Name), perPage, offset/perPage+1)
		send := ""
		if offset == 0 && perPage == pullsPerPage {
			send = etag
		}
		status, header, body, err := reader.Read(ctx, path, send)
		list.Calls++
		if errors.Is(err, ErrResponseTooLarge) && perPage > minPullsPerPage {
			perPage /= 2
			continue
		}
		if err != nil {
			return list, err
		}
		if offset == 0 && status == http.StatusNotModified {
			list.NotModified, list.ETag = true, etag
			return list, nil
		}
		if status != http.StatusOK {
			if status == http.StatusForbidden || status == http.StatusTooManyRequests {
				return list, fmt.Errorf("listing pull requests: GitHub answered %d (rate limit remaining %q)", status, header.Get("X-RateLimit-Remaining"))
			}
			return list, fmt.Errorf("listing pull requests: GitHub answered %d", status)
		}
		if offset == 0 && perPage == pullsPerPage {
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
				Ref  string `json:"ref"`
				SHA  string `json:"sha"`
				Repo *struct {
					ID            int64  `json:"id"`
					DefaultBranch string `json:"default_branch"`
				} `json:"repo"`
			}
		}
		if err := json.Unmarshal(body, &rows); err != nil {
			return list, fmt.Errorf("listing pull requests: %w", err)
		}
		if offset >= maxPulls {
			// Past the bound: this page only says whether more were due.
			list.Truncated = len(rows) > 0 && !rows[0].UpdatedAt.Before(since)
			return list, nil
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
			if base := row.Base.Repo; base != nil {
				pull.DefaultBranch = base.DefaultBranch
				pull.CrossRepository = row.Head.Repo == nil || row.Head.Repo.ID != base.ID
			}
			if row.MergeCommitSHA != nil {
				pull.MergeCommitSHA = *row.MergeCommitSHA
			}
			if row.User != nil {
				pull.Author, pull.Bot = row.User.Login, row.User.Type == "Bot"
			}
			list.Pulls = append(list.Pulls, pull)
		}
		if len(rows) < perPage {
			return list, nil
		}
		offset += perPage
	}
}
