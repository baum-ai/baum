package entirecheckpoint

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/lastpersonlabs/reviewpal/internal/goldentest"
)

func TestCorpusFixturesGolden(t *testing.T) {
	for _, name := range []string{"two_claude", "codex", "skill_events"} {
		t.Run(name, func(t *testing.T) {
			checkpoint, err := ParseCheckpoint(osDirFS(filepath.Join("testdata", name)))
			if err != nil {
				t.Fatal(err)
			}
			goldentest.Golden(t, filepath.Join("testdata", "golden", name+".json"), summarize(checkpoint))
		})
	}
}

func TestParseCheckpointResolvesBranchRootRefs(t *testing.T) {
	checkpoint, err := ParseCheckpoint(osDirFS(filepath.Join("testdata", "two_claude")))
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

func TestParseCheckpointAcceptsMissingOptionalFiles(t *testing.T) {
	checkpoint, err := ParseCheckpoint(osDirFS(filepath.Join("testdata", "skill_events")))
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

func TestParseCheckpointRejectsEscapingRef(t *testing.T) {
	root := fstest.MapFS{
		"metadata.json": &fstest.MapFile{Data: []byte(`{"checkpoint_id":"abc","sessions":[{"metadata":"../../outside/metadata.json"}]}`)},
	}
	_, err := ParseCheckpoint(root)
	if !errors.Is(err, ErrEscapingSessionRef) {
		t.Fatalf("error = %v, want ErrEscapingSessionRef", err)
	}
}

func TestMetadataUnknownKeysAreRetainedForSurvey(t *testing.T) {
	root := fstest.MapFS{
		"metadata.json":   &fstest.MapFile{Data: []byte(`{"checkpoint_id":"abc","cli_version":"1.2.3","future_checkpoint":true,"sessions":[{}]}`)},
		"0/metadata.json": &fstest.MapFile{Data: []byte(`{"session_id":"s","cli_version":"1.2.4","future_session":{"x":1}}`)},
	}
	checkpoint, err := ParseCheckpoint(root)
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

func TestSurveySkipsNilSnapshots(t *testing.T) {
	checkpoint := &Checkpoint{Sessions: []*SessionSnapshot{
		nil,
		{Metadata: SessionMetadata{CLIVersion: "1.2.3"}},
	}}
	shape := Survey([]*Checkpoint{checkpoint})
	if shape.CLIVersions.Seen["1.2.3"] != 1 {
		t.Fatalf("versions = %+v", shape.CLIVersions)
	}
}

func TestContentHashMismatchIsData(t *testing.T) {
	verification := VerifyContentHash([]byte("different"), true, "sha256:0000")
	if !verification.Checked || !verification.Mismatch || verification.Actual == "" {
		t.Fatalf("verification = %+v", verification)
	}
}

type fixtureSummary struct {
	CheckpointID string           `json:"checkpoint_id"`
	CLIVersion   string           `json:"cli_version"`
	Sessions     []sessionSummary `json:"sessions"`
	Shape        Shape            `json:"shape"`
}

type sessionSummary struct {
	Index          int              `json:"index"`
	SessionID      string           `json:"session_id"`
	Agent          string           `json:"agent"`
	FullBytes      int              `json:"full_bytes"`
	CompactRecords int              `json:"compact_records"`
	BlockKinds     map[string]int   `json:"block_kinds"`
	PromptPresent  bool             `json:"prompt_present"`
	SkillEvents    int              `json:"skill_events"`
	Hash           HashVerification `json:"hash"`
}

func summarize(checkpoint *Checkpoint) fixtureSummary {
	summary := fixtureSummary{
		CheckpointID: checkpoint.Metadata.CheckpointID,
		CLIVersion:   checkpoint.Metadata.CLIVersion,
		Shape:        Survey([]*Checkpoint{checkpoint}),
	}
	for _, snapshot := range checkpoint.Sessions {
		session := sessionSummary{
			Index: snapshot.Index, SessionID: snapshot.Metadata.SessionID, Agent: snapshot.Metadata.Agent,
			FullBytes: len(snapshot.Full), CompactRecords: len(snapshot.Compact),
			BlockKinds: map[string]int{}, PromptPresent: snapshot.PromptPresent,
			SkillEvents: len(snapshot.Metadata.SkillEvents), Hash: snapshot.Hash,
		}
		for _, record := range snapshot.Compact {
			for _, block := range record.Content {
				session.BlockKinds[string(block.Kind)]++
			}
		}
		summary.Sessions = append(summary.Sessions, session)
	}
	return summary
}

// osDirFS is a seam so fixture tests use the same fs.FS contract as tar/git
// callers while keeping their paths relative in failures.
func osDirFS(dir string) fs.FS { return os.DirFS(dir) }
