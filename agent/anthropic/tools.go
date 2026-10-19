package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // image.DecodeConfig reads the formats Read sends.
	_ "image/jpeg"
	_ "image/png"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// The tools read the run directory and nothing else. Every path is relative
// to it: an absolute path, a ".." element or a backslash is refused, and the
// directory is opened as an os.Root, so a symbolic link that leaves it cannot
// be read. Listing and searching never follow a link. Nothing writes, runs a
// command or reaches the network.
const (
	// maxReadFileBytes is the largest file Read returns.
	maxReadFileBytes = 4 << 20
	// maxImageBytes and maxImageSide bound one image Read returns, and
	// maxImages and maxRunImageBytes the images of one run, so a request
	// stays far below the API's size limit.
	maxImageBytes    = 1 << 20
	maxImageSide     = 2000
	maxImages        = 10
	maxRunImageBytes = 5 << 20
	// maxReadLines is Read's default and largest line count.
	maxReadLines = 2000
	// maxLineChars bounds one line Read returns.
	maxLineChars = 2000
	// maxToolOutputBytes bounds the text of one tool result.
	maxToolOutputBytes = 64 << 10
	// maxGlobMatches and maxGrepMatches bound what one search returns.
	maxGlobMatches = 500
	maxGrepMatches = 200
	// maxGrepLineChars bounds one matching line.
	maxGrepLineChars = 300
	// maxGrepFileBytes is the largest file Grep searches.
	maxGrepFileBytes = 4 << 20
	// maxWalkEntries bounds the entries one search visits.
	maxWalkEntries = 200_000
	// maxPatternBytes bounds a glob or regular expression.
	maxPatternBytes = 1024
	// maxGlobStars bounds the "**" segments of a glob.
	maxGlobStars = 8
)

// refusedPath stands in an event for a path that did not resolve to an
// existing file or directory in the run directory, so model text that names
// nothing real never reaches the diagnostics.
const refusedPath = "(refused path)"

type toolbox struct {
	root *os.Root
	// images and imageBytes count the images Read has returned in this run.
	images, imageBytes int
}

func openTools(dir string) (*toolbox, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("anthropic: opening the run directory: %w", err)
	}
	return &toolbox{root: root}, nil
}

func (t *toolbox) close() { _ = t.root.Close() }

// toolCall is what one tool call produced: the result content and the
// metadata an event may hold (never the content or a search pattern).
type toolCall struct {
	content       []json.RawMessage
	isError       bool
	status        string
	path          string
	offset, limit int
	bytes         int
}

func failed(status, text string) toolCall {
	return toolCall{content: textContent(text), isError: true, status: status, bytes: len(text)}
}

func (t *toolbox) run(ctx context.Context, name string, input json.RawMessage) toolCall {
	var call toolCall
	switch name {
	case "Read":
		var in struct {
			FilePath string `json:"file_path"`
			Offset   int    `json:"offset"`
			Limit    int    `json:"limit"`
		}
		if err := strictDecode(input, &in); err != nil {
			return failed("invalid_input", "invalid Read input: "+err.Error())
		}
		call = t.read(in.FilePath, in.Offset, in.Limit)
		call.offset, call.limit = in.Offset, in.Limit
	case "Glob":
		var in struct {
			Pattern string `json:"pattern"`
			Path    string `json:"path"`
		}
		if err := strictDecode(input, &in); err != nil {
			return failed("invalid_input", "invalid Glob input: "+err.Error())
		}
		call = t.glob(ctx, in.Pattern, in.Path)
	case "Grep":
		var in struct {
			Pattern    string `json:"pattern"`
			Path       string `json:"path"`
			Glob       string `json:"glob"`
			IgnoreCase bool   `json:"ignore_case"`
		}
		if err := strictDecode(input, &in); err != nil {
			return failed("invalid_input", "invalid Grep input: "+err.Error())
		}
		call = t.grep(ctx, in.Pattern, in.Path, in.Glob, in.IgnoreCase)
	default:
		return failed("unknown_tool", fmt.Sprintf("unknown tool %q; use Read, Glob, Grep or StructuredOutput", boundedText(name, 64)))
	}
	if call.path == "" {
		call.path = refusedPath
	} else {
		call.path = boundedText(call.path, 256)
	}
	return call
}

func strictDecode(input json.RawMessage, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	return decoder.Decode(into)
}

