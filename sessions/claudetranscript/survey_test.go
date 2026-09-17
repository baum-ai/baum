package claudetranscript

import (
	"strings"
	"testing"
)

func surveyOf(t *testing.T, jsonl string) Shape {
	t.Helper()
	lines, _, err := Decode(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return Survey([]*File{{Lines: lines}})
}

// A line with no version is counted as unknown rather than failing: control lines
// never carry one.
func TestSurveyCountsVersionsAndTheirBounds(t *testing.T) {
	shape := surveyOf(t, strings.Join([]string{
		`{"type":"mode","mode":"plan","sessionId":"S"}`,
		`{"type":"user","uuid":"a","sessionId":"S","version":"2.1.9","timestamp":"2026-07-30T09:00:00.000Z","message":{"role":"user","content":"hi"}}`,
		`{"type":"user","uuid":"b","sessionId":"S","version":"2.1.10","timestamp":"2026-07-30T09:00:01.000Z","message":{"role":"user","content":"hi"}}`,
		`{"type":"user","uuid":"c","sessionId":"S","version":"2.1.10","timestamp":"2026-07-30T09:00:02.000Z","message":{"role":"user","content":"hi"}}`,
	}, "\n")+"\n")

	want := map[string]int{"unknown": 1, "2.1.9": 1, "2.1.10": 2}
	for version, count := range want {
		if got := shape.Versions.Seen[version]; got != count {
			t.Errorf("version %s counted %d times, want %d", version, got, count)
		}
	}
	if len(shape.Versions.Seen) != len(want) {
		t.Errorf("versions seen = %v, want %v", shape.Versions.Seen, want)
	}
	// Numeric ordering, which a string compare would get backwards.
	if shape.Versions.Min != "2.1.9" || shape.Versions.Max != "2.1.10" {
		t.Errorf("bounds = %s..%s, want 2.1.9..2.1.10", shape.Versions.Min, shape.Versions.Max)
	}
}

func TestSurveyReportsUnknownVersionsWhenNoLineCarriesOne(t *testing.T) {
	shape := surveyOf(t, `{"type":"mode","mode":"plan","sessionId":"S"}`+"\n")
	if shape.Versions.Min != "unknown" || shape.Versions.Max != "unknown" {
		t.Errorf("bounds = %s..%s, want unknown..unknown", shape.Versions.Min, shape.Versions.Max)
	}
}

func TestSurveyCountsContentShapes(t *testing.T) {
	shape := surveyOf(t, strings.Join([]string{
		`{"type":"user","uuid":"a","sessionId":"S","timestamp":"2026-07-30T09:00:00.000Z","message":{"role":"user","content":"typed"}}`,
		`{"type":"user","uuid":"b","sessionId":"S","timestamp":"2026-07-30T09:00:01.000Z","message":{"role":"user","content":[{"type":"text","text":"typed"}]}}`,
		`{"type":"user","uuid":"c","sessionId":"S","timestamp":"2026-07-30T09:00:02.000Z","message":{"role":"user","content":12}}`,
		`{"type":"user","uuid":"d","sessionId":"S","timestamp":"2026-07-30T09:00:03.000Z","message":{"role":"user"}}`,
		`{"type":"mode","mode":"plan","sessionId":"S"}`,
	}, "\n")+"\n")

	want := map[string]int{"string": 1, "array": 1, "other": 1, "absent": 1}
	for name, count := range want {
		if got := shape.Content[name]; got != count {
			t.Errorf("content shape %s counted %d times, want %d", name, got, count)
		}
	}
	// The control line has no message and must not be counted as one.
	if len(shape.Content) != len(want) {
		t.Errorf("content shapes = %v, want %v", shape.Content, want)
	}
}

// A field the decoder does not read is counted, so a format that grows one shows
// up as a number instead of going unnoticed.
func TestSurveyCountsUnreadTopLevelKeys(t *testing.T) {
	shape := surveyOf(t, strings.Join([]string{
		`{"type":"user","uuid":"a","sessionId":"S","timestamp":"2026-07-30T09:00:00.000Z","entrypoint":"cli",` +
			`"message":{"role":"user","content":"hi"},"somethingNew":{"nested":[1,{"deep":true}]}}`,
		`{"type":"mode","mode":"plan","sessionId":"S","entrypoint":"cli"}`,
	}, "\n")+"\n")

	want := map[string]int{"entrypoint": 2, "somethingNew": 1}
	for key, count := range want {
		if got := shape.UnreadKeys[key]; got != count {
			t.Errorf("unread key %s counted %d times, want %d", key, got, count)
		}
	}
	if len(shape.UnreadKeys) != len(want) {
		t.Errorf("unread keys = %v, want %v", shape.UnreadKeys, want)
	}
}
