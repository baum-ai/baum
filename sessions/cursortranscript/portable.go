package cursortranscript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
)

type PortableRecordKind string

const (
	PortableUser      PortableRecordKind = "user"
	PortableAssistant PortableRecordKind = "assistant"
	PortableTurnEnd   PortableRecordKind = "turn_ended"
)

type PortableBlockKind string

const (
	PortableText      PortableBlockKind = "text"
	PortableToolBlock PortableBlockKind = "tool_use"
)

type PortableRecord struct {
	Ordinal int                `json:"ordinal"`
	Kind    PortableRecordKind `json:"kind"`
	Blocks  []PortableBlock    `json:"blocks,omitempty"`
	Status  string             `json:"status,omitempty"`
	Error   json.RawMessage    `json:"error,omitempty"`
	Raw     json.RawMessage    `json:"raw"`
}

type PortableBlock struct {
	Index int               `json:"index"`
	Kind  PortableBlockKind `json:"kind"`
	Text  string            `json:"text,omitempty"`
	Tool  *PortableToolUse  `json:"tool_use,omitempty"`
	Raw   json.RawMessage   `json:"raw"`
}

type PortableToolUse struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func DecodePortable(r io.Reader) ([]PortableRecord, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	var records []PortableRecord
	for ordinal := 1; scanner.Scan(); ordinal++ {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		record, err := decodePortableRecord(raw, ordinal)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading Cursor portable transcript: %w", err)
	}
	return records, nil
}

func decodePortableRecord(raw []byte, ordinal int) (PortableRecord, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return PortableRecord{}, fmt.Errorf("Cursor portable line %d is not a JSON object", ordinal)
	}
	record := PortableRecord{Ordinal: ordinal, Raw: append(json.RawMessage(nil), raw...)}
	if fields["role"] != nil {
		if !sameKeys(fields, "message", "role") {
			return PortableRecord{}, fmt.Errorf("Cursor portable line %d has unknown message shape", ordinal)
		}
		var role string
		if err := json.Unmarshal(fields["role"], &role); err != nil || (role != "user" && role != "assistant") {
			return PortableRecord{}, fmt.Errorf("Cursor portable line %d has unknown role %q", ordinal, role)
		}
		if role == "user" {
			record.Kind = PortableUser
		} else {
			record.Kind = PortableAssistant
		}
		blocks, err := decodePortableMessage(fields["message"], record.Kind, ordinal)
		if err != nil {
			return PortableRecord{}, err
		}
		record.Blocks = blocks
		return record, nil
	}

	if fields["type"] == nil || fields["status"] == nil ||
		(!sameKeys(fields, "status", "type") && !sameKeys(fields, "error", "status", "type")) {
		return PortableRecord{}, fmt.Errorf("Cursor portable line %d has unknown record shape", ordinal)
	}
	var kind, status string
	if err := json.Unmarshal(fields["type"], &kind); err != nil || kind != string(PortableTurnEnd) {
		return PortableRecord{}, fmt.Errorf("Cursor portable line %d has unknown type %q", ordinal, kind)
	}
	if err := json.Unmarshal(fields["status"], &status); err != nil || (status != "success" && status != "error") {
		return PortableRecord{}, fmt.Errorf("Cursor portable line %d has unknown turn status %q", ordinal, status)
	}
	if status == "success" && fields["error"] != nil {
		return PortableRecord{}, fmt.Errorf("Cursor portable line %d has an error on a successful turn", ordinal)
	}
	if status == "error" && fields["error"] == nil {
		return PortableRecord{}, fmt.Errorf("Cursor portable line %d has an error turn without error detail", ordinal)
	}
	record.Kind, record.Status = PortableTurnEnd, status
	record.Error = append(json.RawMessage(nil), fields["error"]...)
	return record, nil
}

func decodePortableMessage(raw json.RawMessage, role PortableRecordKind, ordinal int) ([]PortableBlock, error) {
	message := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &message); err != nil || !sameKeys(message, "content") {
		return nil, fmt.Errorf("Cursor portable line %d has invalid message envelope", ordinal)
	}
	var encoded []json.RawMessage
	if err := json.Unmarshal(message["content"], &encoded); err != nil {
		return nil, fmt.Errorf("Cursor portable line %d has non-array content", ordinal)
	}
	blocks := make([]PortableBlock, 0, len(encoded))
	for index, rawBlock := range encoded {
		block, err := decodePortableBlock(rawBlock, role, ordinal, index)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func decodePortableBlock(raw json.RawMessage, role PortableRecordKind, ordinal, index int) (PortableBlock, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return PortableBlock{}, fmt.Errorf("Cursor portable line %d block %d is not an object", ordinal, index)
	}
	var kind string
	if err := json.Unmarshal(fields["type"], &kind); err != nil {
		return PortableBlock{}, fmt.Errorf("Cursor portable line %d block %d has no string type", ordinal, index)
	}
	block := PortableBlock{Index: index, Raw: append(json.RawMessage(nil), raw...)}
	switch {
	case kind == "text" && sameKeys(fields, "text", "type"):
		if err := json.Unmarshal(fields["text"], &block.Text); err != nil {
			return PortableBlock{}, fmt.Errorf("Cursor portable line %d block %d has non-string text", ordinal, index)
		}
		block.Kind = PortableText
	case role == PortableAssistant && kind == "tool_use" && sameKeys(fields, "input", "name", "type"):
		var tool PortableToolUse
		if err := json.Unmarshal(fields["name"], &tool.Name); err != nil || tool.Name == "" {
			return PortableBlock{}, fmt.Errorf("Cursor portable line %d block %d has invalid tool name", ordinal, index)
		}
		if input := bytes.TrimSpace(fields["input"]); len(input) == 0 || !json.Valid(input) {
			return PortableBlock{}, fmt.Errorf("Cursor portable line %d block %d has invalid tool input", ordinal, index)
		} else {
			tool.Input = append(json.RawMessage(nil), input...)
		}
		block.Kind, block.Tool = PortableToolBlock, &tool
	default:
		return PortableBlock{}, fmt.Errorf("Cursor portable line %d block %d has unknown %s block shape", ordinal, index, role)
	}
	return block, nil
}

func sameKeys(fields map[string]json.RawMessage, expected ...string) bool {
	if len(fields) != len(expected) {
		return false
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	slices.Sort(expected)
	return slices.Equal(keys, expected)
}
