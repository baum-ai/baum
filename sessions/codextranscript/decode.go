package codextranscript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/baumai/baum/sessions/traceprocessing"
)

// firstLineBufferBytes is the read buffer DecodeFirst starts with. Decode needs
// a large buffer because it reads every record of a rollout, but DecodeFirst
// reads one leading session_meta record, and store discovery calls it once per
// rollout in the store — a 1 MiB buffer per file made allocation, not reading,
// the largest cost of scanning the Codex store. readLine accumulates across
// bufio.ErrBufferFull, so this is a starting size, not a line limit.
const firstLineBufferBytes = 64 << 10

type rawEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Ordinal   *int            `json:"ordinal"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

var ErrIncompleteFirstLine = errors.New("incomplete first rollout line")

// DecodeFirst decodes only the first non-empty record.
func DecodeFirst(r io.Reader) (Line, error) {
	reader := bufio.NewReaderSize(r, firstLineBufferBytes)
	for {
		raw, readErr := traceprocessing.ReadJSONLLine(reader)
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 {
			line, outerOrdinal, err := decodeLine(trimmed, 1)
			if err != nil {
				if errors.Is(readErr, io.EOF) && !json.Valid(trimmed) {
					return Line{}, ErrIncompleteFirstLine
				}
				return Line{}, err
			}
			if line.SessionMeta == nil {
				return Line{}, fmt.Errorf("rollout does not start with session_meta")
			}
			if err := validateOuterOrdinal(line.Ordinal, outerOrdinal, line.SessionMeta.HistoryMode == "paginated"); err != nil {
				return Line{}, err
			}
			return line, nil
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return Line{}, ErrIncompleteFirstLine
			}
			return Line{}, readErr
		}
	}
}

// Decode reads a JSONL rollout.
func Decode(r io.Reader) (lines []Line, torn bool, err error) {
	paginated := false
	first := true
	return traceprocessing.ScanJSONL(r, func(raw []byte, ordinal int) (Line, error) {
		line, outerOrdinal, err := decodeLine(raw, ordinal)
		if err != nil {
			return Line{}, err
		}
		if first && line.SessionMeta != nil {
			paginated = line.SessionMeta.HistoryMode == "paginated"
		}
		if err := validateOuterOrdinal(line.Ordinal, outerOrdinal, paginated); err != nil {
			return Line{}, err
		}
		first = false
		return line, nil
	})
}

func decodeLine(raw []byte, ordinal int) (Line, *int, error) {
	if raw[0] != '{' {
		return Line{}, nil, fmt.Errorf("line %d is not a JSON object", ordinal)
	}
	var envelope rawEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Line{}, nil, fmt.Errorf("line %d: %w", ordinal, err)
	}
	if envelope.Type == "" {
		return Line{}, nil, fmt.Errorf("line %d has no type", ordinal)
	}
	if err := requireObject(envelope.Payload, fmt.Sprintf("line %d payload", ordinal)); err != nil {
		return Line{}, nil, err
	}
	timestamp, err := time.Parse(time.RFC3339Nano, envelope.Timestamp)
	if err != nil {
		return Line{}, nil, fmt.Errorf("line %d has invalid timestamp %q: %w", ordinal, envelope.Timestamp, err)
	}
	line := Line{
		Ordinal: ordinal, Timestamp: timestamp.UTC(), Type: envelope.Type,
		Class: Class(envelope.Type), Raw: cloneRaw(raw),
		Payload: cloneRaw(envelope.Payload),
	}

	switch line.Class {
	case ClassSessionMeta:
		line.SessionMeta, err = decodeSessionMeta(envelope.Payload)
	case ClassTurnContext:
		line.TurnContext, err = decodeTurnContext(envelope.Payload)
	case ClassResponseItem:
		line.ResponseItem, err = decodeResponseItem(envelope.Payload)
	case ClassEventMsg:
		line.EventMsg, err = decodeEventMsg(envelope.Payload)
	case ClassCompacted:
		line.Compacted = &RawPayload{Raw: cloneRaw(envelope.Payload)}
	case ClassWorldState:
		line.WorldState = &RawPayload{Raw: cloneRaw(envelope.Payload)}
	case ClassInterAgentMetadata:
		line.InterAgentMetadata, err = decodeTyped[InterAgentMeta](envelope.Payload)
		if line.InterAgentMetadata != nil {
			line.InterAgentMetadata.Raw = cloneRaw(envelope.Payload)
		}
	case ClassInterAgentCommunication:
		line.InterAgentCommunication, err = decodeTyped[InterAgentCommunication](envelope.Payload)
		if line.InterAgentCommunication != nil {
			line.InterAgentCommunication.Raw = cloneRaw(envelope.Payload)
		}
	case ClassTokenUsageRecord:
		line.TokenUsageRecord, err = decodeTyped[TokenUsageRecord](envelope.Payload)
		if line.TokenUsageRecord != nil {
			line.TokenUsageRecord.Raw = cloneRaw(envelope.Payload)
		}
	case ClassRealtime:
		line.Realtime = &RawPayload{Raw: cloneRaw(envelope.Payload)}
	default:
		return Line{}, nil, fmt.Errorf("line %d has unknown type %q", ordinal, envelope.Type)
	}
	if err != nil {
		return Line{}, nil, fmt.Errorf("line %d %s: %w", ordinal, envelope.Type, err)
	}
	return line, envelope.Ordinal, nil
}

func validateOuterOrdinal(ordinal int, outer *int, paginated bool) error {
	if !paginated {
		if outer != nil {
			return fmt.Errorf("line %d uses a paginated ordinal in a legacy rollout", ordinal)
		}
		return nil
	}
	if outer == nil {
		return fmt.Errorf("line %d has no paginated ordinal", ordinal)
	}
	if want := ordinal - 1; *outer != want {
		return fmt.Errorf("line %d has paginated ordinal %d, want %d", ordinal, *outer, want)
	}
	return nil
}

func decodeSessionMeta(raw json.RawMessage) (*SessionMeta, error) {
	var payload struct {
		ID                          string          `json:"id"`
		SessionID                   string          `json:"session_id"`
		ParentThreadID              string          `json:"parent_thread_id"`
		ForkedFromID                string          `json:"forked_from_id"`
		Timestamp                   string          `json:"timestamp"`
		CWD                         string          `json:"cwd"`
		CLIVersion                  string          `json:"cli_version"`
		Source                      json.RawMessage `json:"source"`
		ThreadSource                string          `json:"thread_source"`
		AgentPath                   string          `json:"agent_path"`
		AgentNickname               string          `json:"agent_nickname"`
		AgentRole                   string          `json:"agent_role"`
		ModelProvider               *string         `json:"model_provider"`
		HistoryMode                 string          `json:"history_mode"`
		HistoryBase                 json.RawMessage `json:"history_base"`
		SubagentHistoryStartOrdinal *int            `json:"subagent_history_start_ordinal"`
		MultiAgentVersion           *string         `json:"multi_agent_version"`
		Git                         *GitInfo        `json:"git"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	if payload.ID == "" {
		return nil, fmt.Errorf("payload has no id")
	}
	if len(payload.HistoryBase) > 0 && !bytes.Equal(bytes.TrimSpace(payload.HistoryBase), []byte("null")) {
		return nil, fmt.Errorf("external history_base is unsupported")
	}
	if payload.HistoryMode != "" && payload.HistoryMode != "legacy" && payload.HistoryMode != "paginated" {
		return nil, fmt.Errorf("unknown history_mode %q", payload.HistoryMode)
	}
	source, err := decodeSource(payload.Source)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	meta := &SessionMeta{
		ID: payload.ID, SessionID: payload.SessionID, ParentThreadID: payload.ParentThreadID,
		ForkedFromID: payload.ForkedFromID, CWD: payload.CWD, CLIVersion: payload.CLIVersion,
		Source: source, ThreadSource: payload.ThreadSource, AgentPath: payload.AgentPath,
		AgentNickname: payload.AgentNickname, AgentRole: payload.AgentRole,
		ModelProvider: payload.ModelProvider, HistoryMode: payload.HistoryMode,
		HistoryBase: payload.HistoryBase, SubagentHistoryStartOrdinal: payload.SubagentHistoryStartOrdinal,
		MultiAgentVersion: payload.MultiAgentVersion, Git: payload.Git,
		Raw: cloneRaw(raw),
	}
	if meta.SessionID == "" {
		meta.SessionID = meta.ID
	}
	if meta.HistoryMode == "" {
		meta.HistoryMode = "legacy"
	}
	if meta.AgentPath == "" && source.ThreadSpawn != nil && source.ThreadSpawn.AgentPath != nil {
		meta.AgentPath = *source.ThreadSpawn.AgentPath
	}
	if payload.Timestamp != "" {
		parsed, err := time.Parse(time.RFC3339Nano, payload.Timestamp)
		if err != nil {
			return nil, fmt.Errorf("timestamp %q: %w", payload.Timestamp, err)
		}
		parsed = parsed.UTC()
		meta.Timestamp = &parsed
	}
	return meta, nil
}

