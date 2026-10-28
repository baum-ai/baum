package fix

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/github"
)

// A fix of review comments: the agent gets the pull request's unresolved
// review threads, each under a local id (T1, T2…) baum maps to the thread's
// GitHub node id, and reports per id whether it addressed the thread. After
// the push, and only then, baum replies on each addressed thread with a text
// it composes itself and resolves it (Reply). The threads' text is other
// people's and untrusted; the agent's answer is untrusted too, so it is parsed
// strictly and its summaries are reduced to one plain line before baum posts
// them.

// Bounds on what review threads contribute to a prompt.
const (
	MaxThreads      = 20
	maxCommentBytes = 2 << 10
	// MaxReplySummary bounds, in characters, the agent's summary in a reply.
	MaxReplySummary = 200
)

// Thread is an unresolved review thread a fix addresses. Ref is its id in the
// prompt and in the agent's answer; ID, its GitHub node id, never reaches the
// agent.
type Thread struct {
	Ref, ID, Path string
	// Line is 0 for a comment on the whole file.
	Line     int
	Outdated bool
	// Comments are the comments of trusted authors, their bodies bounded.
	Comments []github.ThreadComment
	// LeftOut counts the thread's comments not in Comments: beyond the
	// first ones baum read, or from accounts it does not trust.
	LeftOut int
}

// Selection is the threads a fix addresses, and what it left out.
type Selection struct {
	Threads []Thread
	// Capped: more unresolved threads than MaxThreads, or than baum read.
	Capped bool
	// Untrusted counts the unresolved threads left out because an account
	// without access to the repository started them.
	Untrusted int
}

// trusted reports a comment whose text a fix may act on: from an owner,
// member or collaborator of the repository, or from an app someone with
// admin access installed on it, such as a review bot. Anyone with a GitHub
// account can comment on a public repository's pull request; their threads
// are left out.
func trusted(comment github.ThreadComment) bool {
	switch comment.Association {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	}
	return comment.Bot
}

// SelectThreads picks the threads a fix addresses from what GitHub
// returned: those the signed-in account can reply to and resolve, whose
// first comment comes from a trusted author, the first MaxThreads in
// GitHub's order. Within a thread, comments of untrusted authors are left
// out.
func SelectThreads(list github.ReviewThreads) Selection {
	out := Selection{Capped: list.Capped}
	for _, thread := range list.Unresolved {
		if !thread.CanReply || !thread.CanResolve || len(thread.Comments) == 0 {
			continue
		}
		if !trusted(thread.Comments[0]) {
			out.Untrusted++
			continue
		}
		if len(out.Threads) == MaxThreads {
			out.Capped = true
			continue
		}
		t := Thread{Ref: fmt.Sprintf("T%d", len(out.Threads)+1), ID: thread.ID, Path: bounded(thread.Path, 512), Line: thread.Line, Outdated: thread.Outdated}
		for _, comment := range thread.Comments {
			if !trusted(comment) {
				continue
			}
			comment.Author, comment.Body = bounded(comment.Author, 64), bounded(comment.Body, maxCommentBytes)
			t.Comments = append(t.Comments, comment)
		}
		t.LeftOut = max(thread.Total, len(thread.Comments)) - len(t.Comments)
		out.Threads = append(out.Threads, t)
	}
	return out
}

