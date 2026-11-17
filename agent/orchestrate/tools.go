package orchestrate

import (
	"strings"
)

// Tool is one tool of baum's MCP server a review call may use: its name and
// one line on what to read with it.
type Tool struct {
	Name, Use string
}

// ReviewTools are the tools of baum's MCP server every call of a review is
// told about, in the order the prompt lists them. It is the one place the
// list lives: each call's "Tools you have" section is built from it
// (toolsSection). The calls hold a token scoped to the pull request under
// review, which reads only that pull request's repository.
var ReviewTools = []Tool{
	{Name: "pull_request", Use: "the pull request: description, author, branches, state"},
	{Name: "facts", Use: "the pull request's facts at a glance"},
	{Name: "diff", Use: "the diff, or one hunk's patch by its ID"},
	{Name: "file", Use: "any file at the head or the base, by path"},
	{Name: "git_log", Use: "the history of the repository or of a path"},
	{Name: "git_blame", Use: "who changed each line of a file, and in which commit"},
	{Name: "commits", Use: "the pull request's commits"},
	{Name: "commit", Use: "one commit"},
	{Name: "commit_patch", Use: "one commit's patch"},
	{Name: "pull_request_timeline", Use: "the pushes and force pushes"},
	{Name: "checks", Use: "the checks at the head, with their conclusions"},
	{Name: "check_log", Use: "the last lines of a failing check's log"},
	{Name: "validation_runs", Use: "the tests and checks the linked sessions ran"},
	{Name: "review_threads", Use: "the review threads, by state, with their comments"},
	{Name: "sessions", Use: "the agent sessions linked to the pull request, with their prompts"},
	{Name: "session_transcript", Use: "a linked session's transcript, from a turn, a page at a time"},
	{Name: "transcript_search", Use: "search the linked sessions' transcripts"},
	{Name: "pull_request_decisions", Use: "the decisions made in the linked sessions"},
	{Name: "decisions", Use: "the repository's recorded decisions"},
	{Name: "decision", Use: "one decision"},
	{Name: "features", Use: "the repository's features"},
	{Name: "feature", Use: "one feature and its history"},
	{Name: "related_pull_requests", Use: "other pull requests that touch the same code or are linked"},
	{Name: "issue", Use: "an issue the pull request names"},
	{Name: "search", Use: "search the repository's pull requests, sessions and decisions"},
	{Name: "review_versions", Use: "the review's earlier versions"},
	{Name: "review_version_diff", Use: "what changed in the code between two reviewed versions"},
	{Name: "review_outline", Use: "an earlier version's sections"},
	{Name: "review_section", Use: "one section of an earlier version"},
	{Name: "review_context", Use: "an earlier version's \"Where this fits\""},
}

// toolsSection is a call's "Tools you have" section: with baum's MCP
// server, the ReviewTools as baum's tools; without it, where the same data
// is in the run directory.
func toolsSection(mcp bool) string {
	var b strings.Builder
	b.WriteString("## Tools you have\n\n")
	if mcp {
		b.WriteString("baum's tools (the `baum` MCP server), scoped to this pull request:\n\n")
		for _, t := range ReviewTools {
			b.WriteString("- `" + t.Name + "`: " + t.Use + "\n")
		}
		b.WriteString("\nThe file tools read the checkout in the run directory: `head/` and `merge-base/`. `bundle.json` there holds the same pull request as one JSON document.\n")
		return b.String()
	}
	b.WriteString("The file tools, over the run directory:\n\n" +
		"- `bundle.json`: the whole pull request as one JSON document: `pull_request` (with its `commits`), `diff` (every hunk with its `patch`), `checks`, `conversation`, `threads`, `sessions` (prompts, command events, decisions), `references`, `attachments`, `review_config` and, on a new version, `previous`. Search it for a hunk's ID to read its patch.\n" +
		"- `head/` and `merge-base/`: the checkout of the head and of the base.\n")
	return b.String()
}
