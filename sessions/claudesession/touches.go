package claudesession

import (
	"encoding/json"

	"github.com/baumai/baum/sessions/claudetranscript"
	"github.com/baumai/baum/sessions/session"
)

// toolInput is the part of a tool's arguments that names a file.
type toolInput struct {
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	Offset       *int   `json:"offset"`
	Limit        *int   `json:"limit"`
}

// addTouchesFromInput records the file a call names, before its result arrives.
func (f *fileState) addTouchesFromInput(use *session.ToolUse, block claudetranscript.Block) {
	if !use.Verb.FileVerb() {
		return
	}
	var input toolInput
	if err := json.Unmarshal(use.Input, &input); err != nil {
		return
	}
	path := input.FilePath
	if path == "" {
		path = input.NotebookPath
	}
	if path == "" {
		return
	}

	kind := session.TouchEdit
	if use.Verb == session.VerbRead {
		kind = session.TouchRead
	}
	touch := &session.FileTouch{
		ID:          f.ids.Mint("file_touch", block.ID, path),
		ToolUseID:   &use.ID,
		UserInputID: use.UserInputID,
		SessionID:   f.sessionID,
		Path:        path,
		Kind:        kind,
	}
	if input.Offset != nil && *input.Offset > 0 {
		start := *input.Offset
		touch.StartLine = &start
		if input.Limit != nil && *input.Limit > 0 {
			end := start + *input.Limit - 1
			touch.EndLine = &end
		}
	}

	f.touches = append(f.touches, touch)
	f.touchByTool[use.ID] = append(f.touchByTool[use.ID], touch)
}

// refineTouches updates a call's file touches once its result is known.
func (f *fileState) refineTouches(use *session.ToolUse, evidence claudetranscript.ResultEvidence) {
	if !use.Verb.FileVerb() {
		return
	}
	existing := f.touchByTool[use.ID]

	// A result can name a file the arguments did not.
	if len(existing) == 0 {
		if evidence.FilePath == "" {
			return
		}
		kind := session.TouchEdit
		if use.Verb == session.VerbRead {
			kind = session.TouchRead
		}
		touch := &session.FileTouch{
			ID:          f.ids.Mint("file_touch", f.toolUseVendor[use.ID], evidence.FilePath),
			ToolUseID:   &use.ID,
			UserInputID: use.UserInputID,
			SessionID:   f.sessionID,
			Path:        evidence.FilePath,
			Kind:        kind,
		}
		f.touches = append(f.touches, touch)
		f.touchByTool[use.ID] = []*session.FileTouch{touch}
		existing = f.touchByTool[use.ID]
	}

	for _, touch := range existing {
		if evidence.StartLine != nil && *evidence.StartLine > 0 {
			start := *evidence.StartLine
			touch.StartLine = &start
			if evidence.NumLines != nil && *evidence.NumLines > 0 {
				end := start + *evidence.NumLines - 1
				touch.EndLine = &end
			}
		}
		if len(evidence.StructuredPatch) > 0 {
			touch.Hunks = hunksFrom(evidence.StructuredPatch)
		}
	}
}

func hunksFrom(patch []claudetranscript.PatchHunk) []session.Hunk {
	out := make([]session.Hunk, len(patch))
	for i, hunk := range patch {
		out[i] = session.Hunk{
			OldStart: hunk.OldStart,
			OldLines: hunk.OldLines,
			NewStart: hunk.NewStart,
			NewLines: hunk.NewLines,
			Lines:    hunk.Lines,
		}
	}
	return out
}
