package claudetranscript

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/baumai/baum/internal/goldentest"
	"github.com/baumai/baum/sessions/collector/claudecode"
)

// FixtureSession is the transcript tree under testdata: a main thread, the
// subagent it spawned, and the subagent that one spawned in turn.
const FixtureSession = "11111111-2222-3333-4444-555555555555"

// FixturePath is the main transcript of the shared fixture tree, resolved from
// dir (".." from a sibling package's tests).
func FixturePath(dir string) string {
	return filepath.Join(dir, "internal", "traceprocessing", "claudetranscript", "testdata", FixtureSession+".jsonl")
}

func fixtureTree(t *testing.T) *Tree {
	t.Helper()
	tree, err := DecodeLocalFiles(claudecode.SessionFiles(filepath.Join("testdata", FixtureSession+".jsonl")))
	if err != nil {
		t.Fatalf("DecodeLocalFiles: %v", err)
	}
	return tree
}

func TestDecodeLocalFilesFindsTheWholeTree(t *testing.T) {
	tree := fixtureTree(t)

	if tree.Main.SessionID != FixtureSession {
		t.Fatalf("main session = %q, want %q", tree.Main.SessionID, FixtureSession)
	}
	if !tree.Main.Main() {
		t.Fatal("the main transcript reports an agent id")
	}
	if len(tree.Subagents) != 2 {
		t.Fatalf("found %d subagents, want 2", len(tree.Subagents))
	}

	// Subagent transcripts carry the parent's session id, which is exactly why
	// the session id does not identify one.
	for _, file := range tree.Subagents {
		if file.SessionID != FixtureSession {
			t.Errorf("%s reports session %q, want the parent's %q", file.Path, file.SessionID, FixtureSession)
		}
		if file.AgentID == "" {
			t.Errorf("%s has no agent id", file.Path)
		}
	}
}

// The spawn tree is only in the sidecars: nested transcripts sit flat in the same
// directory and carry the root session id, so the layout cannot tell you that a2
// was spawned from inside a1.
func TestDecodeLocalFilesReadsTheSpawnLinksFromTheSidecars(t *testing.T) {
	tree := fixtureTree(t)

	spawned := tree.SubagentForToolUse("toolu_spawn")
	if spawned == nil {
		t.Fatal("no subagent is linked to the main thread's spawn")
	}
	if spawned.AgentID != "a1" {
		t.Fatalf("toolu_spawn spawned %q, want a1", spawned.AgentID)
	}
	if spawned.Meta == nil || spawned.Meta.SpawnDepth != 1 || spawned.Meta.ParentAgentID != "" {
		t.Fatalf("a1 meta = %+v, want depth 1 with no parent agent", spawned.Meta)
	}

	// The nested spawn's tool result carries no toolUseResult at all, so the
	// documented agentId link would miss it entirely.
	nested := tree.SubagentForToolUse("toolu_nested")
	if nested == nil {
		t.Fatal("the nested spawn is not linked to a subagent")
	}
	if nested.AgentID != "a2" {
		t.Fatalf("toolu_nested spawned %q, want a2", nested.AgentID)
	}
	if nested.Meta.SpawnDepth != 2 || nested.Meta.ParentAgentID != "a1" {
		t.Fatalf("a2 meta = %+v, want depth 2 under a1", nested.Meta)
	}

	if tree.SubagentForToolUse("toolu_denied") != nil {
		t.Error("a refused spawn was linked to a transcript")
	}
	if tree.SubagentForToolUse("") != nil {
		t.Error("an empty tool use id matched a transcript")
	}
}

