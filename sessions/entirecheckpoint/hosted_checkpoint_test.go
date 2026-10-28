package entirecheckpoint

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/baum-ai/baum/sessions/traceprocessing"
)

func TestHostedParseCheckpointResolvesBranchRootRefs(t *testing.T) {
	checkpoint, err := hostedParseCheckpoint(hostedOsDirFS(filepath.Join("testdata", "two_claude")))
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Metadata.CheckpointID != "0004a7a444a0" || len(checkpoint.Sessions) != 2 {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}
	for _, snapshot := range checkpoint.Sessions {
		if !snapshot.FullPresent || !snapshot.CompactPresent || !snapshot.PromptPresent {
			t.Fatalf("session %d presence = full:%v compact:%v prompt:%v", snapshot.Index, snapshot.FullPresent, snapshot.CompactPresent, snapshot.PromptPresent)
		}
		if !snapshot.Hash.Checked || snapshot.Hash.Mismatch {
			t.Fatalf("session %d hash = %+v", snapshot.Index, snapshot.Hash)
		}
	}
}

// Entire's ref store (a checkpoint ref's root tree is the checkpoint) writes
// session paths without the branch's shard prefix: "/0/metadata.json" where
// the branch writes "/00/04a7a444a0/0/metadata.json". Both read the same.
func TestHostedParseCheckpointResolvesRefStoreRefs(t *testing.T) {
	root := filepath.Join("testdata", "two_claude")
	files := fstest.MapFS{}
	err := fs.WalkDir(os.DirFS(root), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return err
		}
		if name == "metadata.json" {
			data = []byte(strings.ReplaceAll(string(data), `"/00/04a7a444a0/`, `"/`))
			if !strings.Contains(string(data), `"/0/metadata.json"`) {
				t.Fatalf("metadata.json was not rewritten to the ref layout: %s", data)
			}
		}
		files[name] = &fstest.MapFile{Data: data}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := hostedParseCheckpoint(files)
	if err != nil {
		t.Fatal(err)
	}
	branch, err := hostedParseCheckpoint(hostedOsDirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(refs.Sessions) != len(branch.Sessions) {
		t.Fatalf("sessions = %d, want %d", len(refs.Sessions), len(branch.Sessions))
	}
	for i, snapshot := range refs.Sessions {
		want := branch.Sessions[i]
		if snapshot.Directory != want.Directory || snapshot.Metadata.SessionID != want.Metadata.SessionID ||
			string(snapshot.Full) != string(want.Full) || string(snapshot.CompactRaw) != string(want.CompactRaw) ||
			string(snapshot.Prompt) != string(want.Prompt) || snapshot.Hash != want.Hash {
			t.Fatalf("session %d read from the ref layout differs from the branch layout", i)
		}
	}
}

func TestHostedParseCheckpointAcceptsMissingOptionalFiles(t *testing.T) {
	checkpoint, err := hostedParseCheckpoint(hostedOsDirFS(filepath.Join("testdata", "skill_events")))
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.Sessions) != 3 {
		t.Fatalf("sessions = %d, want 3", len(checkpoint.Sessions))
	}
	for _, snapshot := range checkpoint.Sessions {
		if snapshot.FullPresent || snapshot.CompactPresent || snapshot.PromptPresent {
			t.Fatalf("session %d unexpectedly found an optional file", snapshot.Index)
		}
	}
	if got := len(checkpoint.Sessions[2].Metadata.SkillEvents); got != 2 {
		t.Fatalf("skill events = %d, want 2", got)
	}
}

func TestHostedParseCheckpointRejectsEscapingRef(t *testing.T) {
	root := fstest.MapFS{
		"metadata.json": &fstest.MapFile{Data: []byte(`{"checkpoint_id":"abc","sessions":[{"metadata":"../../outside/metadata.json"}]}`)},
	}
	_, err := hostedParseCheckpoint(root)
	if !errors.Is(err, ErrEscapingSessionRef) {
		t.Fatalf("error = %v, want ErrEscapingSessionRef", err)
	}
}

