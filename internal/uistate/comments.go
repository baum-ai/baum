package uistate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ReviewComment is a comment to add to a pending review, as AddReviewComment
// takes it: what it is about (nil for the pull request in general), its
// body, and the diff line it is placed on at Head (nil and empty for none).
type ReviewComment struct {
	Target *CommentTarget `json:"target,omitempty"`
	Body   string         `json:"body"`
	Place  *CommentPlace  `json:"place,omitempty"`
	Head   string         `json:"head,omitempty"`
}

// CommentTarget is what a pending review comment is about.
type CommentTarget struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Title string `json:"title"`
}

// CommentPlace is the diff line a pending review comment is placed on.
type CommentPlace struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Side      string `json:"side"`
	StartLine *int   `json:"start_line,omitempty"`
}

// ErrReviewFull is a pending review that holds MaxReviewComments comments.
var ErrReviewFull = fmt.Errorf("%w: a pending review holds at most %d comments", ErrInvalid, MaxReviewComments)

// commentsClient is the mutation client of the comments the daemon adds
// (AddReviewComment); its sequence continues from the record's own mark.
const commentsClient = "daemon-review-comments"

// ReviewKey is the key of a pull request's pending review: the repository
// lowercased, as the app keys it, and the number.
func ReviewKey(repository string, number int) string {
	return strings.ToLower(repository) + "#" + strconv.Itoa(number)
}

// AddReviewComment appends a comment to the pending review of key, keeping
// its other comments, its note and its sent reviews, and returns the
// comment's new id and the saved record. A review saved meanwhile is read
// again. A full review is ErrReviewFull, and a comment the record would not
// hold an error wrapping ErrInvalid that says why.
func (s *Store) AddReviewComment(key string, comment ReviewComment) (string, Record, error) {
	if !valid("review", key) {
		return "", Record{}, fmt.Errorf("%w: not a pull request's review key", ErrInvalid)
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", Record{}, err
	}
	id := hex.EncodeToString(b)
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	target := CommentTarget{Kind: "general", Title: "The pull request"}
	if comment.Target != nil {
		target = *comment.Target
	}
	added, err := json.Marshal(struct {
		ID      string        `json:"id"`
		Target  CommentTarget `json:"target"`
		Body    string        `json:"body"`
		Place   *CommentPlace `json:"place"`
		Head    string        `json:"head"`
		Created int64         `json:"created"`
	}{id, target, comment.Body, comment.Place, comment.Head, now.UnixMilli()})
	if err != nil {
		return "", Record{}, err
	}
	if err := CheckReviewComment(added); err != nil {
		return "", Record{}, err
	}
	for attempt := 0; ; attempt++ {
		current, err := s.Read("review", key)
		if err != nil {
			return "", Record{}, err
		}
		fields := map[string]json.RawMessage{}
		if len(current.Value) > 0 && string(current.Value) != "null" {
			if err := json.Unmarshal(current.Value, &fields); err != nil {
				return "", Record{}, ErrUnreadable
			}
		}
		var comments []json.RawMessage
		if raw, ok := fields["comments"]; ok {
			if err := json.Unmarshal(raw, &comments); err != nil {
				return "", Record{}, ErrUnreadable
			}
		}
		if len(comments) >= MaxReviewComments {
			return "", Record{}, ErrReviewFull
		}
		if _, ok := fields["note"]; !ok {
			fields["note"] = json.RawMessage(`""`)
		}
		if fields["comments"], err = json.Marshal(append(comments, added)); err != nil {
			return "", Record{}, err
		}
		value, err := json.Marshal(fields)
		if err != nil {
			return "", Record{}, err
		}
		mutation := commentsClient + ":" + strconv.FormatUint(current.Clients[commentsClient]+1, 10)
		saved, err := s.Replace("review", key, current.Revision, mutation, value)
		var stale *StaleError
		if errors.As(err, &stale) && attempt < 4 {
			continue
		}
		return id, saved, err
	}
}
