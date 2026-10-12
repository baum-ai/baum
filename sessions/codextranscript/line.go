package codextranscript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Class is the closed outer rollout record class.
type Class string

const (
	ClassSessionMeta             Class = "session_meta"
	ClassTurnContext             Class = "turn_context"
	ClassResponseItem            Class = "response_item"
	ClassEventMsg                Class = "event_msg"
	ClassCompacted               Class = "compacted"
	ClassWorldState              Class = "world_state"
	ClassInterAgentMetadata      Class = "inter_agent_communication_metadata"
	ClassInterAgentCommunication Class = "inter_agent_communication"
	ClassTokenUsageRecord        Class = "token_usage_record"
	ClassRealtime                Class = "realtime_item"
)

// Line is one decoded rollout envelope. Exactly one typed payload pointer is
// set according to Class; Raw and Payload retain the complete source record.
type Line struct {
	Ordinal   int             `json:"ordinal"`
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Class     Class           `json:"class"`
	Raw       json.RawMessage `json:"raw"`
	Payload   json.RawMessage `json:"payload"`

	SessionMeta             *SessionMeta             `json:"session_meta,omitempty"`
	TurnContext             *TurnContext             `json:"turn_context,omitempty"`
	ResponseItem            *ResponseItem            `json:"response_item,omitempty"`
	EventMsg                *EventMsg                `json:"event_msg,omitempty"`
	Compacted               *RawPayload              `json:"compacted,omitempty"`
	WorldState              *RawPayload              `json:"world_state,omitempty"`
	InterAgentMetadata      *InterAgentMeta          `json:"inter_agent_communication_metadata,omitempty"`
	InterAgentCommunication *InterAgentCommunication `json:"inter_agent_communication,omitempty"`
	TokenUsageRecord        *TokenUsageRecord        `json:"token_usage_record,omitempty"`
	Realtime                *RawPayload              `json:"realtime_item,omitempty"`
}

type File struct {
	Path          string       `json:"path"`
	Meta          *SessionMeta `json:"session_meta"`
	Lines         []Line       `json:"lines"`
	TornFinalLine bool         `json:"torn_final_line"`
}

// RawPayload is a known context-only payload whose vendor fields stay opaque.
type RawPayload struct {
	Raw json.RawMessage `json:"raw"`
}

type SessionMeta struct {
	ID                          string          `json:"id"`
	SessionID                   string          `json:"session_id"`
	ParentThreadID              string          `json:"parent_thread_id,omitempty"`
	ForkedFromID                string          `json:"forked_from_id,omitempty"`
	Timestamp                   *time.Time      `json:"timestamp,omitempty"`
	CWD                         string          `json:"cwd,omitempty"`
	CLIVersion                  string          `json:"cli_version,omitempty"`
	Source                      Source          `json:"source"`
	ThreadSource                string          `json:"thread_source,omitempty"`
	AgentPath                   string          `json:"agent_path,omitempty"`
	AgentNickname               string          `json:"agent_nickname,omitempty"`
	AgentRole                   string          `json:"agent_role,omitempty"`
	ModelProvider               *string         `json:"model_provider,omitempty"`
	HistoryMode                 string          `json:"history_mode,omitempty"`
	HistoryBase                 json.RawMessage `json:"history_base,omitempty"`
	SubagentHistoryStartOrdinal *int            `json:"subagent_history_start_ordinal,omitempty"`
	MultiAgentVersion           *string         `json:"multi_agent_version,omitempty"`
	Git                         *GitInfo        `json:"git,omitempty"`
	Raw                         json.RawMessage `json:"raw"`
}

type GitInfo struct {
	CommitHash    string `json:"commit_hash,omitempty"`
	Branch        string `json:"branch,omitempty"`
	RepositoryURL string `json:"repository_url,omitempty"`
}

type Source struct {
	Unit        string          `json:"unit,omitempty"`
	Custom      string          `json:"custom,omitempty"`
	Internal    string          `json:"internal,omitempty"`
	Subagent    string          `json:"subagent,omitempty"`
	Other       string          `json:"other,omitempty"`
	ThreadSpawn *ThreadSpawn    `json:"thread_spawn,omitempty"`
	Raw         json.RawMessage `json:"raw"`
}

