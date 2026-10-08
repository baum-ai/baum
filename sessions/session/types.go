// Package session defines the vendor-agnostic session model. It is the shape we store: adapters read a vendor transcript
// and emit a Graph, and a store writes that Graph to a database.
//
// The package has no I/O and no vendor knowledge. Every id in a Graph is one of
// ours (see ids.go); vendor identifiers live only in VendorID rows.
package session

import (
	"encoding/json"
	"time"
)

// Outcome is the session end label.
// Exactly one value is stored per session, and commit evidence outranks
// interrupt evidence.
type Outcome string

const (
	OutcomeUnknown     Outcome = "unknown"
	OutcomeInterrupted Outcome = "interrupted"
	OutcomeCommitted   Outcome = "committed"
	OutcomeAbandoned   Outcome = "abandoned"
)

// Valid reports whether o is a known outcome.
func (o Outcome) Valid() bool {
	switch o {
	case OutcomeUnknown, OutcomeInterrupted, OutcomeCommitted, OutcomeAbandoned:
		return true
	}
	return false
}

// ToolVerb is our normalized category for a tool call (§5.2). The vendor name
// stays on ToolUse.Name; the verb is what cross-vendor queries group by.
type ToolVerb string

const (
	VerbRead          ToolVerb = "read"
	VerbEdit          ToolVerb = "edit"
	VerbSearch        ToolVerb = "search"
	VerbExecute       ToolVerb = "execute"
	VerbSpawnSubagent ToolVerb = "spawn_subagent"
	VerbOther         ToolVerb = "other"
)

// Valid reports whether v is a known verb.
func (v ToolVerb) Valid() bool {
	switch v {
	case VerbRead, VerbEdit, VerbSearch, VerbExecute, VerbSpawnSubagent, VerbOther:
		return true
	}
	return false
}

// FileVerb reports whether a tool with this verb can name a file path, and so
// can produce file touches (§3.8 creation rule 1).
func (v ToolVerb) FileVerb() bool { return v == VerbRead || v == VerbEdit }

// ToolStatus is the portable outcome category of a tool call (§5.3). Vendor
// signals — exit codes, error flags, interrupt markers — map into exactly one
// of these; they are not promoted to fields of their own.
type ToolStatus string

const (
	// StatusNoResult means no tool result row exists for this tool use.
	StatusNoResult    ToolStatus = "no_result"
	StatusUnknown     ToolStatus = "unknown"
	StatusOK          ToolStatus = "ok"
	StatusError       ToolStatus = "error"
	StatusInterrupted ToolStatus = "interrupted"
)

// Valid reports whether s is a known status.
func (s ToolStatus) Valid() bool {
	switch s {
	case StatusNoResult, StatusUnknown, StatusOK, StatusError, StatusInterrupted:
		return true
	}
	return false
}

// InputKind classifies a user input (§5.4).
type InputKind string

const (
	KindPrompt       InputKind = "prompt"
	KindSlashCommand InputKind = "slash_command"
	KindInterrupt    InputKind = "interrupt"
	KindOther        InputKind = "other"
)

// Valid reports whether k is a known kind.
func (k InputKind) Valid() bool {
	switch k {
	case KindPrompt, KindSlashCommand, KindInterrupt, KindOther:
		return true
	}
	return false
}

// InputOrigin records who caused a user-input-shaped action (§5.5).
type InputOrigin string

const (
	OriginHuman   InputOrigin = "human"
	OriginAgent   InputOrigin = "agent"
	OriginSystem  InputOrigin = "system"
	OriginUnknown InputOrigin = "unknown"
)

// Valid reports whether o is a known origin.
func (o InputOrigin) Valid() bool {
	switch o {
	case OriginHuman, OriginAgent, OriginSystem, OriginUnknown:
		return true
	}
	return false
}

// TouchKind is how a tool use touched a file (§5.6).
type TouchKind string

const (
	TouchRead TouchKind = "read"
	TouchEdit TouchKind = "edit"
)

