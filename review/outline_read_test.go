package review

import (
	"bytes"
	"strings"
	"testing"
)

// ReadOutline is OutlineOf without the fallback to the document itself.
func TestReadOutlineNeverReturnsTheDocument(t *testing.T) {
	document := []byte(`{"schema":"baum.review/1","summary":{"text":"overview","context":{"text":"CTX"},"review_minutes":3},"sections":[{"id":"sec_1","title":"T","kind":"logic","hunks":["h_1"],"assessment":"a","details":"pending","summary":{"text":"DETAILS"},"risks":[]}],"relations":[],"index":{"hunks":[]}}`)
	outline, err := ReadOutline(document)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(outline, OutlineOf(document)) {
		t.Errorf("ReadOutline %s differs from OutlineOf %s", outline, OutlineOf(document))
	}
	for _, left := range []string{"CTX", "DETAILS", "review_minutes", "details", "index"} {
		if strings.Contains(string(outline), left) {
			t.Errorf("the outline keeps %s: %s", left, outline)
		}
	}
	for _, unreadable := range []string{`["x"]`, `"x"`, `{"sections":`, ``} {
		if out, err := ReadOutline([]byte(unreadable)); err == nil || out != nil {
			t.Errorf("ReadOutline(%q) = %q, %v", unreadable, out, err)
		}
		// The staged stage's own helper keeps its fallback.
		if got := OutlineOf([]byte(unreadable)); string(got) != unreadable {
			t.Errorf("OutlineOf(%q) = %q", unreadable, got)
		}
	}
}