// Digest identifies the threads a fix was confirmed for, with their
// comments: a reply or an edit after the confirmation changes it, and the fix
// must be confirmed again.
func Digest(threads []Thread) string {
	if len(threads) == 0 {
		return ""
	}
	raw, _ := json.Marshal(threads)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// Excerpt is the headline of a thread's first comment, at most 120
// characters, for the confirmation to show.
func Excerpt(t Thread) string {
	if len(t.Comments) == 0 {
		return ""
	}
	return truncate(Headline(t.Comments[0].Body), 120)
}

// threadText is a thread as the prompt quotes it, between untrusted-data
// markers.
func threadText(t Thread) string {
	var b strings.Builder
	fmt.Fprintf(&b, "File: %s\n", t.Path)
	switch {
	case t.Line == 0:
		b.WriteString("Line: none (a comment on the whole file)\n")
	case t.Outdated:
		fmt.Fprintf(&b, "Line: %d when the comment was written; the code has changed since\n", t.Line)
	default:
		fmt.Fprintf(&b, "Line: %d\n", t.Line)
	}
	for i, comment := range t.Comments {
		author := comment.Author
		if author == "" {
			author = "(a deleted account)"
		}
		fmt.Fprintf(&b, "\nComment %d, by %s:\n%s\n", i+1, author, strings.TrimRight(comment.Body, "\n"))
	}
	if t.LeftOut > 0 {
		fmt.Fprintf(&b, "\n(%d more comments are not shown)\n", t.LeftOut)
	}
	return b.String()
}

// Answer is what the agent reported for one thread.
type Answer struct {
	Addressed bool
	// Summary is one plain line: see cleanSummary.
	Summary string
}

// answerFence opens the agent's answer block: a code fence whose info string
// carries the prompt's random tag, so a block quoted from a thread or a file,
// which cannot know the tag, is never taken for it.
func answerFence(tag string) string { return "```baum-threads " + tag }

var threadRef = regexp.MustCompile(`^T[1-9][0-9]?$`)

// parseAnswer takes the agent's answer block, the last fence opened with
// answerFence and closed before the message ends, out of its final message.
// It returns the message without the block and the answers by thread ref.
// Anything malformed answers nothing: a block that does not decode as a JSON
// array of {id, addressed, summary}, a field of another type or name. An
// entry whose id names no thread, or repeats one, is ignored; summaries are
// cleaned (cleanSummary) with secrets masked.
func parseAnswer(ctx context.Context, message, tag string, threads []Thread) (string, map[string]Answer) {
	answers := map[string]Answer{}
	open := answerFence(tag)
	// An index into "\n"+message is the index of the fence in message.
	start := strings.LastIndex("\n"+message, "\n"+open+"\n")
	if start < 0 {
		return message, answers
	}
	body := message[start+len(open)+1:]
	end := strings.Index(body, "\n```")
	if end < 0 || strings.TrimSpace(body[end+len("\n```"):]) != "" {
		return message, answers
	}
	prose := strings.TrimSpace(message[:start])
	decoder := json.NewDecoder(strings.NewReader(body[:end]))
	decoder.DisallowUnknownFields()
	var entries []struct {
		ID        *string `json:"id"`
		Addressed *bool   `json:"addressed"`
		Summary   *string `json:"summary"`
	}
	if decoder.Decode(&entries) != nil || decoder.More() {
		return prose, answers
	}
	known := map[string]bool{}
	for _, t := range threads {
		known[t.Ref] = true
	}
	redactor, err := bundle.NewRedactor()
	if err != nil {
		return prose, answers
	}
	for _, entry := range entries {
		if entry.ID == nil || entry.Addressed == nil || entry.Summary == nil {
			continue
		}
		ref := *entry.ID
		if !threadRef.MatchString(ref) || !known[ref] {
			continue
		}
		if _, seen := answers[ref]; seen {
			continue
		}
		summary, _, err := redactor.RedactString(ctx, *entry.Summary)
		if err != nil {
			continue
		}
		answers[ref] = Answer{Addressed: *entry.Addressed, Summary: cleanSummary(summary)}
	}
	return prose, answers
}

var (
	markdownLink = regexp.MustCompile(`!?\[([^\]]*)\]\s*(?:\([^)]*\)|\[[^\]]*\])`)
	bareURL      = regexp.MustCompile(`(?i)(?:\b[a-z][a-z0-9+.-]*://|\bwww\.)\S*`)
	spaces       = regexp.MustCompile(`\s+`)
)

// cleanSummary reduces the agent's summary of a thread to what baum posts on
// GitHub under the user's name: one line of plain text, at most
// MaxReplySummary characters. Markdown links and images keep only their
// text; URLs, HTML and character references, @-mentions (an "@" would
// notify whoever it names) and control or invisible formatting characters are
// removed.
func cleanSummary(summary string) string {
	text := plain(summary)
	text = markdownLink.ReplaceAllString(text, "$1")
	text = bareURL.ReplaceAllString(text, "")
	text = strings.Map(func(r rune) rune {
		switch r {
		case '@', '&', '<', '>', '[', ']':
			return -1
		}
		return r
	}, text)
	return truncate(strings.TrimSpace(spaces.ReplaceAllString(text, " ")), MaxReplySummary)
}

// plain is text as valid UTF-8 on one line: control and format characters
// (line breaks, bidirectional overrides, zero-width characters) become
// spaces.
func plain(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(text, ""))
}

