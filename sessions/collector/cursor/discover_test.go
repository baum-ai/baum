package cursor

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/sessions/cursortranscript"
)

func TestFindSessionsFiltersByRepositoryAndCarriesNestedTree(t *testing.T) {
	store := filepath.Join("..", "..", "cursortranscript", "testdata", "store.vscdb")

	// Discovery localizes the fixture's recorded POSIX workspace paths with
	// filepath.Clean, so the roots are spelled the local way too.
	none, _, err := FindSessions([]string{store}, []string{filepath.FromSlash("/elsewhere")})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("out-of-repository sessions = %+v", none)
	}

	sessions, report, err := FindSessions([]string{store}, []string{filepath.FromSlash("/current"), filepath.FromSlash("/repo")})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 0 {
		t.Fatalf("scan report = %+v", report)
	}
	if len(sessions) != 3 {
		t.Fatalf("sessions = %d, want root, headerless, and draft: %+v", len(sessions), sessions)
	}
	var rootFound bool
	for _, found := range sessions {
		if found.SessionID != "root" {
			continue
		}
		rootFound = true
		if found.WorkingDir != filepath.FromSlash("/repo") || len(found.Files) != 1 || len(found.Files[0].Data) == 0 || found.Files[0].Path != "" {
			t.Fatalf("root discovery = %+v", found)
		}
		tree, err := cursortranscript.DecodeTreePayload(found.Files[0].Data)
		if err != nil {
			t.Fatal(err)
		}
		if len(tree.Children) != 1 || tree.Children[0].ComposerID() != "child" ||
			len(tree.Children[0].Children) != 1 || tree.Children[0].Children[0].ComposerID() != "grandchild" {
			t.Fatalf("nested tree = %+v", tree)
		}
		if len(tree.Composer.Diffs) != 2 || tree.Composer.Diffs[0].DiffID != "11111111-1111-4111-8111-111111111111" ||
			tree.Composer.Diffs[1].DiffID != "55555555-5555-4555-8555-555555555555" {
			t.Fatalf("collected diffs = %+v", tree.Composer.Diffs)
		}
	}
	if !rootFound {
		t.Fatal("root composer was not collected")
	}
}

func TestFindSessionsDoesNotReportBrokenOtherRepository(t *testing.T) {
	source := filepath.Join("..", "..", "cursortranscript", "testdata", "store.vscdb")
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(t.TempDir(), "state.vscdb")
	if err := os.WriteFile(store, body, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", store)
	if err != nil {
		t.Fatal(err)
	}
	workspace := `{"workspaceIdentifier":{"uri":{"scheme":"file","fsPath":"/other-repository"}}}`
	if _, err := db.Exec(`INSERT INTO composerHeaders VALUES (?, '', 0, 0, 0, 0, 0, 0, ?)`, "broken-other", workspace); err != nil {
		t.Fatal(err)
	}
	composer := `{"composerId":"broken-other","workspaceIdentifier":{"uri":{"scheme":"file","fsPath":"/other-repository"}},"fullConversationHeadersOnly":[{"bubbleId":"missing","type":1}]}`
	if _, err := db.Exec(`INSERT INTO cursorDiskKV VALUES (?, ?)`, "composerData:broken-other", composer); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	sessions, report, err := FindSessions([]string{store}, []string{filepath.FromSlash("/repo")})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 || len(report.Skipped) != 0 {
		t.Fatalf("sessions/report = %+v / %+v", sessions, report)
	}
}

func TestFindSessionsSkipsBaumAgentRuns(t *testing.T) {
	source := filepath.Join("..", "..", "cursortranscript", "testdata", "store.vscdb")
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(t.TempDir(), "state.vscdb")
	if err := os.WriteFile(store, body, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", store)
	if err != nil {
		t.Fatal(err)
	}
	// Both composers lack the bubble they list, so collecting either is a
	// reported failure: the first proves the rows are read, the second that
	// the run directory is skipped before that.
	for id, workspace := range map[string]string{"normal": "/repo/work", "self-run": "/repo/" + agent.RunDirPrefix + "123"} {
		identifier := `{"workspaceIdentifier":{"uri":{"scheme":"file","fsPath":"` + workspace + `"}}}`
		if _, err := db.Exec(`INSERT INTO composerHeaders VALUES (?, '', 0, 0, 0, 0, 0, 0, ?)`, id, identifier); err != nil {
			t.Fatal(err)
		}
		composer := `{"composerId":"` + id + `","workspaceIdentifier":{"uri":{"scheme":"file","fsPath":"` + workspace + `"}},"fullConversationHeadersOnly":[{"bubbleId":"missing","type":1}]}`
		if _, err := db.Exec(`INSERT INTO cursorDiskKV VALUES (?, ?)`, "composerData:"+id, composer); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, report, err := FindSessions([]string{store}, []string{filepath.FromSlash("/repo")})
	if err != nil {
		t.Fatal(err)
	}
	var reported []string
	for _, skipped := range report.Skipped {
		reported = append(reported, skipped.Path)
	}
	if len(reported) != 1 || reported[0] != store+"#normal" {
		t.Fatalf("reported %v, want only the normal composer", reported)
	}
}
