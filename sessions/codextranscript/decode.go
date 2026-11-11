package codextranscript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"github.com/baum-ai/baum/sessions/traceprocessing"
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

// errUnknownType is a record class or item type this reader does not know.
// decodeLine keeps such a line, marked Unknown, rather than failing the
// rollout: Codex adds types between releases.
var errUnknownType = errors.New("unknown type")

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
			if err := validateOuterOrdinal(line, outerOrdinal, line.SessionMeta.HistoryMode == "paginated"); err != nil {
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
	var order ordering
	return traceprocessing.ScanJSONL(r, order.decode)
}

// ordering is what checking a line's envelope ordinal needs from the lines
// before it: whether one has been read, and whether the first was the
// session_meta of a paginated rollout.
type ordering struct {
	started, paginated bool
}

// decode decodes one line and checks its envelope ordinal.
func (o *ordering) decode(raw []byte, ordinal int) (Line, error) {
	line, outerOrdinal, err := decodeLine(raw, ordinal)
	if err != nil {
		return Line{}, err
	}
	if !o.started && line.SessionMeta != nil {
		o.paginated = line.SessionMeta.HistoryMode == "paginated"
	}
	if err := validateOuterOrdinal(line, outerOrdinal, o.paginated); err != nil {
		return Line{}, err
	}
	o.started = true
	return line, nil
}

// Reading is one rollout decoded up to a byte offset, kept so that a rollout
// that grew is read from where the last read stopped rather than from its
// start. Continue decodes what was appended; File returns the rollout as a
// whole read of the same bytes would. The envelope ordinal check keeps what
// the first line said about pagination across reads.
//
// A Reading leaves out the copies of each record that the mapper never reads
// (Line.Raw, Line.Payload but on event_msg lines, and the Raw of response
// items, events and turn contexts), so the lines it keeps take a fraction of
// the memory. A Reading is not safe for concurrent use.
type Reading struct {
	label string
	lines []Line
	next  int
	order ordering
	size  int64
	sum   uint32
}

// NewReading starts reading a rollout, reported as label.
func NewReading(label string) *Reading {
	return &Reading{label: label, next: 1}
}

// Continue decodes the lines src holds that end in a newline. src continues
// the rollout where the previous call's consumed bytes ended. A final line
// without a newline is not consumed: it is returned as rest, for File, and
// the next call reads it again from its first byte. After an error the
// Reading must be dropped.
func (r *Reading) Continue(src io.Reader) (consumed int64, rest []byte, err error) {
	consumed, next, rest, err := traceprocessing.ScanWholeJSONL(src, r.next, func(raw []byte, ordinal int) error {
		line, err := r.order.decode(raw, ordinal)
		if err != nil {
			return err
		}
		line = slim(line)
		r.lines = append(r.lines, line)
		// The typed payload takes about as much as the record it was decoded
		// from.
		r.size += int64(unsafe.Sizeof(line)) + int64(len(raw)) + int64(len(line.Payload))
		return nil
	}, func(consumed []byte) {
		r.sum = crc32.Update(r.sum, traceprocessing.Castagnoli, consumed)
	})
	if err != nil {
		return 0, nil, fmt.Errorf("reading rollout %s: %w", r.label, err)
	}
	r.next = next
	return consumed, rest, nil
}

// Lines is the number of lines read so far.
func (r *Reading) Lines() int { return len(r.lines) }

// Sum is the CRC-32C (traceprocessing.Castagnoli) of the bytes the lines read
// so far were decoded from: every byte Continue consumed.
func (r *Reading) Sum() uint32 { return r.sum }

// Bytes estimates the memory the lines read so far take.
func (r *Reading) Bytes() int64 { return r.size }

// File is the rollout read so far, followed by rest, the final line the last
// Continue left unread, decoded as Decode decodes a final line. The Reading
// is unchanged, and the File shares its lines, so neither may be changed. A
// final line that decodes is added to a copy of the lines: the spare capacity
// after them is where the next Continue appends, while the File may still be
// mapped. Codex ends every record it writes with a newline, so rest is
// normally empty or torn and nothing is copied.
func (r *Reading) File(rest []byte) (*File, error) {
	lines := r.lines[:len(r.lines):len(r.lines)]
	order := r.order
	line, ok, torn, err := traceprocessing.DecodeFinalJSONL(rest, r.next, order.decode)
	if err != nil {
		return nil, fmt.Errorf("reading rollout %s: %w", r.label, err)
	}
	if ok {
		lines = append(lines, slim(line))
	}
	return newFile(lines, torn, r.label)
}

// slim drops the copies of the record a Reading does not keep. Payload stays
// on the lines codexsession stores as tool completions: event_msg lines, and
// web_search_call response items, which are their own completion. Every other
// record is read through its typed fields.
func slim(line Line) Line {
	line.Raw = nil
	if line.Class != ClassEventMsg && (line.ResponseItem == nil || line.ResponseItem.WebSearchCall == nil) {
		line.Payload = nil
	}
	if line.ResponseItem != nil {
		item := *line.ResponseItem
		item.Raw = nil
		line.ResponseItem = &item
	}
	if line.EventMsg != nil {
		event := *line.EventMsg
		event.Raw = nil
		line.EventMsg = &event
	}
	if line.TurnContext != nil {
		context := *line.TurnContext
		context.Raw = nil
		line.TurnContext = &context
	}
	return line
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
		err = errUnknownType
	}
	if errors.Is(err, errUnknownType) {
		line.Unknown, err = true, nil
	}
	if err != nil {
		return Line{}, nil, fmt.Errorf("line %d %s: %w", ordinal, envelope.Type, err)
	}
	return line, envelope.Ordinal, nil
}

func validateOuterOrdinal(line Line, outer *int, paginated bool) error {
	ordinal := line.Ordinal
	if !paginated {
		if outer != nil {
			return fmt.Errorf("line %d uses a paginated ordinal in a legacy rollout", ordinal)
		}
		return nil
	}
	if outer == nil {
		// Codex omits envelope ordinals on reasoning and compaction records
		// in otherwise paginated rollouts. They still occupy their physical
		// line; every supplied ordinal must agree with that position.
		if line.Compacted != nil || (line.ResponseItem != nil && line.ResponseItem.Reasoning != nil) {
			return nil
		}
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
	if head.Type == "" {
		return nil, fmt.Errorf("payload has no type")
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
	case "additional_tools":
		item.AdditionalTools = &RawPayload{Raw: cloneRaw(raw)}
	case "configuration_update":
		item.ConfigurationUpdate = &RawPayload{Raw: cloneRaw(raw)}
	default:
		return item, errUnknownType
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
	if head.Type == "" {
		return nil, fmt.Errorf("payload has no type")
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
		return event, errUnknownType
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
	return newFile(lines, torn, label)
}

// newFile checks decoded lines as one rollout.
func newFile(lines []Line, torn bool, label string) (*File, error) {
	if len(lines) == 0 {
		return nil, fmt.Errorf("rollout %s is empty", label)
	}
	if lines[0].SessionMeta == nil {
		return nil, fmt.Errorf("rollout %s does not start with session_meta", label)
	}
	return &File{Path: label, Meta: lines[0].SessionMeta, Lines: lines, TornFinalLine: torn}, nil
}