// truncate cuts text to limit characters, marking a cut with "…".
func truncate(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

// ThreadWriter is the GitHub access the replies need; *github.Client is one.
type ThreadWriter interface {
	ThreadState(ctx context.Context, repository github.Repository, id string) (github.ThreadState, error)
	ReplyToThread(ctx context.Context, repository github.Repository, id, body string) error
	ResolveThread(ctx context.Context, repository github.Repository, id string) error
}

// What became of a thread after a fix.
const (
	// ThreadResolved: baum replied and resolved the thread.
	ThreadResolved = "resolved"
	// ThreadReplied: baum replied, but could not resolve the thread.
	ThreadReplied = "replied"
	// ThreadSkipped: the agent did not address the thread, or said nothing
	// of it; baum left it untouched.
	ThreadSkipped = "skipped"
	// ThreadAlreadyResolved: someone resolved the thread during the fix.
	ThreadAlreadyResolved = "already_resolved"
	// ThreadFailed: the reply could not be posted.
	ThreadFailed = "failed"
)

// ThreadOutcome is what became of one thread, with the agent's cleaned
// summary of it.
type ThreadOutcome struct {
	Ref     string `json:"ref"`
	Outcome string `json:"outcome"`
	Summary string `json:"summary,omitempty"`
}

// replyText is baum's reply on a thread the agent addressed.
func replyText(commit, summary string) string {
	short := commit[:min(len(commit), 7)]
	if summary == "" {
		return "Addressed in " + short + "."
	}
	return "Addressed in " + short + ": " + summary
}

// reply replies on each thread the agent addressed and resolves it, in
// order, after the fix pushed commit. Before replying it reads the thread
// again: one resolved meanwhile, or no longer on this pull request or open
// to the signed-in account's reply, is left alone. A failure is the thread's
// outcome, never the fix's.
func reply(ctx context.Context, w ThreadWriter, repository github.Repository, number int, threads []Thread, answers map[string]Answer, commit string) []ThreadOutcome {
	out := make([]ThreadOutcome, 0, len(threads))
	for _, t := range threads {
		answer, ok := answers[t.Ref]
		outcome := ThreadOutcome{Ref: t.Ref, Outcome: ThreadSkipped, Summary: answer.Summary}
		if ok && answer.Addressed {
			outcome.Outcome = replyTo(ctx, w, repository, number, t, replyText(commit, answer.Summary))
		}
		out = append(out, outcome)
	}
	return out
}

func replyTo(ctx context.Context, w ThreadWriter, repository github.Repository, number int, t Thread, body string) string {
	state, err := w.ThreadState(ctx, repository, t.ID)
	switch {
	case err != nil:
		return ThreadFailed
	case !strings.EqualFold(state.Repository, repository.Owner+"/"+repository.Name) || state.Number != number:
		return ThreadFailed
	case state.Resolved:
		return ThreadAlreadyResolved
	case !state.CanReply:
		return ThreadFailed
	}
	if w.ReplyToThread(ctx, repository, t.ID, body) != nil {
		return ThreadFailed
	}
	if !state.CanResolve || w.ResolveThread(ctx, repository, t.ID) != nil {
		return ThreadReplied
	}
	return ThreadResolved
}