func TestHostedParseCheckpointRejectsSessionsReadingTheSameFile(t *testing.T) {
	big := &fstest.MapFile{Data: []byte("{}\n")}
	for name, files := range map[string]fstest.MapFS{
		"same explicit ref": {
			"metadata.json": &fstest.MapFile{Data: []byte(`{"checkpoint_id":"abc","sessions":[` +
				`{"metadata":"0/metadata.json","transcript":"0/full.jsonl"},` +
				`{"metadata":"1/metadata.json","transcript":"/0/full.jsonl"}]}`)},
			"0/metadata.json": &fstest.MapFile{Data: []byte(`{"session_id":"a"}`)},
			"0/full.jsonl":    big,
			"1/metadata.json": &fstest.MapFile{Data: []byte(`{"session_id":"b"}`)},
		},
		// Missing transcript refs fall back to the metadata's directory, so
		// distinct entries can still land on one full.jsonl.
		"fallback to the same directory": {
			"metadata.json": &fstest.MapFile{Data: []byte(`{"checkpoint_id":"abc","sessions":[` +
				`{"metadata":"p0/0/metadata.json","transcript":"p0/0/full.jsonl"},` +
				`{"metadata":"p1/0/metadata.json","transcript":"p1/0/full.jsonl"}]}`)},
			"p0/0/metadata.json": &fstest.MapFile{Data: []byte(`{"session_id":"a"}`)},
			"p1/0/metadata.json": &fstest.MapFile{Data: []byte(`{"session_id":"b"}`)},
			"0/full.jsonl":       big,
		},
		"implicit and explicit": {
			"metadata.json":   &fstest.MapFile{Data: []byte(`{"checkpoint_id":"abc","sessions":[{},{"metadata":"1/metadata.json","transcript":"0/full.jsonl"}]}`)},
			"0/metadata.json": &fstest.MapFile{Data: []byte(`{"session_id":"a"}`)},
			"0/full.jsonl":    big,
			"1/metadata.json": &fstest.MapFile{Data: []byte(`{"session_id":"b"}`)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := hostedParseCheckpoint(files); err == nil {
				t.Fatal("expected an error for two sessions reading one file")
			}
		})
	}
}

func TestHostedParseCheckpointRejectsOneSessionReadingAFileTwice(t *testing.T) {
	// Every ref names one file that decodes as session metadata, so each
	// reader would otherwise get its own copy of the same bytes.
	root := fstest.MapFS{
		"metadata.json": &fstest.MapFile{Data: []byte(`{"checkpoint_id":"abc","sessions":[` +
			`{"metadata":"0/blob.json","transcript":"0/blob.json","prompt":"0/blob.json","assets_manifest":"0/blob.json"}]}`)},
		"0/blob.json": &fstest.MapFile{Data: []byte(`{"session_id":"a"}`)},
	}
	_, err := hostedParseCheckpoint(root)
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("error = %v, want a repeated read", err)
	}
}

func TestHostedParseCheckpointRejectsRepeatedSessionID(t *testing.T) {
	root := fstest.MapFS{
		"metadata.json":   &fstest.MapFile{Data: []byte(`{"checkpoint_id":"abc","sessions":[{},{}]}`)},
		"0/metadata.json": &fstest.MapFile{Data: []byte(`{"session_id":"a"}`)},
		"1/metadata.json": &fstest.MapFile{Data: []byte(`{"session_id":"a"}`)},
	}
	if _, err := hostedParseCheckpoint(root); err == nil {
		t.Fatal("expected an error for a repeated session_id")
	}
}

func TestHostedParseCheckpointRejectsTooManySessions(t *testing.T) {
	sessions := strings.TrimSuffix(strings.Repeat("{},", maxSessions+1), ",")
	root := fstest.MapFS{
		"metadata.json": &fstest.MapFile{Data: []byte(`{"checkpoint_id":"abc","sessions":[` + sessions + `]}`)},
	}
	if _, err := hostedParseCheckpoint(root); err == nil {
		t.Fatal("expected an error for too many sessions")
	}
}

func TestHostedDecodeCheckpointMetadataStopsAtSessionCap(t *testing.T) {
	// A million empty entries would expand to roughly 100 MB of SessionRefs
	// if the array were decoded before the cap was checked.
	raw := []byte(`{"checkpoint_id":"abc","sessions":[` +
		strings.TrimSuffix(strings.Repeat("{},", 1<<20), ",") + `]}`)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := hostedDecodeCheckpointMetadata(raw)
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("expected an error for too many sessions")
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 32<<20 {
		t.Fatalf("decoding allocated %d bytes before rejecting the sessions array", allocated)
	}
}

