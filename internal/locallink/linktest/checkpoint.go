package linktest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions/collector/claudecode"
)

// AddEditor adds a Claude Code session with the given id to the fixture's
// store, one that only edited files (relative to the clone), between minutes
// 10 and 16 of the given hour (two digits, UTC) of the first commit's day, and
// named no commit. The first commit is at 10:30.
func (f Fixture) AddEditor(t *testing.T, id, hour string, files ...string) {
	t.Helper()
	const usage = `{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"service_tier":"standard"}`
	common := func(kind, uuid, parent, at string) string {
		return fmt.Sprintf(`"cwd":%q,"gitBranch":"feature","isSidechain":false,"sessionId":%q,"type":%q,"uuid":%q,"parentUuid":%s,"timestamp":%q`,
			f.Repo, id, kind, uuid, parent, at)
	}
	var uses, results []string
	for i, file := range files {
		uses = append(uses, fmt.Sprintf(`{"type":"tool_use","id":"toolu_%d","name":"Edit","input":{"file_path":%q,"old_string":"","new_string":"x"}}`,
			i, filepath.Join(f.Repo, file)))
		results = append(results, fmt.Sprintf(`{"tool_use_id":"toolu_%d","type":"tool_result","content":"edited"}`, i))
	}
	lines := []string{
		`{` + common("user", "u-1", "null", "2026-07-01T"+hour+":10:00.000Z") + `,"promptId":"p-1","message":{"role":"user","content":"edit files"}}`,
		`{` + common("assistant", "a-1", `"u-1"`, "2026-07-01T"+hour+":15:00.000Z") + `,"requestId":"req_1","message":{"id":"msg_1","role":"assistant","model":"claude-opus-5","stop_reason":"tool_use","usage":` + usage +
			`,"content":[` + strings.Join(uses, ",") + `]}}`,
		`{` + common("user", "u-2", `"a-1"`, "2026-07-01T"+hour+":16:00.000Z") + `,"promptId":"p-1","message":{"role":"user","content":[` + strings.Join(results, ",") + `]}}`,
	}
	write(t, filepath.Join(f.Store.ClaudeProjectsDir, claudecode.EncodeProjectDir(f.Repo), id+".jsonl"), strings.Join(lines, "\n")+"\n")
}

const (
	// CheckpointID is the Entire checkpoint AddCheckpoint commits.
	CheckpointID = "0004a7a444a0"
	// CheckpointTrailer names CheckpointID the way Entire does in a commit.
	CheckpointTrailer = "Entire-Checkpoint: " + CheckpointID
)

// CheckpointSessions are the ids of the two Claude Code sessions the
// checkpoint records; neither is SessionID.
var CheckpointSessions = []string{"7f85b0e9-4f7a-4a81-a27e-1393aa1e80f6", "d9f72f5a-32ee-4a09-83dc-6c2d52918c0a"}

// AddCheckpoint commits the two-session checkpoint fixture to the clone's
// Entire checkpoint branch at CheckpointID, and leaves the clone on main.
func (f Fixture) AddCheckpoint(t *testing.T) {
	t.Helper()
	f.addCheckpoint(t, func(text string) string { return text })
}

// AddCheckpointEditing is AddCheckpoint with the checkpoint's first session
// editing path, relative to the clone, instead of only reading a file.
func (f Fixture) AddCheckpointEditing(t *testing.T, path string) {
	t.Helper()
	f.addCheckpoint(t, func(text string) string {
		return strings.ReplaceAll(text, `"name":"Read","input":{"file_path":"/repo/parse.go"}`,
			`"name":"Edit","input":{"file_path":"/repo/`+path+`","old_string":"a","new_string":"b"}`)
	})
}

func (f Fixture) addCheckpoint(t *testing.T, edit func(string) string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the checkpoint fixture")
	}
	src := filepath.Join(filepath.Dir(file), "..", "..", "..", "sessions", "entirecheckpoint", "testdata", "two_claude")
	git(t, f.Repo, "checkout", "-q", "--orphan", "entire/checkpoints/v1")
	git(t, f.Repo, "rm", "-rqf", ".")
	if err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		write(t, filepath.Join(f.Repo, CheckpointID[:2], CheckpointID[2:], rel), edit(string(data)))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	git(t, f.Repo, "add", "-A")
	git(t, f.Repo, "commit", "-q", "-m", "Checkpoint: "+CheckpointID)
	git(t, f.Repo, "checkout", "-q", "main")
}
