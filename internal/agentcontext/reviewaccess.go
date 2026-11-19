package agentcontext

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/baum-ai/baum/agent"
)

// A Review token is held by the agent that generates a pull request's review
// on the person's own computer, with their own agent login. It may read
// whatever the app can show (every repository and pull request, the global
// lists, the Timeline, transcripts, analytics, settings as they are shown)
// and change nothing: no proposal, operation, tile or layout edit, or
// setting. Which tools it sees is decided per tool, here, never by scope: a
// tool is visible to a Review token only when reviewReadTools names it and it
// neither proposes nor edits (New refuses a listed tool that does). A tool
// added anywhere else is invisible to Review tokens until it is listed, and a
// test makes every tool be classified as a review read, a chat-only read, or
// a write.

// reviewReadTools are the tools a Review token lists and calls: every tool
// that only reads, but the person's own private state (chatOnlyReadTools).
var reviewReadTools = []string{
	// What every page has.
	"current_page", "read_more", "search", "link", "app_help", "daemon_status",
	// A pull request: GitHub's view, the saved review and its versions, the
	// diff, commits, checks, threads, jobs, sessions and their transcripts.
	"pull_request", "diff", "commits", "commit", "commit_patch", "file", "checks", "check_log",
	"review_threads", "merge_state", "update_branch_options", "rerun_options",
	"jobs", "job_transcript", "sessions", "session_transcript", "transcript_search",
	"facts", "review_outline", "review_section", "review_context", "review_draft", "review_versions",
	"review_version_diff", "pull_request_decisions", "pull_request_timeline", "related_pull_requests",
	"git_log", "git_blame", "issue", "validation_runs",
	// Review groups and stacks.
	"group", "group_versions", "group_patch", "group_file",
	// The lists: inbox, pull requests, sessions, jobs, repositories.
	"inbox", "inboxes", "test_query", "pull_requests",
	"live_sessions", "live_session_transcript", "agent_requests", "agent_messages",
	"job_list", "job", "job_failure",
	"repositories", "clones_found", "github_repositories", "clone_target",
	// The Timeline.
	"decisions", "decision", "features", "feature", "feature_build", "file_history", "timeline_transcript",
	// Analytics, and Settings as the app shows them (reads, never saves).
	"analytics", "plan_usage", "diagnostics_summary",
	"settings_general", "settings_appearance", "settings_reviews", "settings_fixes", "settings_connections", "settings_computer",
	"prompts", "prompt", "repository_reviews", "repository_prompts", "repository_registration",
}

// chatOnlyReadTools only read, but stay the chat's: the person's private draft
// review and their conversations with the review's agent, the part of the
// page they picked, and the tile library tools that serve making a tile.
var chatOnlyReadTools = []string{"pending_review", "review_conversations", "focus", "tile_type", "tile_guide"}

// reviewReadable reports whether a Review token may list and call t.
func (t Tool) reviewReadable() bool {
	return !t.writes() && slices.Contains(reviewReadTools, t.Name)
}

// writes reports a tool that records or changes something: a proposal, an
// operation's propose_* tool, or an edit made at once.
func (t Tool) writes() bool { return t.Proposes || t.Edits }

// checkReviewTools refuses a server whose review list names a tool that
// writes.
func checkReviewTools(tools []Tool) error {
	for _, t := range tools {
		if t.writes() && slices.Contains(reviewReadTools, t.Name) {
			return fmt.Errorf("tool %s writes: a review may not call it", t.Name)
		}
	}
	return nil
}

// reviewUnreadable are the routes a Review token's reads never reach,
// whatever a tool declares: the person's own saved UI state (their draft
// review among it) and their conversations with a review's agent.
var reviewUnreadable = []string{"/api/ui-state/review", "/api/prs/chat"}

// bound reports a consumer tied to one pull request, which its tools default
// to: an action or a review.
func (c CallContext) bound() bool {
	return c.Consumer.Kind == Action || c.Consumer.Kind == Review
}

// reviewTarget is Target for a Review token: what the call names, else the
// review's own repository and pull request. Any repository may be named.
func (c CallContext) reviewTarget(repository string, number int) (string, int) {
	switch {
	case repository == "":
		repository = c.Consumer.Repository
		if number == 0 {
			number = c.Consumer.PullRequest
		}
	case number == 0 && strings.EqualFold(repository, c.Consumer.Repository):
		number = c.Consumer.PullRequest
	}
	return repository, number
}

// MintReview mints a Review token for the review of repository's pull
// request number, and the function that revokes it. Safe for concurrent use.
func (r *Registry) MintReview(repository string, number int) (token string, revoke func(), err error) {
	token, _, revoke, err = r.Mint(Consumer{Kind: Review, Repository: repository, PullRequest: number})
	return token, revoke, err
}

// ReviewMCP gives review generation runs baum's MCP server. The daemon
// builds the MCP server after the job executor, so a ReviewMCP starts empty
// and Serve gives it the registry and the server's URL later; until then
// Attach gives no server. It is safe for concurrent use.
type ReviewMCP struct {
	mu       sync.RWMutex
	registry *Registry
	url      func() string
}

// Serve has Attach mint tokens in registry for the MCP route url returns
// (the app server's origin and "/api/mcp", known once it listens).
func (m *ReviewMCP) Serve(registry *Registry, url func() string) {
	m.mu.Lock()
	m.registry, m.url = registry, url
	m.mu.Unlock()
}

// Attach mints a Review token for repository's pull request number and
// returns the server a run gives its agent (agent.Request.MCP) and the
// function that revokes the token, which the caller calls once the run ends.
// A nil server, with a revoke that does nothing and no error, means there is
// none to give yet (the MCP server or the app server is not up): the run goes
// ahead without it.
func (m *ReviewMCP) Attach(repository string, number int) (*agent.MCPServer, func(), error) {
	nothing := func() {}
	if m == nil {
		return nil, nothing, nil
	}
	m.mu.RLock()
	registry, url := m.registry, m.url
	m.mu.RUnlock()
	if registry == nil || url == nil {
		return nil, nothing, nil
	}
	address := url()
	if address == "" {
		return nil, nothing, nil
	}
	token, revoke, err := registry.MintReview(repository, number)
	if err != nil {
		return nil, nothing, err
	}
	server := &agent.MCPServer{URL: address, Token: token}
	if err := server.Validate(); err != nil {
		revoke()
		return nil, nothing, err
	}
	return server, revoke, nil
}