func decodeTurnContext(raw json.RawMessage) (*TurnContext, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	return &TurnContext{Fields: fields, Raw: cloneRaw(raw)}, nil
}

func decodeResponseItem(raw json.RawMessage) (*ResponseItem, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, err
	}
	item := &ResponseItem{Type: head.Type, Raw: cloneRaw(raw)}
	var err error
	switch head.Type {
	case "message":
		item.Message, err = decodeTyped[Message](raw)
	case "agent_message":
		item.AgentMessage, err = decodeTyped[AgentMessage](raw)
	case "reasoning":
		item.Reasoning, err = decodeTyped[Reasoning](raw)
	case "function_call":
		item.FunctionCall, err = decodeTyped[FunctionCall](raw)
	case "function_call_output":
		item.FunctionOutput, err = decodeTyped[FunctionCallOutput](raw)
	case "custom_tool_call":
		item.CustomCall, err = decodeTyped[CustomToolCall](raw)
	case "custom_tool_call_output":
		item.CustomOutput, err = decodeTyped[CustomToolCallOutput](raw)
	case "tool_search_call":
		item.ToolSearchCall, err = decodeTyped[ToolSearchCall](raw)
	case "tool_search_output":
		item.ToolSearchOutput, err = decodeTyped[ToolSearchOutput](raw)
	case "web_search_call":
		item.WebSearchCall, err = decodeTyped[WebSearchCall](raw)
	case "image_generation_call":
		item.ImageGeneration = &RawPayload{Raw: cloneRaw(raw)}
	case "compaction":
		item.Compaction = &RawPayload{Raw: cloneRaw(raw)}
	default:
		return nil, fmt.Errorf("unknown response_item type %q", head.Type)
	}
	return item, err
}