// The main file's stem is the session id. It wins over the lines, which is what
// keeps a copied transcript from claiming the session it was copied from.
func TestDecodeLocalFilesWithoutSubagentsTakesTheSessionFromTheFileName(t *testing.T) {
	const stem = "22222222-2222-2222-2222-222222222222"
	dir := t.TempDir()
	path := filepath.Join(dir, stem+".jsonl")
	if err := os.WriteFile(path, []byte(envelope+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := DecodeLocalFiles(claudecode.SessionFiles(path))
	if err != nil {
		t.Fatalf("DecodeLocalFiles: %v", err)
	}
	if len(tree.Subagents) != 0 {
		t.Fatalf("found %d subagents, want none", len(tree.Subagents))
	}
	if tree.Main.SessionID != stem {
		t.Fatalf("session = %q, want the file's stem %q", tree.Main.SessionID, stem)
	}
}

func decodeLocalTestTree(t *testing.T, mainLine string, subagentLine ...string) (*Tree, error) {
	t.Helper()
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "full.jsonl")
	files := map[string]string{mainPath: mainLine}
	if len(subagentLine) > 0 {
		files[filepath.Join(dir, "full", "subagents", "agent-b9.jsonl")] = subagentLine[0]
	}
	for path, contents := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return DecodeLocalFiles(claudecode.SessionFiles(mainPath))
}

// A file whose name is not a session id — an extracted or embedded transcript —
// falls back to what the lines say.
func TestDecodeLocalFilesFallsBackToTheSessionOnTheLines(t *testing.T) {
	tree, err := decodeLocalTestTree(t, envelope)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if tree.Main.SessionID != "S" {
		t.Fatalf("session = %q, want the id from the line", tree.Main.SessionID)
	}
}

func TestDecodeLocalFilesRejectsATranscriptWithNoSession(t *testing.T) {
	if _, err := decodeLocalTestTree(t, `{"type":"mode","mode":"plan"}`); err == nil || !strings.Contains(err.Error(), "names no session") {
		t.Fatalf("want a missing-session error, got %v", err)
	}
}

// A subagent transcript must say which agent it belongs to. Reading one whose
// lines disagree with its file name would attribute its work to the wrong session.
func TestDecodeLocalFilesRejectsAMisattributedSubagent(t *testing.T) {
	line := `{"type":"assistant","uuid":"a","sessionId":"S","agentId":"other","isSidechain":true,` +
		`"timestamp":"2026-07-30T09:00:00.000Z","message":{"id":"m","content":[]}}`
	_, err := decodeLocalTestTree(t, envelope, line)
	if err == nil || !strings.Contains(err.Error(), "reports agent") {
		t.Fatalf("want a mismatched-agent error, got %v", err)
	}
}

func TestDecodeLocalFilesRejectsASubagentLineThatIsNotASidechain(t *testing.T) {
	line := `{"type":"assistant","uuid":"a","sessionId":"S","agentId":"b9",` +
		`"timestamp":"2026-07-30T09:00:00.000Z","message":{"id":"m","content":[]}}`
	_, err := decodeLocalTestTree(t, envelope, line)
	if err == nil || !strings.Contains(err.Error(), "not marked as a sidechain") {
		t.Fatalf("want a sidechain error, got %v", err)
	}
}

// classification is the golden shape of a tree: how many of each type and class
// there are, and which transcript each spawn opened. A format change shows up
// here as a moved count.
type classification struct {
	Transcripts []transcriptShape `json:"transcripts"`
}

type transcriptShape struct {
	File          string         `json:"file"`
	SessionID     string         `json:"session_id"`
	AgentID       string         `json:"agent_id,omitempty"`
	SpawnedBy     string         `json:"spawned_by_tool_use,omitempty"`
	SpawnDepth    int            `json:"spawn_depth,omitempty"`
	ParentAgentID string         `json:"parent_agent_id,omitempty"`
	Lines         int            `json:"lines"`
	TornFinalLine bool           `json:"torn_final_line"`
	Types         map[string]int `json:"types"`
	Classes       map[string]int `json:"classes"`
	UserKinds     map[string]int `json:"user_kinds,omitempty"`
	Blocks        map[string]int `json:"blocks,omitempty"`
	ToolUses      []string       `json:"tool_uses,omitempty"`
	ToolResults   []string       `json:"tool_results,omitempty"`
}

func TestClassificationGolden(t *testing.T) {
	tree := fixtureTree(t)

	got := classification{}
	for _, file := range tree.Files() {
		shape := transcriptShape{
			File:          filepath.Base(file.Path),
			SessionID:     file.SessionID,
			AgentID:       file.AgentID,
			Lines:         len(file.Lines),
			TornFinalLine: file.TornFinalLine,
			Types:         map[string]int{},
			Classes:       map[string]int{},
			UserKinds:     map[string]int{},
			Blocks:        map[string]int{},
		}
		if file.Meta != nil {
			shape.SpawnedBy = file.Meta.ToolUseID
			shape.SpawnDepth = file.Meta.SpawnDepth
			shape.ParentAgentID = file.Meta.ParentAgentID
		}
		for _, line := range file.Lines {
			shape.Types[line.Type]++
			shape.Classes[string(line.Class)]++
			if line.Class == ClassUser {
				shape.UserKinds[string(line.UserKind)]++
			}
			for _, block := range line.Message.Blocks() {
				shape.Blocks[block.Type]++
				switch block.Type {
				case "tool_use":
					shape.ToolUses = append(shape.ToolUses, fmt.Sprintf("%s:%s", block.Name, block.ID))
				case "tool_result":
					shape.ToolResults = append(shape.ToolResults, block.ToolUseID)
				}
			}
		}
		sort.Strings(shape.ToolUses)
		sort.Strings(shape.ToolResults)
		got.Transcripts = append(got.Transcripts, shape)
	}

	goldentest.Golden(t, filepath.Join("testdata", "golden", "classification.json"), got)
}
