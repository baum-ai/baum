// Package bundle defines the evidence bundle: the input a review generation
// run reads, and the diff index that anchors its hunks.
//
// Bundle is a dedicated type, not a host's stored rows or API types
// directly: its fields are the only way data leaves the host for the model,
// so a new column elsewhere cannot widen what a generation run reads by
// accident.
package bundle

import (
	"encoding/json"
	"time"
)

// Bundle is the generation run's input: everything the model may cite, each
// with the identifier its anchor type resolves against.
type Bundle struct {
	Repository   string             `json:"repository"`
	PullRequest  PullRequest        `json:"pull_request"`
	Diff         []Hunk             `json:"diff"`
	Checks       []Check            `json:"checks"`
	Conversation []ConversationItem `json:"conversation"`
	// Threads are the pull request's review threads with their resolution,
	// from the stored GitHub activity snapshot; empty when none was synced.
	Threads    []Thread    `json:"threads"`
	Sessions   []Session   `json:"sessions"`
	References []Reference `json:"references"`
	// Attachments are the stored description images, in description order.
	// The byte fit counts them and never trims them.
	Attachments  []Attachment     `json:"attachments,omitempty"`
	ReviewConfig *ReviewConfig    `json:"review_config,omitempty"`
	Previous     *PreviousVersion `json:"previous,omitempty"`
	// Sources records which parts had data available, so an empty slot
	// downstream reads as absent input rather than model omission.
	Sources Sources `json:"sources"`
	// Truncated is one plain-language generator.truncated note per cut.
	Truncated []string `json:"truncated,omitempty"`

	// MaskedSpanCount is not part of the encoded bundle a model reads: it is
	// recorded with the stored version.
	MaskedSpanCount int `json:"-"`
	// Digest is the bundle digest, computed from the encoded, redacted,
	// trimmed bundle, and stored with the version.
	Digest string `json:"-"`
}

type PullRequest struct {
	Number            int    `json:"number"`
	State             string `json:"state"`
	Draft             bool   `json:"draft"`
	Title             string `json:"title"`
	Body              string `json:"body"`
	AuthorLogin       string `json:"author_login"`
	AuthorAssociation string `json:"author_association"`
	AuthorBot         bool   `json:"author_bot"`
	BaseRef           string `json:"base_ref"`
	BaseSHA           string `json:"base_sha"`
	// HeadRef is the pull request's own branch name — where a contributor
	// actually puts ticket keys and other references, unlike BaseRef (almost
	// always the default branch).
	HeadRef            string   `json:"head_ref"`
	HeadSHA            string   `json:"head_sha"`
	MergeBaseSHA       string   `json:"merge_base_sha"`
	MergedSHA          string   `json:"merged_sha,omitempty"`
	RequestedReviewers []string `json:"requested_reviewers"`
	// OpenedAt is when GitHub reports the pull request was opened.
	OpenedAt *time.Time `json:"opened_at,omitempty"`
	Commits  []Commit   `json:"commits"`
}

// Commit is one synced commit of the pull request. Additions and
// Deletions are its change against its first parent, read only for commits
// made after the pull request was opened, and nil when the mirror cannot
// compare it; a merge commit carries zero, since what it brings in is not
// this pull request's change.
type Commit struct {
	SHA         string     `json:"sha"`
	Subject     string     `json:"subject"`
	CommittedAt *time.Time `json:"committed_at,omitempty"`
	Additions   *int       `json:"additions,omitempty"`
	Deletions   *int       `json:"deletions,omitempty"`

	// Parents are the commit's parent SHAs, which the host reads to choose
	// the comparison for Additions and Deletions; not part of the encoded
	// bundle.
	Parents []string `json:"-"`
}