func decodeEventMsg(raw json.RawMessage) (*EventMsg, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, err
	}
	event := &EventMsg{Type: head.Type, Raw: cloneRaw(raw)}
	var err error
	switch head.Type {
	case "task_started":
		event.TaskStarted, err = decodeTyped[TaskStarted](raw)
	case "task_complete":
		event.TaskComplete, err = decodeTyped[TaskComplete](raw)
	case "turn_aborted":
		event.TurnAborted, err = decodeTyped[TurnAborted](raw)
	case "user_message":
		event.UserMessage, err = decodeTyped[UserMessage](raw)
	case "agent_message":
		event.AgentMessage, err = decodeTyped[AgentMessageEvent](raw)
	case "agent_reasoning":
		event.AgentReasoning, err = decodeTyped[AgentReasoningEvent](raw)
	case "token_count":
		event.TokenCount, err = decodeTyped[TokenCount](raw)
	case "exec_command_end":
		event.ExecCommandEnd, err = decodeTyped[ExecCommandEnd](raw)
	case "patch_apply_end":
		event.PatchApplyEnd, err = decodeTyped[PatchApplyEnd](raw)
	case "mcp_tool_call_end":
		event.MCPToolCallEnd, err = decodeTyped[MCPToolCallEnd](raw)
	case "web_search_end":
		event.WebSearchEnd, err = decodeTyped[WebSearchEnd](raw)
	case "sub_agent_activity":
		event.SubAgentActivity, err = decodeTyped[SubAgentActivity](raw)
	case "thread_settings_applied":
		event.ThreadSettingsApplied, err = decodeTyped[ThreadSettingsApplied](raw)
	case "thread_rolled_back":
		event.ThreadRolledBack, err = decodeTyped[ThreadRolledBack](raw)
	case "item_completed":
		event.ItemCompleted, err = decodeTyped[ItemCompleted](raw)
	case "context_compacted":
		event.ContextCompacted = true
	case "thread_goal_updated":
		event.ThreadGoalUpdated = &RawPayload{Raw: cloneRaw(raw)}
	default:
		return nil, fmt.Errorf("unknown event_msg type %q", head.Type)
	}
	return event, err
}

func ReadFile(path string) (*File, error) {
	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	return readFile(os.DirFS(dir), name, path)
}

func readFile(fsys fs.FS, name, label string) (*File, error) {
	handle, err := fsys.Open(name)
	if err != nil {
		return nil, fmt.Errorf("opening rollout %s: %w", label, err)
	}
	defer func() { _ = handle.Close() }()
	return Read(handle, label)
}

// Read decodes and validates one rollout from a reader.
func Read(reader io.Reader, label string) (*File, error) {
	lines, torn, err := Decode(reader)
	if err != nil {
		return nil, fmt.Errorf("reading rollout %s: %w", label, err)
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("rollout %s is empty", label)
	}
	if lines[0].SessionMeta == nil {
		return nil, fmt.Errorf("rollout %s does not start with session_meta", label)
	}
	return &File{Path: label, Meta: lines[0].SessionMeta, Lines: lines, TornFinalLine: torn}, nil
}