// Valid reports whether k is a known touch kind.
func (k TouchKind) Valid() bool { return k == TouchRead || k == TouchEdit }

// UsageScope is the finest owner token usage can be attributed to (§5.7).
type UsageScope string

const (
	UsageScopeInput   UsageScope = "input"
	UsageScopeSession UsageScope = "session"
)

// Valid reports whether s is a known usage scope.
func (s UsageScope) Valid() bool { return s == UsageScopeInput || s == UsageScopeSession }

// TokenBasis says whether token counts came from the vendor or an estimator.
// Empty preserves the historical representation: analytics derives reported
// versus unknown from the counters and aggregate row shape.
type TokenBasis string

const (
	TokenBasisReported          TokenBasis = "tokens_reported"
	TokenBasisAggregateReported TokenBasis = "aggregate_reported"
	TokenBasisEstimated         TokenBasis = "estimated"
	TokenBasisUnknown           TokenBasis = "unknown"
)

// Speed values for ModelInvocation.Speed: the service speed the vendor billed a
// request at. Fast and ultrafast requests cost a premium over standard ones.
const (
	SpeedStandard  = "standard"
	SpeedFast      = "fast"
	SpeedUltrafast = "ultrafast"
)

// SpeedValid reports whether s is a known invocation speed.
func SpeedValid(s string) bool { return s == SpeedStandard || s == SpeedFast || s == SpeedUltrafast }

func (b TokenBasis) Valid() bool {
	switch b {
	case TokenBasisReported, TokenBasisAggregateReported, TokenBasisEstimated, TokenBasisUnknown:
		return true
	}
	return false
}

// EntityType names the kind of entity a VendorID row points at (§6).
type EntityType string

const (
	EntitySession          EntityType = "session"
	EntityUserInput        EntityType = "user_input"
	EntityModelInvocation  EntityType = "model_invocation"
	EntityAssistantMessage EntityType = "assistant_message"
	EntityToolUse          EntityType = "tool_use"
	EntityToolResult       EntityType = "tool_result"
	EntityFileTouch        EntityType = "file_touch"
	EntitySessionAttribute EntityType = "session_attribute"
	EntityTokenUsage       EntityType = "token_usage"
)

// Valid reports whether t is a known entity type.
func (t EntityType) Valid() bool {
	switch t {
	case EntitySession, EntityUserInput, EntityModelInvocation, EntityAssistantMessage,
		EntityToolUse, EntityToolResult, EntityFileTouch, EntitySessionAttribute, EntityTokenUsage:
		return true
	}
	return false
}

// Session is one agent work stretch (§3.1). A nested session has both
// ParentSessionID and SpawnedByToolUseID set; a top-level session has neither.
type Session struct {
	ID                 string     `json:"id"`
	ParentSessionID    *string    `json:"parent_session_id"`
	SpawnedByToolUseID *string    `json:"spawned_by_tool_use_id"`
	StartedAt          *time.Time `json:"started_at,omitempty"`
	EndedAt            *time.Time `json:"ended_at,omitempty"`
	Title              *string    `json:"title,omitempty"`
	WorkingDir         *string    `json:"working_dir,omitempty"`
	GitBranch          *string    `json:"git_branch,omitempty"`
	Outcome            Outcome    `json:"outcome"`
	// RepoRoot and WorktreeRoot are the repository and worktree directories the
	// session ran in, read from the developer's filesystem rather than the
	// transcript. Null for every session that arrived without them.
	RepoRoot     *string `json:"repo_root,omitempty"`
	WorktreeRoot *string `json:"worktree_root,omitempty"`
}

// SpawnLink is the parent session and tool use that created a nested session.
type SpawnLink struct {
	ParentSessionID string
	ToolUseID       string
}

// LinkSpawn applies a nested session's optional spawn relationship.
func (s *Session) LinkSpawn(link *SpawnLink) {
	if link != nil {
		s.ParentSessionID = &link.ParentSessionID
		s.SpawnedByToolUseID = &link.ToolUseID
	}
}

