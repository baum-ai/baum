package cursortranscript

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultStorePath(t *testing.T) {
	// os.UserConfigDir reads a different variable on each OS, so point the one
	// this OS honours at a temp dir and expect Cursor's layout beneath it.
	base := t.TempDir()
	config := base
	switch runtime.GOOS {
	case "windows":
		t.Setenv("AppData", base)
	case "darwin":
		t.Setenv("HOME", base)
		config = filepath.Join(base, "Library", "Application Support")
	default:
		t.Setenv("XDG_CONFIG_HOME", base)
	}
	want := filepath.Join(config, "Cursor", "User", "globalStorage", "state.vscdb")
	if got := DefaultStorePath(); got != want {
		t.Fatalf("DefaultStorePath = %q, want %q", got, want)
	}
}

func TestOpenRejectsBackupStore(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "state.vscdb.backup")); err == nil || !strings.Contains(err.Error(), "backup stores") {
		t.Fatalf("Open(.backup) error = %v", err)
	}
}

func fixtureStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join("testdata", "store.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestDecodeFixtureFields(t *testing.T) {
	data, err := DecodeComposerData([]byte(`{
		"composerId":"root","_v":17,"status":"aborted",
		"modelConfig":{"modelName":"default-model"},
		"workspaceIdentifier":{"uri":{"fsPath":"/repo","scheme":"file"}},
		"createdOnBranch":"feature/cursor","committedToBranch":"feature/cursor"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if data.Version != 17 || data.Status != "aborted" || data.ModelConfig.ModelName != "default-model" {
		t.Fatalf("composer data = %+v", data)
	}
	if data.CreatedOnBranch != "feature/cursor" || data.CommittedToBranch != "feature/cursor" {
		t.Fatalf("branch fields = %q / %q", data.CreatedOnBranch, data.CommittedToBranch)
	}
	if data.WorkspaceIdentifier.URI.FilePath() != "/repo" {
		t.Fatalf("workspace = %+v", data.WorkspaceIdentifier)
	}

	read, err := DecodeBubble([]byte(`{
		"bubbleId":"read","type":2,
		"tokenCount":{"inputTokens":100,"outputTokens":20},
		"modelInfo":{"modelName":"override-model"},
		"toolFormerData":{"name":"read_file_v2","toolCallId":"call-read","status":"completed"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if read.BubbleID != "read" || read.Tool.ToolName() != "read_file_v2" || read.Tool.Status != "completed" {
		t.Fatalf("read bubble = %+v", read)
	}
	if read.TokenCount.InputTokens != 100 || read.ModelInfo.ModelName != "override-model" {
		t.Fatalf("read usage/model = %+v / %+v", read.TokenCount, read.ModelInfo)
	}
	if read.Tool.AdditionalData != nil {
		t.Fatalf("ordinary tool has additional data: %+v", read.Tool.AdditionalData)
	}
	spawnBubble, err := DecodeBubble([]byte(`{
		"bubbleId":"spawn","type":2,
		"toolFormerData":{"name":"Task","toolCallId":"call-child","additionalData":{"subagentComposerId":"child"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	spawn := spawnBubble.Tool
	if spawn.AdditionalData.SubagentComposerID != "child" || spawn.ToolCallID != "call-child" {
		t.Fatalf("spawn tool = %+v", spawn)
	}
	degenerateBubble, err := DecodeBubble([]byte(`{
		"bubbleId":"degenerate","type":2,"toolFormerData":{"name":null}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !degenerateBubble.Tool.Degenerate() {
		t.Fatal("null-name tool was not classified as degenerate")
	}
}

func TestResourceURIUsesTheRecordedPathFormat(t *testing.T) {
	if got := (ResourceURI{FSPath: `d:\repo\pkg\..\edit.go`}).FilePath(); got != "D:/repo/edit.go" {
		t.Fatalf("Windows resource path = %q", got)
	}
	if got := (ResourceURI{Path: "/repo/pkg/../edit.go", Scheme: "file"}).FilePath(); got != "/repo/edit.go" {
		t.Fatalf("POSIX resource path = %q", got)
	}
}

func TestDecodeRejectsUnknownBubbleType(t *testing.T) {
	_, err := DecodeBubble([]byte(`{"bubbleId":"x","type":7}`))
	if err == nil || !strings.Contains(err.Error(), "unknown bubble type 7") {
		t.Fatalf("error = %v", err)
	}
}
