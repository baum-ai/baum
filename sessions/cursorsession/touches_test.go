package cursorsession

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lastpersonlabs/baum/linkage/sessioncorrelate"
	"github.com/lastpersonlabs/baum/sessions/cursortranscript"
	"github.com/lastpersonlabs/baum/sessions/session"
)

func buildDiffs(t *testing.T, bubbles []*cursortranscript.Bubble, unlisted []*cursortranscript.Bubble, refs cursortranscript.CodeBlockData, diffs []*cursortranscript.CodeBlockDiff) (*session.Graph, *Report) {
	t.Helper()
	return buildDiffsAt(t, "/repo", bubbles, unlisted, refs, diffs)
}

func buildDiffsAt(t *testing.T, workingDir string, bubbles []*cursortranscript.Bubble, unlisted []*cursortranscript.Bubble, refs cursortranscript.CodeBlockData, diffs []*cursortranscript.CodeBlockDiff) (*session.Graph, *Report) {
	t.Helper()
	headers := make([]cursortranscript.BubbleRef, 0, len(bubbles))
	for _, bubble := range bubbles {
		headers = append(headers, cursortranscript.BubbleRef{BubbleID: bubble.BubbleID, Type: bubble.Type})
	}
	graph, report, err := Build(&cursortranscript.Tree{Composer: &cursortranscript.Composer{
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

func TestWindowsDiffPathJoinsRelativeToolTouchOnLinuxWorker(t *testing.T) {
	input := inputBubble("u")
	edit := toolBubble("edit", "completed", "model")
	edit.Tool.Name = name("edit_file_v2")
	edit.Tool.Params = json.RawMessage(`{"relativeWorkspacePath":"pkg\\edit.go"}`)
	refs := cursortranscript.CodeBlockData{diffRef("windows", "edit", "accepted", `D:\repo\pkg\edit.go`)}
	diffs := []*cursortranscript.CodeBlockDiff{diff("windows", cursortranscript.DiffHunk{
		Original: cursortranscript.DiffRange{StartLineNumber: 2, EndLineNumberExclusive: 3},
		Modified: []string{"replacement"},
	})}

	graph, report := buildDiffsAt(t, `d:\repo`, []*cursortranscript.Bubble{input, edit}, nil, refs, diffs)
	if len(graph.FileTouches) != 1 {
		t.Fatalf("Windows diff produced touches %+v, want one joined touch", graph.FileTouches)
	}
	touch := graph.FileTouches[0]
	if touch.Path != "D:/repo/pkg/edit.go" || touch.ToolUseID == nil || len(touch.Hunks) != 1 {
		t.Fatalf("Windows diff touch = %+v", touch)
	}
	if notesContain(report.Notes, "mapped as an orphan edit touch") {
		t.Fatalf("Windows diff was reported as an orphan: %v", report.Notes)
	}
	correlated := sessioncorrelate.Correlate(graph, Vendor)
	if len(correlated.Files) != 1 || correlated.Files[0].Path != "pkg/edit.go" || correlated.Files[0].Kind != session.TouchEdit {
		t.Fatalf("Windows diff evidence = %+v, want pkg/edit.go edit", correlated.Files)
	}
}

func diff(id string, hunks ...cursortranscript.DiffHunk) *cursortranscript.CodeBlockDiff {
	return &cursortranscript.CodeBlockDiff{DiffID: id, NewModelDiffWrtV0: hunks}
}

func diffRef(id, bubble, status, path string) cursortranscript.CodeBlockRef {
	return cursortranscript.CodeBlockRef{
		BubbleID: bubble, DiffID: id, Status: status,
		URI: cursortranscript.ResourceURI{FSPath: path},
	}
}

func TestDiffsEnrichOneExistingToolTouch(t *testing.T) {
	input := inputBubble("u")
	edit := toolBubble("edit", "completed", "model")
	edit.Tool.Name = name("edit_file_v2")
	edit.Tool.Params = json.RawMessage(`{"relativeWorkspacePath":"pkg/edit.go"}`)
	refs := cursortranscript.CodeBlockData{
		diffRef("first", "edit", "accepted", "/repo/pkg/edit.go"),
		diffRef("second", "edit", "completed", "/repo/pkg/edit.go"),
	}
	diffs := []*cursortranscript.CodeBlockDiff{
		diff("first", cursortranscript.DiffHunk{
			Original: cursortranscript.DiffRange{StartLineNumber: 2, EndLineNumberExclusive: 2},
			Modified: []string{"one", "two"},
		}, cursortranscript.DiffHunk{
			Original: cursortranscript.DiffRange{StartLineNumber: 5, EndLineNumberExclusive: 6},
			Modified: []string{"replacement"},
		}),
		diff("second", cursortranscript.DiffHunk{
			Original: cursortranscript.DiffRange{StartLineNumber: 9, EndLineNumberExclusive: 11},
			Modified: []string{"shorter"},
		}),
	}

	graph, report := buildDiffs(t, []*cursortranscript.Bubble{input, edit}, nil, refs, diffs)
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

func TestAcceptedDiffWithoutToolMapsOwnedOrphan(t *testing.T) {
	first := inputBubble("first")
	second := inputBubble("second")
	text := assistantBubble("text")
	ref := diffRef("orphan", "text", "accepted", "pkg/orphan.go")
	graph, report := buildDiffs(t, []*cursortranscript.Bubble{first, second, text}, nil,
		cursortranscript.CodeBlockData{ref}, []*cursortranscript.CodeBlockDiff{diff("orphan")})

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
	if !notesContain(report.Notes, "mapped as an orphan edit touch") {
		t.Fatalf("orphan report notes = %v", report.Notes)
	}
}

func TestRejectedDiffCreatesNoEvidenceOrWarning(t *testing.T) {
	input, text := inputBubble("u"), assistantBubble("text")
	graph, report := buildDiffs(t, []*cursortranscript.Bubble{input, text}, nil,
		cursortranscript.CodeBlockData{
			diffRef("rejected", "text", "rejected", "/repo/no.go"),
			diffRef("missing-rejected", "text", "rejected", "/repo/still-no.go"),
		},
		[]*cursortranscript.CodeBlockDiff{diff("rejected", cursortranscript.DiffHunk{
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

func TestUnlistedAndAmbiguousDiffsAreReportedNotMapped(t *testing.T) {
	input := inputBubble("u")
	listed := assistantBubble("listed")
	unlisted := assistantBubble("unlisted")
	refs := cursortranscript.CodeBlockData{
		diffRef("unlisted", "unlisted", "accepted", "/repo/unlisted.go"),
		diffRef("duplicate", "listed", "accepted", "/repo/one.go"),
		diffRef("duplicate", "listed", "accepted", "/repo/two.go"),
		diffRef("missing", "listed", "accepted", "/repo/missing.go"),
	}
	graph, report := buildDiffs(t, []*cursortranscript.Bubble{input, listed}, []*cursortranscript.Bubble{unlisted}, refs,
		[]*cursortranscript.CodeBlockDiff{diff("unlisted"), diff("duplicate")})
	if len(graph.FileTouches) != 0 {
		t.Fatalf("unjoinable diff touches = %+v", graph.FileTouches)
	}
	for _, want := range []string{
		"which is not a listed owned assistant bubble",
		"has 2 codeBlockData references",
		"has codeBlockData but no captured diff record",
	} {
		if !notesContain(report.Notes, want) {
			t.Errorf("notes %v do not contain %q", report.Notes, want)
		}
	}
}

func TestInvalidDiffRangeIsReportedNotMapped(t *testing.T) {
	input, text := inputBubble("u"), assistantBubble("text")
	graph, report := buildDiffs(t, []*cursortranscript.Bubble{input, text}, nil,
		cursortranscript.CodeBlockData{diffRef("invalid", "text", "accepted", "/repo/invalid.go")},
		[]*cursortranscript.CodeBlockDiff{diff("invalid", cursortranscript.DiffHunk{
			Original: cursortranscript.DiffRange{StartLineNumber: 0, EndLineNumberExclusive: 1},
			Modified: []string{"invalid"},
		})})
	if len(graph.FileTouches) != 0 || !notesContain(report.Notes, "has invalid line ranges") {
		t.Fatalf("invalid diff mapping = %+v / %v", graph.FileTouches, report.Notes)
	}
}

func TestDiffRangeRejectsNegativeCumulativeNewStart(t *testing.T) {
	input, text := inputBubble("u"), assistantBubble("text")
	graph, report := buildDiffs(t, []*cursortranscript.Bubble{input, text}, nil,
		cursortranscript.CodeBlockData{diffRef("invalid-delta", "text", "accepted", "/repo/invalid.go")},
		[]*cursortranscript.CodeBlockDiff{diff("invalid-delta",
			cursortranscript.DiffHunk{
				Original: cursortranscript.DiffRange{StartLineNumber: 1, EndLineNumberExclusive: 5},
			},
			cursortranscript.DiffHunk{
				Original: cursortranscript.DiffRange{StartLineNumber: 3, EndLineNumberExclusive: 4},
				Modified: []string{"too early after deletion"},
			},
		)})
	if len(graph.FileTouches) != 0 || !notesContain(report.Notes, "has invalid line ranges") {
		t.Fatalf("negative cumulative new_start mapping = %+v / %v", graph.FileTouches, report.Notes)
	}
}

func TestDiffRangesRejectOverlapAndBacktracking(t *testing.T) {
	for name, hunks := range map[string][]cursortranscript.DiffHunk{
		"overlap": {
			{Original: cursortranscript.DiffRange{StartLineNumber: 5, EndLineNumberExclusive: 10}},
			{Original: cursortranscript.DiffRange{StartLineNumber: 7, EndLineNumberExclusive: 8}, Modified: []string{"overlap"}},
		},
		"backtracking": {
			{Original: cursortranscript.DiffRange{StartLineNumber: 5, EndLineNumberExclusive: 5}, Modified: []string{"insert"}},
			{Original: cursortranscript.DiffRange{StartLineNumber: 4, EndLineNumberExclusive: 4}, Modified: []string{"earlier"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if mapped, _, _, ok := cursorDiffHunks(hunks); ok {
				t.Fatalf("cursorDiffHunks mapped out-of-order ranges: %+v", mapped)
			}
		})
	}
}

func TestDiffPathMismatchMapsSeparateOrphan(t *testing.T) {
	input := inputBubble("u")
	edit := toolBubble("edit", "completed", "model")
	edit.Tool.Name = name("edit_file_v2")
	edit.Tool.Params = json.RawMessage(`{"relativeWorkspacePath":"tool.go"}`)
	graph, report := buildDiffs(t, []*cursortranscript.Bubble{input, edit}, nil,
		cursortranscript.CodeBlockData{diffRef("other-path", "edit", "accepted", "/repo/diff.go")},
		[]*cursortranscript.CodeBlockDiff{diff("other-path", cursortranscript.DiffHunk{
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
		!notesContain(report.Notes, "mapped as an orphan edit touch") {
		t.Fatalf("path-mismatch mapping = %+v / %v", graph.FileTouches, report.Notes)
	}
}

func TestDiffCaptureWarningDistinguishesOldAndEmptyPayloads(t *testing.T) {
	input := inputBubble("u")
	_, oldReport, err := buildBubbles(t, "completed", "", input)
	if err != nil {
		t.Fatal(err)
	}
	if !notesContain(oldReport.Notes, "payload predates codeBlockDiff capture") {
		t.Fatalf("old payload notes = %v", oldReport.Notes)
	}

	_, currentReport := buildDiffs(t, []*cursortranscript.Bubble{inputBubble("u")}, nil, nil,
		[]*cursortranscript.CodeBlockDiff{})
	if notesContain(currentReport.Notes, "payload predates codeBlockDiff capture") {
		t.Fatalf("captured empty payload notes = %v", currentReport.Notes)
	}
}

func notesContain(notes []string, part string) bool {
	for _, note := range notes {
		if strings.Contains(note, part) {
			return true
		}
	}
	return false
}
