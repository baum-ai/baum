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
	{Name: "pull_request", Use: "the pull request: description, author, branches, state and labels; start here"},
	{Name: "facts", Use: "the pull request's facts at a glance: size, checks, threads, sessions"},
	{Name: "diff", Use: "the diff, or one hunk's patch by its ID; read the patches of the hunks you write about"},
	{Name: "file", Use: "any file at the head, the base or a commit, beyond what a patch shows"},
	{Name: "git_log", Use: "the history of the repository or of a path, for why code is as it is"},
	{Name: "git_blame", Use: "which commit last changed each line of a file"},
	{Name: "commits", Use: "the pull request's commits, with their messages"},
	{Name: "commit", Use: "one commit's message and files"},
	{Name: "commit_patch", Use: "one commit's patch, when a change was made in steps"},
	{Name: "pull_request_timeline", Use: "the pushes and force pushes, for what changed since an earlier version"},
	{Name: "checks", Use: "the checks at the head and their conclusions"},
	{Name: "check_log", Use: "the end of a failing check's log, for why it failed"},
	{Name: "review_threads", Use: "the review threads (by state: open, resolved or all) with their comments"},
	{Name: "merge_state", Use: "whether the pull request can merge and what blocks it"},
	{Name: "sessions", Use: "the agent sessions linked to the pull request, with their prompts: what was asked"},
	{Name: "session_transcript", Use: "a linked session's transcript, a page at a time from a turn (from_turn, limit)"},
	{Name: "transcript_search", Use: "search the linked sessions' transcripts for a name or a phrase"},
	{Name: "validation_runs", Use: "the tests and checks the linked sessions ran, and what they showed"},
	{Name: "pull_request_decisions", Use: "the decisions made in the linked sessions that shaped this pull request"},
	{Name: "decisions", Use: "the repository's recorded decisions, for a rule the change must keep"},
	{Name: "decision", Use: "one recorded decision in full"},
	{Name: "features", Use: "the repository's features, for where the change sits"},
	{Name: "feature", Use: "one feature and its history"},
	{Name: "file_history", Use: "the pull requests and sessions that changed a file"},
	{Name: "related_pull_requests", Use: "other pull requests that touch the same code or are linked to this one"},
	{Name: "issue", Use: "an issue the pull request names"},
	{Name: "search", Use: "search the repository's pull requests, sessions and decisions"},
	{Name: "jobs", Use: "baum's jobs on this pull request, such as fixes"},
	{Name: "job_transcript", Use: "one job's transcript"},
	{Name: "review_versions", Use: "the review's earlier versions"},
	{Name: "review_version_diff", Use: "what changed in the code between two reviewed versions"},
	{Name: "review_outline", Use: "an earlier version's sections, to keep their ids and titles"},
	{Name: "review_section", Use: "one section of an earlier version, to keep its risks' ids"},
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
