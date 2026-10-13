package bundle

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// anyPath accepts every path, so a test sees which arguments are path-shaped
// enough to be offered to a host's knownPath at all.
func anyPath(string) bool { return true }

func TestParseCommandParaphraseSimpleCommand(t *testing.T) {
	entries := ParseCommandParaphrase("go test ./...", anyPath)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %+v", entries)
	}
	if entries[0].Program != "go" || entries[0].Category != "test" {
		t.Errorf("unexpected entry: %+v", entries[0])
	}
}

func TestParseCommandParaphrasePipeline(t *testing.T) {
	entries := ParseCommandParaphrase("cat foo.txt | grep bar", anyPath)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries for a pipeline, got %+v", entries)
	}
	if entries[0].Program != "cat" || entries[1].Program != "grep" {
		t.Errorf("unexpected programs: %+v", entries)
	}
}

func TestParseCommandParaphraseAndChain(t *testing.T) {
	entries := ParseCommandParaphrase("go build ./... && go vet ./...", anyPath)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries for a && chain, got %+v", entries)
	}
	if entries[0].Category != "build" || entries[1].Category != "lint" {
		t.Errorf("unexpected categories: %+v", entries)
	}
}

func TestParseCommandParaphraseMixedChainKeepsOrder(t *testing.T) {
	got := ParseCommandParaphrase("cd web && pnpm install && pnpm test 2>&1 | tail -n 20; git status", anyPath)
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
	entries := ParseCommandParaphrase("/usr/local/bin/frobnicate --deep", anyPath)
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
	entries := ParseCommandParaphrase("/opt/toolchain/bin/go test ./pkg/... && /usr/bin/git log", anyPath)
	want := []CommandEntry{{Program: "go", Category: "test", Target: "pkg/..."}, {Program: "git", Category: "git"}}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries = %+v, want %+v", entries, want)
	}
}

func TestParseCommandParaphraseAbsolutePathTargetIsExcluded(t *testing.T) {
	// The target is kept only when it is a repository-relative path: an
	// absolute local path must never become the target (and must never
	// appear anywhere in the entry).
	entries := ParseCommandParaphrase("cat /home/user/.ssh/id_rsa", anyPath)
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %+v", entries)
	}
	if entries[0].Target != "" {
		t.Errorf("expected no target for an absolute path, got %q", entries[0].Target)
	}
}

func TestParseCommandParaphraseRepositoryRelativeTarget(t *testing.T) {
	entries := ParseCommandParaphrase("go test ./internal/prreview/...", anyPath)
	if len(entries) != 1 || entries[0].Target != "internal/prreview/..." {
		t.Fatalf("expected repository-relative target, got %+v", entries)
	}
}

func TestParseCommandParaphraseNeverLeaksRawCommandLine(t *testing.T) {
	command := "curl -H 'Authorization: Bearer sk-supersecret' https://internal.example.com/admin"
	entries := ParseCommandParaphrase(command, anyPath)
	for _, e := range entries {
		if strings.Contains(e.Program, "sk-supersecret") || strings.Contains(e.Target, "sk-supersecret") {
			t.Fatalf("raw command content leaked into paraphrase: %+v", e)
		}
	}
}

func TestParseCommandParaphraseUnparseableOrEmpty(t *testing.T) {
	for _, command := range []string{"", "   ", "echo 'unterminated"} {
		if entries := ParseCommandParaphrase(command, anyPath); entries != nil {
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
		"cat src/../README.md":                                 "README.md",
		"cat ..\\..\\secret.txt":                               "",
		"cat \\\\server\\share\\notes.txt":                     "",
		"cat user:token@example.com/file.txt":                  "",
		// "@", ":" and "\\" can be in a file name, but a target holding one is
		// dropped rather than told apart from a remote or a credential.
		"cat packages/@acme/widget/index.ts": "",
		"cat docs/release:notes.md":          "",
		"cat src/weird\\\\name.txt":          "",
		"cat .":                              "",
		"cat src/main.go":                    "src/main.go",
		"cat README.md":                      "README.md",
	} {
		entries := ParseCommandParaphrase(command, anyPath)
		if len(entries) != 1 || entries[0].Target != want {
			t.Errorf("%q: want target %q, got %+v", command, want, entries)
		}
	}
}

// No part of a command's raw text but a strictly path-shaped argument becomes
// its target: not inline code, a commit message, an assignment, a remote or
// a search pattern.
func TestParseCommandParaphraseKeepsNoRawTextAsTarget(t *testing.T) {
	for command, want := range map[string][]CommandEntry{
		`bash -c "cd web/app && API_TOKEN=hunter2 npm test"`:  {{Program: "bash", Category: "other"}},
		`sh -c 'python3 scripts/x.py --password hunter2'`:     {{Program: "sh", Category: "other"}},
		`git commit -m "Fix src/x.go for customer Acme Corp"`: {{Program: "git", Category: "git"}},
		`git commit -m "Bump version to 1.2.3"`:               {{Program: "git", Category: "git"}},
		`git commit -m fix.go`:                                {{Program: "git", Category: "git"}},
		`python3 -c "import os; print(os.environ['SECRET'])"`: {{Program: "python3", Category: "other"}},
		`node -e "require('./x.js')" app.js`:                  {{Program: "node", Category: "other", Target: "app.js"}},
		`env DB_PASSWORD=hunter2.prod ./run.sh`:               {{Program: "env", Category: "other", Target: "run.sh"}},
		`ssh deploy@db.internal.example.com`:                  {{Program: "ssh", Category: "other"}},
		`grep -r "TODO(alice)." .`:                            {{Program: "grep", Category: "other"}},
		`mysql --password hunter2.prod schema.sql`:            {{Program: "mysql", Category: "other", Target: "schema.sql"}},
		`curl -H "Authorization: Bearer x" -o out/page.html`:  {{Program: "curl", Category: "other", Target: "out/page.html"}},
		`echo "$SECRET" > notes/today.txt`:                    {{Program: "echo", Category: "other"}},
		`cat src/*.go`:                                        {{Program: "cat", Category: "other"}},
	} {
		got := ParseCommandParaphrase(command, anyPath)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", command, got, want)
		}
		encoded := fmt.Sprintf("%+v", got)
		for _, leaked := range []string{"hunter2", "Acme", "1.2.3", "SECRET", "internal.example", "TODO", "API_TOKEN", "Bearer"} {
			if strings.Contains(encoded, leaked) {
				t.Errorf("%s: paraphrase leaks %q: %s", command, leaked, encoded)
			}
		}
	}
}

