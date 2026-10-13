package github

import (
	"strconv"
	"strings"

	"github.com/lastpersonlabs/baum/bundle"
)

// mapEvidence maps what a fetch read onto the bundle as the hosted service
// maps its synchronized rows, through the same bundle helpers.
func mapEvidence(raw rawEvidence, sources bundle.Sources, opts FetchOptions) Evidence {
	b := bundle.Bundle{Repository: raw.repository, Sources: sources}
	b.PullRequest = pullRequest(raw.header, raw.reviewers, raw.commits)
	b.Checks = checks(raw.suites, raw.statuses, raw.required, sources[SourceRequiredChecks])
	b.Conversation = conversation(raw, opts.BotReportMarkers)
	b.Threads = []bundle.Thread{}
	if sources[SourceReviewThreads] {
		bundle.AttachThreads(&b, threadInputs(raw.threads))
	}
	bundle.SortConversation(b.Conversation)

	var images []bundle.RenderedImage
	if raw.header.BodyHTML != "" {
		images = bundle.RenderedImages(raw.header.BodyHTML, bundle.MarkdownUploads(raw.header.Body))
	}
	return Evidence{
		Repository: b.Repository, PullRequest: b.PullRequest, Checks: b.Checks,
		Conversation: b.Conversation, Threads: b.Threads, Images: images, Sources: b.Sources,
	}
}

func pullRequest(header pullRequestHeader, reviewers []reviewRequest, commits []commitNode) bundle.PullRequest {
	openedAt := header.CreatedAt
	pr := bundle.PullRequest{
		Number: header.Number, State: bundle.PullRequestState(header.State, header.Merged), Draft: header.IsDraft,
		Title: header.Title, Body: header.Body, AuthorLogin: login(header.Author),
		AuthorAssociation: header.AuthorAssociation, AuthorBot: bundle.IsBotActor(typename(header.Author)),
		BaseRef: header.BaseRefName, BaseSHA: header.BaseRefOID, HeadRef: header.HeadRefName, HeadSHA: header.HeadRefOID,
		RequestedReviewers: requestedReviewers(reviewers), OpenedAt: &openedAt,
	}
	if header.Merged && header.MergeCommit != nil {
		pr.MergedSHA = header.MergeCommit.OID
	}
	for _, commit := range commits {
		committedAt := commit.CommittedDate
		subject, _, _ := strings.Cut(commit.Message, "\n")
		parents := make([]string, 0, len(commit.Parents.Nodes))
		for _, parent := range commit.Parents.Nodes {
			parents = append(parents, parent.OID)
		}
		pr.Commits = append(pr.Commits, bundle.Commit{SHA: commit.OID, Subject: subject, CommittedAt: &committedAt, Parents: parents})
	}
	return pr
}

// requestedReviewers lists each pending review request's user login or team
// slug, skipping a request whose reviewer did not resolve.
func requestedReviewers(requests []reviewRequest) []string {
	logins := make([]string, 0, len(requests))
	for _, request := range requests {
		switch reviewer := request.RequestedReviewer; {
		case reviewer.User.Login != "":
			logins = append(logins, reviewer.User.Login)
		case reviewer.Team.Slug != "":
			logins = append(logins, reviewer.Team.Slug)
		}
	}
	return logins
}

// checks lists the head's check runs, the newest run of each name in a suite
// since a re-run supersedes the earlier one, and its commit statuses. With
// protection readable a check is required when its name is a required
// context; otherwise required stays unknown.
func checks(suites []checkSuite, statuses []statusContext, required map[string]bool, requiredKnown bool) []bundle.Check {
	var out []bundle.Check
	for _, suite := range suites {
		if suite.DatabaseID == 0 {
			continue
		}
		newest := map[string]checkRun{}
		var names []string
		for _, run := range suite.CheckRuns.Nodes {
			if run.DatabaseID == 0 {
				continue
			}
			kept, seen := newest[run.Name]
			if !seen {
				names = append(names, run.Name)
			}
			if !seen || run.DatabaseID > kept.DatabaseID {
				newest[run.Name] = run
			}
		}
		for _, name := range names {
			run := newest[name]
			out = append(out, bundle.Check{
				ID: bundle.CheckRunID(int64(run.DatabaseID)), Name: run.Name, Kind: "check_run",
				Status: strings.ToLower(run.Status), Conclusion: strings.ToLower(valueOrEmpty(run.Conclusion)),
				URL: run.Permalink, StartedAt: run.StartedAt, CompletedAt: run.CompletedAt,
			})
		}
	}
	for _, status := range statuses {
		createdAt := status.CreatedAt
		state := strings.ToLower(status.State)
		out = append(out, bundle.Check{
			ID: bundle.StatusCheckID(status.ID, 0), Name: status.Context, Kind: "status", Status: state, Conclusion: state,
			URL: valueOrEmpty(status.TargetURL), CompletedAt: &createdAt,
		})
	}
	if requiredKnown {
		for i := range out {
			isRequired := required[out[i].Name]
			out[i].Required = &isRequired
		}
	}
	bundle.SortChecks(out)
	return out
}

