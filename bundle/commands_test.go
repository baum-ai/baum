package bundle

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseCommandParaphraseSimpleCommand(t *testing.T) {
	entries := ParseCommandParaphrase("go test ./...")
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %+v", entries)
	}
	if entries[0].Program != "go" || entries[0].Category != "test" {
		t.Errorf("unexpected entry: %+v", entries[0])
	}
}

func TestParseCommandParaphrasePipeline(t *testing.T) {
	entries := ParseCommandParaphrase("cat foo.txt | grep bar")
	if len(entries) != 2 {
		t.Fatalf("want 2 entries for a pipeline, got %+v", entries)
	}
	if entries[0].Program != "cat" || entries[1].Program != "grep" {
		t.Errorf("unexpected programs: %+v", entries)
	}
}

func TestParseCommandParaphraseAndChain(t *testing.T) {
	entries := ParseCommandParaphrase("go build ./... && go vet ./...")
	if len(entries) != 2 {
		t.Fatalf("want 2 entries for a && chain, got %+v", entries)
	}
	if entries[0].Category != "build" || entries[1].Category != "lint" {
		t.Errorf("unexpected categories: %+v", entries)
	}
}

func TestParseCommandParaphraseMixedChainKeepsOrder(t *testing.T) {
	got := ParseCommandParaphrase("cd web && pnpm install && pnpm test 2>&1 | tail -n 20; git status")
	want := []CommandEntry{
		{Program: "cd", Category: "other"},
		{Program: "pnpm", Category: "package"},
		{Program: "pnpm", Category: "test"},
		{Program: "tail", Category: "other"},
		{Program: "git", Category: "git"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entries = %+v, want %+v", got, want)
	}
}

func TestParseCommandParaphraseUnknownProgramKeepsBaseNameCategoryOther(t *testing.T) {
	entries := ParseCommandParaphrase("/usr/local/bin/frobnicate --deep")
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %+v", entries)
	}
	if entries[0].Program != "frobnicate" {
		t.Errorf("expected base name only, got %q", entries[0].Program)
	}
	if entries[0].Category != "other" {
		t.Errorf("expected category other, got %q", entries[0].Category)
	}
}

func TestParseCommandParaphraseAbsoluteExecutableIsCategorizedByBaseName(t *testing.T) {
	entries := ParseCommandParaphrase("/opt/toolchain/bin/go test ./pkg/... && /usr/bin/git log")
	want := []CommandEntry{{Program: "go", Category: "test", Target: "./pkg/..."}, {Program: "git", Category: "git"}}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries = %+v, want %+v", entries, want)
	}
}

func TestParseCommandParaphraseAbsolutePathTargetIsExcluded(t *testing.T) {
	// The target is kept only when it is a repository-relative path: an
	// absolute local path must never become the target (and must never
	// appear anywhere in the entry).
	entries := ParseCommandParaphrase("cat /home/user/.ssh/id_rsa")
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %+v", entries)
	}
	if entries[0].Target != "" {
		t.Errorf("expected no target for an absolute path, got %q", entries[0].Target)
	}
}

func TestParseCommandParaphraseRepositoryRelativeTarget(t *testing.T) {
	entries := ParseCommandParaphrase("go test ./internal/prreview/...")
	if len(entries) != 1 || entries[0].Target != "./internal/prreview/..." {
		t.Fatalf("expected repository-relative target, got %+v", entries)
	}
}

func TestParseCommandParaphraseNeverLeaksRawCommandLine(t *testing.T) {
	command := "curl -H 'Authorization: Bearer sk-supersecret' https://internal.example.com/admin"
	entries := ParseCommandParaphrase(command)
	for _, e := range entries {
		if strings.Contains(e.Program, "sk-supersecret") || strings.Contains(e.Target, "sk-supersecret") {
			t.Fatalf("raw command content leaked into paraphrase: %+v", e)
		}
	}
}

func TestParseCommandParaphraseUnparseableOrEmpty(t *testing.T) {
	for _, command := range []string{"", "   ", "echo 'unterminated"} {
		if entries := ParseCommandParaphrase(command); entries != nil {
			t.Errorf("%q: want no entries, got %+v", command, entries)
		}
	}
}

func TestCommandOutcomeMapping(t *testing.T) {
	for status, want := range map[string]string{
		"":            "unknown",
		"no_result":   "unknown",
		"unknown":     "unknown",
		"ok":          "succeeded",
		"error":       "failed",
		"interrupted": "interrupted",
	} {
		if got := CommandOutcome(status); got != want {
			t.Errorf("CommandOutcome(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestCommandTextStringAndArrayForms(t *testing.T) {
	if got := CommandText([]byte(`{"command":"go test ./..."}`)); got != "go test ./..." {
		t.Errorf("string form: got %q", got)
	}
	if got := CommandText([]byte(`{"cmd":["go","test","./..."]}`)); got != "go test ./..." {
		t.Errorf("array form: got %q", got)
	}
	if got := CommandText([]byte(`{}`)); got != "" {
		t.Errorf("missing keys: got %q", got)
	}
	if got := CommandText([]byte(`not json`)); got != "" {
		t.Errorf("invalid input: got %q", got)
	}
}

func TestParseCommandParaphraseDropsNonRepositoryTargets(t *testing.T) {
	for command, want := range map[string]string{
		"cat ../../etc/passwd":                "",
		"cat ..":                              "",
		"cat src/../../secret.txt":            "",
		"curl https://example.com/install.sh": "",
		"git clone https://user:token@github.com/org/repo.git": "",
		"rsync host:backup/dump.sql":                           "",
		"cat C:\\Users\\me\\notes.txt":                         "",
		"scp deploy@host:releases/app.tar.gz dist/app.tar.gz":  "dist/app.tar.gz",
		"cat src/../README.md":                                 "src/../README.md",
		"cat ..\\..\\secret.txt":                               "",
		"cat \\\\server\\share\\notes.txt":                     "",
		"cat user:token@example.com/file.txt":                  "",
		"cat packages/@acme/widget/index.ts":                   "packages/@acme/widget/index.ts",
		"cat docs/release:notes.md":                            "docs/release:notes.md",
		"cat src/weird\\\\name.txt":                            "src/weird\\\\name.txt",
	} {
		entries := ParseCommandParaphrase(command)
		if len(entries) != 1 || entries[0].Target != want {
			t.Errorf("%q: want target %q, got %+v", command, want, entries)
		}
	}
}
