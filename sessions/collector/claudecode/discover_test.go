package claudecode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lastpersonlabs/baum/agent"
	"github.com/lastpersonlabs/baum/sessions/collector/model"
)

// transcript builds a minimal parseable Claude Code transcript with a sidecar
// first line (no cwd) so probing has to scan past it.
func transcript(sessionID, cwd string) string {
	return fmt.Sprintf(`{"type":"mode","mode":"normal","sessionId":%[1]q}
{"parentUuid":null,"isSidechain":false,"cwd":%[2]q,"gitBranch":"main","type":"user","uuid":"u-1","sessionId":%[1]q,"timestamp":"2026-07-01T10:00:00.000Z","promptId":"p-1","message":{"role":"user","content":"hello"}}
`, sessionID, cwd)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindSessionsVerifiesRecordedWorkingDir(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "work", "demo")
	sub := filepath.Join(repo, "platform")
	projects := filepath.Join(base, "claude", "projects")

	sessions := []struct{ launchDir, sessionID string }{
		{repo, "aaaaaaaa-0000-4000-8000-000000000001"},
		{sub, "bbbbbbbb-0000-4000-8000-000000000002"},
		// Sibling path whose encoded project name shares the repo's prefix: a
		// candidate directory that cwd verification must reject.
		{repo + "-extra", "cccccccc-0000-4000-8000-000000000003"},
		{filepath.Join(base, "elsewhere"), "dddddddd-0000-4000-8000-000000000004"},
	}
	for _, s := range sessions {
		writeFile(t, filepath.Join(projects, EncodeProjectDir(s.launchDir), s.sessionID+".jsonl"),
			transcript(s.sessionID, s.launchDir))
	}
	// Entries inside a matching project directory that are not transcripts.
	writeFile(t, filepath.Join(projects, EncodeProjectDir(repo), "subagents", "agent-1.jsonl"), "ignored\n")
	writeFile(t, filepath.Join(projects, EncodeProjectDir(repo), "notes.txt"), "not a transcript")

	found, _, err := FindSessions(projects, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, session := range found {
		got[session.SessionID] = session.WorkingDir
	}
	want := map[string]string{
		"aaaaaaaa-0000-4000-8000-000000000001": repo,
		"bbbbbbbb-0000-4000-8000-000000000002": sub,
	}
	if len(got) != len(want) {
		t.Fatalf("sessions = %+v, want %v", found, want)
	}
	for id, cwd := range want {
		if got[id] != cwd {
			t.Errorf("session %s working dir = %q, want %q", id, got[id], cwd)
		}
	}
}

// TestFindSessionsReportsUnreadableTranscripts: a transcript that cannot be
// placed in a repository is named in the report rather than dropped in
// silence. "no sessions here" and "sessions I could not read" are different
// answers and must not look the same to a caller.
func TestFindSessionsReportsUnreadableTranscripts(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	projects := filepath.Join(base, "projects")
	projectDir := filepath.Join(projects, EncodeProjectDir(repo))
	const readable = "aaaaaaaa-0000-4000-8000-000000000001"
	writeFile(t, filepath.Join(projectDir, readable+".jsonl"), transcript(readable, repo))
	// Parseable records, but no cwd in any of them: nothing places this in a
	// repository, so it is neither a session nor a silent non-event.
	writeFile(t, filepath.Join(projectDir, "bbbbbbbb-0000-4000-8000-000000000002.jsonl"),
		`{"type":"mode","mode":"normal"}`+"\n")

	sessions, report, err := FindSessions(projects, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != readable {
		t.Fatalf("sessions = %+v, want only the readable transcript", sessions)
	}
	if len(report.Skipped) != 1 || !strings.HasSuffix(report.Skipped[0].Path, "000000000002.jsonl") {
		t.Fatalf("report = %+v, want the cwd-less transcript", report.Skipped)
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

// TestSessionFilesNamesTheSubagentTree is the load-bearing case for uploads: an
// archive carries no directory layout, so every subagent transcript and every
// spawn sidecar has to be named here or it is lost.
func TestSessionFilesNamesTheSubagentTree(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	projects := filepath.Join(base, "projects")
	const sessionID = "aaaaaaaa-0000-4000-8000-000000000001"
	projectDir := filepath.Join(projects, EncodeProjectDir(repo))
	writeFile(t, filepath.Join(projectDir, sessionID+".jsonl"), transcript(sessionID, repo))
	writeFile(t, filepath.Join(projectDir, sessionID, "subagents", "agent-b.jsonl"), "subagent\n")
	writeFile(t, filepath.Join(projectDir, sessionID, "subagents", "agent-b.meta.json"), "{}\n")
	writeFile(t, filepath.Join(projectDir, sessionID, "subagents", "agent-a.jsonl"), "subagent\n")
	writeFile(t, filepath.Join(projectDir, sessionID, "subagents", "agent-a.meta.json"), "{}\n")
	writeFile(t, filepath.Join(projectDir, sessionID, "subagents", "agent-.jsonl"), "excluded\n")
	// Neither of these is part of a session tree.
	writeFile(t, filepath.Join(projectDir, sessionID, "memory", "notes.md"), "excluded\n")
	writeFile(t, filepath.Join(projectDir, sessionID, "tool-results", "result.txt"), "excluded\n")

	sessions, _, err := FindSessions(projects, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v, want one", sessions)
	}
	want := []model.SessionFile{
		{Name: sessionID + ".jsonl", Kind: model.KindTranscript},
		{Name: "agent-a.jsonl", Kind: model.KindSubTranscript},
		{Name: "agent-a.meta.json", Kind: model.KindSubMetadata},
		{Name: "agent-b.jsonl", Kind: model.KindSubTranscript},
		{Name: "agent-b.meta.json", Kind: model.KindSubMetadata},
	}
	files := sessions[0].Files
	if len(files) != len(want) {
		t.Fatalf("files = %+v, want %+v", files, want)
	}
	for i, file := range want {
		if files[i].Name != file.Name || files[i].Kind != file.Kind {
			t.Errorf("files[%d] = (%q, %q), want (%q, %q)", i, files[i].Name, files[i].Kind, file.Name, file.Kind)
		}
		if files[i].Path == "" {
			t.Errorf("files[%d] has no local path", i)
		}
	}
}

func TestEncodeProjectDir(t *testing.T) {
	got := EncodeProjectDir("/home/user/my_repo.v2")
	want := "-home-user-my-repo-v2"
	if got != want {
		t.Errorf("EncodeProjectDir = %q, want %q", got, want)
	}
}

func TestFindSessionsSkipsBaumAgentRuns(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	projects := filepath.Join(base, "claude", "projects")
	for id, launchDir := range map[string]string{
		"aaaaaaaa-0000-4000-8000-000000000001": repo,
		"bbbbbbbb-0000-4000-8000-000000000002": filepath.Join(repo, agent.RunDirPrefix+"123"),
		"cccccccc-0000-4000-8000-000000000003": filepath.Join(repo, agent.RunDirPrefix+"123", "checkout"),
	} {
		writeFile(t, filepath.Join(projects, EncodeProjectDir(launchDir), id+".jsonl"), transcript(id, launchDir))
	}
	found, report, err := FindSessions(projects, []string{repo})
	if err != nil || len(report.Skipped) != 0 {
		t.Fatalf("%v %+v", err, report)
	}
	if len(found) != 1 || found[0].WorkingDir != repo {
		t.Fatalf("sessions = %+v, want only the one in the repository itself", found)
	}
}
