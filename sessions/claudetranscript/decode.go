package claudetranscript

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
	"unsafe"

	"github.com/baum-ai/baum/sessions/traceprocessing"
)

// Decode reads a transcript as classified lines.
func Decode(r io.Reader) (lines []Line, torn bool, err error) {
	seenUUID := map[string]int{}
	seenToolUse := map[string]int{}
	return traceprocessing.ScanJSONL(r, func(raw []byte, ordinal int) (Line, error) {
		line, err := decodeLine(raw, ordinal)
		if err != nil {
			return Line{}, err
		}
		if err := trackIDs(line, ordinal, seenUUID, seenToolUse); err != nil {
			return Line{}, err
		}
		return line, nil
	})
}

// trackIDs rejects the id collisions that would make a join ambiguous: a uuid
// repeated inside one file, or two tool calls sharing an id means an
// assumption the mapping relies on has stopped holding.
func trackIDs(line Line, ordinal int, seenUUID, seenToolUse map[string]int) error {
	if line.UUID != "" {
		if first, dup := seenUUID[line.UUID]; dup {
			return fmt.Errorf("line %d repeats the uuid of line %d (%s)", ordinal, first, line.UUID)
		}
		seenUUID[line.UUID] = ordinal
	}
	if line.Class != ClassAssistant {
		return nil
	}
	for _, block := range line.Message.Blocks() {
		if block.Type != "tool_use" || block.ID == "" {
			continue
		}
		if first, dup := seenToolUse[block.ID]; dup {
			return fmt.Errorf("line %d repeats tool use id %s from line %d", ordinal, block.ID, first)
		}
		seenToolUse[block.ID] = ordinal
	}
	return nil
}

// untrackIDs forgets the ids trackIDs recorded for the line at ordinal, so a
// line decoded only to look at it leaves the record as it was.
func untrackIDs(line Line, ordinal int, seenUUID, seenToolUse map[string]int) {
	if at, ok := seenUUID[line.UUID]; ok && at == ordinal {
		delete(seenUUID, line.UUID)
	}
	if line.Class != ClassAssistant {
		return
	}
	for _, block := range line.Message.Blocks() {
		if at, ok := seenToolUse[block.ID]; ok && at == ordinal {
			delete(seenToolUse, block.ID)
		}
	}
}

// Reading is one transcript decoded up to a byte offset, kept so that a
// transcript that grew is read from where the last read stopped rather than
// from its start. Continue decodes what was appended; File returns the
// transcript as a whole read of the same bytes would. The id checks Decode
// makes run over the whole transcript, earlier reads included.
//
// A Reading leaves out Line.Raw, which only Survey reads, so the lines it
// keeps take about half the memory. A Reading is not safe for concurrent use.
type Reading struct {
	name, label, agentID string
	lines                []Line
	next                 int
	seenUUID             map[string]int
	seenToolUse          map[string]int
	size                 int64
}

// NewReading starts reading the transcript called name (its base name, which
// can carry the session id), reported as label, of the subagent agentID or of
// the main thread when agentID is empty.
func NewReading(name, label, agentID string) *Reading {
	return &Reading{name: name, label: label, agentID: agentID, next: 1, seenUUID: map[string]int{}, seenToolUse: map[string]int{}}
}

// Continue decodes the lines r holds that end in a newline. r continues the
// transcript where the previous call's consumed bytes ended. A final line
// without a newline is not consumed: it is returned as rest, for File, and
// the next call reads it again from its first byte. After an error the
// Reading must be dropped.
func (r *Reading) Continue(src io.Reader) (consumed int64, rest []byte, err error) {
	consumed, next, rest, err := traceprocessing.ScanWholeJSONL(src, r.next, func(raw []byte, ordinal int) error {
		line, err := decodeLine(raw, ordinal)
		if err != nil {
			return err
		}
		if err := trackIDs(line, ordinal, r.seenUUID, r.seenToolUse); err != nil {
			return err
		}
		line.Raw = nil
		r.lines = append(r.lines, line)
		r.size += lineBytes(&line)
		return nil
	})
	if err != nil {
		return 0, nil, fmt.Errorf("reading transcript %s: %w", r.label, err)
	}
	r.next = next
	return consumed, rest, nil
}