// conversation lists the issue comments, submitted reviews and review
// comments, flagged for bots and agents. A bot's comment that opens with the
// summary-comment marker is a baum host's own summary and is left out.
func conversation(raw rawEvidence, botReportMarkers []string) []bundle.ConversationItem {
	var items []bundle.ConversationItem
	add := func(item bundle.ConversationItem) {
		bundle.FlagBotContribution(&item)
		bundle.FlagBotReport(&item, botReportMarkers)
		items = append(items, item)
	}
	for _, comment := range raw.comments {
		if comment.FullDatabaseID == 0 {
			continue
		}
		bot := bundle.IsBotActor(typename(comment.Author))
		if bot && strings.HasPrefix(strings.TrimSpace(comment.Body), bundle.SummaryCommentMarker) {
			continue
		}
		createdAt := comment.CreatedAt
		add(bundle.ConversationItem{
			ID: strconv.FormatInt(int64(comment.FullDatabaseID), 10), Kind: "comment",
			AuthorLogin: login(comment.Author), AuthorAssociation: comment.AuthorAssociation, Bot: bot,
			Body: comment.Body, CreatedAt: &createdAt,
		})
	}
	for _, review := range raw.reviews {
		if review.FullDatabaseID == 0 || strings.EqualFold(review.State, "PENDING") {
			continue
		}
		item := bundle.ConversationItem{
			ID: strconv.FormatInt(int64(review.FullDatabaseID), 10), Kind: "review",
			AuthorLogin: login(review.Author), AuthorAssociation: review.AuthorAssociation, Bot: bundle.IsBotActor(typename(review.Author)),
			Body: review.Body, State: strings.ToLower(review.State), CreatedAt: review.SubmittedAt,
		}
		if review.Commit != nil {
			item.Commit = review.Commit.OID
		}
		add(item)
	}
	for _, thread := range raw.threads {
		for _, comment := range submittedComments(thread) {
			if comment.FullDatabaseID == 0 {
				continue
			}
			createdAt := comment.CreatedAt
			item := bundle.ConversationItem{
				ID: strconv.FormatInt(int64(comment.FullDatabaseID), 10), Kind: "review_comment",
				AuthorLogin: login(comment.Author), AuthorAssociation: comment.AuthorAssociation, Bot: bundle.IsBotActor(typename(comment.Author)),
				Body: comment.Body, Path: comment.Path, CreatedAt: &createdAt,
			}
			if comment.PullRequestReview != nil && comment.PullRequestReview.FullDatabaseID != 0 {
				item.Review = strconv.FormatInt(int64(comment.PullRequestReview.FullDatabaseID), 10)
			}
			add(item)
		}
	}
	return items
}

func threadInputs(threads []reviewThread) []bundle.ReviewThreadInput {
	inputs := make([]bundle.ReviewThreadInput, 0, len(threads))
	for _, thread := range threads {
		comments := submittedComments(thread)
		if thread.ID == "" || len(comments) == 0 {
			continue
		}
		input := bundle.ReviewThreadInput{
			ID: thread.ID, Path: thread.Path, Side: thread.DiffSide, Line: thread.Line,
			Resolved: thread.IsResolved, Outdated: thread.IsOutdated,
		}
		for _, comment := range comments {
			input.Comments = append(input.Comments, bundle.ReviewThreadComment{
				ID: strconv.FormatInt(int64(comment.FullDatabaseID), 10), URL: comment.URL,
			})
		}
		inputs = append(inputs, input)
	}
	return inputs
}

// submittedComments leaves out the token owner's draft comments, which no
// one else can see; a thread holding only drafts is not yet on GitHub for
// anyone else either.
func submittedComments(thread reviewThread) []reviewComment {
	comments := make([]reviewComment, 0, len(thread.Comments.Nodes))
	for _, comment := range thread.Comments.Nodes {
		if !strings.EqualFold(comment.State, "PENDING") {
			comments = append(comments, comment)
		}
	}
	return comments
}

func login(a *actor) string {
	if a == nil {
		return ""
	}
	return a.Login
}

func typename(a *actor) string {
	if a == nil {
		return ""
	}
	return a.Typename
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
