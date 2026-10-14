package cursortranscript

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/baum-ai/baum/sessions/sourcepath"
)

const (
	BubbleTypeUser      = 1
	BubbleTypeAssistant = 2
)

// Header is one composerHeaders index row and its denormalized JSON value.
type Header struct {
	ComposerID    string      `json:"composer_id"`
	WorkspaceID   string      `json:"workspace_id,omitempty"`
	CreatedAt     int64       `json:"created_at,omitempty"`
	LastUpdatedAt int64       `json:"last_updated_at,omitempty"`
	IsArchived    bool        `json:"is_archived"`
	IsSubagent    bool        `json:"is_subagent"`
	Value         HeaderValue `json:"value"`
}

type HeaderValue struct {
	Name                string               `json:"name,omitempty"`
	WorkspaceIdentifier *WorkspaceIdentifier `json:"workspaceIdentifier,omitempty"`
}

type WorkspaceIdentifier struct {
	ID  string          `json:"id,omitempty"`
	URI ResourceURI     `json:"uri"`
	Raw json.RawMessage `json:"-"`
}

func (w *WorkspaceIdentifier) UnmarshalJSON(raw []byte) error {
	type fields WorkspaceIdentifier
	var decoded fields
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*w = WorkspaceIdentifier(decoded)
	w.Raw = append(w.Raw[:0], raw...)
	return nil
}

func (w WorkspaceIdentifier) MarshalJSON() ([]byte, error) {
	if len(w.Raw) > 0 && json.Valid(w.Raw) {
		return w.Raw, nil
	}
	type fields WorkspaceIdentifier
	return json.Marshal(fields(w))
}

type ResourceURI struct {
	FSPath string `json:"fsPath,omitempty"`
	Path   string `json:"path,omitempty"`
	Scheme string `json:"scheme,omitempty"`
}

func (u ResourceURI) FilePath() string {
	if u.FSPath != "" {
		return sourcepath.Normalize(u.FSPath)
	}
	if u.Path != "" && (u.Scheme == "" || u.Scheme == "file") {
		return sourcepath.Normalize(u.Path)
	}
	return ""
}

type BubbleRef struct {
	BubbleID string `json:"bubbleId"`
	Type     int    `json:"type"`
}

type ModelConfig struct {
	ModelName string          `json:"modelName,omitempty"`
	Raw       json.RawMessage `json:"-"`
}

func (m *ModelConfig) UnmarshalJSON(raw []byte) error {
	type fields ModelConfig
	var decoded fields
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*m = ModelConfig(decoded)
	m.Raw = append(m.Raw[:0], raw...)
	return nil
}

func (m ModelConfig) MarshalJSON() ([]byte, error) {
	if len(m.Raw) > 0 && json.Valid(m.Raw) {
		return m.Raw, nil
	}
	type fields ModelConfig
	return json.Marshal(fields(m))
}

// ComposerData is one composerData:<composer-id> snapshot.
type ComposerData struct {
	ComposerID           string               `json:"composerId"`
	Version              int                  `json:"_v"`
	Status               string               `json:"status"`
	Headers              []BubbleRef          `json:"fullConversationHeadersOnly"`
	CreatedAt            int64                `json:"createdAt"`
	LastUpdatedAt        *int64               `json:"lastUpdatedAt"`
	UnifiedMode          string               `json:"unifiedMode"`
	SubComposerIDs       []string             `json:"subComposerIds"`
	SubagentComposerIDs  []string             `json:"subagentComposerIds"`
	ModelConfig          *ModelConfig         `json:"modelConfig"`
	ContextTokensUsed    *int64               `json:"contextTokensUsed"`
	ContextTokenLimit    *int64               `json:"contextTokenLimit"`
	PromptTokenBreakdown json.RawMessage      `json:"promptTokenBreakdown"`
	WorkspaceIdentifier  *WorkspaceIdentifier `json:"workspaceIdentifier"`
	CreatedOnBranch      string               `json:"createdOnBranch"`
	CommittedToBranch    string               `json:"committedToBranch"`
	Name                 string               `json:"name"`
	AgentBackend         string               `json:"agentBackend"`
	CodeBlocks           CodeBlockData        `json:"codeBlockData,omitempty"`
}

func (d ComposerData) MarshalJSON() ([]byte, error) {
	d.CodeBlocks = nil
	type fields ComposerData
	return json.Marshal(fields(d))
}

type TokenCount struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

type ContextWindowStatus struct {
	TokensUsed int64 `json:"tokensUsed"`
	TokenLimit int64 `json:"tokenLimit"`
}

