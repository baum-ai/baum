package claudetranscript

import (
	"encoding/json"
)

// ResultEvidence is what a tool-result line's `toolUseResult` says about the tool
// that ran.
type ResultEvidence struct {
	Present         bool
	Text            string
	Stdout          string
	Stderr          string
	Interrupted     bool
	ExitCode        *int
	FilePath        string
	StructuredPatch []PatchHunk
	StartLine       *int
	NumLines        *int
	// AgentID names the subagent a spawn created. ResumedAgentID names the one a
	// SendMessage call woke up again.
	AgentID        string
	ResumedAgentID string
	Status         string
	Raw            json.RawMessage
}

type PatchHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// exitCodeKeys are the spellings of a process exit status.
var exitCodeKeys = []string{"exitCode", "exit_code", "returnCode", "return_code"}

func DecodeResultEvidence(raw json.RawMessage) ResultEvidence {
	evidence := ResultEvidence{Raw: raw}
	if len(raw) == 0 {
		return evidence
	}
	evidence.Present = true

	switch raw[0] {
	case '"':
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			evidence.Text = text
		}
		return evidence
	case '[':
		// An array payload carries no fields we read; its text is on the
		// tool_result block instead.
		return evidence
	case '{':
	default:
		return evidence
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return evidence
	}

	evidence.Stdout = stringField(fields, "stdout")
	evidence.Stderr = stringField(fields, "stderr")
	evidence.Interrupted = boolField(fields, "interrupted")
	evidence.FilePath = stringField(fields, "filePath")
	evidence.AgentID = stringField(fields, "agentId")
	evidence.ResumedAgentID = stringField(fields, "resumedAgentId")
	evidence.Status = stringField(fields, "status")

	for _, key := range exitCodeKeys {
		if code, ok := intField(fields, key); ok {
			evidence.ExitCode = &code
			break
		}
	}

	if patch, ok := fields["structuredPatch"]; ok {
		var hunks []PatchHunk
		if err := json.Unmarshal(patch, &hunks); err == nil {
			evidence.StructuredPatch = hunks
		}
	}

	// A read's span lives one level down, under `file`, together with the path.
	if file, ok := fields["file"]; ok {
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(file, &inner); err == nil {
			if evidence.FilePath == "" {
				evidence.FilePath = stringField(inner, "filePath")
			}
			if start, ok := intField(inner, "startLine"); ok {
				evidence.StartLine = &start
			}
			if lines, ok := intField(inner, "numLines"); ok {
				evidence.NumLines = &lines
			}
		}
	}

	return evidence
}

func stringField(fields map[string]json.RawMessage, key string) string {
	raw, ok := fields[key]
	if !ok || len(raw) == 0 || raw[0] != '"' {
		return ""
	}
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		return ""
	}
	return out
}

func boolField(fields map[string]json.RawMessage, key string) bool {
	raw, ok := fields[key]
	if !ok {
		return false
	}
	var out bool
	if err := json.Unmarshal(raw, &out); err != nil {
		return false
	}
	return out
}

func intField(fields map[string]json.RawMessage, key string) (int, bool) {
	raw, ok := fields[key]
	if !ok {
		return 0, false
	}
	var out int
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, false
	}
	return out, true
}
