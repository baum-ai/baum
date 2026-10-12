// Package claudetranscript reads Claude Code transcripts as typed, classified
// lines.
package claudetranscript

import (
	"encoding/json"
	"time"
)

// Class is the line class.
type Class string

const (
	ClassUser           Class = "user"
	ClassAssistant      Class = "assistant"
	ClassSystem         Class = "system"
	ClassAttachment     Class = "attachment"
	ClassProgress       Class = "progress"
	ClassControl        Class = "control"
	ClassUnknownControl Class = "unknown_control"
)

// UserKind discriminates what a `user` line carries.
type UserKind string

const (
	UserText        UserKind = "user_text"
	UserToolResults UserKind = "user_tool_results"
	UserMixed       UserKind = "user_mixed"
	UserOther       UserKind = "user_other"
)

// envelopeTypes are the classes that carry a uuid and take part in the parentUuid chain.
var envelopeTypes = map[string]Class{
	"user":       ClassUser,
	"assistant":  ClassAssistant,
	"system":     ClassSystem,
	"attachment": ClassAttachment,
	"progress":   ClassProgress,
}

// controlTypes are the non-envelope records we recognise.
var controlTypes = map[string]bool{
	"last-prompt":           true,
	"mode":                  true,
	"permission-mode":       true,
	"ai-title":              true,
	"custom-title":          true,
	"agent-name":            true,
	"bridge-session":        true,
	"queue-operation":       true,
	"file-history-snapshot": true,
	"file-history-delta":    true,
	"worktree-state":        true,
	"relocated":             true,
	"pr-link":               true,
}

// Line is one JSONL record, classified and decoded as far as the model defines.
type Line struct {
	Ordinal  int
	Class    Class
	Type     string
	UserKind UserKind
	Raw      json.RawMessage

	UUID       string
	ParentUUID string
	SessionID  string
	AgentID    string
	PromptID   string
	RequestID  string
	Timestamp  *time.Time

	IsSidechain bool
	IsMeta      bool
	CWD         string
	GitBranch   string
	Version     string

	// Subtype is the `system` line's subtype.
	Subtype string

	// PromptSource, Origin and ToolDenialKind appear on `user` lines. The first
	// two say whether a person typed the text or the harness injected it; the
	// third says why a tool was refused.
	PromptSource   string
	Origin         json.RawMessage
	ToolDenialKind string

	// ToolUseResult is the structured payload beside a tool result.
	ToolUseResult json.RawMessage

	// Message is the model message on `user` and `assistant` lines.
	Message *Message

	// Control carries the payload of a control line.
	Control Control
	// Attachment carries the small typed subset of attachment payloads.
	Attachment Attachment
}

// Envelope reports whether this line takes part in the uuid chain.
func (l *Line) Envelope() bool {
	switch l.Class {
	case ClassUser, ClassAssistant, ClassSystem, ClassAttachment, ClassProgress:
		return true
	}
	return false
}

// Message is one model message.
type Message struct {
	ID         string
	Role       string
	Model      string
	StopReason *string
	Usage      *Usage
	Content    json.RawMessage
}

// Usage is the token accounting of one reply: four disjoint buckets, plus the
// cache-write TTL split and the service speed when the transcript reports them.
type Usage struct {
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64

	// CacheWrite1h is usage.cache_creation.ephemeral_1h_input_tokens. Nil means
	// the transcript did not report it (no cache_creation object, or a value
	// that is not an integer).
	CacheWrite1h *int64
	// Speed is usage.speed verbatim ("standard", "fast"); empty when absent.
	Speed string
}

// Control is the union of the control-line payloads.
type Control struct {
	Mode            string
	PermissionMode  string
	AITitle         string
	AgentName       string
	LeafUUID        string
	LastPrompt      string
	BridgeSessionID string
	LastSequenceNum *int64
	Operation       string
	Content         string
	Value           json.RawMessage
}

// Attachment is the typed envelope payload used by queued_command.
type Attachment struct {
	Type        string
	Prompt      string
	CommandMode string
	Origin      json.RawMessage
	Timestamp   *time.Time
}

// Block is one element of a message's content array.
type Block struct {
	Type string
	Text string

	// tool_use
	ID    string
	Name  string
	Input json.RawMessage

	// tool_result
	ToolUseID string
	IsError   *bool
	Content   json.RawMessage

	Raw json.RawMessage
}

// Blocks decodes the content array. A string payload has no blocks.
func (m *Message) Blocks() []Block {
	if m == nil || len(m.Content) == 0 || m.Content[0] != '[' {
		return nil
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(m.Content, &raws); err != nil {
		return nil
	}
	blocks := make([]Block, 0, len(raws))
	for _, raw := range raws {
		var b struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   *bool           `json:"is_error"`
			Content   json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(raw, &b); err != nil {
			blocks = append(blocks, Block{Raw: raw})
			continue
		}
		blocks = append(blocks, Block{
			Type: b.Type, Text: b.Text, ID: b.ID, Name: b.Name, Input: b.Input,
			ToolUseID: b.ToolUseID, IsError: b.IsError, Content: b.Content, Raw: raw,
		})
	}
	return blocks
}

// Text is the message's text.
func (m *Message) Text() string {
	if m == nil || len(m.Content) == 0 {
		return ""
	}
	if m.Content[0] == '"' {
		var s string
		if err := json.Unmarshal(m.Content, &s); err == nil {
			return s
		}
		return ""
	}
	return joinBlockText(m.Blocks())
}

// ResultText is a tool result block's text.
func (b Block) ResultText() string {
	if len(b.Content) == 0 {
		return ""
	}
	if b.Content[0] == '"' {
		var s string
		if err := json.Unmarshal(b.Content, &s); err == nil {
			return s
		}
		return ""
	}
	inner := Message{Content: b.Content}
	return joinBlockText(inner.Blocks())
}

func joinBlockText(blocks []Block) string {
	out := ""
	for _, b := range blocks {
		if b.Type != "text" || b.Text == "" {
			continue
		}
		if out != "" {
			out += "\n"
		}
		out += b.Text
	}
	return out
}

// classifyUserContent implements the discriminant, with one correction:
// an array whose every block is text is text, not unclassified content.
func classifyUserContent(content json.RawMessage) UserKind {
	if len(content) == 0 {
		return UserOther
	}
	switch content[0] {
	case '"':
		return UserText
	case '[':
	default:
		return UserOther
	}

	message := Message{Content: content}
	blocks := message.Blocks()
	if len(blocks) == 0 {
		return UserOther
	}
	results, texts, others := 0, 0, 0
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			results++
		case "text":
			texts++
		default:
			others++
		}
	}
	switch {
	case results > 0 && texts == 0 && others == 0:
		return UserToolResults
	case results > 0:
		return UserMixed
	case texts > 0 && others == 0:
		return UserText
	default:
		return UserOther
	}
}
