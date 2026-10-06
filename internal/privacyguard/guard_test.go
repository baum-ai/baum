// Package privacyguard fails the build when published files mention material
// that belongs to the private monorepo or to a developer's machine.
//
// The module is mirrored to a public repository, so every tracked file is
// scanned, plus untracked files that are not git-ignored. Binary files are
// scanned through their printable ASCII, UTF-16LE and UTF-16BE strings. Justified
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
	// ok reports whether a match is acceptable after all; nil accepts none.
	ok func(match []byte) bool
}{
	{"old-name", regexp.MustCompile(`(?i)recount` + `able`), nil},
	{"monorepo", regexp.MustCompile(`(?i)baum-` + `monorepo`), nil},
	{"platform-pkg", regexp.MustCompile(`platform/` + `internal`), nil},
	{"ui-app", regexp.MustCompile(`ui/` + `app/`), nil},
	{"private-specs", regexp.MustCompile(`specs/` + `platform`), nil},
	{"mac-home", regexp.MustCompile(`(?i)/Us` + `ers/`), nil},
	{"dev-home", regexp.MustCompile(`/home/([A-Za-z0-9._-]+)`), syntheticHome},
	{"worktree", regexp.MustCompile(`\.(?:claude|agents)/work` + `trees`), nil},
	{"company", regexp.MustCompile(`(?i)arv` + `ina`), nil},
	{"private-product", regexp.MustCompile(`(?i)fact` + `beat`), nil},
	{"linear-workspace", regexp.MustCompile(`(?i)linear\.app/last-` + `person-labs`), nil},
	{"issue-key", regexp.MustCompile(`\b(?:E` + `NG|A` + `GE)-\d+\b`), nil},
}

// syntheticHomes are the home directory names that may appear in fixtures.
var syntheticHomes = map[string]bool{"dev": true, "user": true}

func syntheticHome(match []byte) bool {
	return syntheticHomes[string(bytes.TrimPrefix(match, []byte("/home/")))]
}

// Printable runs in the three encodings a binary can hold: ASCII/UTF-8,
// UTF-16LE and UTF-16BE. The wide runs are narrowed to ASCII before matching.
var (
	asciiRun   = regexp.MustCompile(`[\x20-\x7e]{6,}`)
	utf16LERun = regexp.MustCompile(`(?:[\x20-\x7e]\x00){6,}`)
	utf16BERun = regexp.MustCompile(`(?:\x00[\x20-\x7e]){6,}`)
)

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
		for _, h := range findings(data) {
			if key := rel + " " + h.id; allowed[key] {
				used[key] = true
				continue
			}
			failures = append(failures, rel+":"+strconv.Itoa(h.line)+": "+h.id)
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

type finding struct {
	id   string
	line int
}

// findings returns the first forbidden match of each pattern in a file.
func findings(data []byte) []finding {
	var out []finding
	for _, f := range forbidden {
		for _, text := range scannable(data) {
			if h, ok := firstMatch(f.id, text, f.re, f.ok); ok {
				out = append(out, h)
				break
			}
		}
	}
	return out
}

func firstMatch(id string, text []byte, re *regexp.Regexp, ok func([]byte) bool) (finding, bool) {
	for _, loc := range re.FindAllIndex(text, -1) {
		if ok != nil && ok(text[loc[0]:loc[1]]) {
			continue
		}
		return finding{id: id, line: 1 + bytes.Count(text[:loc[0]], []byte("\n"))}, true
	}
	return finding{}, false
}

func TestScanFindsPrivateStringsInEveryEncoding(t *testing.T) {
	target := "/Us" + "ers/bob/work"
	wide := func(s string, be bool) []byte {
		var b []byte
		for _, c := range []byte(s) {
			if be {
				b = append(b, 0, c)
			} else {
				b = append(b, c, 0)
			}
		}
		return b
	}
	cases := map[string][]byte{
		"ascii":     []byte("\x00\x01" + target + "\x00"),
		"utf16le":   append([]byte("\x00\x01\xff"), wide(target, false)...),
		"utf16be":   append([]byte("\x00\x01\xff"), wide(target, true)...),
		"lowercase": []byte("see /us" + "ers/bob/work"),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			got := findings(data)
			if len(got) != 1 || got[0].id != "mac-home" {
				t.Fatalf("findings = %+v, want one mac-home", got)
			}
		})
	}
}

func TestDevHomeAllowsOnlySyntheticNames(t *testing.T) {
	for in, want := range map[string]bool{
		"/home/dev/app": false, "/home/user/app": false,
		"/home/" + "daniel/app": true, "/home/" + "runner/work": true,
	} {
		if got := len(findings([]byte(in))) > 0; got != want {
			t.Errorf("%q flagged = %v, want %v", in, got, want)
		}
	}
}

// scannable returns the text of a file: the whole content for text, and the
// narrowed printable runs of each encoding for anything containing a NUL byte.
func scannable(data []byte) [][]byte {
	if !bytes.Contains(data, []byte{0}) {
		return [][]byte{data}
	}
	runs := asciiRun.FindAll(data, -1)
	for _, wide := range append(utf16LERun.FindAll(data, -1), utf16BERun.FindAll(data, -1)...) {
		runs = append(runs, bytes.ReplaceAll(wide, []byte{0}, nil))
	}
	return runs
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
	var files []string
	// Tracked files, plus untracked files that are not ignored so a new file
	// is caught before it is added.
	for _, args := range [][]string{
		{"ls-files", "-z", "--", "."},
		{"ls-files", "-z", "--others", "--exclude-standard", "--", "."},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			files = nil
			break
		}
		for _, name := range strings.Split(string(out), "\x00") {
			if name != "" {
				files = append(files, name)
			}
		}
	}
	if len(files) > 0 {
		return files
	}
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
