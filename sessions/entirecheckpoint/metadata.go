package entirecheckpoint

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// CheckpointMetadata is the checkpoint-level metadata.json record. Entire has
// added fields over time, so fields are optional and unknown top-level keys are
// retained for drift reporting.
type CheckpointMetadata struct {
	CLIVersion          string        `json:"cli_version"`
	CheckpointID        string        `json:"checkpoint_id"`
	Strategy            string        `json:"strategy"`
	Branch              string        `json:"branch"`
	CheckpointsCount    int           `json:"checkpoints_count"`
	FilesTouched        []string      `json:"files_touched"`
	Sessions            []SessionRefs `json:"sessions"`
	TokenUsage          *TokenUsage   `json:"token_usage"`
	CombinedAttribution *Attribution  `json:"combined_attribution"`
	CommitSHA           string        `json:"commit_sha"`
	HasReview           *bool         `json:"has_review"`
	HasInvestigation    *bool         `json:"has_investigation"`
	Imported            *bool         `json:"imported"`

	Raw         json.RawMessage `json:"-"`
	Keys        []string        `json:"-"`
	UnknownKeys []string        `json:"-"`
}

// SessionRefs names the files for one wrapped session. Values are branch-root
// relative in stored checkpoints, although ParseCheckpoint also accepts the
// positional layout directly.
type SessionRefs struct {
	Metadata          string `json:"metadata"`
	Transcript        string `json:"transcript"`
	CompactTranscript string `json:"compact_transcript"`
	ContentHash       string `json:"content_hash"`
	Prompt            string `json:"prompt"`
	AssetsManifest    string `json:"assets_manifest"`
}

// SessionMetadata is one wrapped session's metadata.json record.
type SessionMetadata struct {
	CLIVersion                  string              `json:"cli_version"`
	CheckpointID                string              `json:"checkpoint_id"`
	SessionID                   string              `json:"session_id"`
	Strategy                    string              `json:"strategy"`
	CreatedAt                   string              `json:"created_at"`
	Branch                      string              `json:"branch"`
	CheckpointsCount            int                 `json:"checkpoints_count"`
	SaveStepCount               *int                `json:"save_step_count"`
	FilesTouched                []string            `json:"files_touched"`
	Agent                       string              `json:"agent"`
	Model                       string              `json:"model"`
	TurnID                      string              `json:"turn_id"`
	CompactTranscriptStart      *int                `json:"compact_transcript_start"`
	CheckpointTranscriptStart   *int                `json:"checkpoint_transcript_start"`
	TranscriptLinesAtStart      *int                `json:"transcript_lines_at_start"`
	TranscriptIdentifierAtStart json.RawMessage     `json:"transcript_identifier_at_start"`
	TokenUsage                  *TokenUsage         `json:"token_usage"`
	SessionMetrics              *SessionMetrics     `json:"session_metrics"`
	InitialAttribution          *Attribution        `json:"initial_attribution"`
	PromptAttributions          []PromptAttribution `json:"prompt_attributions"`
	SkillEventsVersion          *int                `json:"skill_events_version"`
	SkillEvents                 []SkillEvent        `json:"skill_events"`
	IsTask                      *bool               `json:"is_task"`
	ToolUseID                   string              `json:"tool_use_id"`
	Kind                        string              `json:"kind"`
	Summary                     json.RawMessage     `json:"summary"`
	ReviewSkills                []string            `json:"review_skills"`
	ReviewPrompt                string              `json:"review_prompt"`
	InvestigateRunID            string              `json:"investigate_run_id"`
	InvestigateTopic            string              `json:"investigate_topic"`
	CommitSHA                   string              `json:"commit_sha"`
	Imported                    *bool               `json:"imported"`

	Raw         json.RawMessage `json:"-"`
	Keys        []string        `json:"-"`
	UnknownKeys []string        `json:"-"`
}

