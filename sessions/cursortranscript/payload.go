package cursortranscript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const TreePayloadVersion = "1"

type TreePayload struct {
	SchemaVersion string            `json:"schema_version"`
	Tree          *Tree             `json:"tree"`
	RawFields     map[string]string `json:"raw_fields,omitempty"`
}

func EncodeTreePayload(tree *Tree) ([]byte, error) {
	if tree == nil || tree.ComposerID() == "" {
		return nil, fmt.Errorf("encoding Cursor tree payload: no root composer")
	}
	rawFields, err := treeRawFields(tree)
	if err != nil {
		return nil, fmt.Errorf("encoding Cursor tree payload: %w", err)
	}
	body, err := json.Marshal(TreePayload{
		SchemaVersion: TreePayloadVersion, Tree: tree, RawFields: rawFields,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding Cursor tree payload: %w", err)
	}
	return append(body, '\n'), nil
}

func DecodeTreePayload(body []byte) (*Tree, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var payload TreePayload
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decoding Cursor tree payload: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decoding Cursor tree payload: trailing JSON value")
		}
		return nil, fmt.Errorf("decoding Cursor tree payload: %w", err)
	}
	if payload.SchemaVersion != TreePayloadVersion {
		return nil, fmt.Errorf("unsupported Cursor tree payload version %q", payload.SchemaVersion)
	}
	if payload.Tree == nil || payload.Tree.ComposerID() == "" {
		return nil, fmt.Errorf("decoding Cursor tree payload: no root composer")
	}
	if err := restoreTreeRawFields(payload.Tree, payload.RawFields); err != nil {
		return nil, fmt.Errorf("decoding Cursor tree payload: %w", err)
	}
	return payload.Tree, nil
}

func treeRawFields(tree *Tree) (map[string]string, error) {
	fields := map[string]string{}
	for _, composer := range tree.Composers() {
		composerID := composer.Data.ComposerID
		if composer.Data.CodeBlocks != nil {
			if err := keepJSON(fields, rawKey(composerID, "data.codeBlockData"), composer.Data.CodeBlocks); err != nil {
				return nil, err
			}
		}
		if composer.Diffs != nil {
			if err := keepJSON(fields, rawKey(composerID, "diffs"), composer.Diffs); err != nil {
				return nil, err
			}
		}
		if composer.Data.ModelConfig != nil {
			keepRaw(fields, rawKey(composerID, "data.modelConfig"), composer.Data.ModelConfig.Raw)
		}
		if composer.Data.WorkspaceIdentifier != nil {
			keepRaw(fields, rawKey(composerID, "data.workspaceIdentifier"), composer.Data.WorkspaceIdentifier.Raw)
		}
		keepRaw(fields, rawKey(composerID, "data.promptTokenBreakdown"), composer.Data.PromptTokenBreakdown)
		for _, bubble := range append(append([]*Bubble(nil), composer.Bubbles...), composer.Unlisted...) {
			prefix := rawKey(composerID, "bubble", bubble.BubbleID)
			if bubble.Tool == nil {
				continue
			}
			keepRaw(fields, prefix+"\x00tool.params", bubble.Tool.Params)
			keepRaw(fields, prefix+"\x00tool.rawArgs", bubble.Tool.RawArgs)
			keepRaw(fields, prefix+"\x00tool.result", bubble.Tool.Result)
			keepRaw(fields, prefix+"\x00tool.error", bubble.Tool.Error)
		}
	}
	if len(fields) == 0 {
		return nil, nil
	}
	return fields, nil
}

func restoreTreeRawFields(tree *Tree, fields map[string]string) error {
	for _, composer := range tree.Composers() {
		composerID := composer.Data.ComposerID
		if err := restoreJSON(&composer.Data.CodeBlocks, fields, rawKey(composerID, "data.codeBlockData")); err != nil {
			return err
		}
		if err := restoreJSON(&composer.Diffs, fields, rawKey(composerID, "diffs")); err != nil {
			return err
		}
		if composer.Data.ModelConfig != nil {
			restoreRaw(&composer.Data.ModelConfig.Raw, fields, rawKey(composerID, "data.modelConfig"))
		}
		if composer.Data.WorkspaceIdentifier != nil {
			restoreRaw(&composer.Data.WorkspaceIdentifier.Raw, fields, rawKey(composerID, "data.workspaceIdentifier"))
		}
		restoreRaw(&composer.Data.PromptTokenBreakdown, fields, rawKey(composerID, "data.promptTokenBreakdown"))
		for _, bubble := range append(append([]*Bubble(nil), composer.Bubbles...), composer.Unlisted...) {
			prefix := rawKey(composerID, "bubble", bubble.BubbleID)
			if bubble.Tool == nil {
				continue
			}
			restoreRaw(&bubble.Tool.Params, fields, prefix+"\x00tool.params")
			restoreRaw(&bubble.Tool.RawArgs, fields, prefix+"\x00tool.rawArgs")
			restoreRaw(&bubble.Tool.Result, fields, prefix+"\x00tool.result")
			restoreRaw(&bubble.Tool.Error, fields, prefix+"\x00tool.error")
		}
	}
	return nil
}

func rawKey(parts ...string) string { return strings.Join(parts, "\x00") }

func keepRaw(fields map[string]string, key string, raw json.RawMessage) {
	if raw != nil {
		fields[key] = string(raw)
	}
}

func keepJSON(fields map[string]string, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encoding raw field %q: %w", key, err)
	}
	fields[key] = string(raw)
	return nil
}

func restoreJSON(target any, fields map[string]string, key string) error {
	raw, present := fields[key]
	if !present {
		return nil
	}
	if isNull([]byte(raw)) {
		return fmt.Errorf("raw field %q is null", key)
	}
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		return fmt.Errorf("decoding raw field %q: %w", key, err)
	}
	return nil
}

func restoreRaw(target *json.RawMessage, fields map[string]string, key string) {
	if raw, present := fields[key]; present {
		*target = append((*target)[:0], raw...)
	}
}