// Hunk is one entry of the diff index.
type Hunk struct {
	// ID hashes the file path and normalised hunk content, so it survives a
	// context shift and a rebase.
	ID      string `json:"id"`
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	// Change is "added", "modified", "removed" or "renamed".
	Change string `json:"change"`
	// Role is one of code, test, generated, migration, docs, config,
	// vendored.
	Role      string `json:"role"`
	StartLine int    `json:"start_line,omitempty"`
	LineCount int    `json:"line_count,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	DiffURL   string `json:"diff_url"`
	// Patch is empty for a textless hunk: binary, unreadable, or omitted by
	// the checkout budget; the host sets TextlessReason for its omissions.
	Patch          string `json:"patch,omitempty"`
	Textless       bool   `json:"textless,omitempty"`
	TextlessReason string `json:"textless_reason,omitempty"`
}

// Check is one check run or commit status for the head.
type Check struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind is "check_run" or "status".
	Kind       string `json:"kind"`
	Conclusion string `json:"conclusion,omitempty"`
	Status     string `json:"status,omitempty"`
	// Required is true or false from readable branch protection, and
	// explicitly nil when the host cannot read it. Hosts that do not sync
	// branch protection or rulesets always leave it nil.
	Required    *bool      `json:"required"`
	URL         string     `json:"url,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// ConversationItem is one issue comment, review or review comment.
type ConversationItem struct {
	ID string `json:"id"`
	// Kind is "comment", "review" or "review_comment".
	Kind              string `json:"kind"`
	AuthorLogin       string `json:"author_login"`
	AuthorAssociation string `json:"author_association,omitempty"`
	// Bot is true when GitHub reports the author as a Bot actor.
	Bot bool `json:"bot"`
	// BotTool is the disclosed or recognised automation provider, set only
	// when Bot is true or a disclosure was found in the body; the helpers run
	// per contribution.
	BotTool string `json:"bot_tool,omitempty"`
	// BotReport is true for a bot-report marker from REVIEW.md's marker
	// registry, which is not built, so it is always false for now. The
	// host's own summary comment is not in the conversation at all.
	BotReport bool       `json:"bot_report,omitempty"`
	Body      string     `json:"body"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	// State is the review verdict for a review item.
	State string `json:"state,omitempty"`
	// Path is set for a review comment.
	Path string `json:"path,omitempty"`
	// Commit is the head a review assessed.
	Commit string `json:"commit,omitempty"`
	// Review is the review a review comment was submitted with.
	Review string `json:"review,omitempty"`
	// Thread and Resolved place a review comment in its review thread when
	// the activity snapshot holds it; ThreadRoot marks the thread's first
	// comment.
	Thread     string `json:"thread,omitempty"`
	ThreadRoot bool   `json:"thread_root,omitempty"`
	Resolved   *bool  `json:"resolved,omitempty"`
}

// Thread is one review thread. Line is the current new-side line, null once
// outdated.
type Thread struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Line     *int   `json:"line"`
	Resolved bool   `json:"resolved"`
	Outdated bool   `json:"outdated"`
	URL      string `json:"url,omitempty"`
}

// Session is one linked session. Restricted is true for a session of which
// the pull request's repository is not a connected member: only RootSessionID
// and Vendor are populated, everywhere in the bundle.
type Session struct {
	RootSessionID string     `json:"id"`
	Vendor        string     `json:"vendor"`
	Restricted    bool       `json:"restricted"`
	Models        []string   `json:"models,omitempty"`
	Title         string     `json:"title,omitempty"`
	Branch        string     `json:"branch,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	Outcome       string     `json:"outcome,omitempty"`

	Prompts       []Prompt       `json:"prompts,omitempty"`
	CommandEvents []CommandEvent `json:"command_events,omitempty"`
	// Decisions are the decisions a host extracted from the session's
	// transcript, in the host's order. Nil means none were extracted (off,
	// failed or skipped); a non-nil empty list means the session was
	// extracted and made none. A restricted session never carries them.
	Decisions *[]SessionDecision `json:"decisions,omitempty"`

	// Confidence, HasStartedAt and StartedAtValue are the sort keys used when
	// capping and ordering sessions; not part of the encoded bundle.
	Confidence     float64   `json:"-"`
	HasStartedAt   bool      `json:"-"`
	StartedAtValue time.Time `json:"-"`
}

// Prompt is one human prompt. Index is
// the prompt's position among the session's human prompts; ID is its stored
// identifier, the prompt anchor.
type Prompt struct {
	At           *time.Time `json:"at,omitempty"`
	Index        int        `json:"index"`
	ID           string     `json:"id"`
	Text         string     `json:"text"`
	Reply        string     `json:"reply,omitempty"`
	TouchedPaths []string   `json:"touched_paths,omitempty"`
}

// SessionDecision is one decision extracted from a session: a choice
// between plausible alternatives that shaped the repository. Who is "human",
// "agent" or "agent_proposed_human_approved"; Superseded marks a choice a
// later decision of the session reversed or replaced. Turns are prompt
// indexes of the session, the turns where it was proposed, made or
// implemented; Files are repository-relative paths edited in those turns.
type SessionDecision struct {
	Summary             string   `json:"summary"`
	Who                 string   `json:"who"`
	Category            string   `json:"category"`
	RejectedAlternative string   `json:"rejected_alternative,omitempty"`
	Superseded          bool     `json:"superseded,omitempty"`
	Turns               []int    `json:"turns"`
	Files               []string `json:"files,omitempty"`
}

// CommandEvent is one execute tool use.
// ID is the tool-use identifier the session_event anchor cites.
type CommandEvent struct {
	ID   string     `json:"id"`
	Time *time.Time `json:"time,omitempty"`
	// Outcome is "succeeded", "failed", "interrupted" or "unknown".
	Outcome  string         `json:"outcome"`
	Commands []CommandEntry `json:"commands"`
}

// CommandEntry is one simple command within a (possibly compound or
// piped) execute tool use.
type CommandEntry struct {
	Program  string `json:"program"`
	Category string `json:"category"`
	Target   string `json:"target,omitempty"`
}

// Reference is one extracted reference, extracted before redaction and
// trimming so a reference inside cut text still survives.
type Reference struct {
	// Kind is one of: issue_key, spec, spec_assertion, cherry_pick,
	// stacked_on, supersedes, includes, follow_up, image, video,
	// authorship_disclosure.
	Kind  string `json:"kind"`
	Value string `json:"value"`
	// Source names where the reference was found: "body", "commit_subject",
	// "branch", or "comment:<id>".
	Source string `json:"source"`
}

// Attachment is one stored description image. Its file is in the run
// directory at Path.
type Attachment struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	SourceURL   string `json:"source_url"`
	ContentType string `json:"content_type"`
	// Alt and Heading, the nearest markdown heading above the image, are
	// redacted and capped.
	Alt      string `json:"alt,omitempty"`
	Heading  string `json:"heading,omitempty"`
	Position int    `json:"position"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	SHA256   string `json:"sha256"`
}

// ReviewConfig is REVIEW.md at the pull request's base commit, the
// target branch's policy. Present is false when the file is absent there,
// in which case defaults apply and only ChangedInPullRequest can be set.
// Content is REVIEW.md's own text, verbatim and unredacted (it has no
// audience but the review agent, which can read the same file unredacted off
// the checkout anyway), given to generation up front rather than requiring
// it to browse for the file itself; it is empty when the file is absent, not
// valid UTF-8, or was capped by the host's size limit. Location, syntax and
// override acceptance are still shaping: the text is visible as context but
// is not parsed, no roles or markers are extracted, and overrides are not
// accepted.
type ReviewConfig struct {
	Present bool   `json:"present"`
	Digest  string `json:"digest,omitempty"`
	Content string `json:"content,omitempty"`
	// ChangedInPullRequest is true when the diff index touches REVIEW.md.
	// The head version in the checkout is then a proposal, not the policy
	// this review applies.
	ChangedInPullRequest bool `json:"changed_in_pull_request,omitempty"`
}

// PreviousVersion is the current version's sections, for regeneration
// to partition against.
type PreviousVersion struct {
	Version  int               `json:"version"`
	Sections []PreviousSection `json:"sections"`
}

type PreviousSection struct {
	ID                string          `json:"id"`
	Title             string          `json:"title"`
	Kind              string          `json:"kind"`
	HunkContentHashes []string        `json:"hunk_content_hashes"`
	Content           json.RawMessage `json:"content,omitempty"`
}

// Sources records which bundle parts had data available.
type Sources map[string]bool