// TopLevel reports whether this session has no parent.
func (s *Session) TopLevel() bool { return s.ParentSessionID == nil }

// SessionAttribute is one append-only row of session-scoped state becoming
// effective at a point in time (§3.10). Key is the vendor's string, unrenamed.
// Value is JSON; JSON null means the key is unset from ValidFrom forward.
type SessionAttribute struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	ValidFrom time.Time       `json:"valid_from"`
}

// UserInput is a record of an action the user triggered (§3.2). Everything a
// session does hangs off one of these.
type UserInput struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"session_id"`
	Ordinal     int             `json:"ordinal"`
	Text        string          `json:"text"`
	TriggeredAt *time.Time      `json:"triggered_at,omitempty"`
	Interrupted bool            `json:"interrupted"`
	Kind        InputKind       `json:"kind"`
	Origin      InputOrigin     `json:"origin"`
	Metadata    json.RawMessage `json:"metadata"`
}

// ModelInvocation is one model API reply (§3.4). Token counts are 0 when
// unknown, never negative.
type ModelInvocation struct {
	ID                      string  `json:"id"`
	SessionID               string  `json:"session_id"`
	UserInputID             string  `json:"user_input_id"`
	ParentModelInvocationID *string `json:"parent_model_invocation_id"`
	Model                   *string `json:"model,omitempty"`
	InputTokens             int64   `json:"input_tokens"`
	OutputTokens            int64   `json:"output_tokens"`
	CacheReadTokens         int64   `json:"cache_read_tokens"`
	CacheWriteTokens        int64   `json:"cache_write_tokens"`
	// CacheWrite1hTokens is the part of CacheWriteTokens written with the
	// 1-hour cache TTL (the rest used the 5-minute TTL). Nil means the vendor did
	// not report the split.
	CacheWrite1hTokens *int64     `json:"cache_write_1h_tokens,omitempty"`
	TotalTokens        int64      `json:"total_tokens"`
	TokenBasis         TokenBasis `json:"token_basis,omitempty"`
	// Speed is SpeedStandard, SpeedFast or SpeedUltrafast. Nil means the vendor did not report it.
	Speed      *string    `json:"speed,omitempty"`
	StopReason *string    `json:"stop_reason,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
}

// AssistantMessage is one block of model-visible output from one model
// invocation (§3.5). At least one of Text or Content is set.
type AssistantMessage struct {
	ID                string          `json:"id"`
	SessionID         string          `json:"session_id"`
	UserInputID       string          `json:"user_input_id"`
	ModelInvocationID string          `json:"model_invocation_id"`
	Text              *string         `json:"text,omitempty"`
	Content           json.RawMessage `json:"content,omitempty"`
	Timestamp         *time.Time      `json:"timestamp,omitempty"`
}

// ToolUse is one tool call issued by a model invocation (§3.6).
type ToolUse struct {
	ID                string          `json:"id"`
	SessionID         string          `json:"session_id"`
	UserInputID       string          `json:"user_input_id"`
	ModelInvocationID string          `json:"model_invocation_id"`
	Name              string          `json:"name"`
	Verb              ToolVerb        `json:"verb"`
	Input             json.RawMessage `json:"input"`
	Status            ToolStatus      `json:"status"`
	Metadata          json.RawMessage `json:"metadata"`
	Timestamp         *time.Time      `json:"timestamp,omitempty"`
}

// ToolResult is the outcome of one tool use (§3.7). There is at most one per
// tool use, and its Status equals the tool use's Status once applied.
type ToolResult struct {
	ID          string          `json:"id"`
	ToolUseID   string          `json:"tool_use_id"`
	SessionID   string          `json:"session_id"`
	UserInputID string          `json:"user_input_id"`
	Status      ToolStatus      `json:"status"`
	OutputText  *string         `json:"output_text,omitempty"`
	Output      json.RawMessage `json:"output,omitempty"`
	ErrorText   *string         `json:"error_text,omitempty"`
	Timestamp   *time.Time      `json:"timestamp,omitempty"`
}

