// Package privacyguard fails the build when published files mention material
// that belongs to the private monorepo or to a developer's machine.
//
// The module is mirrored to a public repository, so every tracked file is
// scanned. Binary files are scanned through their printable strings. Justified
// exceptions live in allowlist.txt next to this file.
package privacyguard

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// forbidden lists what must not appear anywhere in the module. Each pattern has
// a short id that the allowlist refers to.
var forbidden = []struct {
	id string
	re *regexp.Regexp
}{
	{"old-name", regexp.MustCompile(`(?i)recount` + `able`)},
	{"monorepo", regexp.MustCompile(`(?i)baum-` + `monorepo`)},
	{"platform-pkg", regexp.MustCompile(`platform/` + `internal`)},
	{"ui-app", regexp.MustCompile(`ui/` + `app/`)},
	{"private-specs", regexp.MustCompile(`specs/` + `platform`)},
	{"mac-home", regexp.MustCompile(`/Us` + `ers/`)},
	{"dev-home", regexp.MustCompile(`/home/` + `daniel`)},
	{"company", regexp.MustCompile(`(?i)arv` + `ina`)},
	{"private-product", regexp.MustCompile(`(?i)fact` + `beat`)},
	{"linear-workspace", regexp.MustCompile(`(?i)linear\.app/last-` + `person-labs`)},
	{"issue-key", regexp.MustCompile(`\b(?:E` + `NG|A` + `GE)-\d+\b`)},
}

// printableRun matches the text a binary file such as a SQLite database holds.
var printableRun = regexp.MustCompile(`[\x20-\x7e]{6,}`)

func TestPublishedFilesCarryNoPrivateMaterial(t *testing.T) {
	root := moduleRoot(t)
	allowed := loadAllowlist(t)
	used := map[string]bool{}
	var failures []string
	for _, rel := range trackedFiles(t, root) {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range scannable(data) {
			for _, f := range forbidden {
				loc := f.re.FindIndex(text)
				if loc == nil {
					continue
				}
				if key := rel + " " + f.id; allowed[key] {
					used[key] = true
					continue
				}
				line := 1 + bytes.Count(text[:loc[0]], []byte("\n"))
				failures = append(failures, rel+":"+strconv.Itoa(line)+": "+f.id)
			}
		}
	}
	for key := range allowed {
		if !used[key] {
			t.Errorf("allowlist entry %q matches nothing; remove it", key)
		}
	}
	sort.Strings(failures)
	for _, f := range failures {
		t.Error(f)
	}
	if len(failures) > 0 {
		t.Log("reword the text, or add a justified entry to internal/privacyguard/allowlist.txt")
	}
}

// scannable returns the text of a file: the whole content for text, and the
// printable runs for anything containing a NUL byte.
func scannable(data []byte) [][]byte {
	if !bytes.Contains(data, []byte{0}) {
		return [][]byte{data}
	}
	return printableRun.FindAll(data, -1)
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// trackedFiles returns the git-tracked files below root, slash-separated and
// relative to root. Outside a git checkout it walks the directory instead.
func trackedFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z", "--", ".")
	cmd.Dir = root
	if out, err := cmd.Output(); err == nil {
		var files []string
		for _, name := range strings.Split(string(out), "\x00") {
			if name != "" {
				files = append(files, name)
			}
		}
		if len(files) > 0 {
			return files
		}
	}
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		files = append(files, filepath.ToSlash(rel))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// loadAllowlist reads allowlist.txt: one "<path> <pattern id>" per line, with
// blank lines and # comments ignored.
func loadAllowlist(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open("allowlist.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ids := map[string]bool{}
	for _, p := range forbidden {
		ids[p.id] = true
	}
	allowed := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !ids[fields[1]] {
			t.Fatalf("allowlist line %q: want \"<path> <pattern id>\"", line)
		}
		allowed[fields[0]+" "+fields[1]] = true
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return allowed
}