func (t *TokenCount) HasUsage() bool {
	return t != nil && (t.InputTokens != 0 || t.OutputTokens != 0)
}

type ModelInfo struct {
	ModelName string `json:"modelName"`
}

type AdditionalData struct {
	Status             string          `json:"status,omitempty"`
	TerminationReason  string          `json:"terminationReason,omitempty"`
	SubagentComposerID string          `json:"subagentComposerId,omitempty"`
	Raw                json.RawMessage `json:"-"`
}

func (a *AdditionalData) UnmarshalJSON(raw []byte) error {
	type fields AdditionalData
	var decoded fields
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*a = AdditionalData(decoded)
	a.Raw = append(a.Raw[:0], raw...)
	return nil
}

func (a AdditionalData) MarshalJSON() ([]byte, error) {
	if len(a.Raw) > 0 && json.Valid(a.Raw) {
		return a.Raw, nil
	}
	type fields AdditionalData
	return json.Marshal(fields(a))
}

// ToolRecord is Cursor's call-and-result record embedded in one bubble.
type ToolRecord struct {
	Tool           json.RawMessage `json:"tool"`
	Name           *string         `json:"name"`
	ToolCallID     string          `json:"toolCallId"`
	ModelCallID    string          `json:"modelCallId"`
	Status         string          `json:"status"`
	Params         json.RawMessage `json:"params"`
	RawArgs        json.RawMessage `json:"rawArgs"`
	Result         json.RawMessage `json:"result"`
	ToolIndex      *int            `json:"toolIndex"`
	UserDecision   string          `json:"userDecision"`
	Error          json.RawMessage `json:"error"`
	AdditionalData *AdditionalData `json:"additionalData"`
}

func (t *ToolRecord) Degenerate() bool {
	return t == nil || t.Name == nil || *t.Name == ""
}

func (t *ToolRecord) ToolName() string {
	if t == nil || t.Name == nil {
		return ""
	}
	return *t.Name
}

// Bubble is one bubbleId:<composer-id>:<bubble-id> record.
type Bubble struct {
	Key                           string               `json:"key"`
	ComposerID                    string               `json:"composer_id"`
	BubbleID                      string               `json:"bubbleId"`
	Type                          int                  `json:"type"`
	Version                       int                  `json:"_v"`
	Text                          string               `json:"text"`
	RichText                      json.RawMessage      `json:"richText"`
	Thinking                      json.RawMessage      `json:"thinking"`
	TokenCount                    *TokenCount          `json:"tokenCount"`
	ModelInfo                     *ModelInfo           `json:"modelInfo"`
	CapabilityType                *int                 `json:"capabilityType"`
	SubagentSpawnTaskToolCallID   string               `json:"subagentSpawnTaskToolCallId"`
	CreatedAt                     string               `json:"createdAt"`
	ErrorDetails                  json.RawMessage      `json:"errorDetails"`
	Context                       json.RawMessage      `json:"context"`
	ContextWindowStatusAtCreation *ContextWindowStatus `json:"contextWindowStatusAtCreation"`
	Tool                          *ToolRecord          `json:"toolFormerData"`
}

func DecodeComposerData(raw []byte) (*ComposerData, error) {
	if isNull(raw) {
		return nil, nil
	}
	var data ComposerData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("decoding composer data: %w", err)
	}
	return &data, nil
}

func DecodeBubble(raw []byte) (*Bubble, error) {
	if isNull(raw) {
		return nil, nil
	}
	var discriminant struct {
		Type *int `json:"type"`
	}
	if err := json.Unmarshal(raw, &discriminant); err != nil {
		return nil, fmt.Errorf("decoding bubble: %w", err)
	}
	if discriminant.Type == nil {
		return nil, nil
	}
	var bubble Bubble
	if err := json.Unmarshal(raw, &bubble); err != nil {
		return nil, fmt.Errorf("decoding bubble: %w", err)
	}
	switch bubble.Type {
	case BubbleTypeUser, BubbleTypeAssistant:
	default:
		return nil, fmt.Errorf("unknown bubble type %d", bubble.Type)
	}
	return &bubble, nil
}

func decodeHeaderValue(raw []byte) (HeaderValue, error) {
	if isNull(raw) {
		return HeaderValue{}, nil
	}
	var value HeaderValue
	if err := json.Unmarshal(raw, &value); err != nil {
		return HeaderValue{}, fmt.Errorf("decoding composer header value: %w", err)
	}
	return value, nil
}

func isNull(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}
