package cursortranscript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// CodeBlockRef joins one structured diff to its bubble and file.
type CodeBlockRef struct {
	BubbleID    string      `json:"bubbleId"`
	CodeBlockID string      `json:"codeblockId"`
	DiffID      string      `json:"diffId"`
	Status      string      `json:"status"`
	URI         ResourceURI `json:"uri"`
}

// CodeBlockData is the diff-bearing subset of composerData.codeBlockData.
type CodeBlockData []CodeBlockRef

func (d *CodeBlockData) UnmarshalJSON(raw []byte) error {
	if isNull(raw) {
		*d = nil
		return nil
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		return json.Unmarshal(raw, (*[]CodeBlockRef)(d))
	}

	var files map[string]json.RawMessage
	if err := json.Unmarshal(raw, &files); err != nil {
		return fmt.Errorf("decoding code block data: %w", err)
	}
	var refs []CodeBlockRef
	for _, fileRaw := range files {
		var blocks map[string]json.RawMessage
		if err := json.Unmarshal(fileRaw, &blocks); err != nil {
			var legacy []CodeBlockRef
			if arrayErr := json.Unmarshal(fileRaw, &legacy); arrayErr != nil {
				return fmt.Errorf("decoding code block data: %w", err)
			}
			for _, ref := range legacy {
				if ref.DiffID != "" {
					refs = append(refs, ref)
				}
			}
			continue
		}
		for id, blockRaw := range blocks {
			var ref CodeBlockRef
			if err := json.Unmarshal(blockRaw, &ref); err != nil {
				return fmt.Errorf("decoding code block data: %w", err)
			}
			if ref.CodeBlockID == "" {
				ref.CodeBlockID = id
			}
			if ref.CodeBlockID != id {
				return fmt.Errorf("code block %s contains codeblockId %s", id, ref.CodeBlockID)
			}
			if ref.DiffID != "" {
				refs = append(refs, ref)
			}
		}
	}
	slices.SortFunc(refs, func(a, b CodeBlockRef) int {
		return bytes.Compare([]byte(a.DiffID), []byte(b.DiffID))
	})
	*d = refs
	return nil
}

type DiffRange struct {
	StartLineNumber        int `json:"startLineNumber"`
	EndLineNumberExclusive int `json:"endLineNumberExclusive"`
}

func (r *DiffRange) UnmarshalJSON(raw []byte) error {
	var required struct {
		StartLineNumber        *int `json:"startLineNumber"`
		EndLineNumberExclusive *int `json:"endLineNumberExclusive"`
	}
	if err := json.Unmarshal(raw, &required); err != nil {
		return err
	}
	if required.StartLineNumber == nil || required.EndLineNumberExclusive == nil {
		return fmt.Errorf("diff range is missing required coordinates")
	}
	r.StartLineNumber = *required.StartLineNumber
	r.EndLineNumberExclusive = *required.EndLineNumberExclusive
	return nil
}

type DiffHunk struct {
	Original DiffRange `json:"original"`
	Modified []string  `json:"modified"`
}

func (h *DiffHunk) UnmarshalJSON(raw []byte) error {
	var required struct {
		Original json.RawMessage `json:"original"`
		Modified json.RawMessage `json:"modified"`
	}
	if err := json.Unmarshal(raw, &required); err != nil {
		return err
	}
	if isNull(required.Original) || isNull(required.Modified) {
		return fmt.Errorf("diff hunk is missing required fields")
	}
	var modified []json.RawMessage
	if err := json.Unmarshal(required.Modified, &modified); err != nil {
		return err
	}
	for i, rawLine := range modified {
		var line *string
		if err := json.Unmarshal(rawLine, &line); err != nil {
			return fmt.Errorf("decoding modified line %d: %w", i, err)
		}
		if line == nil {
			return fmt.Errorf("modified line %d is null", i)
		}
	}
	type fields DiffHunk
	var decoded fields
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*h = DiffHunk(decoded)
	return nil
}

// CodeBlockDiff is one codeBlockDiff:<composer-id>:<diff-id> record.
type CodeBlockDiff struct {
	DiffID                 string     `json:"diff_id"`
	NewModelDiffWrtV0      []DiffHunk `json:"newModelDiffWrtV0"`
	OriginalModelDiffWrtV0 []DiffHunk `json:"originalModelDiffWrtV0"`
}

func (d *CodeBlockDiff) UnmarshalJSON(raw []byte) error {
	var required struct {
		NewModelDiffWrtV0      json.RawMessage `json:"newModelDiffWrtV0"`
		OriginalModelDiffWrtV0 json.RawMessage `json:"originalModelDiffWrtV0"`
	}
	if err := json.Unmarshal(raw, &required); err != nil {
		return err
	}
	if isNull(required.NewModelDiffWrtV0) || isNull(required.OriginalModelDiffWrtV0) {
		return fmt.Errorf("code block diff is missing required diff arrays")
	}
	type fields CodeBlockDiff
	var decoded fields
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*d = CodeBlockDiff(decoded)
	return nil
}

func DecodeCodeBlockDiff(raw []byte) (*CodeBlockDiff, error) {
	if isNull(raw) {
		return nil, nil
	}
	var diff CodeBlockDiff
	if err := json.Unmarshal(raw, &diff); err != nil {
		return nil, fmt.Errorf("decoding code block diff: %w", err)
	}
	return &diff, nil
}
