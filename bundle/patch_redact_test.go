package bundle

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// base64Lines returns synthetic base64 lines of the given lengths, each
// starting with a letter, as git's default section heading picks: random
// text in a key's line shape, not key material.
func base64Lines(seed uint64, lengths ...int) []string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	const alphabet = letters + "0123456789+/"
	random := rand.New(rand.NewPCG(seed, 0))
	lines := make([]string, len(lengths))
	for i, n := range lengths {
		line := []byte{letters[random.IntN(len(letters))]}
		for len(line) < n {
			line = append(line, alphabet[random.IntN(len(alphabet))])
		}
		lines[i] = string(line)
	}
	return lines
}

// repeatLength returns n copies of length, then last.
func repeatLength(n, length, last int) []int {
	lengths := make([]int, n, n+1)
	for i := range lengths {
		lengths[i] = length
	}
	return append(lengths, last)
}

// gitDiff commits before as key.txt in a new repository, writes after over
// it and returns git's patch for the change.
func gitDiff(t *testing.T, before, after string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	write := func(text string) {
		if err := os.WriteFile(filepath.Join(dir, "key.txt"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	write(before)
	run("add", "key.txt")
	run("commit", "-q", "-m", "key")
	write(after)
	return run("diff", "-U3", "--no-color", "--", "key.txt")
}

// An edit inside a committed key gives hunks of its body lines alone, with
// no armor for the engine to find, and git heads each hunk with a key line.
// Base64 runs and headings mask them: no body line of a PEM, OpenSSH or
// PuTTY key survives in the bundle, and the hunks stay parseable.
func TestBase64MasksAKeyEditedInPlace(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	label := func(kind string) string { return kind + "PRIVATE KEY" }
	pemBody := base64Lines(1, repeatLength(20, 64, 44)...)
	opensshBody := base64Lines(2, repeatLength(20, 70, 50)...)
	public := base64Lines(3, repeatLength(5, 64, 20)...)
	private := base64Lines(4, repeatLength(13, 64, 44)...)
	keys := []struct {
		name        string
		head, tail  []string
		body        []string
		middle, end int // the body line edited, and the first line after the body
	}{
		{"pem", []string{"-----BEGIN " + label("RSA ") + "-----"}, []string{"-----END " + label("RSA ") + "-----"}, pemBody, 10, 0},
		{"openssh", []string{"-----BEGIN " + label("OPENSSH ") + "-----"}, []string{"-----END " + label("OPENSSH ") + "-----"}, opensshBody, 10, 0},
		{"putty", append([]string{"PuTTY-User-Key-File-3: ssh-rsa", "Encryption: none", "Comment: rsa-key-20261021", "Public-Lines: 6"}, append(public, "Private-Lines: 14")...),
			[]string{"Private-MAC: " + strings.Repeat("0a1b2c3d", 8)}, private, 7, 0},
	}
	for _, key := range keys {
		var config []string
		for i := range 30 {
			config = append(config, fmt.Sprintf("# config line %d", i+1))
		}
		file := func(body, config []string) string {
			lines := append(append(append(append([]string{}, key.head...), body...), key.tail...), config...)
			return strings.Join(lines, "\n") + "\n"
		}
		edited := append([]string{}, key.body...)
		edited[key.middle] = "Zz" + edited[key.middle][2:]
		configEdited := append([]string{}, config...)
		configEdited[2] = "# config line three"
		for _, change := range []struct {
			name string
			body []string
		}{{"mid-key and config edit", edited}, {"config edit", key.body}} {
			patch := gitDiff(t, file(key.body, config), file(change.body, configEdited))
			for _, limit := range []int{0, 3} {
				where := fmt.Sprintf("%s, %s, cap %d", key.name, change.name, limit)
				got, want, b := assembleFilePatch(t, redactor, patch, limit)
				text := strings.Join(got, "\n")
				if text != strings.Join(want, "\n") {
					t.Fatalf("%s: the bundle's hunks differ from the whole patch's:\n%s\nwant\n%s", where, text, strings.Join(want, "\n"))
				}
				for _, line := range append(append([]string{}, key.body...), edited[key.middle]) {
					if strings.Contains(text, line) {
						t.Fatalf("%s: key line %q survives:\n%s\nfrom\n%s", where, line, text, patch)
					}
				}
				for i, hunk := range got {
					if hunkHeaderLen(hunk[:strings.IndexByte(hunk+"\n", '\n')]) == 0 || b.Diff[i].StartLine == 0 {
						t.Fatalf("%s: hunk %d lost its header: %q", where, i, hunk)
					}
				}
				if (limit == 0 && !strings.Contains(text, "+# config line three")) || b.MaskedSpanCount == 0 {
					t.Fatalf("%s: masked %d spans:\n%s", where, b.MaskedSpanCount, text)
				}
			}
		}
	}
}

// Base64 masking takes runs of at least three long lines, with a short line
// at either end, and long base64 headings; ordinary base64 is left alone.
func TestBase64MasksTakeRunsAndHeadingsOnly(t *testing.T) {
	long := base64Lines(5, 40, 64, 64, 64, 64)
	short := base64Lines(6, 39, 12, 8)
	cases := []struct {
		name, patch string
		masked      []string // the line contents masked; every other line is kept
	}{
		{"run with short ends", "@@ -1,7 +1,7 @@\n x = 1\n " + short[1] + "\n-" + long[0] + "\n+" + long[1] + "\n " + long[2] + "\n " + short[2] + "\n y = 2",
			[]string{short[1], long[0], long[1], long[2], short[2]}},
		{"run across a header", "@@ -1,3 +1,3 @@\n x\n " + long[1] + "\n " + long[2] + "\n@@ -9,2 +9,2 @@ func f() {\n " + long[3] + "\n y = 2",
			[]string{long[1], long[2], long[3]}},
		{"two long lines", "@@ -1,4 +1,4 @@\n x\n+" + long[1] + "\n+" + long[2] + "\n y", nil},
		{"one long line", "@@ -1,3 +1,3 @@\n x = \"\"\n+" + long[4] + "\n y", nil},
		{"short base64 lines", "@@ -1,5 +1,5 @@\n+" + short[0] + "\n+" + short[1] + "\n+" + short[2] + "\n+" + short[0] + "\n+" + short[1], nil},
		{"long lines with other text", "@@ -1,3 +1,3 @@\n+key = " + long[1] + "\n+key = " + long[2] + "\n+key = " + long[3], nil},
		{"long base64 heading", "@@ -40,3 +40,3 @@ " + long[0] + "\n x\n-y\n+z", []string{long[0]}},
		{"short base64 heading", "@@ -40,3 +40,3 @@ " + short[0] + "\n x\n-y\n+z", nil},
	}
	for _, c := range cases {
		masks, _ := base64Masks(c.patch)
		got := maskRange(c.patch, c.patch, masks, 0, len(c.patch))
		want := c.patch
		for _, line := range c.masked {
			masked := []byte(line)
			for i := range masked {
				masked[i] = maskByte(masked[i])
			}
			want = strings.Replace(want, line, string(masked), 1)
		}
		if got != want {
			t.Errorf("%s:\n%s\nwant\n%s", c.name, got, want)
		}
	}
}

// Redaction fails closed: a hunk whose patch changed since parsing is masked
// whole but for its diff syntax, not redacted on its own.
func TestPatchRedactionFailsClosed(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat(" "+strings.Repeat("context ", 8)+"\n", 2*redactionMargin/65)
	patch := "@@ -1,600 +1,600 @@\n" + long + "@@ -900,2 +900,2 @@\n-a\n+b\n"
	comparison := Comparison{Files: []FileChange{{NewPath: "a.txt", Status: "modified"}}}
	hunks, _, err := BuildDiffIndexWithin(comparison, fetcher(patchOf("a.txt", []byte(patch))), RoleForPath, DiffLimits{MaxHunkLines: 3})
	if err != nil {
		t.Fatal(err)
	}
	if hunks[0].file == nil || hunks[0].file.text == "" {
		t.Fatal("a cut first hunk must keep its file's whole patch")
	}
	hunks[1].Patch = "@@ -900,2 +900,2 @@\n-a\n+changed after parsing"
	b := &Bundle{Diff: hunks}
	if err := Redact(context.Background(), b, redactor); err != nil {
		t.Fatal(err)
	}
	if want := "@@ -900,2 +900,2 @@\n-x\n+xxxxxxx*xxxxx*xxxxxxx"; b.Diff[1].Patch != want {
		t.Fatalf("changed hunk = %q, want %q", b.Diff[1].Patch, want)
	}
	if b.MaskedSpanCount != 1 || !strings.HasPrefix(b.Diff[0].Patch, "@@ -1,600 +1,600 @@\n context") {
		t.Fatalf("masked %d spans; first hunk %.40q", b.MaskedSpanCount, b.Diff[0].Patch)
	}
	for _, hunk := range b.Diff {
		if hunk.file != nil || hunk.offset != 0 {
			t.Fatalf("Redact left the parse state on %+v", hunk)
		}
	}
}

// A file's hunks are read in file order whatever their order in the diff.
func TestPatchRedactionReadsHunksInFileOrder(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	body := base64Lines(7, 64, 64, 64)
	patch := "@@ -1,2 +1,2 @@\n x\n " + body[0] + "\n@@ -9,3 +9,3 @@\n " + body[1] + "\n " + body[2] + "\n-y\n+z\n"
	comparison := Comparison{Files: []FileChange{{NewPath: "a.txt", Status: "modified"}}}
	for _, reversed := range []bool{false, true} {
		hunks, _, err := BuildDiffIndexWithin(comparison, fetcher(patchOf("a.txt", []byte(patch))), RoleForPath, DiffLimits{})
		if err != nil {
			t.Fatal(err)
		}
		if reversed {
			hunks[0], hunks[1] = hunks[1], hunks[0]
		}
		b := &Bundle{Diff: hunks}
		if err := Redact(context.Background(), b, redactor); err != nil {
			t.Fatal(err)
		}
		for _, hunk := range b.Diff {
			for _, line := range body {
				if strings.Contains(hunk.Patch, line) {
					t.Fatalf("reversed %t: key line survives in %q", reversed, hunk.Patch)
				}
			}
		}
	}
}

// A hunk other than the last whose text trims trailing blank lines keeps
// its file's whole patch, so the text Redact reads keeps those lines.
func TestParsingKeepsTheWholePatchForTrimmedBlankLines(t *testing.T) {
	for patch, whole := range map[string]bool{
		"@@ -1,3 +1,3 @@\n+a\n\n\n@@ -9 +9 @@\n+b\n":   true,
		"@@ -1,3 +1,3 @@\n+a\n+c\n@@ -9 +9 @@\n+b\n\n": false,
	} {
		hunks, file, textless := parseFileHunks("a.txt", []byte(patch), 0)
		if textless || len(hunks) != 2 || (file.text != "") != whole {
			t.Fatalf("%q: %d hunks, whole patch kept %t, want %t", patch, len(hunks), file.text != "", whole)
		}
		if whole && file.text != strings.TrimSuffix(patch, "\n") {
			t.Fatalf("whole patch = %q", file.text)
		}
	}
}
