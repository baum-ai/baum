package livesessions

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/baum-ai/baum/internal/hookevents"
)

// cursorFixture tracks the Cursor conversation "root" of a copy of the
// committed Cursor store.
func cursorFixture(t *testing.T) (*transcriptFixture, string) {
	t.Helper()
	f := newTranscriptFixture(t)
	body, err := os.ReadFile(filepath.Join("..", "..", "sessions", "cursortranscript", "testdata", "store.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(f.home, "state.vscdb")
	write(t, store, string(body))
	f.live.CursorStore = store
	cursorEvent(t, f, "before-submit-prompt")
	return f, store
}

func cursorEvent(t *testing.T, f *transcriptFixture, name string) {
	t.Helper()
	e := hookevents.New(hookevents.KindAgent, AgentCursor, name, "/work/demo", "test")
	e.Time = f.clock.now
	e.Session = &hookevents.Session{ID: "root", Cwd: "/work/demo"}
	if !apply(f.tracker, e) {
		t.Fatalf("cursor event %s changed nothing", name)
	}
}

// updateCursor sets a JSON field of a composer's record, as Cursor does.
func updateCursor(t *testing.T, store, composer, field, value string) {
	t.Helper()
	db, err := sql.Open("sqlite", store)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE cursorDiskKV SET value = json_set(value, ?, ?) WHERE key = ?`, "$."+field, value, "composerData:"+composer); err != nil {
		t.Fatal(err)
	}
}

func TestCursorTitleIsTheConversationNameReadWithoutItsTree(t *testing.T) {
	f, store := cursorFixture(t)
	if got := titles(t, f)["cursor/root"]; got != "Cursor fixture" {
		t.Fatalf("title %q", got)
	}
	updateCursor(t, store, "root", "name", "Renamed")
	if got := titles(t, f)["cursor/root"]; got != "Cursor fixture" {
		t.Fatalf("title %q read again too soon", got)
	}
	f.clock.advance(CursorTitleRecheckEvery)
	if got := titles(t, f)["cursor/root"]; got != "Renamed" {
		t.Fatalf("title %q", got)
	}
}

func TestEndedSessionIsNotDescribedAgain(t *testing.T) {
	f, store := cursorFixture(t)
	updateCursor(t, store, "root", "name", "Before the end")
	if got := titles(t, f)["cursor/root"]; got != "Before the end" {
		t.Fatalf("title %q", got)
	}
	f.clock.advance(CursorTitleRecheckEvery)
	cursorEvent(t, f, "session-end")
	updateCursor(t, store, "root", "name", "Final name")
	f.clock.advance(CursorTitleRecheckEvery)
	if got := titles(t, f)["cursor/root"]; got != "Final name" {
		t.Fatalf("the read after the end missed the name: %q", got)
	}
	// Ended and read: whatever the store holds now is not looked at.
	updateCursor(t, store, "root", "name", "Too late")
	f.clock.advance(10 * CursorTitleRecheckEvery)
	if got := titles(t, f)["cursor/root"]; got != "Final name" {
		t.Fatalf("an ended session was described again: %q", got)
	}
	// A session that comes back is described as before.
	f.clock.advance(CursorTitleRecheckEvery)
	cursorEvent(t, f, "before-submit-prompt")
	f.clock.advance(CursorTitleRecheckEvery)
	if got := titles(t, f)["cursor/root"]; got != "Too late" {
		t.Fatalf("a resumed session kept its old title: %q", got)
	}
}

func TestCursorVersionFollowsTheConversationNotTheDatabase(t *testing.T) {
	_, store := cursorFixture(t)
	version := func(id string) string {
		t.Helper()
		v, err := Location{ID: id, store: store}.Version()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	root := version("root")
	updateCursor(t, store, "child", "name", "Elsewhere")
	if got := version("root"); got != root {
		t.Fatal("another conversation's write changed this one's version")
	}
	updateCursor(t, store, "root", "lastUpdatedAt", "1")
	if got := version("root"); got == root {
		t.Fatal("the conversation's own write left its version")
	}
	// Without a record the database's own size and times stand in.
	if _, err := (Location{ID: "no-such", store: store}).Version(); err != nil {
		t.Fatal(err)
	}
}
