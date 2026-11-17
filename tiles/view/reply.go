package view

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Reply is what the view call writes (its model view): the composition's
// overview, placements and diagrams. Every property is required and absent
// values are explicit nulls in its schema, so the same schema serves
// providers' strict structured outputs. A diagram's source is hunk IDs,
// which the host makes anchors.
type Reply struct {
	Overview ReplyOverview  `json:"overview"`
	Sections []Placement    `json:"sections"`
	Diagrams []ReplyDiagram `json:"diagrams"`
}

// ReplyOverview is the overview as the view call writes it.
type ReplyOverview struct {
	RiskSummary *string       `json:"risk_summary"`
	Verdict     *ReplyVerdict `json:"verdict"`
	Screenshots []string      `json:"screenshots"`
	Diagram     *string       `json:"diagram"`
	Intent      []Intent      `json:"intent"`
}

// ReplyVerdict is a verdict as the view call writes it.
type ReplyVerdict struct {
	Text   string       `json:"text"`
	Points []ReplyPoint `json:"points"`
}

// ReplyPoint is a verdict point as the view call writes it.
type ReplyPoint struct {
	Text    string  `json:"text"`
	Section *string `json:"section"`
}

// ReplyDiagram is a native tile as the view call writes it: the fields of
// its type are set, the others absent. Columns is a columns tile's columns
// (objects) or a table's headings (strings).
type ReplyDiagram struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Title   string          `json:"title"`
	Caption *string         `json:"caption"`
	Source  []string        `json:"source"`
	Lanes   []replyLane     `json:"lanes,omitempty"`
	Columns json.RawMessage `json:"columns,omitempty"`
	Actors  []string        `json:"actors,omitempty"`
	Steps   []replySeqStep  `json:"steps,omitempty"`
	Rows    [][]string      `json:"rows,omitempty"`
}

type replyLane struct {
	Label *string         `json:"label"`
	Steps []replyFlowStep `json:"steps"`
}

type replyFlowStep struct {
	Title string  `json:"title"`
	Note  *string `json:"note"`
	Tone  *string `json:"tone"`
	Join  *string `json:"join"`
}

type replyColumn struct {
	Label string      `json:"label"`
	Tone  *string     `json:"tone"`
	Items []replyItem `json:"items"`
}

type replyItem struct {
	Title string  `json:"title"`
	Note  *string `json:"note"`
}

