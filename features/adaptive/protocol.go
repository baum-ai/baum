// Package adaptive defines the checkpoint protocol for repository-aware feature
// builds. The host owns evidence reads and persistence; the agent reads source
// and asks for the evidence it needs in its next structured reply.
package adaptive

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

const Version = "feature-build-public-v1"

//go:embed prompt.txt
var Prompt string

// Scope defines one outcome independently of its current members.
type Scope struct {
	Key        string   `json:"key"`
	Title      string   `json:"title"`
	Purpose    string   `json:"purpose"`
	Boundaries string   `json:"boundaries"`
	Paths      []string `json:"paths"`
	Examples   []int    `json:"examples"`
	Digest     string   `json:"digest"`
}

// Assignment settles one selected PR. An empty scope explicitly skips work
// outside every substantial outcome; the reason must explain the choice.
type Assignment struct {
	Number int    `json:"number"`
	Scope  string `json:"scope"`
	Reason string `json:"reason"`
}

// Reply is one valid work step, separate from schema-validation repairs.
// Selected is additive across all steps.
type Reply struct {
	Action      string       `json:"action"`
	Since       string       `json:"since"`
	Numbers     []int        `json:"numbers"`
	Paths       []string     `json:"paths"`
	Selected    []int        `json:"selected"`
	Scopes      []Scope      `json:"scopes"`
	Assignments []Assignment `json:"assignments"`
	Reason      string       `json:"reason"`
}

var keyPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,79}$`)

// Parse checks the protocol shape. The host additionally validates source,
// catalog and membership references against the pinned evidence.
func Parse(data []byte) (Reply, error) {
	var r Reply
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&r); err != nil {
		return r, err
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return r, errors.New("expected one reply document")
	}
	switch r.Action {
	case "catalog", "details", "code", "checkpoint", "finish":
	default:
		return r, errors.New("unknown feature-build action")
	}
	if utf8.RuneCountInString(r.Reason) > 2000 || strings.TrimSpace(r.Reason) == "" {
		return r, errors.New("a short reason is required")
	}
	if len(r.Scopes) > 300 || len(r.Numbers) > 100 || len(r.Paths) > 20 {
		return r, errors.New("split evidence requests or scope proposals into smaller steps")
	}
	keys := map[string]bool{}
	for _, s := range r.Scopes {
		if !keyPattern.MatchString(s.Key) || keys[s.Key] {
			return r, errors.New("scope keys must be unique short identifiers")
		}
		keys[s.Key] = true
		if strings.TrimSpace(s.Title) == "" || utf8.RuneCountInString(s.Title) > 120 || strings.TrimSpace(s.Purpose) == "" || utf8.RuneCountInString(s.Purpose) > 1000 || utf8.RuneCountInString(s.Boundaries) > 2000 || utf8.RuneCountInString(s.Digest) > 4000 {
			return r, fmt.Errorf("scope %s needs a concise title, purpose and digest", s.Key)
		}
		for _, p := range s.Paths {
			if !ValidPath(strings.TrimSuffix(p, "/")) {
				return r, fmt.Errorf("invalid scope path %q", p)
			}
		}
	}
	numbers := map[int]bool{}
	for _, a := range r.Assignments {
		if a.Number < 1 || numbers[a.Number] || strings.TrimSpace(a.Reason) == "" || utf8.RuneCountInString(a.Reason) > 2000 {
			return r, errors.New("assignments need unique positive PR numbers and concise reasons")
		}
		numbers[a.Number] = true
	}
	for _, p := range r.Paths {
		if !ValidPath(p) {
			return r, fmt.Errorf("invalid requested path %q", p)
		}
	}
	return r, nil
}

// ValidPath accepts only literal repository-relative paths.
func ValidPath(p string) bool {
	return p != "" && p != "." && path.Clean(p) == p && !strings.HasPrefix(p, "../") && !path.IsAbs(p) && !strings.ContainsAny(p, "\\\x00")
}

// Schema is flat: the CLI returns one next step rather than an entire history.
var Schema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["action","since","numbers","paths","selected","scopes","assignments","reason"],"properties":{"action":{"type":"string","enum":["catalog","details","code","checkpoint","finish"]},"since":{"type":"string"},"numbers":{"type":"array","items":{"type":"integer"}},"paths":{"type":"array","items":{"type":"string"}},"selected":{"type":"array","items":{"type":"integer"}},"scopes":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["key","title","purpose","boundaries","paths","examples","digest"],"properties":{"key":{"type":"string"},"title":{"type":"string"},"purpose":{"type":"string"},"boundaries":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}},"examples":{"type":"array","items":{"type":"integer"}},"digest":{"type":"string"}}}},"assignments":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["number","scope","reason"],"properties":{"number":{"type":"integer"},"scope":{"type":"string"},"reason":{"type":"string"}}}},"reason":{"type":"string"}}}`)
