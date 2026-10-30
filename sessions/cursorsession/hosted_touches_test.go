package cursorsession

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/baum-ai/baum/linkage/sessioncorrelate"
	"github.com/baum-ai/baum/sessions/cursortranscript"
	"github.com/baum-ai/baum/sessions/session"
)

func hostedBuildDiffs(t *testing.T, bubbles []*cursortranscript.Bubble, unlisted []*cursortranscript.Bubble, refs cursortranscript.CodeBlockData, diffs []*cursortranscript.CodeBlockDiff) (*session.Graph, *Report) {
	t.Helper()
	return hostedBuildDiffsAt(t, "/repo", bubbles, unlisted, refs, diffs)
}

func hostedBuildDiffsAt(t *testing.T, workingDir string, bubbles []*cursortranscript.Bubble, unlisted []*cursortranscript.Bubble, refs cursortranscript.CodeBlockData, diffs []*cursortranscript.CodeBlockDiff) (*session.Graph, *Report) {
	t.Helper()
	headers := make([]cursortranscript.BubbleRef, 0, len(bubbles))
	for _, bubble := range bubbles {
		headers = append(headers, cursortranscript.BubbleRef{BubbleID: bubble.BubbleID, Type: bubble.Type})
	}
	graph, report, err := hostedBuild(&cursortranscript.Tree{Composer: &cursortranscript.Composer{
		Data: &cursortranscript.ComposerData{
			ComposerID: "test", Status: "completed", CreatedAt: 1767225600000,
			Headers: headers, CodeBlocks: refs,
			WorkspaceIdentifier: &cursortranscript.WorkspaceIdentifier{
				URI: cursortranscript.ResourceURI{FSPath: workingDir},
				Raw: json.RawMessage(`{"uri":{"scheme":"file","fsPath":"/repo"}}`),
			},
		},
		Bubbles: bubbles, Unlisted: unlisted, Diffs: diffs,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return graph, report
}

func TestHostedWindowsDiffPathJoinsRelativeToolTouchOnLinuxWorker(t *testing.T) {
	input := hostedInputBubble("u")
	edit := hostedToolBubble("edit", "completed", "model")
	edit.Tool.Name = hostedName("edit_file_v2")
	edit.Tool.Params = json.RawMessage(`{"relativeWorkspacePath":"pkg\\edit.go"}`)
	refs := cursortranscript.CodeBlockData{hostedDiffRef("windows", "edit", "accepted", `D:\repo\pkg\edit.go`)}
	diffs := []*cursortranscript.CodeBlockDiff{hostedDiff("windows", cursortranscript.DiffHunk{
		Original: cursortranscript.DiffRange{StartLineNumber: 2, EndLineNumberExclusive: 3},
		Modified: []string{"replacement"},
	})}

	graph, report := hostedBuildDiffsAt(t, `d:\repo`, []*cursortranscript.Bubble{input, edit}, nil, refs, diffs)
	if len(graph.FileTouches) != 1 {
		t.Fatalf("Windows diff produced touches %+v, want one joined touch", graph.FileTouches)
	}
	touch := graph.FileTouches[0]
	if touch.Path != "D:/repo/pkg/edit.go" || touch.ToolUseID == nil || len(touch.Hunks) != 1 {
		t.Fatalf("Windows diff touch = %+v", touch)
	}
	if hostedNotesContain(report.Notes, "mapped as an orphan edit touch") {
		t.Fatalf("Windows diff was reported as an orphan: %v", report.Notes)
	}
	correlated := sessioncorrelate.Correlate(graph, Vendor)
	if len(correlated.Files) != 1 || correlated.Files[0].Path != "pkg/edit.go" || correlated.Files[0].Kind != session.TouchEdit {
		t.Fatalf("Windows diff evidence = %+v, want pkg/edit.go edit", correlated.Files)
	}
}

func hostedDiff(id string, hunks ...cursortranscript.DiffHunk) *cursortranscript.CodeBlockDiff {
	return &cursortranscript.CodeBlockDiff{DiffID: id, NewModelDiffWrtV0: hunks}
}

func hostedDiffRef(id, bubble, status, path string) cursortranscript.CodeBlockRef {
	return cursortranscript.CodeBlockRef{
		BubbleID: bubble, DiffID: id, Status: status,
		URI: cursortranscript.ResourceURI{FSPath: path},
	}
}

func TestHostedDiffsEnrichOneExistingToolTouch(t *testing.T) {
	input := hostedInputBubble("u")
	edit := hostedToolBubble("edit", "completed", "model")
	edit.Tool.Name = hostedName("edit_file_v2")
	edit.Tool.Params = json.RawMessage(`{"relativeWorkspacePath":"pkg/edit.go"}`)
	refs := cursortranscript.CodeBlockData{
		hostedDiffRef("first", "edit", "accepted", "/repo/pkg/edit.go"),
		hostedDiffRef("second", "edit", "completed", "/repo/pkg/edit.go"),
	}
	diffs := []*cursortranscript.CodeBlockDiff{
		hostedDiff("first", cursortranscript.DiffHunk{
			Original: cursortranscript.DiffRange{StartLineNumber: 2, EndLineNumberExclusive: 2},
			Modified: []string{"one", "two"},
		}, cursortranscript.DiffHunk{
			Original: cursortranscript.DiffRange{StartLineNumber: 5, EndLineNumberExclusive: 6},
			Modified: []string{"replacement"},
		}),
		hostedDiff("second", cursortranscript.DiffHunk{
			Original: cursortranscript.DiffRange{StartLineNumber: 9, EndLineNumberExclusive: 11},
			Modified: []string{"shorter"},
		}),
	}

	graph, report := hostedBuildDiffs(t, []*cursortranscript.Bubble{input, edit}, nil, refs, diffs)
	if len(graph.FileTouches) != 1 {
		t.Fatalf("file touches = %+v, want one enriched tool touch", graph.FileTouches)
	}
	touch := graph.FileTouches[0]
	if touch.ToolUseID == nil || touch.Path != "/repo/pkg/edit.go" || len(touch.Hunks) != 3 {
		t.Fatalf("enriched touch = %+v", touch)
	}
	if hunk := touch.Hunks[1]; hunk.OldStart != 5 || hunk.NewStart != 7 || hunk.OldLines != 1 || hunk.NewLines != 1 {
		t.Fatalf("second hunk = %+v, want cumulative insertion delta in new_start", hunk)
	}
	if touch.StartLine == nil || *touch.StartLine != 2 || touch.EndLine == nil || *touch.EndLine != 10 {
		t.Fatalf("touch range = %v..%v, want 2..10", touch.StartLine, touch.EndLine)
	}
	for _, note := range report.Notes {
		if strings.Contains(note, "codeBlockDiff") {
			t.Fatalf("joined diffs produced warning %q", note)
		}
	}
}

func TestHostedAcceptedDiffWithoutToolMapsOwnedOrphan(t *testing.T) {
	first := hostedInputBubble("first")
	second := hostedInputBubble("second")
	text := hostedAssistantBubble("text")
	ref := hostedDiffRef("orphan", "text", "accepted", "pkg/orphan.go")
	graph, report := hostedBuildDiffs(t, []*cursortranscript.Bubble{first, second, text}, nil,
		cursortranscript.CodeBlockData{ref}, []*cursortranscript.CodeBlockDiff{hostedDiff("orphan")})

	if len(graph.FileTouches) != 1 {
		t.Fatalf("file touches = %+v", graph.FileTouches)
	}
	touch := graph.FileTouches[0]
	if touch.ToolUseID != nil || touch.UserInputID != graph.UserInputs[1].ID || touch.Path != "/repo/pkg/orphan.go" || touch.Kind != session.TouchEdit {
		t.Fatalf("orphan touch = %+v", touch)
	}
	if touch.StartLine != nil || touch.EndLine != nil || len(touch.Hunks) != 0 {
		t.Fatalf("empty-hunk diff invented a range: %+v", touch)
	}
	wantSource := "codeBlockDiff:test:orphan"
	foundSource := false
	for _, vendorID := range graph.VendorIDs {
		foundSource = foundSource || vendorID.OurID == touch.ID && vendorID.VendorIDKind == "diffId" &&
			vendorID.SourceRef != nil && *vendorID.SourceRef == wantSource
	}
	if !foundSource {
		t.Fatalf("orphan touch has no diff source_ref in %+v", graph.VendorIDs)
	}
	if !hostedNotesContain(report.Notes, "mapped as an orphan edit touch") {
		t.Fatalf("orphan report notes = %v", report.Notes)
	}
}

func TestHostedRejectedDiffCreatesNoEvidenceOrWarning(t *testing.T) {
	input, text := hostedInputBubble("u"), hostedAssistantBubble("text")
	graph, report := hostedBuildDiffs(t, []*cursortranscript.Bubble{input, text}, nil,
		cursortranscript.CodeBlockData{
			hostedDiffRef("rejected", "text", "rejected", "/repo/no.go"),
			hostedDiffRef("missing-rejected", "text", "rejected", "/repo/still-no.go"),
		},
		[]*cursortranscript.CodeBlockDiff{hostedDiff("rejected", cursortranscript.DiffHunk{
			Original: cursortranscript.DiffRange{StartLineNumber: 1, EndLineNumberExclusive: 2},
			Modified: []string{"proposal"},
		})})
	if len(graph.FileTouches) != 0 {
		t.Fatalf("rejected diff touches = %+v", graph.FileTouches)
	}
	for _, note := range report.Notes {
		if strings.Contains(note, "codeBlockDiff") {
			t.Fatalf("rejected diff produced warning %q", note)
		}
	}
}

func TestHostedUnlistedAndAmbiguousDiffsAreReportedNotMapped(t *testing.T) {
	input := hostedInputBubble("u")
	listed := hostedAssistantBubble("listed")
	unlisted := hostedAssistantBubble("unlisted")
	refs := cursortranscript.CodeBlockData{
		hostedDiffRef("unlisted", "unlisted", "accepted", "/repo/unlisted.go"),
		hostedDiffRef("duplicate", "listed", "accepted", "/repo/one.go"),
		hostedDiffRef("duplicate", "listed", "accepted", "/repo/two.go"),
		hostedDiffRef("missing", "listed", "accepted", "/repo/missing.go"),
	}
	graph, report := hostedBuildDiffs(t, []*cursortranscript.Bubble{input, listed}, []*cursortranscript.Bubble{unlisted}, refs,
		[]*cursortranscript.CodeBlockDiff{hostedDiff("unlisted"), hostedDiff("duplicate")})
	if len(graph.FileTouches) != 0 {
		t.Fatalf("unjoinable diff touches = %+v", graph.FileTouches)
	}
	for _, want := range []string{
		"which is not a listed owned assistant bubble",
		"has 2 codeBlockData references",
		"has codeBlockData but no captured diff record",
	} {
		if !hostedNotesContain(report.Notes, want) {
			t.Errorf("notes %v do not contain %q", report.Notes, want)
		}
	}
}

func TestHostedInvalidDiffRangeIsReportedNotMapped(t *testing.T) {
	input, text := hostedInputBubble("u"), hostedAssistantBubble("text")
	graph, report := hostedBuildDiffs(t, []*cursortranscript.Bubble{input, text}, nil,
		cursortranscript.CodeBlockData{hostedDiffRef("invalid", "text", "accepted", "/repo/invalid.go")},
		[]*cursortranscript.CodeBlockDiff{hostedDiff("invalid", cursortranscript.DiffHunk{
			Original: cursortranscript.DiffRange{StartLineNumber: 0, EndLineNumberExclusive: 1},
			Modified: []string{"invalid"},
		})})
	if len(graph.FileTouches) != 0 || !hostedNotesContain(report.Notes, "has invalid line ranges") {
		t.Fatalf("invalid diff mapping = %+v / %v", graph.FileTouches, report.Notes)
	}
}

func TestHostedDiffRangeRejectsNegativeCumulativeNewStart(t *testing.T) {
	input, text := hostedInputBubble("u"), hostedAssistantBubble("text")
	graph, report := hostedBuildDiffs(t, []*cursortranscript.Bubble{input, text}, nil,
		cursortranscript.CodeBlockData{hostedDiffRef("invalid-delta", "text", "accepted", "/repo/invalid.go")},
		[]*cursortranscript.CodeBlockDiff{hostedDiff("invalid-delta",
			cursortranscript.DiffHunk{
				Original: cursortranscript.DiffRange{StartLineNumber: 1, EndLineNumberExclusive: 5},
			},
			cursortranscript.DiffHunk{
				Original: cursortranscript.DiffRange{StartLineNumber: 3, EndLineNumberExclusive: 4},
				Modified: []string{"too early after deletion"},
			},
		)})
	if len(graph.FileTouches) != 0 || !hostedNotesContain(report.Notes, "has invalid line ranges") {
		t.Fatalf("negative cumulative new_start mapping = %+v / %v", graph.FileTouches, report.Notes)
	}
}

func TestHostedDiffRangesRejectOverlapAndBacktracking(t *testing.T) {
	for hostedName, hunks := range map[string][]cursortranscript.DiffHunk{
		"overlap": {
			{Original: cursortranscript.DiffRange{StartLineNumber: 5, EndLineNumberExclusive: 10}},
			{Original: cursortranscript.DiffRange{StartLineNumber: 7, EndLineNumberExclusive: 8}, Modified: []string{"overlap"}},
		},
		"backtracking": {
			{Original: cursortranscript.DiffRange{StartLineNumber: 5, EndLineNumberExclusive: 5}, Modified: []string{"insert"}},
			{Original: cursortranscript.DiffRange{StartLineNumber: 4, EndLineNumberExclusive: 4}, Modified: []string{"earlier"}},
		},
	} {
		t.Run(hostedName, func(t *testing.T) {
			if mapped, _, _, ok := cursorDiffHunks(hunks); ok {
				t.Fatalf("cursorDiffHunks mapped out-of-order ranges: %+v", mapped)
			}
		})
	}
}

func TestHostedDiffPathMismatchMapsSeparateOrphan(t *testing.T) {
	input := hostedInputBubble("u")
	edit := hostedToolBubble("edit", "completed", "model")
	edit.Tool.Name = hostedName("edit_file_v2")
	edit.Tool.Params = json.RawMessage(`{"relativeWorkspacePath":"tool.go"}`)
	graph, report := hostedBuildDiffs(t, []*cursortranscript.Bubble{input, edit}, nil,
		cursortranscript.CodeBlockData{hostedDiffRef("other-path", "edit", "accepted", "/repo/diff.go")},
		[]*cursortranscript.CodeBlockDiff{hostedDiff("other-path", cursortranscript.DiffHunk{
			Original: cursortranscript.DiffRange{StartLineNumber: 4, EndLineNumberExclusive: 5},
			Modified: []string{"changed"},
		})})
	if len(graph.FileTouches) != 2 {
		t.Fatalf("path-mismatch touches = %+v", graph.FileTouches)
	}
	byPath := map[string]session.FileTouch{}
	for _, touch := range graph.FileTouches {
		byPath[touch.Path] = touch
	}
	toolTouch, toolOK := byPath["/repo/tool.go"]
	orphanTouch, orphanOK := byPath["/repo/diff.go"]
	if !toolOK || !orphanOK || toolTouch.ToolUseID == nil || orphanTouch.ToolUseID != nil ||
		!hostedNotesContain(report.Notes, "mapped as an orphan edit touch") {
		t.Fatalf("path-mismatch mapping = %+v / %v", graph.FileTouches, report.Notes)
	}
}

func TestHostedDiffCaptureWarningDistinguishesOldAndEmptyPayloads(t *testing.T) {
	input := hostedInputBubble("u")
	_, oldReport, err := hostedBuildBubbles(t, "completed", "", input)
	if err != nil {
		t.Fatal(err)
	}
	if !hostedNotesContain(oldReport.Notes, "payload predates codeBlockDiff capture") {
		t.Fatalf("old payload notes = %v", oldReport.Notes)
	}

	_, currentReport := hostedBuildDiffs(t, []*cursortranscript.Bubble{hostedInputBubble("u")}, nil, nil,
		[]*cursortranscript.CodeBlockDiff{})
	if hostedNotesContain(currentReport.Notes, "payload predates codeBlockDiff capture") {
		t.Fatalf("captured empty payload notes = %v", currentReport.Notes)
	}
}

func hostedNotesContain(notes []string, part string) bool {
	for _, note := range notes {
		if strings.Contains(note, part) {
			return true
		}
	}
	return false
}