type replySeqStep struct {
	From int     `json:"from"`
	To   int     `json:"to"`
	Text string  `json:"text"`
	Tone *string `json:"tone"`
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ReplySchema returns the reply's JSON Schema.
func ReplySchema() []byte { return []byte(replySchema) }

var replySchema = func() string {
	nullable := func(limit int) string { return fmt.Sprintf(`{"type": ["string", "null"], "maxLength": %d}`, limit) }
	text := func(limit int) string {
		return fmt.Sprintf(`{"type": "string", "minLength": 1, "maxLength": %d}`, limit)
	}
	flow, columns := diagramSchema(TypeFlow, true), diagramSchema(TypeColumns, true)
	sequence, table := diagramSchema(TypeSequence, true), diagramSchema(TypeTable, true)
	schema := fmt.Sprintf(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["overview", "sections", "diagrams"],
  "properties": {
    "overview": {
      "type": "object",
      "additionalProperties": false,
      "required": ["risk_summary", "verdict", "screenshots", "diagram", "intent"],
      "properties": {
        "risk_summary": %s,
        "verdict": {"anyOf": [
          {"type": "null"},
          {
            "type": "object",
            "additionalProperties": false,
            "required": ["text", "points"],
            "properties": {
              "text": %s,
              "points": {"type": "array", "minItems": 1, "maxItems": %d, "items": {
                "type": "object", "additionalProperties": false, "required": ["text", "section"],
                "properties": {"text": %s, "section": {"type": ["string", "null"]}}
              }}
            }
          }
        ]},
        "screenshots": {"type": "array", "maxItems": %d, "items": {"type": "string"}},
        "diagram": {"type": ["string", "null"]},
        "intent": {"type": "array", "maxItems": %d, "items": {
          "type": "object", "additionalProperties": false, "required": ["asked", "got", "session", "prompt"],
          "properties": {"asked": %s, "got": %s, "session": {"type": "string"}, "prompt": {"type": "integer", "minimum": 1}}
        }}
      }
    },
    "sections": {"type": "array", "items": {
      "type": "object", "additionalProperties": false, "required": ["id", "tiles"],
      "properties": {"id": {"type": "string"}, "tiles": {"type": "array", "maxItems": %d, "items": {"type": "string"}}}
    }},
    "diagrams": {"type": "array", "maxItems": %d, "items": {"anyOf": [%s, %s, %s, %s]}}
  }
}`, nullable(MaxRiskSummary), text(MaxVerdictText), MaxVerdictPoints, text(MaxPointText), MaxScreenshots, MaxIntent,
		text(MaxIntentText), text(MaxIntentText), MaxPlaced, MaxDiagrams, flow, columns, sequence, table)
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(schema)); err != nil {
		panic("view: the reply schema is not JSON: " + err.Error())
	}
	return compact.String()
}()

// DiagramSchema is the JSON Schema of one native tile of a type (flow,
// columns, sequence or table) as the view call writes it, without its id:
// its title, caption, source hunk IDs and its type's own fields, every
// property required and absent values null, so it serves providers' strict
// structured outputs. Nil for any other type.
func DiagramSchema(kind string) []byte {
	if !slices.Contains(NativeTypes, kind) {
		return nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(diagramSchema(kind, false))); err != nil {
		panic("view: a diagram schema is not JSON: " + err.Error())
	}
	return compact.Bytes()
}

// diagramSchema is the schema of one reply diagram of a type, with its id
// or without it.
func diagramSchema(kind string, withID bool) string {
	nullable := func(limit int) string { return fmt.Sprintf(`{"type": ["string", "null"], "maxLength": %d}`, limit) }
	text := func(limit int) string {
		return fmt.Sprintf(`{"type": "string", "minLength": 1, "maxLength": %d}`, limit)
	}
	tone := `{"type": ["string", "null"], "enum": ["plain", "new", "good", "bad", "muted", "missing", null]}`
	source := fmt.Sprintf(`{"type": "array", "maxItems": %d, "items": {"type": "string", "pattern": "^h_[0-9a-f]{12}(-[0-9]+)?$"}}`, MaxSource)
	diagram := func(props, required string) string {
		id, idRequired := "", ""
		if withID {
			id, idRequired = `"id": {"type": "string", "pattern": "^dg_[a-z0-9_]{1,60}$"},`, `"id", `
		}
		return fmt.Sprintf(`{
          "type": "object",
          "additionalProperties": false,
          "required": [%s"type", "title", "caption", "source", %s],
          "properties": {
            %s
            "type": {"type": "string", "enum": [%q]},
            "title": %s,
            "caption": %s,
            "source": %s,
            %s
          }
        }`, idRequired, required, id, kind, text(MaxTitle), nullable(MaxCaption), source, props)
	}
	switch kind {
	case TypeFlow:
		return diagram(fmt.Sprintf(`"lanes": {"type": "array", "minItems": 1, "maxItems": %d, "items": {
              "type": "object", "additionalProperties": false, "required": ["label", "steps"],
              "properties": {
                "label": %s,
                "steps": {"type": "array", "minItems": 1, "maxItems": %d, "items": {
                  "type": "object", "additionalProperties": false, "required": ["title", "note", "tone", "join"],
                  "properties": {"title": %s, "note": %s, "tone": %s, "join": {"type": ["string", "null"], "enum": ["arrow", "both", "none", null]}}
                }}
              }
            }}`, MaxLanes, nullable(MaxLaneLabel), MaxLaneSteps, text(MaxStepTitle), nullable(MaxNote), tone), `"lanes"`)
	case TypeColumns:
		return diagram(fmt.Sprintf(`"columns": {"type": "array", "minItems": %d, "maxItems": %d, "items": {
              "type": "object", "additionalProperties": false, "required": ["label", "tone", "items"],
              "properties": {
                "label": %s, "tone": %s,
                "items": {"type": "array", "minItems": 1, "maxItems": %d, "items": {
                  "type": "object", "additionalProperties": false, "required": ["title", "note"],
                  "properties": {"title": %s, "note": %s}
                }}
              }
            }}`, MinColumns, MaxColumns, text(MaxColumnLabel), tone, MaxColumnItems, text(MaxItemTitle), nullable(MaxNote)), `"columns"`)
	case TypeSequence:
		return diagram(fmt.Sprintf(`"actors": {"type": "array", "minItems": %d, "maxItems": %d, "items": %s},
            "steps": {"type": "array", "minItems": 1, "maxItems": %d, "items": {
              "type": "object", "additionalProperties": false, "required": ["from", "to", "text", "tone"],
              "properties": {"from": {"type": "integer", "minimum": 0, "maximum": %d}, "to": {"type": "integer", "minimum": 0, "maximum": %d}, "text": %s, "tone": %s}
            }}`, MinActors, MaxActors, text(MaxActor), MaxSequence, MaxActors-1, MaxActors-1, text(MaxSequenceText), tone), `"actors", "steps"`)
	case TypeTable:
		return diagram(fmt.Sprintf(`"columns": {"type": "array", "minItems": %d, "maxItems": %d, "items": %s},
            "rows": {"type": "array", "minItems": 1, "maxItems": %d, "items": {"type": "array", "minItems": %d, "maxItems": %d, "items": {"type": "string", "maxLength": %d}}}`,
			MinTableColumns, MaxTableColumns, text(MaxTableHeading), MaxTableRows, MinTableColumns, MaxTableColumns, MaxCell), `"columns", "rows"`)
	}
	return ""
}

// CheckDiagram checks one native tile as the view call would write it (a
// ReplyDiagram, its ID ignored) against the hunks of the pull request, as
// Normalize checks a composition's diagrams: it returns the tile's title and
// its data in its type's stored shape (FlowData, ColumnsData, SequenceData
// or TableData), cleaned and cut, with its anchors kept to hunks hasHunk
// knows, and the fixes made; or the problem that drops it, in the host's
// words.
func CheckDiagram(d ReplyDiagram, hasHunk func(string) bool) (title string, data json.RawMessage, report Report, problem string) {
	encoded, ok := diagramData(d)
	if !ok {
		return "", nil, report, "it does not have the properties of its type"
	}
	raw, err := json.Marshal(encoded)
	if err != nil || encoded == nil {
		return "", nil, report, fmt.Sprintf("its type is not one of %s", strings.Join(NativeTypes, ", "))
	}
	hunks := map[string]bool{}
	for _, id := range d.Source {
		if hasHunk != nil && hasHunk(id) {
			hunks[id] = true
		}
	}
	check := &nativeCheck{review: &Review{hunks: hunks}, report: &report}
	checked, ok := check.check(NativeTile{ID: "dg_tile", Type: d.Type, Title: d.Title, Data: raw})
	if !ok {
		return "", nil, report, check.problem
	}
	return checked.Title, checked.Data, report, ""
}

// maxReplyProblems bounds the problems ParseReply reports.
const maxReplyProblems = 5

// ParseReply decodes a reply. It is lenient where the host's checks
// (Normalize) repair or drop what is wrong (limits, unknown IDs, enums):
// it fails only on a reply that is not JSON, not an object, or whose values
// do not have the schema's types, with up to five problems in the host's
// words (it never quotes the reply).
func ParseReply(raw []byte) (Reply, []string) {
	trimmed := bytes.TrimSpace(raw)
	if !json.Valid(trimmed) {
		return Reply{}, []string{"/: the reply is not valid JSON"}
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Reply{}, []string{"/: must be an object"}
	}
	var reply Reply
	if err := json.Unmarshal(trimmed, &reply); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			at := "/" + strings.ReplaceAll(typeErr.Field, ".", "/")
			return Reply{}, []string{fmt.Sprintf("%s: must not be a %s", at, typeErr.Value)}
		}
		return Reply{}, []string{"/: the reply does not decode as a reply"}
	}
	var problems []string
	for i, d := range reply.Diagrams {
		if len(problems) == maxReplyProblems {
			break
		}
		if _, ok := diagramData(d); !ok {
			problems = append(problems, fmt.Sprintf("/diagrams/%d: must have the properties of its type (flow: lanes; columns: columns of objects; sequence: actors and steps; table: columns of strings and rows)", i))
		}
	}
	if problems != nil {
		return Reply{}, problems
	}
	return reply, nil
}

// diagramData is a reply diagram's data in its type's stored shape; false
// when it does not have its type's fields. An unknown type gives no data
// and true: normalisation drops the tile.
func diagramData(d ReplyDiagram) (any, bool) {
	source := []Anchor{}
	for _, id := range d.Source {
		source = append(source, Anchor{Type: "hunk", ID: id})
	}
	caption := str(d.Caption)
	switch d.Type {
	case TypeFlow:
		if len(d.Lanes) == 0 {
			return nil, false
		}
		out := FlowData{Caption: caption, Source: source}
		for _, l := range d.Lanes {
			lane := Lane{Label: str(l.Label), Steps: []FlowStep{}}
			for _, s := range l.Steps {
				lane.Steps = append(lane.Steps, FlowStep{Title: s.Title, Note: str(s.Note), Tone: str(s.Tone), Join: str(s.Join)})
			}
			out.Lanes = append(out.Lanes, lane)
		}
		return out, true
	case TypeColumns:
		var columns []replyColumn
		if json.Unmarshal(d.Columns, &columns) != nil || len(columns) == 0 {
			return nil, false
		}
		out := ColumnsData{Caption: caption, Source: source}
		for _, c := range columns {
			column := Column{Label: c.Label, Tone: str(c.Tone), Items: []ColumnItem{}}
			for _, item := range c.Items {
				column.Items = append(column.Items, ColumnItem{Title: item.Title, Note: str(item.Note)})
			}
			out.Columns = append(out.Columns, column)
		}
		return out, true
	case TypeSequence:
		if len(d.Actors) == 0 || len(d.Steps) == 0 {
			return nil, false
		}
		out := SequenceData{Caption: caption, Source: source, Actors: d.Actors}
		for _, s := range d.Steps {
			out.Steps = append(out.Steps, SequenceStep{From: s.From, To: s.To, Text: s.Text, Tone: str(s.Tone)})
		}
		return out, true
	case TypeTable:
		var headings []string
		if json.Unmarshal(d.Columns, &headings) != nil || len(headings) == 0 || len(d.Rows) == 0 {
			return nil, false
		}
		return TableData{Caption: caption, Source: source, Columns: headings, Rows: d.Rows}, true
	}
	return nil, true
}

// Composition maps a reply onto a composition, before the host adds the
// library and authored tiles and normalises it (Normalize). A section past
// MaxPlaced tiles keeps its first ones; the rest are counted in the report
// and left for Place.
func (r Reply) Composition(report *Report) Composition {
	c := Composition{Schema: CompositionName, Sections: []Placement{}}
	o := r.Overview
	c.Overview = Overview{RiskSummary: str(o.RiskSummary), Screenshots: o.Screenshots, Diagram: o.Diagram, Intent: o.Intent}
	if v := o.Verdict; v != nil {
		verdict := &Verdict{Text: v.Text, Points: []Point{}}
		for _, p := range v.Points {
			verdict.Points = append(verdict.Points, Point{Text: p.Text, Section: str(p.Section)})
		}
		c.Overview.Verdict = verdict
	}
	for _, p := range r.Sections {
		tiles := p.Tiles
		if len(tiles) > MaxPlaced {
			report.fix(FixLimit, fmt.Sprintf("a section had more than %d tiles placed on it", MaxPlaced))
			tiles = tiles[:MaxPlaced]
		}
		c.Sections = append(c.Sections, Placement{ID: p.ID, Tiles: tiles})
	}
	for _, d := range r.Diagrams {
		data, _ := diagramData(d)
		encoded, err := json.Marshal(data)
		if err != nil || data == nil {
			encoded = json.RawMessage(`null`)
		}
		c.Diagrams = append(c.Diagrams, NativeTile{ID: d.ID, Type: d.Type, Title: d.Title, Data: encoded})
	}
	return c
}
