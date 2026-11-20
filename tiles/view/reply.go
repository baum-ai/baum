package view

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// ReplyDiagram is a native tile as a model writes it: the fields of
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
	Rows    [][]Cell        `json:"rows,omitempty"`
}

type replyLane struct {
	Label *string         `json:"label"`
	From  *int            `json:"from"`
	Steps []replyFlowStep `json:"steps"`
}

type replyFlowStep struct {
	Title string  `json:"title"`
	Note  *string `json:"note"`
	More  *string `json:"more"`
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
	From   int     `json:"from"`
	To     int     `json:"to"`
	Text   string  `json:"text"`
	Tone   *string `json:"tone"`
	Reply  *bool   `json:"reply"`
	Change *string `json:"change"`
	Was    *string `json:"was"`
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// DiagramSchema is the JSON Schema of one native tile of a type (flow,
// columns, sequence or table) as a tile agent writes it, without its id:
// its title, caption, source hunk IDs and its type's own fields, every
// property required and absent values null, so it serves providers' strict
// structured outputs. Nil for any other type.
func DiagramSchema(kind string) []byte {
	if !slices.Contains(NativeTypes, kind) {
		return nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(diagramSchema(kind))); err != nil {
		panic("view: a diagram schema is not JSON: " + err.Error())
	}
	return compact.Bytes()
}

// diagramSchema is the schema of one reply diagram of a type, without its
// id.
func diagramSchema(kind string) string {
	nullable := func(limit int) string { return fmt.Sprintf(`{"type": ["string", "null"], "maxLength": %d}`, limit) }
	text := func(limit int) string {
		return fmt.Sprintf(`{"type": "string", "minLength": 1, "maxLength": %d}`, limit)
	}
	tone := `{"type": ["string", "null"], "enum": ["plain", "new", "good", "bad", "muted", "missing", null]}`
	source := fmt.Sprintf(`{"type": "array", "maxItems": %d, "items": {"type": "string", "pattern": "^h_[0-9a-f]{12}(-[0-9]+)?$"}}`, MaxSource)
	diagram := func(props, required string) string {
		return fmt.Sprintf(`{
          "type": "object",
          "additionalProperties": false,
          "required": ["type", "title", "caption", "source", %s],
          "properties": {
            "type": {"type": "string", "enum": [%q]},
            "title": %s,
            "caption": %s,
            "source": %s,
            %s
          }
        }`, required, kind, text(MaxTitle), nullable(MaxCaption), source, props)
	}
	moreRequired, moreProperty := `, "more"`, fmt.Sprintf(`, "more": %s`, nullable(MaxMore))
	switch kind {
	case TypeFlow:
		return diagram(fmt.Sprintf(`"lanes": {"type": "array", "minItems": 1, "maxItems": %d, "items": {
              "type": "object", "additionalProperties": false, "required": ["label", "from", "steps"],
              "properties": {
                "label": %s,
                "from": {"type": ["integer", "null"], "minimum": 1, "maximum": %d},
                "steps": {"type": "array", "minItems": 1, "maxItems": %d, "items": {
                  "type": "object", "additionalProperties": false, "required": ["title", "note"%s, "tone", "join"],
                  "properties": {"title": %s, "note": %s%s, "tone": %s, "join": {"type": ["string", "null"], "enum": ["arrow", "both", "none", null]}}
                }}
              }
            }}`, MaxLanes, nullable(MaxLaneLabel), MaxLaneSteps, MaxLaneSteps, moreRequired, text(MaxStepTitle), nullable(MaxNote), moreProperty, tone), `"lanes"`)
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
              "type": "object", "additionalProperties": false, "required": ["from", "to", "text", "tone", "reply", "change", "was"],
              "properties": {"from": {"type": "integer", "minimum": 0, "maximum": %d}, "to": {"type": "integer", "minimum": 0, "maximum": %d}, "text": %s, "tone": %s, "reply": {"type": ["boolean", "null"]},
                "change": {"type": ["string", "null"], "enum": ["added", "changed", null]}, "was": %s}
            }}`, MinActors, MaxActors, text(MaxActor), MaxSequence, MaxActors-1, MaxActors-1, text(MaxSequenceText), tone, nullable(MaxSequenceText)), `"actors", "steps"`)
	case TypeTable:
		return diagram(fmt.Sprintf(`"columns": {"type": "array", "minItems": %d, "maxItems": %d, "items": %s},
            "rows": {"type": "array", "minItems": 1, "maxItems": %d, "items": {"type": "array", "minItems": %d, "maxItems": %d, "items": {"anyOf": [{"type": "string", "maxLength": %d}, {
              "type": "object", "additionalProperties": false, "required": ["text", "was"],
              "properties": {"text": {"type": "string", "maxLength": %d}, "was": %s}
            }]}}}`,
			MinTableColumns, MaxTableColumns, text(MaxTableHeading), MaxTableRows, MinTableColumns, MaxTableColumns, MaxCell, MaxCell, nullable(MaxCell)), `"columns", "rows"`)
	}
	return ""
}

// CheckDiagram checks one native tile as a tile agent writes it (a
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
			if l.From != nil {
				lane.From = *l.From
			}
			for _, s := range l.Steps {
				lane.Steps = append(lane.Steps, FlowStep{Title: s.Title, Note: str(s.Note), More: str(s.More), Tone: str(s.Tone), Join: str(s.Join)})
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
			out.Steps = append(out.Steps, SequenceStep{From: s.From, To: s.To, Text: s.Text, Tone: str(s.Tone), Reply: s.Reply != nil && *s.Reply, Change: str(s.Change), Was: str(s.Was)})
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
