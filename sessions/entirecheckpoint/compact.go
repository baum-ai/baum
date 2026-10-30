package entirecheckpoint

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/baum-ai/baum/sessions/traceprocessing"
)

var (
	ErrCompactVersion    = errors.New("unsupported Entire compact transcript version")
	ErrCompactRecordType = errors.New("unknown Entire compact record type")
	ErrCompactBlockShape = errors.New("unknown Entire compact block shape")
)

type CompactRecordType string

const (
	CompactUser      CompactRecordType = "user"
	CompactAssistant CompactRecordType = "assistant"
)

type CompactBlockKind string

const (
	CompactUserText      CompactBlockKind = "user_text"
	CompactImage         CompactBlockKind = "image"
	CompactAssistantText CompactBlockKind = "assistant_text"
	CompactToolUse       CompactBlockKind = "tool_use"
)

type CompactRecord struct {
	Ordinal      int               `json:"ordinal"`
	Version      int               `json:"v"`
	Agent        string            `json:"agent"`
	CLIVersion   string            `json:"cli_version"`
	Type         CompactRecordType `json:"type"`
	Timestamp    *time.Time        `json:"ts,omitempty"`
	ID           string            `json:"id,omitempty"`
	InputTokens  int64             `json:"input_tokens,omitempty"`
	OutputTokens int64             `json:"output_tokens,omitempty"`
	Content      []CompactBlock    `json:"content"`
	Raw          json.RawMessage   `json:"raw"`
}

type CompactBlock struct {
	Index   int                  `json:"index"`
	Kind    CompactBlockKind     `json:"kind"`
	ID      string               `json:"id,omitempty"`
	Text    string               `json:"text,omitempty"`
	Image   *CompactImageBlock   `json:"image,omitempty"`
	ToolUse *CompactToolUseBlock `json:"tool_use,omitempty"`
	Raw     json.RawMessage      `json:"raw"`
}

type CompactImageBlock struct {
	Source CompactImageSource `json:"source"`
}

type CompactImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type CompactToolUseBlock struct {
	ID           string             `json:"id"`
	Name         string             `json:"name"`
	Input        json.RawMessage    `json:"input"`
	InputPresent bool               `json:"input_present"`
	Result       *CompactToolResult `json:"result,omitempty"`
}

type CompactToolResult struct {
	Output string             `json:"output"`
	Status string             `json:"status"`
	File   *CompactResultFile `json:"file,omitempty"`
	Raw    json.RawMessage    `json:"raw"`
}

type CompactResultFile struct {
	FilePath string `json:"filePath"`
	NumLines *int   `json:"numLines,omitempty"`
}

type compactEnvelope struct {
	Version      int               `json:"v"`
	Agent        string            `json:"agent"`
	CLIVersion   string            `json:"cli_version"`
	Type         CompactRecordType `json:"type"`
	Timestamp    string            `json:"ts"`
	ID           string            `json:"id"`
	InputTokens  int64             `json:"input_tokens"`
	OutputTokens int64             `json:"output_tokens"`
	Content      []json.RawMessage `json:"content"`
}

// DecodeCompact reads Entire compact transcript v1. Unlike live native
// transcript readers, a truncated final record is an error: checkpoint blobs
// are immutable and must be complete.
func DecodeCompact(r io.Reader) ([]CompactRecord, error) {
	return DecodeCompactWith(r, traceprocessing.Local)
}

func DecodeCompactWith(r io.Reader, profile traceprocessing.MappingProfile) ([]CompactRecord, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	var records []CompactRecord
	for ordinal := 1; scanner.Scan(); ordinal++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		record, err := decodeCompactRecord(raw, ordinal, profile)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading compact transcript: %w", err)
	}
	return records, nil
}