type ThreadSpawn struct {
	ParentThreadID string  `json:"parent_thread_id"`
	Depth          int     `json:"depth"`
	AgentPath      *string `json:"agent_path"`
	AgentNickname  *string `json:"agent_nickname"`
	AgentRole      *string `json:"agent_role"`
}

func decodeSource(raw json.RawMessage) (Source, error) {
	raw = bytes.TrimSpace(raw)
	source := Source{Raw: cloneRaw(raw)}
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return source, nil
	}
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &source.Unit); err != nil {
			return Source{}, err
		}
		return source, nil
	}
	var object struct {
		Custom   string          `json:"custom"`
		Internal string          `json:"internal"`
		Subagent json.RawMessage `json:"subagent"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return Source{}, err
	}
	source.Custom, source.Internal = object.Custom, object.Internal
	if len(object.Subagent) == 0 {
		return source, nil
	}
	if object.Subagent[0] == '"' {
		if err := json.Unmarshal(object.Subagent, &source.Subagent); err != nil {
			return Source{}, err
		}
		return source, nil
	}
	var subagent struct {
		Other       string       `json:"other"`
		ThreadSpawn *ThreadSpawn `json:"thread_spawn"`
	}
	if err := json.Unmarshal(object.Subagent, &subagent); err != nil {
		return Source{}, err
	}
	source.Other, source.ThreadSpawn = subagent.Other, subagent.ThreadSpawn
	return source, nil
}

// TurnContext retains every top-level setting and provides typed accessors for
// the fields used while mapping.
type TurnContext struct {
	Fields map[string]json.RawMessage `json:"fields"`
	Raw    json.RawMessage            `json:"raw"`
}

func (c *TurnContext) String(key string) string {
	if c == nil {
		return ""
	}
	var value string
	_ = json.Unmarshal(c.Fields[key], &value)
	return value
}

func (c *TurnContext) TurnID() string { return c.String("turn_id") }
func (c *TurnContext) Model() string  { return c.String("model") }
func (c *TurnContext) CWD() string    { return c.String("cwd") }

// AttributeFields returns the settings snapshot without the turn identity.
func (c *TurnContext) AttributeFields() map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(c.Fields))
	for key, value := range c.Fields {
		if key != "turn_id" {
			out[key] = value
		}
	}
	return out
}

// ResponseItem is a closed Responses API item. Exactly one typed pointer is set.
type ResponseItem struct {
	Type             string                `json:"type"`
	Raw              json.RawMessage       `json:"raw"`
	Message          *Message              `json:"message,omitempty"`
	AgentMessage     *AgentMessage         `json:"agent_message,omitempty"`
	Reasoning        *Reasoning            `json:"reasoning,omitempty"`
	FunctionCall     *FunctionCall         `json:"function_call,omitempty"`
	FunctionOutput   *FunctionCallOutput   `json:"function_call_output,omitempty"`
	CustomCall       *CustomToolCall       `json:"custom_tool_call,omitempty"`
	CustomOutput     *CustomToolCallOutput `json:"custom_tool_call_output,omitempty"`
	ToolSearchCall   *ToolSearchCall       `json:"tool_search_call,omitempty"`
	ToolSearchOutput *ToolSearchOutput     `json:"tool_search_output,omitempty"`
	WebSearchCall    *WebSearchCall        `json:"web_search_call,omitempty"`
	Compaction       *RawPayload           `json:"compaction,omitempty"`
	ImageGeneration  *RawPayload           `json:"image_generation_call,omitempty"`
}

type ContentItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type Message struct {
	ID      string          `json:"id,omitempty"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Phase   *string         `json:"phase,omitempty"`
}

func (m *Message) Items() ([]ContentItem, error) {
	var items []ContentItem
	if err := json.Unmarshal(m.Content, &items); err != nil {
		return nil, err
	}
	return items, nil
}

type AgentMessage struct {
	ID              string          `json:"id,omitempty"`
	Author          string          `json:"author"`
	Recipient       string          `json:"recipient"`
	OtherRecipients []string        `json:"other_recipients,omitempty"`
	Content         json.RawMessage `json:"content"`
}

type Reasoning struct {
	ID               string          `json:"id,omitempty"`
	Summary          json.RawMessage `json:"summary,omitempty"`
	Content          json.RawMessage `json:"content,omitempty"`
	EncryptedContent string          `json:"encrypted_content,omitempty"`
}

