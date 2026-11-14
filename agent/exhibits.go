package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
)

// StageExhibits labels the exhibits call's files in the run directory, and
// StageFill a fill's progress (StagedProgress.Section is its type).
const (
	StageExhibits = "exhibits"
	StageFill     = "fill"
)

// exhibitsSchema is the exhibits call's reply: at most MaxAuthored exhibits,
// each with exactly one of its three shapes. Every property is required and
// absent values are explicit nulls, so a strict structured output takes it
// as it is.
var exhibitsSchema = fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["exhibits"],
  "properties": {
    "exhibits": {
      "type": "array",
      "maxItems": %[1]d,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["id", "title", "adds", "table", "code", "prose"],
        "properties": {
          "id": {"type": "string", "pattern": "^(table|code|prose)_[a-z0-9_]{1,40}$"},
          "title": {"type": "string", "minLength": 1, "maxLength": %[2]d},
          "adds": {"type": "string", "maxLength": 300},
          "table": {"anyOf": [{"type": "null"}, {
            "type": "object", "additionalProperties": false,
            "required": ["columns", "rows", "source"],
            "properties": {
              "columns": {"type": "array", "minItems": 1, "maxItems": %[3]d, "items": {"type": "string", "maxLength": 60}},
              "rows": {"type": "array", "minItems": 1, "maxItems": %[4]d, "items": {"type": "array", "items": {"type": "string", "maxLength": %[5]d}}},
              "source": {"$ref": "#/$defs/cites"}
            }}]},
          "code": {"anyOf": [{"type": "null"}, {
            "type": "object", "additionalProperties": false,
            "required": ["language", "content", "caption", "source"],
            "properties": {
              "language": {"type": "string", "maxLength": 40},
              "content": {"type": "string", "minLength": 1, "maxLength": %[6]d},
              "caption": {"type": "string", "maxLength": 200},
              "source": {"$ref": "#/$defs/cites"}
            }}]},
          "prose": {"anyOf": [{"type": "null"}, {
            "type": "object", "additionalProperties": false,
            "required": ["text", "anchors"],
            "properties": {
              "text": {"type": "string", "minLength": 1, "maxLength": %[7]d},
              "anchors": {"$ref": "#/$defs/cites"}
            }}]}
        }
      }
    }
  },
  "$defs": {
    "cites": {
      "type": "array", "minItems": 1, "maxItems": 12,
      "items": {
        "type": "object", "additionalProperties": false,
        "required": ["type", "id", "lines", "path"],
        "properties": {
          "type": {"enum": ["hunk", "file"]},
          "id": {"type": ["string", "null"]},
          "lines": {"anyOf": [{"type": "null"}, {"type": "array", "minItems": 2, "maxItems": 2, "items": {"type": "integer", "minimum": 1}}]},
          "path": {"type": ["string", "null"]}
        }
      }
    }
  }
}`, tiles.MaxAuthored, tiles.MaxComposedTitle, maxExhibitColumns, maxExhibitRows, maxExhibitCell, maxExhibitCodeSize, maxExhibitProse+200)

// ExhibitsSchema returns the exhibits call's reply schema.
func ExhibitsSchema() []byte { return []byte(exhibitsSchema) }

// exhibitReply is one exhibit as the model writes it.
type exhibitReply struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Adds  string `json:"adds"`
	Table *struct {
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
		Source  []exCite   `json:"source"`
	} `json:"table"`
	Code *struct {
		Language string   `json:"language"`
		Content  string   `json:"content"`
		Caption  string   `json:"caption"`
		Source   []exCite `json:"source"`
	} `json:"code"`
	Prose *struct {
		Text    string   `json:"text"`
		Anchors []exCite `json:"anchors"`
	} `json:"prose"`
}

type exCite struct {
	Type  string  `json:"type"`
	ID    *string `json:"id"`
	Lines []int   `json:"lines"`
	Path  *string `json:"path"`
}

// exhibitsStage asks one call for authored tiles and keeps those that pass
// the host's checks against the bundle (checkExhibit). It never repairs: an
// exhibit that fails is dropped, and most pull requests have none.
func (s *stagedReview) exhibitsStage(ctx context.Context, budgets Budgets) {
	started := time.Now()
	a := s.tiles
	result := &ExhibitsResult{}
	defer func() {
		result.Elapsed = time.Since(started)
		a.mu.Lock()
		a.exhibits = result
		a.mu.Unlock()
		s.mu.Lock()
		s.report(StagedProgress{Stage: StageExhibits, Failed: result.Reason != ""})
		s.mu.Unlock()
	}()
	if budgets.MaxCalls < 0 || budgets.MaxTokens < 0 {
		result.Reason = FillDroppedBudget
		return
	}
	text := s.prefix + "\n\n" + insertBeforeSubmitting(a.prompt.Exhibits, s.outlineBlock()+"\n")
	budgets.MaxRepairAttempts = 1
	task := s.task(StageExhibits, ExhibitsSchema(), budgets)
	task.Prompt = text
	var accepted []byte
	task.Accept = func(ctx context.Context, _ TaskPass, reply []byte, outcome *Outcome) (Verdict, error) {
		masked, err := RedactTaskOutput(ctx, s.redactor, reply, outcome)
		if err != nil {
			return Verdict{}, errRedaction
		}
		accepted = masked
		return Verdict{Document: masked}, nil
	}
	stage, _, err := s.g.RunTask(ctx, task)
	result.Outcome = stage
	if err != nil {
		result.Reason = FillDroppedFailed
		if ctx.Err() != nil {
			result.Reason = FillDroppedStopped
		}
		return
	}
	var reply struct {
		Exhibits []exhibitReply `json:"exhibits"`
	}
	if json.Unmarshal(accepted, &reply) != nil {
		result.Reason = "the exhibits reply was not an object of its schema"
		return
	}
	result.Returned = len(reply.Exhibits)
	var kept []string
	for _, ex := range reply.Exhibits {
		tile, reason := s.checkExhibit(ex)
		if reason == "" {
			text := ExhibitText(tile)
			for _, other := range kept {
				if review.NearDuplicate(text, other) {
					reason = ExhibitDuplicate
					break
				}
			}
			kept = append(kept, text)
		}
		if reason == "" && len(result.Tiles) >= tiles.MaxAuthored {
			reason = ExhibitLimit
		}
		if reason != "" {
			result.Dropped = append(result.Dropped, DroppedExhibit{ID: plainExhibitID(ex.ID), Reason: reason})
			continue
		}
		result.Tiles = append(result.Tiles, tile)
		if result.Adds == nil {
			result.Adds = map[string]string{}
		}
		result.Adds[tile.ID] = boundRunes(strings.TrimSpace(ex.Adds), 300)
	}
}

func insertBeforeSubmitting(text, insert string) string {
	at := strings.LastIndex(text, "## Submitting\n")
	if at < 0 {
		return text + insert
	}
	return text[:at] + insert + "\n" + text[at:]
}

var exhibitID = regexp.MustCompile(`^(table|code|prose)_[a-z0-9_]{1,64}$`)

func plainExhibitID(id string) string {
	if exhibitID.MatchString(id) {
		return id
	}
	return ""
}

// checkExhibit turns an exhibit into an authored tile, or says why it is
// dropped: its shape (exactly the variant its ID names), its sizes, its
// citations (each hunk in the diff, lines inside it, or dropped when they are
// not; each file in the checkout or the diff; at least one hunk), and for
// code, that its lines are the cited code's.
func (s *stagedReview) checkExhibit(ex exhibitReply) (tiles.AuthoredTile, string) {
	if !exhibitID.MatchString(ex.ID) {
		return tiles.AuthoredTile{}, ExhibitShape
	}
	kind, _, _ := strings.Cut(ex.ID, "_")
	variants := 0
	for _, set := range []bool{ex.Table != nil, ex.Code != nil, ex.Prose != nil} {
		if set {
			variants++
		}
	}
	if variants != 1 || (kind == tiles.TypeTable) != (ex.Table != nil) || (kind == tiles.TypeCode) != (ex.Code != nil) || (kind == tiles.TypeProse) != (ex.Prose != nil) {
		return tiles.AuthoredTile{}, ExhibitShape
	}
	if strings.TrimSpace(ex.Title) == "" {
		return tiles.AuthoredTile{}, ExhibitShape
	}
	var data map[string]any
	var cites []exCite
	switch kind {
	case tiles.TypeTable:
		t := ex.Table
		if len(t.Columns) == 0 || len(t.Rows) == 0 {
			return tiles.AuthoredTile{}, ExhibitShape
		}
		if len(t.Columns) > maxExhibitColumns || len(t.Rows) > maxExhibitRows {
			return tiles.AuthoredTile{}, ExhibitSize
		}
		for _, row := range t.Rows {
			if len(row) != len(t.Columns) {
				return tiles.AuthoredTile{}, ExhibitShape
			}
			for _, cell := range row {
				if utf8.RuneCountInString(cell) > maxExhibitCell {
					return tiles.AuthoredTile{}, ExhibitSize
				}
			}
		}
		cites = t.Source
		data = map[string]any{"columns": t.Columns, "rows": t.Rows}
	case tiles.TypeCode:
		c := ex.Code
		lines := strings.Count(strings.TrimRight(c.Content, "\n"), "\n") + 1
		if strings.TrimSpace(c.Content) == "" {
			return tiles.AuthoredTile{}, ExhibitShape
		}
		if lines > maxExhibitCode+20 || len(c.Content) > maxExhibitCodeSize {
			return tiles.AuthoredTile{}, ExhibitSize
		}
		cites = c.Source
		data = map[string]any{"language": strings.TrimSpace(c.Language), "content": strings.TrimRight(c.Content, "\n")}
		if caption := strings.TrimSpace(c.Caption); caption != "" {
			data["caption"] = caption
		}
	case tiles.TypeProse:
		p := ex.Prose
		if strings.TrimSpace(p.Text) == "" {
			return tiles.AuthoredTile{}, ExhibitShape
		}
		if utf8.RuneCountInString(p.Text) > maxExhibitProse+200 {
			return tiles.AuthoredTile{}, ExhibitSize
		}
		cites = p.Anchors
		data = map[string]any{"text": strings.TrimSpace(p.Text)}
	}
	anchors, hunks, ok := s.exhibitAnchors(cites)
	if !ok {
		return tiles.AuthoredTile{}, ExhibitCitation
	}
	if kind == tiles.TypeCode && !s.codeInSource(ex.Code.Content, hunks, anchors) {
		return tiles.AuthoredTile{}, ExhibitCode
	}
	if kind == tiles.TypeProse {
		data["anchors"] = anchors
	} else {
		data["source"] = anchors
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return tiles.AuthoredTile{}, ExhibitShape
	}
	return tiles.AuthoredTile{ID: ex.ID, Title: strings.TrimSpace(ex.Title), Data: encoded}, ""
}

// exhibitAnchors turns an exhibit's citations into review anchors. A hunk
// not in the diff fails the exhibit; lines outside the hunk's new range are
// dropped, so the whole hunk is cited; a file not in the checkout or the
// diff is dropped. At least one hunk must remain.
func (s *stagedReview) exhibitAnchors(cites []exCite) ([]map[string]any, []string, bool) {
	var anchors []map[string]any
	var hunks []string
	for _, c := range cites {
		switch c.Type {
		case "hunk":
			if c.ID == nil {
				return nil, nil, false
			}
			h, ok := s.tiles.diff[*c.ID]
			if !ok {
				return nil, nil, false
			}
			anchor := map[string]any{"type": "hunk", "id": h.ID}
			if len(c.Lines) == 2 && h.LineCount > 0 && c.Lines[0] <= c.Lines[1] && c.Lines[0] >= h.StartLine && c.Lines[1] <= h.StartLine+h.LineCount-1 {
				anchor["lines"] = []int{c.Lines[0], c.Lines[1]}
			}
			if !slices.Contains(hunks, h.ID) {
				hunks = append(hunks, h.ID)
			}
			anchors = append(anchors, anchor)
		case "file":
			if c.Path == nil || !s.knownPath(*c.Path) {
				continue
			}
			anchors = append(anchors, map[string]any{"type": "file", "path": *c.Path})
		}
	}
	return anchors, hunks, len(hunks) > 0
}

// knownPath reports whether a path is a file of the diff or of the head
// checkout.
func (s *stagedReview) knownPath(path string) bool {
	for _, h := range s.tiles.diff {
		if h.Path == path {
			return true
		}
	}
	if !filepath.IsLocal(path) {
		return false
	}
	info, err := os.Stat(filepath.Join(s.run.Directory, "head", path))
	return err == nil && info.Mode().IsRegular()
}

// codeInSource reports whether a code exhibit's lines are the code it cites:
// every line that is not blank or an elision (…, ...) is, whitespace
// collapsed, a line of a cited hunk's patch or of a cited file in the head
// checkout (a cited hunk's file included), save one line in ten.
func (s *stagedReview) codeInSource(content string, hunks []string, anchors []map[string]any) bool {
	source := map[string]bool{}
	add := func(line string) {
		if key := codeKey(line); key != "" {
			source[key] = true
		}
	}
	paths := map[string]bool{}
	for _, id := range hunks {
		h := s.tiles.diff[id]
		for _, line := range strings.Split(h.Patch, "\n") {
			if strings.HasPrefix(line, "@@") || line == "" {
				continue
			}
			add(line[1:])
		}
		paths[h.Path] = true
	}
	for _, anchor := range anchors {
		if path, ok := anchor["path"].(string); ok {
			paths[path] = true
		}
	}
	for path := range paths {
		if !filepath.IsLocal(path) {
			continue
		}
		f, err := os.Open(filepath.Join(s.run.Directory, "head", path))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for scanner.Scan() {
			add(scanner.Text())
		}
		_ = f.Close()
	}
	checked, missing := 0, 0
	for _, line := range strings.Split(content, "\n") {
		key := codeKey(line)
		if key == "" || elision.MatchString(key) {
			continue
		}
		checked++
		if !source[key] {
			missing++
		}
	}
	return checked > 0 && missing <= checked/10
}

// elision is a line that stands for code left out: an ellipsis, alone or
// in a comment.
var elision = regexp.MustCompile(`^(//|#|--|/\*|\*|;|<!--)?\s*(…|\.\.\.)\s*(\*/|-->)?$`)

// codeKey is a line of code compared with whitespace collapsed.
func codeKey(line string) string {
	return strings.Join(strings.Fields(line), " ")
}

// ExhibitText is an authored tile's prose for the duplication checks: its
// title and its text, table cells or code caption.
func ExhibitText(tile tiles.AuthoredTile) string {
	var data struct {
		Text    string     `json:"text"`
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
		Caption string     `json:"caption"`
		Content string     `json:"content"`
	}
	_ = json.Unmarshal(tile.Data, &data)
	parts := []string{tile.Title, data.Text, data.Caption, strings.Join(data.Columns, " ")}
	for _, row := range data.Rows {
		parts = append(parts, strings.Join(row, " "))
	}
	parts = append(parts, data.Content)
	return strings.Join(parts, " ")
}