type TokenUsage struct {
	InputTokens         int64 `json:"input_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	APICallCount        int64 `json:"api_call_count"`
}

type SessionMetrics struct {
	TurnCount         int64 `json:"turn_count"`
	DurationMS        int64 `json:"duration_ms"`
	ContextTokens     int64 `json:"context_tokens"`
	ContextWindowSize int64 `json:"context_window_size"`
}

type Attribution struct {
	CalculatedAt      string  `json:"calculated_at"`
	AgentLines        int64   `json:"agent_lines"`
	AgentRemoved      int64   `json:"agent_removed"`
	HumanAdded        int64   `json:"human_added"`
	HumanModified     int64   `json:"human_modified"`
	HumanRemoved      int64   `json:"human_removed"`
	TotalCommitted    int64   `json:"total_committed"`
	TotalLinesChanged int64   `json:"total_lines_changed"`
	AgentPercentage   float64 `json:"agent_percentage"`
	MetricVersion     int     `json:"metric_version"`
}

type PromptAttribution struct {
	CheckpointNumber   int              `json:"checkpoint_number"`
	UserLinesAdded     int64            `json:"user_lines_added"`
	UserLinesRemoved   int64            `json:"user_lines_removed"`
	AgentLinesAdded    int64            `json:"agent_lines_added"`
	AgentLinesRemoved  int64            `json:"agent_lines_removed"`
	UserAddedPerFile   map[string]int64 `json:"user_added_per_file"`
	UserRemovedPerFile map[string]int64 `json:"user_removed_per_file"`
}

type SkillEvent struct {
	ID               string            `json:"id"`
	EventType        string            `json:"event_type"`
	Skill            Skill             `json:"skill"`
	Source           SkillSource       `json:"source"`
	TurnID           string            `json:"turn_id"`
	TranscriptAnchor *TranscriptAnchor `json:"transcript_anchor"`
	Timestamp        string            `json:"timestamp"`
	Native           SkillNative       `json:"native"`
	Collapse         SkillCollapse     `json:"collapse"`
}

type Skill struct {
	Name string `json:"name"`
}

type SkillSource struct {
	Agent      string `json:"agent"`
	Signal     string `json:"signal"`
	Confidence string `json:"confidence"`
}

type TranscriptAnchor struct {
	Unit      string   `json:"unit"`
	Start     int      `json:"start"`
	End       int      `json:"end"`
	EntryIDs  []string `json:"entry_ids"`
	ToolUseID string   `json:"tool_use_id"`
}

type SkillNative struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
}

type SkillCollapse struct {
	Target           string `json:"target"`
	Label            string `json:"label"`
	DefaultCollapsed bool   `json:"default_collapsed"`
}

var checkpointMetadataKeys = keySet(
	"cli_version", "checkpoint_id", "strategy", "branch", "checkpoints_count",
	"files_touched", "sessions", "token_usage", "combined_attribution",
	"commit_sha", "has_review", "has_investigation", "imported",
)

var sessionMetadataKeys = keySet(
	"cli_version", "checkpoint_id", "session_id", "strategy", "created_at",
	"branch", "checkpoints_count", "save_step_count", "files_touched", "agent",
	"model", "turn_id", "compact_transcript_start", "checkpoint_transcript_start",
	"transcript_lines_at_start", "transcript_identifier_at_start", "token_usage",
	"session_metrics", "initial_attribution", "prompt_attributions",
	"skill_events_version", "skill_events", "is_task", "tool_use_id", "kind",
	"summary", "review_skills", "review_prompt", "investigate_run_id",
	"investigate_topic", "commit_sha", "imported",
)

func decodeCheckpointMetadata(raw []byte) (CheckpointMetadata, error) {
	var out CheckpointMetadata
	keys, unknown, err := decodeMetadata(raw, &out, checkpointMetadataKeys)
	if err != nil {
		return CheckpointMetadata{}, err
	}
	out.Raw = append(json.RawMessage(nil), bytes.TrimSpace(raw)...)
	out.Keys = keys
	out.UnknownKeys = unknown
	return out, nil
}

func decodeSessionMetadata(raw []byte) (SessionMetadata, error) {
	var out SessionMetadata
	keys, unknown, err := decodeMetadata(raw, &out, sessionMetadataKeys)
	if err != nil {
		return SessionMetadata{}, err
	}
	out.Raw = append(json.RawMessage(nil), bytes.TrimSpace(raw)...)
	out.Keys = keys
	out.UnknownKeys = unknown
	return out, nil
}

func decodeMetadata(raw []byte, out any, known map[string]bool) ([]string, []string, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, nil, err
	}
	if fields == nil {
		return nil, nil, fmt.Errorf("metadata is not a JSON object")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(fields))
	var unknown []string
	for key := range fields {
		keys = append(keys, key)
		if !known[key] {
			unknown = append(unknown, key)
		}
	}
	slices.Sort(keys)
	slices.Sort(unknown)
	return keys, unknown, nil
}

func keySet(keys ...string) map[string]bool {
	out := make(map[string]bool, len(keys))
	for _, key := range keys {
		out[key] = true
	}
	return out
}