type FunctionCall struct {
	ID        string `json:"id,omitempty"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Arguments string `json:"arguments"`
}

type FunctionCallOutput struct {
	ID     string          `json:"id,omitempty"`
	CallID string          `json:"call_id"`
	Output json.RawMessage `json:"output"`
}

type CustomToolCall struct {
	ID     string `json:"id,omitempty"`
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Input  string `json:"input"`
	Status string `json:"status,omitempty"`
}

type CustomToolCallOutput struct {
	ID     string          `json:"id,omitempty"`
	CallID string          `json:"call_id"`
	Output json.RawMessage `json:"output"`
}

type ToolSearchCall struct {
	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Arguments json.RawMessage `json:"arguments"`
	Status    string          `json:"status,omitempty"`
	Execution string          `json:"execution,omitempty"`
}

type ToolSearchOutput struct {
	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Status    string          `json:"status,omitempty"`
	Execution string          `json:"execution,omitempty"`
	Tools     json.RawMessage `json:"tools,omitempty"`
}

type WebSearchCall struct {
	Status string          `json:"status,omitempty"`
	Action json.RawMessage `json:"action,omitempty"`
}

// EventMsg is a closed saved lifecycle/UI event. Exactly one typed pointer is set.
type EventMsg struct {
	Type                  string                 `json:"type"`
	Raw                   json.RawMessage        `json:"raw"`
	TaskStarted           *TaskStarted           `json:"task_started,omitempty"`
	TaskComplete          *TaskComplete          `json:"task_complete,omitempty"`
	TurnAborted           *TurnAborted           `json:"turn_aborted,omitempty"`
	UserMessage           *UserMessage           `json:"user_message,omitempty"`
	AgentMessage          *AgentMessageEvent     `json:"agent_message,omitempty"`
	AgentReasoning        *AgentReasoningEvent   `json:"agent_reasoning,omitempty"`
	TokenCount            *TokenCount            `json:"token_count,omitempty"`
	ExecCommandEnd        *ExecCommandEnd        `json:"exec_command_end,omitempty"`
	PatchApplyEnd         *PatchApplyEnd         `json:"patch_apply_end,omitempty"`
	MCPToolCallEnd        *MCPToolCallEnd        `json:"mcp_tool_call_end,omitempty"`
	WebSearchEnd          *WebSearchEnd          `json:"web_search_end,omitempty"`
	SubAgentActivity      *SubAgentActivity      `json:"sub_agent_activity,omitempty"`
	ThreadSettingsApplied *ThreadSettingsApplied `json:"thread_settings_applied,omitempty"`
	ThreadRolledBack      *ThreadRolledBack      `json:"thread_rolled_back,omitempty"`
	ItemCompleted         *ItemCompleted         `json:"item_completed,omitempty"`
	ContextCompacted      bool                   `json:"context_compacted,omitempty"`
	ThreadGoalUpdated     *RawPayload            `json:"thread_goal_updated,omitempty"`
}

type TaskStarted struct {
	TurnID string `json:"turn_id"`
}

type TaskComplete struct {
	TurnID           string  `json:"turn_id"`
	LastAgentMessage *string `json:"last_agent_message,omitempty"`
}

type TurnAborted struct {
	TurnID string `json:"turn_id"`
	Reason string `json:"reason,omitempty"`
}

type UserMessage struct {
	Message      string          `json:"message"`
	Images       json.RawMessage `json:"images,omitempty"`
	LocalImages  json.RawMessage `json:"local_images,omitempty"`
	Audio        json.RawMessage `json:"audio,omitempty"`
	LocalAudio   json.RawMessage `json:"local_audio,omitempty"`
	TextElements json.RawMessage `json:"text_elements,omitempty"`
}

type AgentMessageEvent struct {
	Message string  `json:"message"`
	Phase   *string `json:"phase,omitempty"`
}

type AgentReasoningEvent struct {
	Text string `json:"text"`
}

type Usage struct {
	InputTokens           *int64 `json:"input_tokens,omitempty"`
	CachedInputTokens     *int64 `json:"cached_input_tokens,omitempty"`
	CacheWriteInputTokens *int64 `json:"cache_write_input_tokens,omitempty"`
	OutputTokens          *int64 `json:"output_tokens,omitempty"`
	ReasoningOutputTokens *int64 `json:"reasoning_output_tokens,omitempty"`
	TotalTokens           *int64 `json:"total_tokens,omitempty"`
}

type TokenInfo struct {
	TotalTokenUsage    Usage  `json:"total_token_usage"`
	LastTokenUsage     Usage  `json:"last_token_usage"`
	ModelContextWindow *int64 `json:"model_context_window,omitempty"`
}

type TokenCount struct {
	Info       *TokenInfo      `json:"info"`
	RateLimits json.RawMessage `json:"rate_limits,omitempty"`
}

type ExecCommandEnd struct {
	CallID           string          `json:"call_id"`
	TurnID           string          `json:"turn_id,omitempty"`
	Status           string          `json:"status,omitempty"`
	ExitCode         *int            `json:"exit_code,omitempty"`
	Command          json.RawMessage `json:"command,omitempty"`
	CWD              string          `json:"cwd,omitempty"`
	Stdout           string          `json:"stdout,omitempty"`
	Stderr           string          `json:"stderr,omitempty"`
	AggregatedOutput string          `json:"aggregated_output,omitempty"`
	FormattedOutput  string          `json:"formatted_output,omitempty"`
}

type PatchChange struct {
	Type        string  `json:"type"`
	UnifiedDiff string  `json:"unified_diff,omitempty"`
	MovePath    *string `json:"move_path,omitempty"`
}

type PatchApplyEnd struct {
	CallID  string                 `json:"call_id"`
	TurnID  string                 `json:"turn_id,omitempty"`
	Status  string                 `json:"status,omitempty"`
	Success *bool                  `json:"success,omitempty"`
	Stdout  string                 `json:"stdout,omitempty"`
	Stderr  string                 `json:"stderr,omitempty"`
	Changes map[string]PatchChange `json:"changes,omitempty"`
}

type MCPToolCallEnd struct {
	CallID     string          `json:"call_id"`
	Invocation json.RawMessage `json:"invocation,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
}