func TestHostedMetadataUnknownKeysAreRetainedForSurvey(t *testing.T) {
	root := fstest.MapFS{
		"metadata.json":   &fstest.MapFile{Data: []byte(`{"checkpoint_id":"abc","cli_version":"1.2.3","future_checkpoint":true,"sessions":[{}]}`)},
		"0/metadata.json": &fstest.MapFile{Data: []byte(`{"session_id":"s","cli_version":"1.2.4","future_session":{"x":1}}`)},
	}
	checkpoint, err := hostedParseCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	shape := Survey([]*Checkpoint{checkpoint})
	if shape.UnknownCheckpointKeys["future_checkpoint"] != 1 || shape.UnknownSessionKeys["future_session"] != 1 {
		t.Fatalf("shape = %+v", shape)
	}
	if shape.CLIVersions.Min != "1.2.3" || shape.CLIVersions.Max != "1.2.4" {
		t.Fatalf("versions = %+v", shape.CLIVersions)
	}
}

func TestHostedSurveySkipsNilSnapshots(t *testing.T) {
	checkpoint := &Checkpoint{Sessions: []*SessionSnapshot{
		nil,
		{Metadata: SessionMetadata{CLIVersion: "1.2.3"}},
	}}
	shape := Survey([]*Checkpoint{checkpoint})
	if shape.CLIVersions.Seen["1.2.3"] != 1 {
		t.Fatalf("versions = %+v", shape.CLIVersions)
	}
}

func TestHostedContentHashMismatchIsData(t *testing.T) {
	verification := VerifyContentHash([]byte("different"), true, "sha256:0000")
	if !verification.Checked || !verification.Mismatch || verification.Actual == "" {
		t.Fatalf("verification = %+v", verification)
	}
}

// osDirFS is a seam so fixture tests use the same fs.FS contract as tar/git
// callers while keeping their paths relative in failures.
func hostedOsDirFS(dir string) fs.FS { return os.DirFS(dir) }

// Tasks sit at the checkpoint root and are listed by directory, so they must
// read the same from the archive filesystem the hosted ingest builds as from
// a directory.
func TestHostedParseCheckpointReadsTasksFromAnArchiveTree(t *testing.T) {
	root := filepath.Join("testdata", "claude_subagent")
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		files[filepath.ToSlash(relative)] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := hostedParseCheckpoint(traceprocessing.HostedBytesFS(files, traceprocessing.TreeFilePolicy))
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.Tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(checkpoint.Tasks))
	}
	review := checkpoint.Tasks[1]
	if review.Directory != "toolu_review" || !review.TranscriptPresent || review.AgentID() != "a1111111111111111" ||
		review.Meta.TaskDescription != "Review parser" || review.Problem != "" {
		t.Fatalf("review task = %+v", review)
	}
	if lost := checkpoint.Tasks[0]; lost.TranscriptPresent || !lost.MetaPresent {
		t.Fatalf("task.json-only task = %+v", lost)
	}
}

func TestHostedTaskProblemsDoNotFailTheCheckpoint(t *testing.T) {
	base := fstest.MapFS{
		"metadata.json":               &fstest.MapFile{Data: []byte(`{"checkpoint_id":"abc","sessions":[{}]}`)},
		"0/metadata.json":             &fstest.MapFile{Data: []byte(`{"session_id":"s"}`)},
		"tasks/toolu_a/task.json":     &fstest.MapFile{Data: []byte(`{"tool_use_id":"toolu_other"}`)},
		"tasks/toolu_b/task.json":     &fstest.MapFile{Data: []byte(`{`)},
		"tasks/toolu_c/agent-x.jsonl": &fstest.MapFile{Data: []byte("{}\n")},
		"tasks/toolu_c/agent-y.jsonl": &fstest.MapFile{Data: []byte("{}\n")},
		"tasks/toolu_d/task.json":     &fstest.MapFile{Data: []byte(`{"agent_id":"z"}`)},
		"tasks/toolu_d/agent-x.jsonl": &fstest.MapFile{Data: []byte("{}\n")},
	}
	checkpoint, err := hostedParseCheckpoint(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(checkpoint.Tasks) != 4 {
		t.Fatalf("tasks = %d, want 4", len(checkpoint.Tasks))
	}
	for _, task := range checkpoint.Tasks {
		if task.Problem == "" {
			t.Errorf("task %s has no problem: %+v", task.Directory, task)
		}
	}
}