func decodeCompactRecord(raw []byte, ordinal int, profile traceprocessing.MappingProfile) (CompactRecord, error) {
	if len(raw) == 0 || raw[0] != '{' {
		return CompactRecord{}, fmt.Errorf("compact line %d is not a JSON object", ordinal)
	}
	var envelope compactEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return CompactRecord{}, fmt.Errorf("compact line %d: %w", ordinal, err)
	}
	if envelope.Version != 1 {
		return CompactRecord{}, fmt.Errorf("%w: line %d has v=%d", ErrCompactVersion, ordinal, envelope.Version)
	}
	if envelope.Type != CompactUser && envelope.Type != CompactAssistant {
		return CompactRecord{}, fmt.Errorf("%w: line %d has type %q", ErrCompactRecordType, ordinal, envelope.Type)
	}
	var timestamp *time.Time
	if envelope.Timestamp != "" {
		parsed, err := time.Parse(time.RFC3339Nano, envelope.Timestamp)
		if err != nil {
			return CompactRecord{}, fmt.Errorf("compact line %d has invalid ts %q: %w", ordinal, envelope.Timestamp, err)
		}
		parsed = parsed.UTC()
		timestamp = &parsed
	}
	record := CompactRecord{
		Ordinal: ordinal, Version: envelope.Version, Agent: envelope.Agent,
		CLIVersion: envelope.CLIVersion, Type: envelope.Type, Timestamp: timestamp,
		ID: envelope.ID, InputTokens: envelope.InputTokens, OutputTokens: envelope.OutputTokens,
		Raw: append(json.RawMessage(nil), raw...),
	}
	for index, rawBlock := range envelope.Content {
		block, err := decodeCompactBlock(envelope.Type, rawBlock, ordinal, index, profile)
		if err != nil {
			return CompactRecord{}, err
		}
		record.Content = append(record.Content, block)
	}
	return record, nil
}

func decodeCompactBlock(role CompactRecordType, raw json.RawMessage, ordinal, index int, profile traceprocessing.MappingProfile) (CompactBlock, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return CompactBlock{}, fmt.Errorf("%w: line %d block %d is not an object", ErrCompactBlockShape, ordinal, index)
	}
	var kind string
	if encoded, ok := fields["type"]; ok {
		if err := json.Unmarshal(encoded, &kind); err != nil {
			return CompactBlock{}, fmt.Errorf("%w: line %d block %d has non-string type", ErrCompactBlockShape, ordinal, index)
		}
	}
	block := CompactBlock{Index: index, Raw: append(json.RawMessage(nil), raw...)}
	switch {
	case role == CompactUser && kind == "" && fields["text"] != nil:
		block.Kind = CompactUserText
		if err := json.Unmarshal(fields["text"], &block.Text); err != nil {
			return CompactBlock{}, fmt.Errorf("%w: line %d block %d has non-string text", ErrCompactBlockShape, ordinal, index)
		}
		if encoded := fields["id"]; encoded != nil {
			if err := json.Unmarshal(encoded, &block.ID); err != nil {
				return CompactBlock{}, fmt.Errorf("%w: line %d block %d has non-string id", ErrCompactBlockShape, ordinal, index)
			}
		}
	case role == CompactUser && kind == "image":
		block.Kind = CompactImage
		var image CompactImageBlock
		if err := json.Unmarshal(raw, &image); err != nil || image.Source.Type == "" {
			return CompactBlock{}, fmt.Errorf("%w: line %d block %d has invalid image source", ErrCompactBlockShape, ordinal, index)
		}
		block.Image = &image
	case role == CompactAssistant && kind == "text" && fields["text"] != nil:
		block.Kind = CompactAssistantText
		if err := json.Unmarshal(fields["text"], &block.Text); err != nil {
			return CompactBlock{}, fmt.Errorf("%w: line %d block %d has non-string text", ErrCompactBlockShape, ordinal, index)
		}
	case role == CompactAssistant && kind == "tool_use":
		tool, err := decodeCompactToolUse(fields, raw, ordinal, index)
		if err != nil {
			return CompactBlock{}, err
		}
		block.Kind = CompactToolUse
		block.ID = tool.ID
		block.ToolUse = tool
	case profile == traceprocessing.Hosted && role == CompactAssistant && kind == "toolCall":
		tool, err := decodeCompactPiToolCall(fields, raw, ordinal, index)
		if err != nil {
			return CompactBlock{}, err
		}
		block.Kind = CompactToolUse
		block.ID = tool.ID
		block.ToolUse = tool
	default:
		return CompactBlock{}, fmt.Errorf("%w: line %d block %d role=%q type=%q", ErrCompactBlockShape, ordinal, index, role, kind)
	}
	return block, nil
}