type WebSearchEnd struct {
	CallID  string          `json:"call_id,omitempty"`
	Query   string          `json:"query,omitempty"`
	Action  json.RawMessage `json:"action,omitempty"`
	Results json.RawMessage `json:"results,omitempty"`
}

type SubAgentActivity struct {
	EventID       string `json:"event_id"`
	AgentThreadID string `json:"agent_thread_id"`
	AgentPath     string `json:"agent_path"`
	Kind          string `json:"kind"`
}

type ThreadSettingsApplied struct {
	ThreadSettings json.RawMessage `json:"thread_settings"`
}

type ThreadRolledBack struct {
	NumTurns int `json:"num_turns"`
}

type ItemCompleted struct {
	ThreadID string          `json:"thread_id"`
	TurnID   string          `json:"turn_id"`
	Item     json.RawMessage `json:"item"`
}

// TokenUsageRecord is the top-level per-request usage record newer Codex
// versions write a few lines before the matching token_count event. Usage is
// the request's own usage (token_count's last_token_usage) and ThreadTokenUsage
// is the thread's running total (token_count's total_token_usage).
type TokenUsageRecord struct {
	ThreadID         string          `json:"thread_id,omitempty"`
	TurnID           string          `json:"turn_id,omitempty"`
	SessionID        string          `json:"session_id,omitempty"`
	RootTurnID       string          `json:"root_turn_id,omitempty"`
	ResponseID       string          `json:"response_id,omitempty"`
	Usage            Usage           `json:"usage"`
	TurnTokenUsage   Usage           `json:"turn_token_usage"`
	ThreadTokenUsage Usage           `json:"thread_token_usage"`
	Raw              json.RawMessage `json:"raw"`
}

type InterAgentMeta struct {
	TriggerTurn bool            `json:"trigger_turn"`
	Raw         json.RawMessage `json:"raw"`
}

type InterAgentCommunication struct {
	Author          string          `json:"author,omitempty"`
	Recipient       string          `json:"recipient,omitempty"`
	OtherRecipients []string        `json:"other_recipients,omitempty"`
	Content         json.RawMessage `json:"content,omitempty"`
	TriggerTurn     *bool           `json:"trigger_turn,omitempty"`
	Raw             json.RawMessage `json:"raw"`
}

func decodeTyped[T any](raw json.RawMessage) (*T, error) {
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func requireObject(raw json.RawMessage, what string) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return fmt.Errorf("%s is not a JSON object", what)
	}
	return nil
}
