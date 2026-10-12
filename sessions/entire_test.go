package sessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lastpersonlabs/baum/sessions/collector/claudecode"
	"github.com/lastpersonlabs/baum/sessions/collector/entire"
	"github.com/lastpersonlabs/baum/sessions/vendors"
)

// The fixture checkpoint 0004a7a444a0 records these two sessions.
const (
	firstSession  = "7f85b0e9-4f7a-4a81-a27e-1393aa1e80f6" // in directory 0
	secondSession = "d9f72f5a-32ee-4a09-83dc-6c2d52918c0a" // in directory 1
)

// addCheckpoint commits a copy of the fixture checkpoint under id, with
// session 0's snapshot dated a day later so it is the newest. edit may change
// or drop (return false) each file, named by its path inside the checkpoint.
func addCheckpoint(t *testing.T, fx fixture, id string, edit func(rel, text string) (string, bool)) {
	t.Helper()
	git(t, fx.repo, "checkout", "-q", entire.CheckpointBranch)
	src := filepath.Join(fx.repo, "00", "04a7a444a0")
	dst := filepath.Join(fx.repo, id[:2], id[2:])
	if err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(string(data), "/00/04a7a444a0/", "/"+id[:2]+"/"+id[2:]+"/")
		text = strings.ReplaceAll(text, `"0004a7a444a0"`, fmt.Sprintf("%q", id))
		if rel == "0/metadata.json" {
			text = strings.ReplaceAll(text, "2026-07-26T15:52:20Z", "2026-07-27T15:52:20Z")
		}
		keep := true
		if edit != nil {
			text, keep = edit(rel, text)
		}
		if keep {
			write(t, filepath.Join(dst, filepath.FromSlash(rel)), text)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	git(t, fx.repo, "add", ".")
	git(t, fx.repo, "commit", "-q", "-m", "checkpoint "+id)
	git(t, fx.repo, "checkout", "-q", "main")
}

func loadEntireOnly(t *testing.T, fx fixture) ([]Session, Report) {
	t.Helper()
	opts := fx.options()
	opts.Sources = []vendors.Slug{Entire}
	sessions, report, err := Load(context.Background(), fx.repo, opts)
	if err != nil {
		t.Fatal(err)
	}
	return sessions, report
}

func byID(sessions []Session) map[string][]Session {
	out := map[string][]Session{}
	for _, s := range sessions {
		out[s.ID] = append(out[s.ID], s)
	}
	return out
}

// Every checkpoint that recorded a session holds a snapshot of it; the
// session is listed once, from the newest.
func TestLoadListsEachEntireSessionOnce(t *testing.T) {
	fx := newFixture(t)
	addCheckpoint(t, fx, "111111111111", nil)
	sessions, report := loadEntireOnly(t, fx)
	if len(report.Failures) != 0 {
		t.Errorf("failures = %+v", report.Failures)
	}
	found := byID(sessions)
	if len(sessions) != 2 || len(found[firstSession]) != 1 || len(found[secondSession]) != 1 {
		t.Fatalf("sessions = %+v, want each of the two once", sessions)
	}
	if got := found[firstSession][0].Path; got != "111111111111" {
		t.Errorf("%s was read from %s, not its newest checkpoint", firstSession, got)
	}
}

// A newest snapshot with no transcript, only metadata and the prompt, does
// not hide the older one that has it.
func TestLoadPrefersASnapshotWithATranscript(t *testing.T) {
	fx := newFixture(t)
	addCheckpoint(t, fx, "222222222222", func(rel, text string) (string, bool) {
		switch rel {
		case "0/full.jsonl", "0/transcript.jsonl", "0/content_hash.txt":
			return "", false
		case "metadata.json":
			text = strings.ReplaceAll(text, `"transcript": "/22/2222222222/0/full.jsonl",`, "")
			text = strings.ReplaceAll(text, `"compact_transcript": "/22/2222222222/0/transcript.jsonl",`, "")
			text = strings.ReplaceAll(text, `"content_hash": "/22/2222222222/0/content_hash.txt",`, "")
		}
		return text, true
	})
	sessions, report := loadEntireOnly(t, fx)
	if len(report.Failures) != 0 {
		t.Errorf("failures = %+v", report.Failures)
	}
	found := byID(sessions)
	if len(found[firstSession]) != 1 {
		t.Fatalf("sessions = %+v", sessions)
	}
	s := found[firstSession][0]
	if s.Path != "0004a7a444a0" || !HasTranscript(s.Graph) {
		t.Errorf("%s was read from %s (transcript %v), want the older checkpoint with its transcript", s.ID, s.Path, HasTranscript(s.Graph))
	}

	// With nothing older to fall back to, the prompt-only snapshot still
	// lists the session.
	only := newFixture(t)
	git(t, only.repo, "checkout", "-q", entire.CheckpointBranch)
	git(t, only.repo, "rm", "-q", filepath.Join("00", "04a7a444a0", "0", "full.jsonl"), filepath.Join("00", "04a7a444a0", "0", "transcript.jsonl"))
	git(t, only.repo, "commit", "-q", "-m", "drop the transcript")
	git(t, only.repo, "checkout", "-q", "main")
	sessions, _ = loadEntireOnly(t, only)
	if found := byID(sessions); len(found[firstSession]) != 1 || HasTranscript(found[firstSession][0].Graph) {
		t.Errorf("sessions = %+v, want the prompt-only snapshot", sessions)
	}
}

// A newest snapshot that cannot be read, or cannot be mapped, is reported and
// the next newest is used instead.
func TestLoadFallsBackFromAnUnusableNewestSnapshot(t *testing.T) {
	for name, edit := range map[string]func(rel, text string) (string, bool){
		// The compact transcript is not JSON: the checkpoint does not parse.
		"unreadable checkpoint": func(rel, text string) (string, bool) {
			if rel == "0/transcript.jsonl" {
				return "not json\n", true
			}
			return text, true
		},
		// The native transcript is not JSON and there is no compact one: the
		// session does not map.
		"unmappable session": func(rel, text string) (string, bool) {
			switch rel {
			case "0/full.jsonl":
				return "not json\n", true
			case "0/transcript.jsonl":
				return "", false
			case "metadata.json":
				return strings.ReplaceAll(text, `"compact_transcript": "/33/3333333333/0/transcript.jsonl",`, ""), true
			}
			return text, true
		},
	} {
		t.Run(name, func(t *testing.T) {
			fx := newFixture(t)
			addCheckpoint(t, fx, "333333333333", edit)
			sessions, report := loadEntireOnly(t, fx)
			found := byID(sessions)
			if len(found[firstSession]) != 1 || found[firstSession][0].Path != "0004a7a444a0" {
				t.Fatalf("sessions = %+v, want %s from the older checkpoint", sessions, firstSession)
			}
			if len(report.Failures) == 0 {
				t.Error("the unusable snapshot was not reported")
			}
		})
	}
}

// A session the agent's own store holds is listed from there, never again
// from a checkpoint that recorded it.
func TestLoadSkipsEntireSessionsTheNativeStoreHas(t *testing.T) {
	fx := newFixture(t)
	write(t, filepath.Join(fx.claudeDir, claudecode.EncodeProjectDir(fx.repo), firstSession+".jsonl"),
		fmt.Sprintf(`{"parentUuid":null,"isSidechain":false,"cwd":%q,"gitBranch":"main","type":"user","uuid":"u-1","sessionId":%q,"timestamp":"2026-07-01T10:00:00.000Z","promptId":"p-1","message":{"role":"user","content":"hello"}}
`, fx.repo, firstSession))
	sessions, _, err := Load(context.Background(), fx.repo, fx.options())
	if err != nil {
		t.Fatal(err)
	}
	found := byID(sessions)
	if len(found[firstSession]) != 1 || found[firstSession][0].Source != vendors.ClaudeCode {
		t.Errorf("%s listed as %+v, want once from Claude Code", firstSession, found[firstSession])
	}
	if len(found[secondSession]) != 1 || found[secondSession][0].Source != Entire {
		t.Errorf("%s listed as %+v, want once from Entire", secondSession, found[secondSession])
	}
}
