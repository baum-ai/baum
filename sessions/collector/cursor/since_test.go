package cursor

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// brokenStaleStore copies the fixture and adds a repository conversation last
// updated at updatedAt (milliseconds) whose record lists a bubble that is not
// there, so reading its tree fails and is reported.
func brokenStaleStore(t *testing.T, id string, updatedAt int64) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "cursortranscript", "testdata", "store.vscdb"))
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
	defer db.Close()
	workspace := `{"workspaceIdentifier":{"uri":{"scheme":"file","fsPath":"/repo"}}}`
	if _, err := db.Exec(`INSERT INTO composerHeaders VALUES (?, '', 0, ?, 0, 0, 0, 0, ?)`, id, updatedAt, workspace); err != nil {
		t.Fatal(err)
	}
	composer := `{"composerId":"` + id + `","workspaceIdentifier":{"uri":{"scheme":"file","fsPath":"/repo"}},"fullConversationHeadersOnly":[{"bubbleId":"missing","type":1}]}`
	if _, err := db.Exec(`INSERT INTO cursorDiskKV VALUES (?, ?)`, "composerData:"+id, composer); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestFindSessionsSinceSkipsOldConversationsBeforeReadingTheirTrees(t *testing.T) {
	oldest := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	store := brokenStaleStore(t, "stale", oldest.UnixMilli())
	roots := []string{filepath.FromSlash("/repo")}

	// Without a bound the broken tree is read, and reported.
	all, report, err := FindSessionsSince([]string{store}, roots, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Path != store+"#stale" {
		t.Fatalf("unbounded report = %+v", report.Skipped)
	}

	// With one it is never read: nothing is reported, and the sessions are
	// exactly those a later filter on the modification time keeps.
	since := oldest.AddDate(0, 6, 0)
	got, report, err := FindSessionsSince([]string{store}, roots, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Skipped) != 0 {
		t.Fatalf("bounded report = %+v", report.Skipped)
	}
	var want, have []string
	for _, s := range all {
		if !s.ModifiedAt.Before(since) {
			want = append(want, s.Path+"@"+s.ModifiedAt.String())
		}
	}
	for _, s := range got {
		have = append(have, s.Path+"@"+s.ModifiedAt.String())
	}
	if len(want) == 0 || !slices.Equal(have, want) {
		t.Fatalf("bounded sessions %v, want %v", have, want)
	}

	// A bound after every conversation leaves none.
	none, _, err := FindSessionsSince([]string{store}, roots, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || len(none) != 0 {
		t.Fatalf("future bound = %+v, %v", none, err)
	}
}

func TestFindSessionsSinceWithAnEarlyBoundReturnsWhatFindSessionsDoes(t *testing.T) {
	store := filepath.Join("..", "..", "cursortranscript", "testdata", "store.vscdb")
	roots := []string{filepath.FromSlash("/repo")}
	all, _, err := FindSessions([]string{store}, roots)
	if err != nil {
		t.Fatal(err)
	}
	bounded, _, err := FindSessionsSince([]string{store}, roots, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 || len(all) != len(bounded) {
		t.Fatalf("%d sessions, %d with an early bound", len(all), len(bounded))
	}
	for i := range all {
		if all[i].Path != bounded[i].Path || !all[i].ModifiedAt.Equal(bounded[i].ModifiedAt) ||
			string(all[i].Files[0].Data) != string(bounded[i].Files[0].Data) {
			t.Fatalf("session %d differs: %+v vs %+v", i, all[i], bounded[i])
		}
	}
}
