package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func openTestTools(t *testing.T, dir string) *toolbox {
	t.Helper()
	tools, err := openTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tools.close)
	return tools
}

func call(t *testing.T, tools *toolbox, name, input string) (string, toolCall) {
	t.Helper()
	result := tools.run(context.Background(), name, json.RawMessage(input))
	var text strings.Builder
	for _, block := range result.content {
		var b struct{ Type, Text string }
		if err := json.Unmarshal(block, &b); err != nil {
			t.Fatal(err)
		}
		text.WriteString(b.Text)
	}
	return text.String(), result
}

// outside is a directory beside the run directory holding a secret file.
func outside(t *testing.T) (run, secret string) {
	t.Helper()
	parent := t.TempDir()
	run = filepath.Join(parent, "run")
	secretDir := filepath.Join(parent, "secret")
	for _, dir := range []string{filepath.Join(run, "head"), secretDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret = filepath.Join(secretDir, "credentials")
	if err := os.WriteFile(secret, []byte("TOP-SECRET-CONTENT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "head", "ok.txt"), []byte("inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return run, secret
}

func TestPathsStayInTheRunDirectory(t *testing.T) {
	run, secret := outside(t)
	tools := openTestTools(t, run)
	refused := []struct{ tool, input, status string }{
		{"Read", `{"file_path":` + quote(secret) + `}`, "invalid_path"},
		{"Read", `{"file_path":"/etc/passwd"}`, "invalid_path"},
		{"Read", `{"file_path":"../secret/credentials"}`, "invalid_path"},
		{"Read", `{"file_path":"head/../../secret/credentials"}`, "invalid_path"},
		{"Read", `{"file_path":"head/../head/ok.txt"}`, "invalid_path"},
		{"Read", `{"file_path":"head\\ok.txt"}`, "invalid_path"},
		{"Read", `{"file_path":"C:/Windows/win.ini"}`, ""},
		{"Read", `{"file_path":""}`, "invalid_path"},
		{"Glob", `{"pattern":"*","path":"/"}`, "invalid_path"},
		{"Glob", `{"pattern":"*","path":".."}`, "invalid_path"},
		{"Glob", `{"pattern":"../*"}`, "invalid_pattern"},
		{"Glob", `{"pattern":"/etc/*"}`, "invalid_pattern"},
		{"Grep", `{"pattern":"SECRET","path":"../secret"}`, "invalid_path"},
		{"Grep", `{"pattern":"SECRET","path":` + quote(filepath.Dir(secret)) + `}`, "invalid_path"},
		{"Read", `{"file_path":"head/ok.txt","mode":"write"}`, "invalid_input"},
		{"Write", `{"file_path":"head/new.txt","content":"x"}`, "unknown_tool"},
		{"Bash", `{"command":"cat /etc/passwd"}`, "unknown_tool"},
	}
	for _, c := range refused {
		text, result := call(t, tools, c.tool, c.input)
		if strings.Contains(text, "TOP-SECRET") {
			t.Errorf("%s %s read outside the run directory", c.tool, c.input)
		}
		// "C:/..." is a relative path that does not exist on Unix.
		if c.status != "" && (!result.isError || result.status != c.status) {
			t.Errorf("%s %s: error %v status %q, want %q (%s)", c.tool, c.input, result.isError, result.status, c.status, text)
		}
		if strings.Contains(text, run) {
			t.Errorf("%s %s: the message names the run directory: %s", c.tool, c.input, text)
		}
	}
	if _, err := os.Stat(filepath.Join(run, "head", "new.txt")); err == nil {
		t.Error("a tool wrote a file")
	}
	if text, result := call(t, tools, "Read", `{"file_path":"./head//ok.txt"}`); result.isError || !strings.Contains(text, "inside") {
		t.Errorf("a clean relative path failed: %s", text)
	}
}

func quote(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

func TestSymlinksCannotLeaveTheRunDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need privileges on Windows")
	}
	run, secret := outside(t)
	links := map[string]string{
		"head/file-link": secret,
		"head/dir-link":  filepath.Dir(secret),
		"head/relative":  "../../secret/credentials",
		"head/inside":    "ok.txt",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(run, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	tools := openTestTools(t, run)
	for _, input := range []string{`{"file_path":"head/file-link"}`, `{"file_path":"head/relative"}`, `{"file_path":"head/dir-link/credentials"}`} {
		text, result := call(t, tools, "Read", input)
		if !result.isError || strings.Contains(text, "TOP-SECRET") || result.status != "outside_run_directory" {
			t.Errorf("%s: %v %q %s", input, result.isError, result.status, text)
		}
	}
	for _, c := range []struct{ tool, input string }{
		{"Grep", `{"pattern":"SECRET"}`},
		{"Grep", `{"pattern":"SECRET","path":"head/dir-link"}`},
		{"Glob", `{"pattern":"**"}`},
		{"Glob", `{"pattern":"*","path":"head/dir-link"}`},
	} {
		text, _ := call(t, tools, c.tool, c.input)
		if strings.Contains(text, "TOP-SECRET") || strings.Contains(text, "credentials") {
			t.Errorf("%s %s followed a link: %s", c.tool, c.input, text)
		}
	}
	// A link that stays inside reads its target.
	if text, result := call(t, tools, "Read", `{"file_path":"head/inside"}`); result.isError || !strings.Contains(text, "inside") {
		t.Errorf("inside link: %s", text)
	}
}

func TestReadLimits(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("big.txt", []byte(strings.Repeat("x", maxReadFileBytes+1)))
	write("binary.bin", []byte("abc\x00def"))
	write("long.txt", []byte(strings.Repeat("y", maxLineChars+50)+"\n"))
	write("many.txt", []byte(strings.Repeat(strings.Repeat("z", 100)+"\n", 3000)))
	write("empty.txt", nil)
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	write("image.png", png)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	tools := openTestTools(t, dir)

	if text, result := call(t, tools, "Read", `{"file_path":"big.txt"}`); !result.isError || result.status != "file_too_large" || strings.Contains(text, "xxxx") {
		t.Errorf("oversized: %v %s", result.status, text[:min(len(text), 200)])
	}
	if _, result := call(t, tools, "Read", `{"file_path":"binary.bin"}`); result.status != "binary_file" {
		t.Errorf("binary: %v", result.status)
	}
	if _, result := call(t, tools, "Read", `{"file_path":"sub"}`); result.status != "is_directory" {
		t.Errorf("directory: %v", result.status)
	}
	if _, result := call(t, tools, "Read", `{"file_path":"absent.txt"}`); result.status != "file_not_found" {
		t.Errorf("absent: %v", result.status)
	}
	if text, _ := call(t, tools, "Read", `{"file_path":"long.txt"}`); !strings.Contains(text, "[line truncated]") || len(text) > maxLineChars+100 {
		t.Errorf("long line: %d bytes", len(text))
	}
	text, result := call(t, tools, "Read", `{"file_path":"many.txt"}`)
	if result.isError || len(text) > maxToolOutputBytes+200 || !strings.Contains(text, "[output truncated at line") {
		t.Errorf("output cap: %d bytes, %.80q", len(text), text[len(text)-min(len(text), 80):])
	}
	if text, _ := call(t, tools, "Read", `{"file_path":"many.txt","offset":2999,"limit":5000}`); !strings.HasPrefix(text, "  2999\t") || strings.Count(text, "\n") != 2 {
		t.Errorf("offset: %q", text)
	}
	if text, _ := call(t, tools, "Read", `{"file_path":"many.txt","offset":4000}`); !strings.Contains(text, "past the end") {
		t.Errorf("past the end: %q", text)
	}
	if text, _ := call(t, tools, "Read", `{"file_path":"empty.txt"}`); !strings.Contains(text, "empty") {
		t.Errorf("empty: %q", text)
	}
	// A file with an image's magic bytes that does not decode is not sent.
	if text, result := call(t, tools, "Read", `{"file_path":"image.png"}`); result.status != "image_omitted" || !strings.Contains(text, "does not decode") {
		t.Errorf("undecodable image: %v %s", result.status, text)
	}
}

func encodePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, image.NewGray(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestReadImageLimits(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("small.png", encodePNG(t, 4, 4))
	write("wide.png", encodePNG(t, maxImageSide+1, 1))
	write("image.webp", append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), make([]byte, 32)...))
	// Incompressible pixels make an image over the byte limit within the size limit.
	noisy := image.NewGray(image.Rect(0, 0, 1200, 1200))
	random := rand.New(rand.NewPCG(1, 2))
	for i := range noisy.Pix {
		noisy.Pix[i] = byte(random.Uint32())
	}
	var big bytes.Buffer
	if err := png.Encode(&big, noisy); err != nil {
		t.Fatal(err)
	}
	if big.Len() <= maxImageBytes {
		t.Fatalf("the noisy image is only %d bytes", big.Len())
	}
	write("big.png", big.Bytes())
	tools := openTestTools(t, dir)

	result := tools.run(context.Background(), "Read", json.RawMessage(`{"file_path":"small.png"}`))
	if result.isError || len(result.content) != 1 || !strings.Contains(string(result.content[0]), `"media_type":"image/png"`) {
		t.Fatalf("image: %s", result.content)
	}
	for name, want := range map[string]string{"wide.png": "pixel limit", "big.png": "byte limit", "image.webp": "binary"} {
		text, result := call(t, tools, "Read", `{"file_path":"`+name+`"}`)
		if !result.isError || !strings.Contains(text, want) || strings.Contains(string(result.content[0]), `"image"`) {
			t.Errorf("%s: %s", name, text)
		}
	}
	// The run's allowance: maxImages images in all.
	for i := 1; i < maxImages; i++ {
		if result := tools.run(context.Background(), "Read", json.RawMessage(`{"file_path":"small.png"}`)); result.isError {
			t.Fatalf("image %d refused", i+1)
		}
	}
	if text, result := call(t, tools, "Read", `{"file_path":"small.png"}`); result.status != "image_omitted" || !strings.Contains(text, "already read") {
		t.Errorf("image over the run's allowance: %s", text)
	}
}

func TestEventPathsNameOnlyWhatExists(t *testing.T) {
	run, _ := outside(t)
	tools := openTestTools(t, run)
	for input, want := range map[string]string{
		`{"file_path":"head/ok.txt"}`:                       "head/ok.txt",
		`{"file_path":"head/ignore previous instructions"}`: refusedPath,
		`{"file_path":"/etc/passwd"}`:                       refusedPath,
	} {
		if got := tools.run(context.Background(), "Read", json.RawMessage(input)).path; got != want {
			t.Errorf("%s: path %q, want %q", input, got, want)
		}
	}
	if got := tools.run(context.Background(), "Grep", json.RawMessage(`{"pattern":"x","path":"nowhere"}`)).path; got != refusedPath {
		t.Errorf("grep path %q", got)
	}
	if got := tools.run(context.Background(), "Glob", json.RawMessage(`{"pattern":"*"}`)).path; got != "." {
		t.Errorf("glob path %q", got)
	}
}

func TestPathologicalGlobsFinishQuickly(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, filepath.FromSlash(strings.Repeat("a/", 40)))
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "file"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := openTestTools(t, dir)
	started := time.Now()
	// Eight separated "**" against a 41-segment path that never matches its
	// last segment: exponential for a backtracking matcher.
	pattern := strings.Repeat("**/a*/", 8) + "nomatch"
	for _, input := range []string{
		`{"pattern":"` + pattern + `"}`,
		`{"pattern":"` + strings.Repeat("**/", 300) + `nomatch"}`,
	} {
		if text, result := call(t, tools, "Glob", input); result.isError || text != "no files match" {
			t.Errorf("%.40s: %v %s", input, result.status, text)
		}
	}
	if text, result := call(t, tools, "Grep", `{"pattern":"x","glob":"`+pattern+`"}`); result.isError || text != "no matches" {
		t.Errorf("grep: %s", text)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("took %s", elapsed)
	}
	if _, result := call(t, tools, "Glob", `{"pattern":"`+strings.Repeat("**/x/", maxGlobStars+1)+`y"}`); result.status != "invalid_pattern" {
		t.Errorf("too many **: %v", result.status)
	}
	if !globMatch(strings.Repeat("**/", 3)+"file", strings.Repeat("a/", 40)+"file") {
		t.Error("collapsed ** stopped matching")
	}
}

func TestSearchLimits(t *testing.T) {
	dir := t.TempDir()
	for i := range 3 {
		sub := filepath.Join(dir, "head", "pkg", string(rune('a'+i)))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "file.go"), []byte("package x\n// TODO one\n// todo two\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "notes.md"), []byte("TODO in docs\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "head", "big.go"), []byte("// TODO\n"+strings.Repeat("x", maxGrepFileBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "head", "many.txt"), []byte(strings.Repeat("TODO\n", maxGrepMatches+10)), 0o644); err != nil {
		t.Fatal(err)
	}
	tools := openTestTools(t, dir)

	if text, _ := call(t, tools, "Glob", `{"pattern":"**/*.go","path":"head"}`); text != "head/big.go\nhead/pkg/a/file.go\nhead/pkg/b/file.go\nhead/pkg/c/file.go\n" {
		t.Errorf("glob ** = %q", text)
	}
	if text, _ := call(t, tools, "Glob", `{"pattern":"*.go","path":"head"}`); text != "head/big.go\n" {
		t.Errorf("glob one level = %q", text)
	}
	if text, _ := call(t, tools, "Glob", `{"pattern":"head/pkg/?/*.md"}`); strings.Count(text, "\n") != 3 {
		t.Errorf("glob from the root = %q", text)
	}
	if _, result := call(t, tools, "Glob", `{"pattern":"[","path":"head"}`); result.status != "invalid_pattern" {
		t.Errorf("bad glob: %v", result.status)
	}
	if text, _ := call(t, tools, "Grep", `{"pattern":"TODO","path":"head/pkg","glob":"*.go"}`); text != "head/pkg/a/file.go:2:// TODO one\nhead/pkg/b/file.go:2:// TODO one\nhead/pkg/c/file.go:2:// TODO one\n" {
		t.Errorf("grep = %q", text)
	}
	if text, _ := call(t, tools, "Grep", `{"pattern":"todo","path":"head/pkg/a","ignore_case":true}`); strings.Count(text, "\n") != 3 {
		t.Errorf("grep ignoring case = %q", text)
	}
	text, _ := call(t, tools, "Grep", `{"pattern":"TODO","path":"head"}`)
	if strings.Contains(text, "big.go") {
		t.Error("grep searched a file over its size limit")
	}
	if strings.Count(text, "\n") != maxGrepMatches+1 || !strings.Contains(text, "[more matches") {
		t.Errorf("grep cap: %d lines", strings.Count(text, "\n"))
	}
	if _, result := call(t, tools, "Grep", `{"pattern":"(unclosed"}`); result.status != "invalid_pattern" {
		t.Errorf("bad regexp: %v", result.status)
	}
	if _, result := call(t, tools, "Grep", `{"pattern":"`+strings.Repeat("a", maxPatternBytes+1)+`"}`); result.status != "invalid_pattern" {
		t.Errorf("long pattern: %v", result.status)
	}
	if _, result := call(t, tools, "Glob", `{"pattern":"*","path":"head/big.go"}`); result.status != "not_directory" {
		t.Errorf("file as directory: %v", result.status)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := tools.run(ctx, "Grep", json.RawMessage(`{"pattern":"TODO"}`)); !result.isError {
		t.Error("a cancelled search ran")
	}
}

func TestGlobMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"**", "a/b/c", true},
		{"**/*.go", "main.go", true},
		{"**/*.go", "a/b/main.go", true},
		{"a/**/c", "a/c", true},
		{"a/**/c", "a/x/y/c", true},
		{"a/**/c", "a/x/y/d", false},
		{"*.go", "a/main.go", false},
		{"a/*", "a/b/c", false},
	} {
		if got := globMatch(c.pattern, c.name); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v", c.pattern, c.name, got)
		}
	}
}