// Lines is the number of lines read so far.
func (r *Reading) Lines() int { return len(r.lines) }

// Bytes estimates the memory the lines read so far take.
func (r *Reading) Bytes() int64 { return r.size }

// lineBytes estimates the memory a decoded line without Raw takes, and the
// two id records' entries for it.
func lineBytes(l *Line) int64 {
	n := unsafe.Sizeof(*l) + uintptr(len(l.Type)+len(l.UUID)+len(l.ParentUUID)+len(l.SessionID)+
		len(l.AgentID)+len(l.PromptID)+len(l.RequestID)+len(l.CWD)+len(l.GitBranch)+len(l.Version)+
		len(l.Subtype)+len(l.PromptSource)+len(l.Origin)+len(l.ToolDenialKind)+len(l.ToolUseResult)+
		len(l.Control.Content)+len(l.Control.Value)+len(l.Control.LastPrompt)+len(l.Control.AITitle)+
		len(l.Attachment.Prompt)+len(l.Attachment.Origin))
	if l.UUID != "" {
		n += 64
	}
	if m := l.Message; m != nil {
		n += unsafe.Sizeof(*m) + uintptr(len(m.ID)+len(m.Role)+len(m.Model)+len(m.Content))
		if m.Usage != nil {
			n += unsafe.Sizeof(*m.Usage)
		}
	}
	return int64(n)
}

// File is the transcript read so far, followed by rest, the final line the
// last Continue left unread, decoded as Decode decodes a final line. The
// Reading is unchanged, and the File shares its lines, so neither may be
// changed.
func (r *Reading) File(rest []byte) (*File, error) {
	lines := r.lines[:len(r.lines):len(r.lines)]
	line, ok, torn, err := traceprocessing.DecodeFinalJSONL(rest, r.next, func(raw []byte, ordinal int) (Line, error) {
		line, err := decodeLine(raw, ordinal)
		if err != nil {
			return Line{}, err
		}
		err = trackIDs(line, ordinal, r.seenUUID, r.seenToolUse)
		untrackIDs(line, ordinal, r.seenUUID, r.seenToolUse)
		return line, err
	})
	if err != nil {
		return nil, fmt.Errorf("reading transcript %s: %w", r.label, err)
	}
	if ok {
		line.Raw = nil
		lines = append(lines, line)
	}
	return newFile(lines, torn, r.name, r.label, r.agentID)
}

// rawLine is the envelope as it appears on disk.
type rawLine struct {
	Type           string          `json:"type"`
	UUID           string          `json:"uuid"`
	ParentUUID     string          `json:"parentUuid"`
	SessionID      string          `json:"sessionId"`
	AgentID        string          `json:"agentId"`
	PromptID       string          `json:"promptId"`
	RequestID      string          `json:"requestId"`
	Timestamp      string          `json:"timestamp"`
	IsSidechain    bool            `json:"isSidechain"`
	IsMeta         bool            `json:"isMeta"`
	CWD            string          `json:"cwd"`
	GitBranch      string          `json:"gitBranch"`
	Version        string          `json:"version"`
	Subtype        string          `json:"subtype"`
	PromptSource   string          `json:"promptSource"`
	Origin         json.RawMessage `json:"origin"`
	ToolDenialKind string          `json:"toolDenialKind"`
	ToolUseResult  json.RawMessage `json:"toolUseResult"`
	Message        *rawMessage     `json:"message"`
	Attachment     json.RawMessage `json:"attachment"`

	Mode            string          `json:"mode"`
	PermissionMode  string          `json:"permissionMode"`
	AITitle         string          `json:"aiTitle"`
	CustomTitle     string          `json:"customTitle"`
	AgentName       string          `json:"agentName"`
	LeafUUID        string          `json:"leafUuid"`
	LastPrompt      string          `json:"lastPrompt"`
	BridgeSessionID string          `json:"bridgeSessionId"`
	LastSequenceNum *int64          `json:"lastSequenceNum"`
	Operation       string          `json:"operation"`
	Content         json.RawMessage `json:"content"`
	RelocatedCWD    json.RawMessage `json:"relocatedCwd"`
	WorktreeSession json.RawMessage `json:"worktreeSession"`
	PRNumber        json.RawMessage `json:"prNumber"`
	PRURL           json.RawMessage `json:"prUrl"`
	PRRepository    json.RawMessage `json:"prRepository"`
}

