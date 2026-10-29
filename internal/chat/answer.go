package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

// Schema is the JSON Schema of an answer. It keeps to what strict structured
// outputs accept (codex.StrictSchema): objects with every property required
// and no others, arrays, strings and integers, and null only through anyOf.
var Schema = []byte(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["answer_markdown", "citations", "suggests_fix"],
  "properties": {
    "answer_markdown": {"type": "string", "minLength": 1, "maxLength": 20000},
    "citations": {
      "type": "array",
      "maxItems": 20,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["path", "start_line", "end_line"],
        "properties": {
          "path": {"type": "string", "minLength": 1, "maxLength": 1024},
          "start_line": {"type": "integer", "minimum": 1},
          "end_line": {"type": "integer", "minimum": 1}
        }
      }
    },
    "suggests_fix": {
      "anyOf": [
        {
          "type": "object",
          "additionalProperties": false,
          "required": ["summary"],
          "properties": {"summary": {"type": "string", "minLength": 1, "maxLength": 2000}}
        },
        {"type": "null"}
      ]
    }
  }
}`)

// Answer is the agent's answer to one question.
type Answer struct {
	Markdown    string      `json:"answer_markdown"`
	Citations   []Citation  `json:"citations"`
	SuggestsFix *Suggestion `json:"suggests_fix"`
}

// Citation is lines of a file at the pull request's head, by its
// repository-relative path.
type Citation struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// Suggestion is a concrete change the answer says should be made.
type Suggestion struct {
	Summary string `json:"summary"`
}

var resolvedSchema = sync.OnceValues(func() (*jsonschema.Resolved, error) {
	var schema jsonschema.Schema
	if err := json.Unmarshal(Schema, &schema); err != nil {
		return nil, err
	}
	return schema.Resolve(nil)
})

// parse reads a reply as an answer, returning findings when the schema
// rejects it.
func parse(document []byte) (Answer, string) {
	schema, err := resolvedSchema()
	if err != nil {
		return Answer{}, err.Error()
	}
	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		return Answer{}, "The reply is not a single JSON document."
	}
	if err := schema.Validate(value); err != nil {
		return Answer{}, err.Error()
	}
	var answer Answer
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&answer); err != nil {
		return Answer{}, err.Error()
	}
	if answer.Citations == nil {
		answer.Citations = []Citation{}
	}
	return answer, ""
}

// maxCitedFile bounds the bytes of a cited file that are read to count its
// lines.
const maxCitedFile = 64 << 20

// checkCitation reports what is wrong with a citation of the checkout whose
// head tree is head, or "" when it names lines that exist. The path must be
// relative and clean, stay inside the head tree after symlinks are resolved
// and name a regular file with the cited lines.
func checkCitation(head string, c Citation) string {
	p := c.Path
	switch {
	case p == "" || len(p) > maxPath || strings.ContainsRune(p, '\\') || strings.ContainsRune(p, 0):
		return "the path is empty or malformed"
	case strings.HasPrefix(p, "/") || filepath.IsAbs(p):
		return "the path is absolute; give it relative to the repository root"
	case path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../") || !filepath.IsLocal(filepath.FromSlash(p)):
		return "the path is not a clean repository-relative path"
	case c.StartLine < 1 || c.EndLine < c.StartLine:
		return "the lines are not a range of 1 or more"
	}
	root, err := filepath.EvalSymlinks(head)
	if err != nil {
		return "the checkout is not available"
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(p)))
	if err != nil {
		return "the file does not exist at the pull request's head"
	}
	if rel, err := filepath.Rel(root, resolved); err != nil || !filepath.IsLocal(rel) {
		return "the path leads outside the checkout"
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "the path is not a file"
	}
	lines, err := countLines(resolved)
	if err != nil {
		return "the file could not be read"
	}
	if c.EndLine > lines {
		return fmt.Sprintf("the file has %d line(s)", lines)
	}
	return ""
}

// countLines counts a file's lines: a last line without a newline counts.
func countLines(name string) (int, error) {
	file, err := os.Open(name)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	reader := io.LimitReader(file, maxCitedFile)
	lines, last := 0, byte('\n')
	buffer := make([]byte, 64<<10)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			lines += bytes.Count(buffer[:n], []byte{'\n'})
			last = buffer[n-1]
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if last != '\n' {
		lines++
	}
	return lines, nil
}

// invalidCitations lists the answer's citations the checkout does not bear
// out, each with why, for a repair.
func invalidCitations(head string, answer Answer) []string {
	var out []string
	for _, c := range answer.Citations {
		if problem := checkCitation(head, c); problem != "" {
			out = append(out, fmt.Sprintf("%s lines %d-%d: %s", c.Path, c.StartLine, c.EndLine, problem))
		}
	}
	return out
}

// keepValid drops the citations the checkout does not bear out.
func keepValid(head string, answer Answer) (Answer, int) {
	kept := make([]Citation, 0, len(answer.Citations))
	for _, c := range answer.Citations {
		if checkCitation(head, c) == "" {
			kept = append(kept, c)
		}
	}
	dropped := len(answer.Citations) - len(kept)
	answer.Citations = kept
	return answer, dropped
}