func decodeCompactToolUse(fields map[string]json.RawMessage, raw json.RawMessage, ordinal, index int) (*CompactToolUseBlock, error) {
	var decoded struct {
		ID     string          `json:"id"`
		Name   string          `json:"name"`
		Input  json.RawMessage `json:"input"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Name == "" {
		return nil, fmt.Errorf("%w: line %d block %d has invalid tool_use identity", ErrCompactBlockShape, ordinal, index)
	}
	tool := &CompactToolUseBlock{ID: decoded.ID, Name: decoded.Name}
	if _, ok := fields["input"]; ok {
		trimmed := bytes.TrimSpace(decoded.Input)
		if len(trimmed) == 0 || !json.Valid(trimmed) || (trimmed[0] != '{' && trimmed[0] != '"') {
			return nil, fmt.Errorf("%w: line %d block %d tool input is not an object or string", ErrCompactBlockShape, ordinal, index)
		}
		tool.Input = append(json.RawMessage(nil), decoded.Input...)
		tool.InputPresent = true
	} else {
		tool.Input = json.RawMessage(`{}`)
	}
	if _, ok := fields["result"]; ok {
		if !isJSONObject(decoded.Result) {
			return nil, fmt.Errorf("%w: line %d block %d tool result is not an object", ErrCompactBlockShape, ordinal, index)
		}
		var result CompactToolResult
		if err := json.Unmarshal(decoded.Result, &result); err != nil || (result.Status != "success" && result.Status != "error") {
			return nil, fmt.Errorf("%w: line %d block %d has invalid result status", ErrCompactBlockShape, ordinal, index)
		}
		result.Raw = append(json.RawMessage(nil), decoded.Result...)
		tool.Result = &result
	}
	return tool, nil
}

// piToolNames mirrors the Entire CLI's own Pi compactor, which title-cases the
// file tools so every agent's compact transcript spells them the same way.
var piToolNames = map[string]string{"edit": "Edit", "read": "Read", "write": "Write"}

// decodeCompactPiToolCall accepts Pi's native tool call item. Entire CLI
// releases through 0.10.6 do not recognise a Pi session that opens with a
// title record, so instead of the Pi compactor's tool_use blocks they copy the
// native items into the compact transcript unnormalised: `arguments` in place
// of `input`, never a result, and streaming leftovers (`intent`,
// `partialArgs`, `streamIndex`) that say nothing about what ran.
func decodeCompactPiToolCall(fields map[string]json.RawMessage, raw json.RawMessage, ordinal, index int) (*CompactToolUseBlock, error) {
	var decoded struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.Name == "" {
		return nil, fmt.Errorf("%w: line %d block %d has invalid toolCall identity", ErrCompactBlockShape, ordinal, index)
	}
	name := decoded.Name
	if normalized, ok := piToolNames[name]; ok {
		name = normalized
	}
	tool := &CompactToolUseBlock{ID: decoded.ID, Name: name}
	if _, ok := fields["arguments"]; ok {
		if !isJSONObject(decoded.Arguments) {
			return nil, fmt.Errorf("%w: line %d block %d toolCall arguments is not an object", ErrCompactBlockShape, ordinal, index)
		}
		tool.Input = append(json.RawMessage(nil), decoded.Arguments...)
		tool.InputPresent = true
	} else {
		tool.Input = json.RawMessage(`{}`)
	}
	return tool, nil
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}
