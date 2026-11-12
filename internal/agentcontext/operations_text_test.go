package agentcontext

import (
	"encoding/json"
	"strings"
	"testing"
)

// The characters a summary never shows: what plain drops (or turns into a
// space) from route text, and what checkAgentText refuses in the agent's.
var invisibles = map[string]string{
	"NUL":                         "\x00",
	"BEL":                         "\a",
	"ESC":                         "\x1b",
	"DEL":                         "\x7f",
	"C1 NEL":                      "\u0085",
	"C1 CSI":                      "\u009b",
	"soft hyphen":                 "\u00ad",
	"combining grapheme joiner":   "\u034f",
	"Hangul choseong filler":      "\u115f",
	"Hangul jungseong filler":     "\u1160",
	"Khmer inherent vowel":        "\u17b4",
	"Khmer inherent vowel aa":     "\u17b5",
	"Mongolian vowel separator":   "\u180e",
	"zero width space":            "\u200b",
	"zero width non-joiner":       "\u200c",
	"zero width joiner":           "\u200d",
	"left-to-right mark":          "\u200e",
	"right-to-left mark":          "\u200f",
	"line separator":              "\u2028",
	"paragraph separator":         "\u2029",
	"left-to-right embedding":     "\u202a",
	"right-to-left override":      "\u202e",
	"word joiner":                 "\u2060",
	"left-to-right isolate":       "\u2066",
	"pop directional isolate":     "\u2069",
	"Hangul filler":               "\u3164",
	"variation selector 16":       "\ufe0f",
	"zero width no-break space":   "\ufeff",
	"halfwidth Hangul filler":     "\uffa0",
	"interlinear annotation":      "\ufff9",
	"tag latin small a":           "\U000e0061",
	"cancel tag":                  "\U000e007f",
	"variation selector 17":       "\U000e0100",
	"invalid UTF-8":               "\xff",
	"invalid UTF-8 continuation":  "\x80",
	"carriage return (multiline)": "\r",
}

func TestPlainShowsNoInvisibleCharacters(t *testing.T) {
	for name, c := range invisibles {
		for _, multiline := range []bool{false, true} {
			got := plain("a"+c+"b", multiline)
			if got != "ab" && got != "a b" && !(multiline && got == "a\nb") {
				t.Errorf("%s (multiline %v): plain gives %q", name, multiline, got)
			}
		}
		if err := checkAgentText("message", "a"+c+"b", true); err == nil {
			t.Errorf("%s: agent text holding it was accepted", name)
		}
		if err := checkAgentText("name", "a"+c+"b", false); err == nil {
			t.Errorf("%s: a one-line agent text holding it was accepted", name)
		}
	}
	// Line breaks and tabs stay in multi-line text and leave one-line text;
	// visible text, accents and emoji are kept as they are.
	if got := plain("one\n\ttwo", true); got != "one\n\ttwo" {
		t.Errorf("multi-line text shows %q", got)
	}
	if got := plain("one\ntwo\tthree", false); got != "one two three" {
		t.Errorf("one-line text shows %q", got)
	}
	for _, visible := range []string{"Añadir búsqueda", "检索", "Search 🔍", "a-b_c.d"} {
		if got := plain(visible, false); got != visible {
			t.Errorf("%q shows as %q", visible, got)
		}
		if err := checkAgentText("message", visible, false); err != nil {
			t.Errorf("%q was refused: %v", visible, err)
		}
	}
	if err := checkAgentText("message", "line one\nline two\ttabbed", true); err != nil {
		t.Error(err)
	}
	if err := checkAgentText("name", "line one\nline two", false); err == nil {
		t.Error("a line break in a one-line agent text was accepted")
	}
}

// baum's sentence names a session, pull request or repository only by an
// identifier that passes its check; anything else is a quoted field.
func TestTheSentenceNamesOnlyCheckedIdentifiers(t *testing.T) {
	s := liveSession{Agent: "claude-code", SessionID: "s1 \u202eevil", Title: "T"}
	if got := s.label(); got != "an agent session" {
		t.Errorf("label %q", got)
	}
	fields := s.fields(nil)
	if len(fields) < 2 || fields[1].Label != "Session id" || !fields[1].Quoted || fields[1].Value != "s1 evil" {
		t.Errorf("fields %+v", fields)
	}
	if got := (liveSession{Agent: "claude-code", SessionID: "s1"}).label(); got != "claude-code session s1" {
		t.Errorf("label %q", got)
	}
	raw, _ := json.Marshal(map[string]any{"repository": "owner/repo; rm -rf", "number": 7, "title": "x"})
	e := reviewEntry{raw: map[string]json.RawMessage{"pull_request": raw}, Number: 7}
	if got := e.label(); got != "a pull request" {
		t.Errorf("label %q", got)
	}
	if f := e.fields(nil); len(f) == 0 || f[0].Label != "Pull request" || !f[0].Quoted {
		t.Errorf("fields %+v", f)
	}
	name, f := repositoryNamed(nil, "not a repo")
	if name != "a repository" || len(f) != 1 || !f[0].Quoted {
		t.Errorf("%q %+v", name, f)
	}
	label, f := jobLabel(map[string]json.RawMessage{"id": json.RawMessage(`"../x"`), "kind": json.RawMessage(`"surprise\u202e"`)})
	if strings.Contains(label, "..") || strings.Contains(label, "surprise") || len(f) != 2 {
		t.Errorf("%q %+v", label, f)
	}
}
