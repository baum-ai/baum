// GitHub evidence mapping every host applies the same way, whether it reads
// stored rows or asks GitHub directly: identifiers, states, bot flags and
// review thread placement.
package bundle

import (
	"sort"
	"strconv"
	"strings"
)

// SummaryCommentMarker opens the pull-request summary comment a baum host
// posts. That comment is the host's own output, not evidence about the pull
// request, so it is left out of the conversation; anyone else's comment
// starting with the marker is an ordinary one.
const SummaryCommentMarker = "<!-- baum:pr-summary -->"

// IsBotActor reports whether a GitHub actor type (GraphQL's __typename, or
// REST's user type) is a Bot.
func IsBotActor(actorType string) bool {
	return strings.EqualFold(strings.TrimSpace(actorType), "Bot")
}

// PullRequestState is GitHub's state lower-cased, or "merged" once the pull
// request is merged.
func PullRequestState(state string, merged bool) string {
	if merged {
		return "merged"
	}
	return strings.ToLower(state)
}

// CheckRunID is a check run's identifier: its GitHub database ID.
func CheckRunID(databaseID int64) string {
	return "check_run:" + strconv.FormatInt(databaseID, 10)
}

// StatusCheckID is a commit status's identifier: its GraphQL node ID, or its
// database ID when the host has no node ID.
func StatusCheckID(nodeID string, databaseID int64) string {
	if nodeID != "" {
		return nodeID
	}
	return "status:" + strconv.FormatInt(databaseID, 10)
}

// SortChecks orders checks by ID, so the same checks always encode the same.
func SortChecks(checks []Check) {
	sort.SliceStable(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
}

// FlagBotReport sets BotReport on a bot's item whose body carries one of
// markers, the bot-report markers a REVIEW.md marker registry names. The
// registry is not built yet, so hosts pass none and BotReport stays false.
func FlagBotReport(item *ConversationItem, markers []string) {
	if !item.Bot {
		return
	}
	for _, marker := range markers {
		if marker != "" && strings.Contains(item.Body, marker) {
			item.BotReport = true
			return
		}
	}
}

// ReviewThreadInput is one review thread as GitHub reports it. Side is the
// diff side of Line ("LEFT" or "RIGHT"); Comments are the thread's comments
// in thread order.
type ReviewThreadInput struct {
	ID       string
	Path     string
	Side     string
	Line     *int
	Resolved bool
	Outdated bool
	Comments []ReviewThreadComment
}

// ReviewThreadComment is a review comment's conversation ID (its GitHub
// database ID) and URL.
type ReviewThreadComment struct {
	ID  string
	URL string
}

// AttachThreads sets the bundle's threads, ordered by ID, places each review
// comment of the conversation in its thread, and records review threads as
// available. A thread's URL is its first comment's, and its line is set only
// for a current right-side line, the only new-side position a hunk holds.
func AttachThreads(b *Bundle, threads []ReviewThreadInput) {
	type placement struct {
		thread   string
		root     bool
		resolved bool
	}
	b.Threads = make([]Thread, 0, len(threads))
	placed := map[string]placement{}
	for _, thread := range threads {
		item := Thread{ID: thread.ID, Path: thread.Path, Resolved: thread.Resolved, Outdated: thread.Outdated}
		if !thread.Outdated && !strings.EqualFold(thread.Side, "LEFT") {
			item.Line = thread.Line
		}
		for i, comment := range thread.Comments {
			if i == 0 {
				item.URL = comment.URL
			}
			placed[comment.ID] = placement{thread: thread.ID, root: i == 0, resolved: thread.Resolved}
		}
		b.Threads = append(b.Threads, item)
	}
	sort.SliceStable(b.Threads, func(i, j int) bool { return b.Threads[i].ID < b.Threads[j].ID })
	for i := range b.Conversation {
		item := &b.Conversation[i]
		if p, ok := placed[item.ID]; ok && item.Kind == "review_comment" {
			item.Thread, item.ThreadRoot, item.Resolved = p.thread, p.root, &p.resolved
		}
	}
	if b.Sources == nil {
		b.Sources = Sources{}
	}
	b.Sources["review_threads"] = true
}