// The program is a plain base name across both separators, never a path
// fragment; a command whose program is a directory or dynamic is left out.
func TestParseCommandParaphraseProgramIsAPlainName(t *testing.T) {
	for command, want := range map[string][]CommandEntry{
		`C:\\Users\\alice\\bin\\deploy.exe --prod`: {{Program: "deploy.exe", Category: "other"}},
		`/home/user/ test`:                         nil,
		`"$HOME/bin/tool" run`:                     nil,
		`$(which go) test ./...`:                   {{Program: "which", Category: "other"}},
		`'my tool' x.txt`:                          nil,
		`./scripts/migrate-db up`:                  {{Program: "migrate-db", Category: "migrate"}},
		`/home/user/ test && go vet ./pkg/...`:     {{Program: "go", Category: "lint", Target: "pkg/..."}},
	} {
		if got := ParseCommandParaphrase(command, anyPath); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", command, got, want)
		}
	}
}

// A target needs positive evidence that it is a repository path: a single
// path-shaped argument the host does not know is not one.
func TestParseCommandParaphraseKeepsOnlyKnownTargets(t *testing.T) {
	known := KnownPaths("src/x.go", "web/app/main.ts", "a.txt", "internal/prreview/builder.go")
	for command, want := range map[string]string{
		`ssh db.internal.example.com`:                         "",
		`curl api.internal.corp/v1/users`:                     "",
		`kubectl --context prod.cluster.internal get pods`:    "",
		`openssl enc -k hunter2.pw -in a.txt`:                 "a.txt",
		`aws configure set aws_secret_access_key AKIAabc.def`: "",
		`echo hunter2.secret`:                                 "",
		`git commit -am wip.release`:                          "",
		`git commit -cm wip.release src/x.go`:                 "src/x.go",
		`go test ./...`:                                       "",
		`go test ./internal/prreview/...`:                     "internal/prreview/...",
		`go test -race ./internal/prreview/...`:               "internal/prreview/...",
		`cat ./web/app/../app/main.ts`:                        "web/app/main.ts",
		`ls web`:                                              "",
		`ls web/app`:                                          "web/app",
		`cat src/other.go`:                                    "",
	} {
		entries := ParseCommandParaphrase(command, known)
		if len(entries) == 0 {
			t.Errorf("%s: no entries", command)
			continue
		}
		if got := entries[0].Target; got != want {
			t.Errorf("%s: target %q, want %q", command, got, want)
		}
	}
	if entries := ParseCommandParaphrase("cat src/x.go", nil); len(entries) != 1 || entries[0].Target != "" {
		t.Errorf("a nil knownPath must keep no target: %+v", entries)
	}
}

func TestKnownPaths(t *testing.T) {
	known := KnownPaths("./web/app/main.ts", "../outside.txt", ".", "docs/")
	for candidate, want := range map[string]bool{
		"web/app/main.ts": true, "web/app": true, "web": true, "docs": true,
		".": false, "": false, "../outside.txt": false, "outside.txt": false, "web/app/other.ts": false,
	} {
		if got := known(candidate); got != want {
			t.Errorf("known(%q) = %v, want %v", candidate, got, want)
		}
	}
}

func TestKeepKnownCommandTargets(t *testing.T) {
	sessions := []Session{{
		Prompts: []Prompt{{TouchedPaths: []string{"notes/plan.md"}}},
		CommandEvents: []CommandEvent{{Commands: []CommandEntry{
			{Program: "go", Category: "test", Target: "pkg/..."},
			{Program: "cat", Category: "other", Target: "notes/plan.md"},
			{Program: "ssh", Category: "other", Target: "db.internal.example.com"},
			{Program: "ls", Category: "other", Target: "notes"},
		}}},
	}}
	KeepKnownCommandTargets(sessions, KnownPaths("pkg/a.go"))
	var got []string
	for _, entry := range sessions[0].CommandEvents[0].Commands {
		got = append(got, entry.Target)
	}
	if want := []string{"pkg/...", "notes/plan.md", "", "notes"}; !reflect.DeepEqual(got, want) {
		t.Errorf("targets = %q, want %q", got, want)
	}
}