// FileTouch is one file path read or edited from structured evidence (§3.8). A
// touch without a path is never created.
type FileTouch struct {
	ID          string    `json:"id"`
	ToolUseID   *string   `json:"tool_use_id"`
	UserInputID string    `json:"user_input_id"`
	SessionID   string    `json:"session_id"`
	Path        string    `json:"path"`
	Kind        TouchKind `json:"kind"`
	StartLine   *int      `json:"start_line,omitempty"`
	EndLine     *int      `json:"end_line,omitempty"`
	Hunks       []Hunk    `json:"hunks,omitempty"`
}

// Hunk is one patch hunk recorded against a file touch.
type Hunk struct {
	OldStart int      `json:"old_start"`
	OldLines int      `json:"old_lines"`
	NewStart int      `json:"new_start"`
	NewLines int      `json:"new_lines"`
	Lines    []string `json:"lines,omitempty"`
}

// TokenUsage stores lossless aggregate usage that cannot be attributed to one
// model invocation (§3.12). Nil counters were not reported by the vendor.
type TokenUsage struct {
	ID               string     `json:"id"`
	SessionID        string     `json:"session_id"`
	UserInputID      *string    `json:"user_input_id"`
	Scope            UsageScope `json:"scope"`
	Model            *string    `json:"model,omitempty"`
	InputTokens      *int64     `json:"input_tokens,omitempty"`
	OutputTokens     *int64     `json:"output_tokens,omitempty"`
	CacheReadTokens  *int64     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens *int64     `json:"cache_write_tokens,omitempty"`
	TotalTokens      *int64     `json:"total_tokens,omitempty"`
	APICallCount     *int64     `json:"api_call_count,omitempty"`
	TokenBasis       TokenBasis `json:"token_basis,omitempty"`
	// Speed is SpeedStandard, SpeedFast or SpeedUltrafast when every request the row counts ran
	// at that speed (a row of one request, such as Codex's unattributed
	// token_count). Nil means not reported.
	Speed *string `json:"speed,omitempty"`
}

// VendorID maps one of our entities to one vendor identifier (§6). Internal
// joins never use these; they exist so a stored row can be traced back to the
// transcript it came from.
//
// SessionID is the session the mapped entity belongs to — its own id when the
// entity is a session. It is not in §6, and it is here because OurID points at
// eight different tables and so cannot carry a foreign key: without an owner
// these rows survive the deletion of everything they describe.
type VendorID struct {
	OurID        string     `json:"our_id"`
	OurType      EntityType `json:"our_type"`
	SessionID    string     `json:"session_id"`
	Vendor       string     `json:"vendor"`
	VendorIDKind string     `json:"vendor_id_kind"`
	VendorID     string     `json:"vendor_id"`
	SourceRef    *string    `json:"source_ref,omitempty"`
}

// Graph is one whole session tree: a top-level session, every session nested
// under it at any depth, and all of their contents.
//
// Slices are ordered for insertion as well as for reading. Sessions are ordered
// parent-before-child, and ModelInvocations parent-before-child along the
// ParentModelInvocationID chain, so a straight pass over each slice never
// references a row that does not exist yet.
type Graph struct {
	Sessions          []Session          `json:"sessions"`
	Attributes        []SessionAttribute `json:"session_attributes"`
	UserInputs        []UserInput        `json:"user_inputs"`
	ModelInvocations  []ModelInvocation  `json:"model_invocations"`
	AssistantMessages []AssistantMessage `json:"assistant_messages"`
	ToolUses          []ToolUse          `json:"tool_uses"`
	ToolResults       []ToolResult       `json:"tool_results"`
	FileTouches       []FileTouch        `json:"file_touches"`
	TokenUsage        []TokenUsage       `json:"token_usage"`
	VendorIDs         []VendorID         `json:"vendor_ids"`
}

// Root returns the top-level session of the tree, or nil for an empty graph.
func (g *Graph) Root() *Session {
	for i := range g.Sessions {
		if g.Sessions[i].TopLevel() {
			return &g.Sessions[i]
		}
	}
	return nil
}
