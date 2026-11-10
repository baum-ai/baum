package fix

import (
	"fmt"
	"strings"
	"testing"
)

func excerpt(t *testing.T, log string, chunk int) string {
	t.Helper()
	var l jobLog
	for b := []byte(log); len(b) > 0; b = b[min(chunk, len(b)):] {
		if n, err := l.Write(b[:min(chunk, len(b))]); err != nil || n != min(chunk, len(b)) {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	return l.String()
}

func stamped(lines ...string) string {
	var b strings.Builder
	for _, line := range lines {
		fmt.Fprintf(&b, "2026-11-09T16:02:22.4563503Z %s\n", line)
	}
	return b.String()
}

func cleanupLines(n int) []string {
	lines := []string{"Post job cleanup."}
	for i := range n {
		lines = append(lines, fmt.Sprintf("cleanup %d", i))
	}
	return append(lines, "##[warning]Found 2 failed/cancelled steps in previous workflow steps", "Cleaning up orphan processes")
}

// The failed step is followed by more cleanup than the excerpt holds, as on
// a Blacksmith runner tearing down Docker and a sticky disk: the excerpt ends
// with the failed step, not with the cleanup.
func TestJobLogEndsWithTheFailedStep(t *testing.T) {
	lines := []string{"##[group]Run gofmt -l .", "##[endgroup]", "##[group]Run go vet ./...", "\x1b[36;1mgo vet ./...\x1b[0m", "##[endgroup]", "go: updates to go.mod needed; to update it:", "\tgo mod tidy", "##[error]Process completed with exit code 1."}
	got := excerpt(t, stamped(append(lines, cleanupLines(300)...)...), 7)
	want := strings.Join([]string{"##[group]Run gofmt -l .", "##[endgroup]", "##[group]Run go vet ./...", "go vet ./...", "##[endgroup]", "go: updates to go.mod needed; to update it:", "\tgo mod tidy", "##[error]Process completed with exit code 1."}, "\n")
	if got != want {
		t.Fatalf("excerpt:\n%s\nwant:\n%s", got, want)
	}
}

// A step that runs after the failure (if: always()) ends the failed step, and
// a cleanup error does not replace a step's.
func TestJobLogKeepsTheStepErrorOverLaterSteps(t *testing.T) {
	lines := []string{"##[group]Run go test ./...", "--- FAIL: TestX", "##[error]Process completed with exit code 1.", "##[group]Run actions/upload-artifact@v4", "uploaded", "Post job cleanup.", "##[error]cache save failed", "done"}
	got := excerpt(t, stamped(lines...), 1<<10)
	if want := "##[group]Run go test ./...\n--- FAIL: TestX\n##[error]Process completed with exit code 1."; got != want {
		t.Fatalf("excerpt %q, want %q", got, want)
	}
}

// Of two failed steps, the later one ends the excerpt.
func TestJobLogEndsWithTheLastFailedStep(t *testing.T) {
	lines := []string{"##[group]Run lint", "##[error]lint failed", "##[group]Run test", "FAIL", "##[error]Process completed with exit code 1.", "##[group]Run report", "ok"}
	if got := excerpt(t, stamped(lines...), 3); !strings.HasSuffix(got, "##[group]Run test\nFAIL\n##[error]Process completed with exit code 1.") {
		t.Fatalf("excerpt %q", got)
	}
}

// Only the cleanup failed: its error is what there is to show.
func TestJobLogFallsBackToACleanupError(t *testing.T) {
	lines := []string{"##[group]Run make", "ok", "Post job cleanup.", "##[error]cache save failed", "Post job cleanup.", "other"}
	if got := excerpt(t, stamped(lines...), 5); got != "##[group]Run make\nok\nPost job cleanup.\n##[error]cache save failed" {
		t.Fatalf("excerpt %q", got)
	}
}

// A log without an error line, or a failure at its very end, gives its last
// lines, at most MaxLogLines.
func TestJobLogWithoutAnErrorKeepsItsEnd(t *testing.T) {
	var lines []string
	for i := range MaxLogLines + 50 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	got := strings.Split(excerpt(t, strings.Join(lines, "\n"), 11), "\n")
	if len(got) != MaxLogLines || got[0] != "line 50" || got[len(got)-1] != fmt.Sprintf("line %d", MaxLogLines+49) {
		t.Fatalf("%d lines, %q … %q", len(got), got[0], got[len(got)-1])
	}
	if got := excerpt(t, stamped("##[group]Run make", "boom", "##[error]exit 2"), 4); got != "##[group]Run make\nboom\n##[error]exit 2" {
		t.Fatalf("excerpt %q", got)
	}
}

// Long lines are cut and the excerpt stays within maxLogBytes, dropping
// whole lines from its start.
func TestJobLogIsBounded(t *testing.T) {
	long := strings.Repeat("y", 5*maxLogLine)
	var lines []string
	for range MaxLogLines {
		lines = append(lines, long)
	}
	got := excerpt(t, strings.Join(append(lines, "##[error]x"), "\n"), 4096)
	if len(got) > maxLogBytes || !strings.HasSuffix(got, "##[error]x") {
		t.Fatalf("%d bytes, ends %q", len(got), got[max(len(got)-20, 0):])
	}
	for line := range strings.SplitSeq(got, "\n") {
		if len(line) > maxLogLine+len("…") {
			t.Fatalf("a line of %d bytes", len(line))
		}
	}
}
