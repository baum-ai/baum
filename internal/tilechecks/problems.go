package tilechecks

import (
	"strings"
	"unicode/utf8"
)

// The problem codes a result can hold. The daemon finds the first five
// before anything is drawn; the check runner reports the rest, except hang,
// which is the daemon's own.
const (
	CodeDefinitionInvalid  = "definition_invalid"
	CodeDataSchemaInvalid  = "data_schema_invalid"
	CodeSampleInvalid      = "sample_invalid"
	CodeDataInvalid        = "data_invalid"
	CodeLint               = "lint"
	CodeNoHello            = "no_hello"
	CodeTimeout            = "timeout"
	CodeHang               = "hang"
	CodeScriptError        = "script_error"
	CodeUnhandledRejection = "unhandled_rejection"
	CodeConsoleError       = "console_error"
	CodeRenderFailed       = "render_failed"
	CodeCSPViolation       = "csp_violation"
	CodeEmpty              = "empty"
	CodeOverflowGlance     = "overflow_glance"
	CodeOverflowX          = "overflow_x"
	CodeLayoutLoop         = "layout_loop"
	CodeUnresolvedCitation = "unresolved_citation"
	CodeSlow               = "slow"
)

// Hints are the fixed line each code's problems carry, as the render check
// section of the tile guide (oss/tiles/kit/GUIDE.md) writes them; a test
// keeps the two the same.
var Hints = map[string]string{
	CodeDefinitionInvalid:  "Fix the definition: the message names the field and the rule it breaks.",
	CodeDataSchemaInvalid:  "Keep the schema in the dialect of section 4: every object closed, every property required, every array and string bounded.",
	CodeSampleInvalid:      "Make the sample's data match the schema, or change the schema; the message names the place.",
	CodeDataInvalid:        "Make the data match the schema; the message names the place.",
	CodeLint:               "Follow the rules of section 2: no network, no external resources, no innerHTML, only the names the kit has.",
	CodeNoHello:            "The frame never started: the HTML did not parse, or a script looped before the kit loaded. Check the markup and the first script.",
	CodeTimeout:            "Draw synchronously, or return a promise that settles within 3 seconds.",
	CodeHang:               "The component never finished drawing and was stopped: look for an endless loop, and change the code before checking again.",
	CodeScriptError:        "Your script threw at the line given; a frequent cause is a value placed where baum.html refuses it.",
	CodeUnhandledRejection: "A promise was rejected and nothing caught it: await it inside the render, or catch it.",
	CodeConsoleError:       "Remove the console.error call, or fix what it reports.",
	CodeRenderFailed:       "The tile did not draw this data: call baum.tile once, and draw what the schema allows instead of calling baum.fail.",
	CodeCSPViolation:       "The tile has only its data: remove the request or external resource the frame's policy blocked.",
	CodeEmpty:              "The face drew nothing visible: draw the data, or call baum.empty(text) when there is nothing to show.",
	CodeOverflowGlance:     "Show fewer rows for that size and say \"N more\" in the foot; put long text in kit-truncate.",
	CodeOverflowX:          "Something from the data does not wrap: add kit-truncate or kit-break, or put the table in kit-scroll.",
	CodeLayoutLoop:         "The detail's height follows the frame's own height: remove vh, vw and percentage heights.",
	CodeUnresolvedCitation: "Cite only what the review holds: a sample's cited hunks must be in its hunks, with lines inside the hunk's range.",
	CodeSlow:               "Drawing took over 500 ms: draw less up front, and put long lists in kit-details.",
}

// The kinds of sample a check draws.
const (
	KindData    = "data"
	KindSample  = "sample"
	KindMinimal = "minimal"
	KindMaximal = "maximal"
)

// Problem is one thing a check found. Sample names the sample it was found
// with ("data" for the pull request's own data), and Face, Size and Theme
// the run, when it was found while drawing.
type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
	Sample  string `json:"sample,omitempty"`
	Face    string `json:"face,omitempty"`
	Size    string `json:"size,omitempty"`
	Theme   string `json:"theme,omitempty"`
	Line    *int   `json:"line,omitempty"`
	Column  *int   `json:"column,omitempty"`
}

// Run is one frame the runner drew: its sample, face, size and theme, the
// box it had, how long it took to be ready and what it drew.
type Run struct {
	Sample        string `json:"sample"`
	Face          string `json:"face"`
	Size          string `json:"size"`
	Theme         string `json:"theme"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	ReadyMS       int    `json:"ready_ms"`
	ContentHeight int    `json:"content_height"`
	TextChars     int    `json:"text_chars"`
}

// The limits of what a runner's report may hold.
const (
	maxProblems = 200
	maxRuns     = 100
	maxMessage  = 500
	maxField    = 64
)

func problem(code, message string) Problem {
	return Problem{Code: code, Message: clip(message, maxMessage), Hint: Hints[code]}
}

func clip(text string, most int) string {
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}
	if len(text) <= most {
		return text
	}
	cut := most - len("…")
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// classify sorts the problems a runner reported into errors and warnings by
// the rules of the render check, given each sample's kind: slow is a
// warning; an overflowing glance only warns for the maximal sample, whose
// card's fade covers it; an unresolved citation counts for the data and the
// author's samples only, since an edge sample's citations need not resolve;
// everything else is an error. Codes the check does not know are dropped,
// and each problem gets its code's hint.
func classify(reported []Problem, kinds map[string]string) (errors, warnings []Problem) {
	for _, p := range reported {
		hint, known := Hints[p.Code]
		if !known || p.Code == CodeHang {
			continue
		}
		p.Hint = hint
		p.Message = clip(p.Message, maxMessage)
		p.Sample, p.Face, p.Size, p.Theme = clip(p.Sample, maxField), clip(p.Face, maxField), clip(p.Size, maxField), clip(p.Theme, maxField)
		kind := kinds[p.Sample]
		switch {
		case p.Code == CodeSlow:
			warnings = append(warnings, p)
		case p.Code == CodeOverflowGlance && kind == KindMaximal:
			warnings = append(warnings, p)
		case p.Code == CodeUnresolvedCitation && (kind == KindMinimal || kind == KindMaximal):
		default:
			errors = append(errors, p)
		}
	}
	return errors, warnings
}

// dedupe keeps the first problem of each code, sample, face and size.
func dedupe(problems []Problem) []Problem {
	type key struct{ code, sample, face, size string }
	seen := map[key]bool{}
	out := problems[:0:0]
	for _, p := range problems {
		k := key{p.Code, p.Sample, p.Face, p.Size}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, p)
	}
	return out
}
