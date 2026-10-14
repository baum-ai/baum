package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/sessions/collector/model"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// rollout builds a minimal parseable Codex rollout file.
func rollout(sessionID, cwd string) string {
	return fmt.Sprintf(`{"timestamp":"2026-07-01T10:00:00.000Z","type":"session_meta","payload":{"id":%q,"cwd":%q,"cli_version":"0.69.0","model_provider":"openai"}}
{"timestamp":"2026-07-01T10:00:05.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}
`, sessionID, cwd)
}

func TestFindSessionsVerifiesRecordedWorkingDir(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	sessionsDir := filepath.Join(base, "codex", "sessions")
	writeFile(t, filepath.Join(sessionsDir, "2026", "07", "01", "rollout-in-repo.jsonl"), rollout("in-repo", repo))
	writeFile(t, filepath.Join(sessionsDir, "2026", "07", "02", "rollout-elsewhere.jsonl"),
		rollout("elsewhere", filepath.Join(base, "elsewhere")))
	writeFile(t, filepath.Join(sessionsDir, "2026", "07", "03", "rollout-no-meta.jsonl"),
		`{"timestamp":"2026-07-03T10:00:00.000Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`+"\n")
	// Not named like a rollout: the store also holds other jsonl files.
	writeFile(t, filepath.Join(sessionsDir, "2026", "07", "03", "history.jsonl"), rollout("history", repo))

	sessions, _, err := FindSessions(sessionsDir, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "in-repo" {
		t.Fatalf("sessions = %+v, want only in-repo", sessions)
	}
}

// TestFindSessionsReportsUnreadableRollouts: a rollout whose first record this
// build cannot decode is named in the report rather than dropped in silence.
// Discovery indexes the store through the typed decoder, so a store-wide format
// change would otherwise present every session as no sessions at all.
func TestFindSessionsReportsUnreadableRollouts(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	sessionsDir := filepath.Join(base, ".codex", "sessions")
	rollout := filepath.Join(sessionsDir, "2026", "07", "22", "rollout-test.jsonl")
	writeFile(t, rollout, fmt.Sprintf(`{"type":"session_meta","payload":{"id":"codex-1","cwd":%q}}`+"\n", repo))

	sessions, report, err := FindSessions(sessionsDir, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %+v, want none", sessions)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Path != rollout ||
		!strings.Contains(report.Skipped[0].Reason, "timestamp") {
		t.Fatalf("report = %+v, want the undecodable rollout", report.Skipped)
	}
}

func TestFindSessionsMissingStore(t *testing.T) {
	sessions, _, err := FindSessions(filepath.Join(t.TempDir(), "absent"), []string{t.TempDir()})
	if err != nil {
		t.Fatalf("missing store: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("sessions = %+v, want none", sessions)
	}
}

// TestFindSessionsNamesTheChildTree covers what an upload has to carry for the
// mapper to rebuild a Codex collaboration tree: the root, its children, and the
// rollout a forked child inherited its turns from.
func TestFindSessionsNamesTheChildTree(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	sessionsDir := filepath.Join(base, "codex", "sessions")
	day := filepath.Join(sessionsDir, "2026", "08", "01")
	writeFile(t, filepath.Join(day, "rollout-root.jsonl"), fmt.Sprintf(
		`{"timestamp":"2026-08-01T12:00:00Z","type":"session_meta","payload":{"id":"root","session_id":"root","cwd":%q,"source":"cli"}}
`, repo))
	writeFile(t, filepath.Join(day, "rollout-child.jsonl"), fmt.Sprintf(
		`{"timestamp":"2026-08-01T12:01:00Z","type":"session_meta","payload":{"id":"child","session_id":"root","cwd":%q,"parent_thread_id":"root","forked_from_id":"fork","source":{"subagent":{"thread_spawn":{"parent_thread_id":"root","depth":1,"agent_path":"worker"}}}}}
`, repo))
	writeFile(t, filepath.Join(day, "rollout-fork.jsonl"), fmt.Sprintf(
		`{"timestamp":"2026-08-01T11:59:00Z","type":"session_meta","payload":{"id":"fork","session_id":"fork","cwd":%q,"source":{"subagent":"review"}}}
{malformed body that FIND must not decode}
`, repo))

	sessions, _, err := FindSessions(sessionsDir, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v, want only the root", sessions)
	}
	want := []model.SessionFile{
		{Name: "rollout-root.jsonl", Kind: model.KindTranscript},
		{Name: "rollout-child.jsonl", Kind: model.KindSubTranscript},
		{Name: "rollout-fork.jsonl", Kind: model.KindForkSource},
	}
	files := sessions[0].Files
	if len(files) != len(want) {
		t.Fatalf("files = %+v, want %+v", files, want)
	}
	for i, file := range want {
		if files[i].Name != file.Name || files[i].Kind != file.Kind {
			t.Errorf("files[%d] = (%q, %q), want (%q, %q)", i, files[i].Name, files[i].Kind, file.Name, file.Kind)
		}
	}
}

// TestFindSessionsFallsBackToTheRootRollout: a torn child tree must still
// upload its root, with a warning, rather than losing the session.
func TestFindSessionsFallsBackToTheRootRollout(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	sessionsDir := filepath.Join(base, ".codex", "sessions")
	day := filepath.Join(sessionsDir, "2026", "07", "22")
	writeFile(t, filepath.Join(day, "rollout-root.jsonl"), fmt.Sprintf(
		`{"timestamp":"2026-07-22T12:00:00Z","type":"session_meta","payload":{"id":"codex-1","session_id":"codex-1","cwd":%q,"source":"cli"}}
`, repo))
	// Child names a fork source that is not in the store, so Discover fails.
	writeFile(t, filepath.Join(day, "rollout-child.jsonl"), fmt.Sprintf(
		`{"timestamp":"2026-07-22T12:01:00Z","type":"session_meta","payload":{"id":"child","session_id":"codex-1","cwd":%q,"parent_thread_id":"codex-1","forked_from_id":"missing-fork","source":{"subagent":{"thread_spawn":{"parent_thread_id":"codex-1","depth":1,"agent_path":"worker"}}}}}
`, repo))

	sessions, _, err := FindSessions(sessionsDir, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "codex-1" {
		t.Fatalf("sessions = %+v", sessions)
	}
	if len(sessions[0].Files) != 1 || sessions[0].Files[0].Kind != model.KindTranscript {
		t.Errorf("files = %+v, want the root rollout alone", sessions[0].Files)
	}
	if len(sessions[0].Warnings) == 0 {
		t.Error("the fallback was not reported as a warning")
	}
}

func TestFindSessionsSkipsBaumAgentRuns(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	sessionsDir := filepath.Join(base, "codex", "sessions")
	writeFile(t, filepath.Join(sessionsDir, "2026", "07", "01", "rollout-in-repo.jsonl"), rollout("in-repo", repo))
	writeFile(t, filepath.Join(sessionsDir, "2026", "07", "02", "rollout-run.jsonl"), rollout("run", filepath.Join(repo, agent.RunDirPrefix+"123")))
	writeFile(t, filepath.Join(sessionsDir, "2026", "07", "03", "rollout-run-checkout.jsonl"),
		rollout("run-checkout", filepath.Join(repo, agent.RunDirPrefix+"123", "checkout")))
	sessions, _, err := FindSessions(sessionsDir, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != "in-repo" {
		t.Fatalf("sessions = %+v, want only in-repo", sessions)
	}
}