// relative checks a path the model gave and returns it in slash form, "." for
// the run directory itself.
func relative(name string, allowRoot bool) (string, error) {
	switch {
	case name == "" || name == ".":
		if allowRoot {
			return ".", nil
		}
		return "", errors.New("a file path is required")
	case strings.ContainsRune(name, 0):
		return "", errors.New("the path contains a NUL byte")
	case strings.Contains(name, `\`):
		return "", errors.New("use forward slashes in paths")
	case path.IsAbs(name) || filepath.IsAbs(name) || filepath.VolumeName(name) != "":
		return "", errors.New("absolute paths are not allowed; give a path relative to the run directory, such as head/README.md")
	}
	for _, element := range strings.Split(name, "/") {
		if element == ".." {
			return "", errors.New(`".." is not allowed; paths stay inside the run directory`)
		}
	}
	return path.Clean(name), nil
}

func (t *toolbox) read(name string, offset, limit int) toolCall {
	rel, err := relative(name, false)
	if err != nil {
		return failed("invalid_path", err.Error())
	}
	file, err := t.root.Open(rel)
	if err != nil {
		return failed(fileStatus(err), fmt.Sprintf("cannot read %s: %s", rel, fileError(err)))
	}
	defer func() { _ = file.Close() }()
	call := t.readOpen(file, rel, offset, limit)
	call.path = rel
	return call
}

func (t *toolbox) readOpen(file *os.File, rel string, offset, limit int) toolCall {
	info, err := file.Stat()
	switch {
	case err != nil:
		return failed("error", fmt.Sprintf("cannot read %s", rel))
	case info.IsDir():
		return failed("is_directory", rel+" is a directory; list it with Glob")
	case !info.Mode().IsRegular():
		return failed("not_regular", rel+" is not a regular file")
	case info.Size() > maxReadFileBytes:
		return failed("file_too_large", fmt.Sprintf("%s is %d bytes, over the %d-byte limit; search it with Grep instead", rel, info.Size(), maxReadFileBytes))
	}
	data, err := io.ReadAll(io.LimitReader(file, maxReadFileBytes+1))
	if err != nil || len(data) > maxReadFileBytes {
		return failed("error", fmt.Sprintf("cannot read %s", rel))
	}
	if media := imageType(data); media != "" {
		return t.image(rel, media, data)
	}
	if binary(data) {
		return failed("binary_file", rel+" is a binary file and is not shown")
	}
	if offset < 1 {
		offset = 1
	}
	if limit < 1 || limit > maxReadLines {
		limit = maxReadLines
	}
	var out strings.Builder
	lines := strings.SplitAfter(string(data), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) == 0 {
		return text(rel + " is empty")
	}
	if offset > len(lines) {
		return text(fmt.Sprintf("%s has %d lines; offset %d is past the end", rel, len(lines), offset))
	}
	end := min(offset-1+limit, len(lines))
	for i := offset - 1; i < end; i++ {
		line := strings.TrimRight(lines[i], "\r\n")
		if len(line) > maxLineChars {
			line = strings.ToValidUTF8(line[:maxLineChars], "") + " [line truncated]"
		}
		entry := fmt.Sprintf("%6d\t%s\n", i+1, line)
		if out.Len()+len(entry) > maxToolOutputBytes {
			fmt.Fprintf(&out, "[output truncated at line %d; read on with offset %d]\n", i, i+1)
			return text(out.String())
		}
		out.WriteString(entry)
	}
	if end < len(lines) {
		fmt.Fprintf(&out, "[%d more lines; read on with offset %d]\n", len(lines)-end, end+1)
	}
	return text(out.String())
}

func text(s string) toolCall { return toolCall{content: textContent(s), bytes: len(s)} }

// imageType is the media type of an image Read may send: PNG, JPEG or GIF,
// the formats the standard library can check. Anything else, WebP included,
// is treated as binary.
func imageType(data []byte) string {
	switch media := http.DetectContentType(data); media {
	case "image/png", "image/jpeg", "image/gif":
		return media
	}
	return ""
}

// image returns an image as an image block when it decodes, is small and the
// run's image allowance has room, and otherwise a short text saying why not.
func (t *toolbox) image(rel, media string, data []byte) toolCall {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	switch {
	case err != nil || "image/"+format != media:
		return failed("image_omitted", rel+" looks like an image but does not decode; it is not shown")
	case config.Width > maxImageSide || config.Height > maxImageSide:
		return failed("image_omitted", fmt.Sprintf("%s is %dx%d pixels, over the %d-pixel limit; it is not shown", rel, config.Width, config.Height, maxImageSide))
	case len(data) > maxImageBytes:
		return failed("image_omitted", fmt.Sprintf("%s is an image of %d bytes, over the %d-byte limit; it is not shown", rel, len(data), maxImageBytes))
	case t.images >= maxImages || t.imageBytes+len(data) > maxRunImageBytes:
		return failed("image_omitted", fmt.Sprintf("%s is not shown: this run has already read its %d images or %d bytes of images", rel, maxImages, maxRunImageBytes))
	}
	t.images++
	t.imageBytes += len(data)
	encoded, _ := json.Marshal(map[string]any{"type": "image", "source": map[string]string{"type": "base64", "media_type": media, "data": base64.StdEncoding.EncodeToString(data)}})
	return toolCall{content: []json.RawMessage{encoded}, bytes: len(data)}
}

func binary(data []byte) bool { return bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 }

func fileStatus(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "file_not_found"
	case errors.Is(err, fs.ErrPermission):
		return "permission_denied"
	}
	return "outside_run_directory"
}

// fileError describes a failure without the absolute run directory.
func fileError(err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "no such file"
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	}
	return "it is outside the run directory or not readable"
}

// walk visits the regular files under base, in lexical order, never following
// a symbolic link. visit returns false to stop.
func (t *toolbox) walk(ctx context.Context, base string, visit func(rel string) bool) error {
	visited := 0
	return fs.WalkDir(t.root.FS(), base, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			if name == base {
				return err
			}
			return nil
		}
		if visited++; visited > maxWalkEntries {
			return fs.SkipAll
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.Type().IsRegular() && !visit(name) {
			return fs.SkipAll
		}
		return nil
	})
}

func (t *toolbox) base(name string) (string, *toolCall) {
	base, err := relative(name, true)
	if err != nil {
		call := failed("invalid_path", err.Error())
		return "", &call
	}
	info, err := fs.Stat(t.root.FS(), base)
	if err != nil {
		call := failed(fileStatus(err), fmt.Sprintf("cannot search %s: %s", base, fileError(err)))
		return "", &call
	}
	if !info.IsDir() {
		call := failed("not_directory", base+" is not a directory")
		return "", &call
	}
	return base, nil
}

func (t *toolbox) glob(ctx context.Context, pattern, dir string) toolCall {
	pattern, err := checkGlob(pattern)
	if err != nil {
		return failed("invalid_pattern", err.Error())
	}
	base, refused := t.base(dir)
	if refused != nil {
		return *refused
	}
	call := t.globIn(ctx, pattern, base)
	call.path = base
	return call
}

func (t *toolbox) globIn(ctx context.Context, pattern, base string) toolCall {
	var matches []string
	more := false
	err := t.walk(ctx, base, func(name string) bool {
		within := name
		if base != "." {
			within = strings.TrimPrefix(name, base+"/")
		}
		if globMatch(pattern, within) {
			if len(matches) == maxGlobMatches {
				more = true
				return false
			}
			matches = append(matches, name)
		}
		return true
	})
	if err != nil {
		return failed("error", "the search failed")
	}
	if len(matches) == 0 {
		return text("no files match")
	}
	out := strings.Join(matches, "\n") + "\n"
	if len(out) > maxToolOutputBytes {
		cut := strings.LastIndexByte(out[:maxToolOutputBytes], '\n')
		out, more = out[:cut+1], true
	}
	if more {
		out += "[more files match; narrow the pattern or the path]\n"
	}
	return text(out)
}

func (t *toolbox) grep(ctx context.Context, pattern, dir, filter string, ignoreCase bool) toolCall {
	if pattern == "" || len(pattern) > maxPatternBytes {
		return failed("invalid_pattern", fmt.Sprintf("the pattern must be 1 to %d bytes", maxPatternBytes))
	}
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	expression, err := regexp.Compile(pattern)
	if err != nil {
		return failed("invalid_pattern", "invalid regular expression (RE2 syntax): "+err.Error())
	}
	if filter != "" {
		if filter, err = checkGlob(filter); err != nil {
			return failed("invalid_pattern", "glob: "+err.Error())
		}
	}
	base, refused := t.base(dir)
	if refused != nil {
		return *refused
	}
	call := t.grepIn(ctx, expression, base, filter)
	call.path = base
	return call
}

func (t *toolbox) grepIn(ctx context.Context, expression *regexp.Regexp, base, filter string) toolCall {
	var out strings.Builder
	matches := 0
	more := false
	walkErr := t.walk(ctx, base, func(name string) bool {
		if filter != "" {
			subject := path.Base(name)
			if strings.Contains(filter, "/") {
				subject = name
			}
			if !globMatch(filter, subject) {
				return true
			}
		}
		file, err := t.root.Open(name)
		if err != nil {
			return true
		}
		defer func() { _ = file.Close() }()
		if info, err := file.Stat(); err != nil || info.Size() > maxGrepFileBytes {
			return true
		}
		reader := bufio.NewReader(io.LimitReader(file, maxGrepFileBytes))
		head, _ := reader.Peek(8192)
		if binary(head) {
			return true
		}
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64<<10), maxGrepFileBytes)
		for line := 1; scanner.Scan(); line++ {
			if !expression.Match(scanner.Bytes()) {
				continue
			}
			if matches == maxGrepMatches {
				more = true
				return false
			}
			found := scanner.Text()
			if len(found) > maxGrepLineChars {
				found = strings.ToValidUTF8(found[:maxGrepLineChars], "") + " [line truncated]"
			}
			entry := fmt.Sprintf("%s:%d:%s\n", name, line, strings.TrimRight(found, "\r"))
			if out.Len()+len(entry) > maxToolOutputBytes {
				more = true
				return false
			}
			out.WriteString(entry)
			matches++
		}
		return true
	})
	if walkErr != nil {
		return failed("error", "the search failed")
	}
	if matches == 0 {
		return text("no matches")
	}
	if more {
		out.WriteString("[more matches; narrow the pattern, the path or the glob]\n")
	}
	return text(out.String())
}

// checkGlob validates a glob and returns it with runs of "**" segments
// collapsed into one, which match the same paths.
func checkGlob(pattern string) (string, error) {
	if pattern == "" || len(pattern) > maxPatternBytes {
		return "", fmt.Errorf("the pattern must be 1 to %d bytes", maxPatternBytes)
	}
	if strings.HasPrefix(pattern, "/") || strings.Contains(pattern, `\`) {
		return "", errors.New("the pattern must be relative and use forward slashes")
	}
	var segments []string
	stars := 0
	for _, segment := range strings.Split(pattern, "/") {
		if segment == ".." {
			return "", errors.New(`".." is not allowed`)
		}
		if _, err := path.Match(segment, ""); err != nil {
			return "", errors.New("malformed glob pattern")
		}
		if segment == "**" {
			if len(segments) > 0 && segments[len(segments)-1] == "**" {
				continue
			}
			if stars++; stars > maxGlobStars {
				return "", fmt.Errorf("the pattern has more than %d ** segments", maxGlobStars)
			}
		}
		segments = append(segments, segment)
	}
	return strings.Join(segments, "/"), nil
}

// globMatch matches a slash-separated path against a pattern whose segments
// are path.Match patterns and where "**" matches any number of directories.
func globMatch(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

// matchSegments decides the match by dynamic programming over pattern and
// path segments, so its time is proportional to their product whatever the
// pattern. reach[j] says the segments seen so far match the first j path
// segments.
func matchSegments(pattern, name []string) bool {
	reach := make([]bool, len(name)+1)
	reach[0] = true
	next := make([]bool, len(name)+1)
	for _, segment := range pattern {
		clear(next)
		if segment == "**" {
			seen := false
			for j := range reach {
				seen = seen || reach[j]
				next[j] = seen
			}
		} else {
			for j := range name {
				if reach[j] {
					next[j+1], _ = path.Match(segment, name[j])
				}
			}
		}
		reach, next = next, reach
	}
	return reach[len(name)]
}

// toolDefinitions are the tools every call offers, in a fixed order so the
// cached prefix stays the same: the three read-only file tools and the
// StructuredOutput tool, whose input schema is the document schema.
func toolDefinitions(schema []byte) ([]json.RawMessage, error) {
	if !json.Valid(schema) {
		return nil, errors.New("anthropic: the output schema is not JSON")
	}
	structured, err := json.Marshal(struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}{structuredOutputTool, "Submit the finished review document, once. The document's top-level fields are this call's arguments.", schema})
	if err != nil {
		return nil, err
	}
	return []json.RawMessage{json.RawMessage(readTool), json.RawMessage(globTool), json.RawMessage(grepTool), structured}, nil
}

const readTool = `{"name":"Read","description":"Read a file in the run directory, with numbered lines. Paths are relative to the run directory (head/..., merge-base/..., bundle.json, schema.json, attachments). Returns at most 2000 lines from offset; images are returned as images.","input_schema":{"type":"object","properties":{"file_path":{"type":"string","description":"Path relative to the run directory, such as head/src/main.go"},"offset":{"type":"integer","description":"First line to return, from 1"},"limit":{"type":"integer","description":"Number of lines, at most 2000"}},"required":["file_path"],"additionalProperties":false}}`

const globTool = `{"name":"Glob","description":"List the files in the run directory whose path matches a glob pattern. Segments use * ? and [..]; ** matches any number of directories. The pattern is matched against paths relative to path.","input_schema":{"type":"object","properties":{"pattern":{"type":"string","description":"Glob pattern, such as **/*.go"},"path":{"type":"string","description":"Directory to search, relative to the run directory, such as head; the run directory when omitted"}},"required":["pattern"],"additionalProperties":false}}`

const grepTool = `{"name":"Grep","description":"Search file contents in the run directory with an RE2 regular expression. Returns matching lines as path:line:text.","input_schema":{"type":"object","properties":{"pattern":{"type":"string","description":"RE2 regular expression"},"path":{"type":"string","description":"Directory to search, relative to the run directory, such as head; the run directory when omitted"},"glob":{"type":"string","description":"Only search files whose name (or, with a slash, whose path) matches this glob, such as *.go"},"ignore_case":{"type":"boolean"}},"required":["pattern"],"additionalProperties":false}}`
