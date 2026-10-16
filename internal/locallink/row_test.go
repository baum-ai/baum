package locallink

import (
	"strings"
	"testing"

	"github.com/baum-ai/baum/linkage/sessioncorrelate"
	"github.com/baum-ai/baum/redact"
	"github.com/baum-ai/baum/sessions"
)

func TestRowOfRedactsPathAndNotes(t *testing.T) {
	engine, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	row, err := rowOf(engine, sessions.Session{Path: "/store/ada.lovelace@example.com/s.jsonl", Notes: []string{"see ada.lovelace@example.com"}}, sessioncorrelate.Result{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row.Path, "ada.lovelace") || strings.Contains(row.Notes[0], "ada.lovelace") {
		t.Fatalf("row = %+v", row)
	}
}
