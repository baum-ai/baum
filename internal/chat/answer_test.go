package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/store"
)

func TestCheckCitation(t *testing.T) {
	head := filepath.Join(t.TempDir(), "head")
	if err := os.MkdirAll(filepath.Join(head, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"src/a.go": "one\ntwo\nthree\n", "b.txt": "one\ntwo", "empty": ""} {
		if err := os.WriteFile(filepath.Join(head, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(head, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("src/a.go", filepath.Join(head, "inside")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		citation Citation
		valid    bool
	}{
		{Citation{"src/a.go", 1, 3}, true},
		{Citation{"src/a.go", 2, 2}, true},
		{Citation{"b.txt", 2, 2}, true},
		{Citation{"inside", 1, 1}, true},
		{Citation{"src/a.go", 1, 4}, false},
		{Citation{"src/a.go", 3, 2}, false},
		{Citation{"src/a.go", 0, 1}, false},
		{Citation{"b.txt", 3, 3}, false},
		{Citation{"empty", 1, 1}, false},
		{Citation{"../a.go", 1, 1}, false},
		{Citation{"src/../src/a.go", 1, 1}, false},
		{Citation{"./src/a.go", 1, 1}, false},
		{Citation{"/etc/passwd", 1, 1}, false},
		{Citation{"src\\a.go", 1, 1}, false},
		{Citation{"escape", 1, 1}, false},
		{Citation{"missing.go", 1, 1}, false},
		{Citation{"src", 1, 1}, false},
		{Citation{"", 1, 1}, false},
	} {
		if problem := checkCitation(head, c.citation); (problem == "") != c.valid {
			t.Errorf("%+v: %q, want valid %v", c.citation, problem, c.valid)
		}
	}
}

func TestParseRequiresTheSchema(t *testing.T) {
	for document, valid := range map[string]bool{
		`{"answer_markdown":"a","citations":[],"suggests_fix":null}`:                                                        true,
		`{"answer_markdown":"a","citations":[{"path":"x","start_line":1,"end_line":2}],"suggests_fix":{"summary":"do it"}}`: true,
		`{"answer_markdown":"a","citations":[]}`:                                                                            false,
		`{"answer_markdown":"","citations":[],"suggests_fix":null}`:                                                         false,
		`{"answer_markdown":"a","citations":[],"suggests_fix":null,"extra":1}`:                                              false,
		`{"answer_markdown":"a","citations":[{"path":"x","start_line":0,"end_line":2}],"suggests_fix":null}`:                false,
		`not json`: false,
	} {
		if _, findings := parse([]byte(document)); (findings == "") != valid {
			t.Errorf("%s: %q", document, findings)
		}
	}
	long := `{"answer_markdown":"` + strings.Repeat("a", MaxAnswerBytes+1) + `","citations":[],"suggests_fix":null}`
	if _, findings := parse([]byte(long)); findings == "" {
		t.Error("an over-long answer was accepted")
	}
}

// The earlier turns given to a seeded session keep the newest that fit.
func TestSeedConversationKeepsTheNewestTurns(t *testing.T) {
	var turns []store.Turn
	for i := range 40 {
		answer := `{"answer_markdown":"` + strings.Repeat("x", 4000) + `","citations":[],"suggests_fix":null}`
		turns = append(turns, store.Turn{Seq: i + 1, Question: "q" + string(rune('A'+i%26)), Answer: []byte(answer), State: store.TurnCompleted})
	}
	turns = append(turns, store.Turn{Seq: 41, Question: "failed", State: store.TurnFailed})
	secret := "AKIA" + "UJZDEGXDNCF32EPF"
	turns = append(turns, store.Turn{Seq: 42, Question: "is " + secret + " live?", Answer: []byte(`{"answer_markdown":"no","citations":[],"suggests_fix":null}`), State: store.TurnCompleted})
	redactor, err := bundle.NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := seedConversation(context.Background(), redactor, turns)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxSeedTurns+4096 || strings.Contains(string(raw), "failed") || !strings.Contains(string(raw), `"qN"`) {
		t.Errorf("%d bytes", len(raw))
	}
	// The reviewer's questions are stored as typed and masked here.
	if strings.Contains(string(raw), secret) || !strings.Contains(string(raw), "live?") {
		t.Errorf("question not masked: %s", raw[len(raw)-200:])
	}
}

func TestChatPromptReframesPerBackend(t *testing.T) {
	claude, codex := prompt.Chat("claude"), prompt.Chat("codex")
	for _, text := range []string{claude, codex} {
		for _, phrase := range []string{"The review is finished", "Do not write, extend or repeat a\nreview document", "untrusted data", "suggests_fix", "head/"} {
			if !strings.Contains(text, phrase) {
				t.Errorf("chat instructions lack %q", phrase)
			}
		}
	}
	if !strings.Contains(claude, "StructuredOutput") || strings.Contains(codex, "StructuredOutput") || !strings.Contains(codex, "final message") {
		t.Error("submit instructions do not match the backend")
	}
}

// A citation keeps the checkout's spelling of its path: on a
// case-insensitive file system a path differing only in case finds the file.
func TestOnDiskSpelling(t *testing.T) {
	head := t.TempDir()
	if err := os.MkdirAll(filepath.Join(head, "Src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(head, "Src", "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"Src/README.md": "Src/README.md", "src/readme.md": "Src/README.md", "src/other.md": ""} {
		got, ok := onDisk(head, path)
		if got != want || ok != (want != "") {
			t.Errorf("%s: %q %v", path, got, ok)
		}
	}
	if err := os.WriteFile(filepath.Join(head, "Src", "readme.MD"), []byte("y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := onDisk(head, "src/Readme.md"); ok {
		t.Error("an ambiguous spelling was resolved")
	}
	answer, dropped := keepValid(head, Answer{Citations: []Citation{{Path: "Src/README.md", StartLine: 1, EndLine: 1}}})
	if dropped != 0 || answer.Citations[0].Path != "Src/README.md" {
		t.Errorf("%+v", answer)
	}
}
