package cursorsession

import (
	"encoding/json"
	"fmt"

	"github.com/lastpersonlabs/reviewpal/sessions/cursortranscript"
	"github.com/lastpersonlabs/reviewpal/sessions/session"
)

type toolParams struct {
	TargetFile            string `json:"targetFile"`
	RelativeWorkspacePath string `json:"relativeWorkspacePath"`
}

func decodedToolParams(raw json.RawMessage) toolParams {
	var params toolParams
	_ = json.Unmarshal(raw, &params)
	return params
}

func touchPath(verb session.ToolVerb, params toolParams) (string, session.TouchKind) {
	switch verb {
	case session.VerbRead:
		return params.TargetFile, session.TouchRead
	case session.VerbEdit:
		return params.RelativeWorkspacePath, session.TouchEdit
	default:
		return "", ""
	}
}

func (f *composerState) addTouch(input *session.UserInput, use *session.ToolUse, bubble *cursortranscript.Bubble, params toolParams) {
	path, kind := touchPath(use.Verb, params)
	if path == "" {
		return
	}
	if kind == session.TouchEdit {
		path = resolvedPath(f.workingDir, path)
	}
	toolUseID := use.ID
	row := &session.FileTouch{
		ID: f.ids.Mint("file_touch", bubble.BubbleID, path), ToolUseID: &toolUseID,
		UserInputID: input.ID, SessionID: f.session.ID, Path: path, Kind: kind,
	}
	f.touches = append(f.touches, row)
	f.vendorRow(row.ID, session.EntityFileTouch, "bubbleId.path", bubble.BubbleID+":"+path, bubble.Key)
}

func (f *composerState) enrichDiffs() {
	if f.composer.Diffs == nil {
		f.b.report.note("composer %s payload predates codeBlockDiff capture; diff enrichment unavailable", f.data.ComposerID)
		return
	}

	refsByDiff := make(map[string][]cursortranscript.CodeBlockRef, len(f.data.CodeBlocks))
	for _, ref := range f.data.CodeBlocks {
		if ref.DiffID != "" {
			refsByDiff[ref.DiffID] = append(refsByDiff[ref.DiffID], ref)
		}
	}
	seenDiffs := make(map[string]bool, len(f.composer.Diffs))
	for _, diff := range f.composer.Diffs {
		if diff == nil {
			continue
		}
		seenDiffs[diff.DiffID] = true
		refs := refsByDiff[diff.DiffID]
		if refsAllRejected(refs) {
			continue
		}
		if len(refs) != 1 {
			f.noteUnjoinableDiff(diff.DiffID, "has %d codeBlockData references, want exactly one", len(refs))
			continue
		}
		f.enrichDiff(diff, refs[0])
	}
	for diffID := range refsByDiff {
		if !seenDiffs[diffID] && !refsAllRejected(refsByDiff[diffID]) {
			f.noteUnjoinableDiff(diffID, "has codeBlockData but no captured diff record")
		}
	}
}

func (f *composerState) enrichDiff(diff *cursortranscript.CodeBlockDiff, ref cursortranscript.CodeBlockRef) {
	switch ref.Status {
	case "accepted", "completed":
	case "rejected":
		return
	default:
		f.noteUnjoinableDiff(diff.DiffID, "has unsupported codeBlockData status %q", ref.Status)
		return
	}

	owner := f.ownerByBubble[ref.BubbleID]
	if owner == nil {
		f.noteUnjoinableDiff(diff.DiffID, "names bubble %s, which is not a listed owned assistant bubble", ref.BubbleID)
		return
	}
	path := resolvedPath(f.workingDir, ref.URI.FilePath())
	if path == "" {
		f.noteUnjoinableDiff(diff.DiffID, "has no file path")
		return
	}
	hunks, startLine, endLine, ok := cursorDiffHunks(diff.NewModelDiffWrtV0)
	if !ok {
		f.noteUnjoinableDiff(diff.DiffID, "has invalid line ranges")
		return
	}

	if evidence := f.toolByBubble[ref.BubbleID]; evidence != nil {
		if touch := f.editTouch(evidence.row.ID, path); touch != nil {
			touch.Hunks = append(touch.Hunks, hunks...)
			mergeTouchRange(touch, startLine, endLine)
			f.vendorRow(touch.ID, session.EntityFileTouch, "diffId", diff.DiffID, f.diffKey(diff.DiffID))
			return
		}
	}

	row := &session.FileTouch{
		ID:          f.ids.Mint("file_touch", "diff", diff.DiffID, path),
		UserInputID: owner.row.ID,
		SessionID:   f.session.ID,
		Path:        path,
		Kind:        session.TouchEdit,
		StartLine:   startLine,
		EndLine:     endLine,
		Hunks:       hunks,
	}
	f.touches = append(f.touches, row)
	f.vendorRow(row.ID, session.EntityFileTouch, "diffId", diff.DiffID, f.diffKey(diff.DiffID))
	f.b.report.note("composer %s codeBlockDiff %s did not join a tool edit touch; mapped as an orphan edit touch", f.data.ComposerID, diff.DiffID)
}

func refsAllRejected(refs []cursortranscript.CodeBlockRef) bool {
	if len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		if ref.Status != "rejected" {
			return false
		}
	}
	return true
}

func (f *composerState) editTouch(toolUseID, path string) *session.FileTouch {
	for _, touch := range f.touches {
		if touch.ToolUseID != nil && *touch.ToolUseID == toolUseID && touch.Kind == session.TouchEdit && touch.Path == path {
			return touch
		}
	}
	return nil
}

func cursorDiffHunks(source []cursortranscript.DiffHunk) ([]session.Hunk, *int, *int, bool) {
	hunks := make([]session.Hunk, 0, len(source))
	delta := 0
	previousOldEnd := 0
	var startLine, endLine *int
	for _, sourceHunk := range source {
		oldStart := sourceHunk.Original.StartLineNumber
		oldEnd := sourceHunk.Original.EndLineNumberExclusive
		oldLines := oldEnd - oldStart
		newStart := oldStart + delta
		newLines := len(sourceHunk.Modified)
		if oldStart < 1 || oldLines < 0 || newStart < 1 || (len(hunks) > 0 && oldStart < previousOldEnd) {
			return nil, nil, nil, false
		}
		hunks = append(hunks, session.Hunk{
			OldStart: oldStart,
			OldLines: oldLines,
			NewStart: newStart,
			NewLines: newLines,
		})
		delta += newLines - oldLines
		previousOldEnd = oldEnd

		changedStart := min(oldStart, newStart)
		changedEnd := max(oldStart+max(oldLines, 1)-1, newStart+max(newLines, 1)-1)
		if startLine == nil || changedStart < *startLine {
			startLine = new(changedStart)
		}
		if endLine == nil || changedEnd > *endLine {
			endLine = new(changedEnd)
		}
	}
	return hunks, startLine, endLine, true
}

func mergeTouchRange(touch *session.FileTouch, startLine, endLine *int) {
	if startLine != nil && (touch.StartLine == nil || *startLine < *touch.StartLine) {
		touch.StartLine = new(*startLine)
	}
	if endLine != nil && (touch.EndLine == nil || *endLine > *touch.EndLine) {
		touch.EndLine = new(*endLine)
	}
}

func (f *composerState) noteUnjoinableDiff(diffID, format string, args ...any) {
	reason := fmt.Sprintf(format, args...)
	f.b.report.note("composer %s codeBlockDiff %s could not be joined: %s", f.data.ComposerID, diffID, reason)
}

func (f *composerState) diffKey(diffID string) string {
	return "codeBlockDiff:" + f.data.ComposerID + ":" + diffID
}