type rawAttachment struct {
	Type        json.RawMessage `json:"type"`
	Prompt      json.RawMessage `json:"prompt"`
	CommandMode json.RawMessage `json:"commandMode"`
	Origin      json.RawMessage `json:"origin"`
	Timestamp   json.RawMessage `json:"timestamp"`
}

type rawMessage struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	StopReason *string         `json:"stop_reason"`
	Usage      *rawUsage       `json:"usage"`
	Content    json.RawMessage `json:"content"`
}

type rawUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	// A speed or 1-hour count of the wrong type reads as not reported; it must
	// not fail the line, whose other counts are good.
	CacheCreation cacheCreation `json:"cache_creation"`
	Speed         lenientString `json:"speed"`
}

// cacheCreation is usage.cache_creation; Hour is ephemeral_1h_input_tokens,
// nil when absent or not an integer.
type cacheCreation struct{ Hour *int64 }

func (c *cacheCreation) UnmarshalJSON(b []byte) error {
	var v struct {
		Hour json.RawMessage `json:"ephemeral_1h_input_tokens"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var n int64
	if json.Unmarshal(v.Hour, &n) == nil {
		c.Hour = &n
	}
	return nil
}

// lenientString is a string that reads as empty when it is anything else.
type lenientString string

func (l *lenientString) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*l = lenientString(s)
	}
	return nil
}

func decodeLine(raw []byte, ordinal int) (Line, error) {
	if raw[0] != '{' {
		return Line{}, fmt.Errorf("line %d is not a JSON object", ordinal)
	}
	var decoded rawLine
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Line{}, fmt.Errorf("line %d: %w", ordinal, err)
	}
	if decoded.Type == "" {
		return Line{}, fmt.Errorf("line %d has no type", ordinal)
	}

	line := Line{
		Ordinal:        ordinal,
		Type:           decoded.Type,
		Raw:            append(json.RawMessage(nil), raw...),
		UUID:           decoded.UUID,
		ParentUUID:     decoded.ParentUUID,
		SessionID:      decoded.SessionID,
		AgentID:        decoded.AgentID,
		PromptID:       decoded.PromptID,
		RequestID:      decoded.RequestID,
		IsSidechain:    decoded.IsSidechain,
		IsMeta:         decoded.IsMeta,
		CWD:            decoded.CWD,
		GitBranch:      decoded.GitBranch,
		Version:        decoded.Version,
		Subtype:        decoded.Subtype,
		PromptSource:   decoded.PromptSource,
		Origin:         decoded.Origin,
		ToolDenialKind: decoded.ToolDenialKind,
		ToolUseResult:  decoded.ToolUseResult,
	}

	switch class, envelope := envelopeTypes[decoded.Type]; {
	case envelope:
		line.Class = class
	case controlTypes[decoded.Type]:
		line.Class = ClassControl
	default:
		line.Class = ClassUnknownControl
	}

	if decoded.Message != nil {
		line.Message = &Message{
			ID:         decoded.Message.ID,
			Role:       decoded.Message.Role,
			Model:      decoded.Message.Model,
			StopReason: decoded.Message.StopReason,
			Content:    decoded.Message.Content,
		}
		if u := decoded.Message.Usage; u != nil {
			line.Message.Usage = &Usage{
				InputTokens:              u.InputTokens,
				OutputTokens:             u.OutputTokens,
				CacheCreationInputTokens: u.CacheCreationInputTokens,
				CacheReadInputTokens:     u.CacheReadInputTokens,
				Speed:                    string(u.Speed),
				CacheWrite1h:             u.CacheCreation.Hour,
			}
		}
	}

	if line.Class == ClassControl {
		title := decoded.AITitle
		if title == "" {
			title = decoded.CustomTitle
		}
		line.Control = Control{
			Mode:            decoded.Mode,
			PermissionMode:  decoded.PermissionMode,
			AITitle:         title,
			AgentName:       decoded.AgentName,
			LeafUUID:        decoded.LeafUUID,
			LastPrompt:      decoded.LastPrompt,
			BridgeSessionID: decoded.BridgeSessionID,
			LastSequenceNum: decoded.LastSequenceNum,
			Operation:       decoded.Operation,
			Content:         decodeControlContent(decoded.Content),
			Value:           controlValue(decoded),
		}
	}
	if len(decoded.Attachment) > 0 && decoded.Attachment[0] == '{' {
		var attachment rawAttachment
		_ = json.Unmarshal(decoded.Attachment, &attachment)
		line.Attachment = Attachment{
			Type: decodeString(attachment.Type), Prompt: decodeString(attachment.Prompt),
			CommandMode: decodeString(attachment.CommandMode),
			Origin:      append(json.RawMessage(nil), attachment.Origin...),
		}
		if timestamp := decodeString(attachment.Timestamp); timestamp != "" {
			if parsed, err := time.Parse(time.RFC3339, timestamp); err == nil {
				utc := parsed.UTC()
				line.Attachment.Timestamp = &utc
			}
		}
	}

	if err := checkRequired(&line, decoded.Timestamp, ordinal); err != nil {
		return Line{}, err
	}
	if line.Class == ClassUser {
		line.UserKind = classifyUserContent(line.Message.Content)
	}
	return line, nil
}

func controlValue(decoded rawLine) json.RawMessage {
	switch decoded.Type {
	case "agent-name":
		return marshalControlValue(decoded.AgentName)
	case "relocated":
		return append(json.RawMessage(nil), decoded.RelocatedCWD...)
	case "worktree-state":
		return append(json.RawMessage(nil), decoded.WorktreeSession...)
	case "pr-link":
		value := struct {
			Number     json.RawMessage `json:"prNumber,omitempty"`
			URL        json.RawMessage `json:"prUrl,omitempty"`
			Repository json.RawMessage `json:"prRepository,omitempty"`
		}{decoded.PRNumber, decoded.PRURL, decoded.PRRepository}
		raw, _ := json.Marshal(value)
		return raw
	default:
		return nil
	}
}

func marshalControlValue(value string) json.RawMessage {
	if value == "" {
		return nil
	}
	raw, _ := json.Marshal(value)
	return raw
}

// decodeControlContent reads a control line's content.
func decodeControlContent(raw json.RawMessage) string {
	return decodeString(raw)
}

func decodeString(raw json.RawMessage) string {
	if len(raw) == 0 || raw[0] != '"' {
		return ""
	}
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func checkRequired(line *Line, timestamp string, ordinal int) error {
	if timestamp != "" {
		parsed, err := time.Parse(time.RFC3339, timestamp)
		if err != nil {
			return fmt.Errorf("line %d has an unreadable timestamp %q: %w", ordinal, timestamp, err)
		}
		utc := parsed.UTC()
		line.Timestamp = &utc
	}

	if !line.Envelope() {
		return nil
	}
	if line.UUID == "" {
		return fmt.Errorf("line %d is a %s envelope with no uuid", ordinal, line.Type)
	}
	if line.Timestamp == nil {
		return fmt.Errorf("line %d is a %s envelope with no timestamp", ordinal, line.Type)
	}

	switch line.Class {
	case ClassUser:
		if line.Message == nil {
			return fmt.Errorf("line %d is a user envelope with no message", ordinal)
		}
	case ClassAssistant:
		if line.Message == nil || line.Message.ID == "" {
			return fmt.Errorf("line %d is an assistant envelope with no message id", ordinal)
		}
	}
	return nil
}
