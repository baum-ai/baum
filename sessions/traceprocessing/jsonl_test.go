package traceprocessing

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

func decodeTestLine(raw []byte, ordinal int) (int, error) {
	if raw[0] != '{' {
		return 0, fmt.Errorf("line %d is not an object", ordinal)
	}
	var value map[string]any
	return ordinal, json.Unmarshal(raw, &value)
}

type failingReader bool

func (r *failingReader) Read(p []byte) (int, error) {
	if *r {
		return 0, errReaderFailure
	}
	*r = true
	return copy(p, "{}\n"), nil
}

var errReaderFailure = errors.New("reader failed")

func TestScanJSONLRecords(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		input                   io.Reader
		want                    []int
		torn, hard, readFailure bool
	}{
		{"no trailing newline", strings.NewReader("{}"), []int{1}, false, false, false},
		{"torn at EOF", strings.NewReader("{}\n{"), []int{1}, true, false, false},
		{"invalid before EOF", strings.NewReader("{}\n{\n"), nil, false, true, false},
		{"valid but wrong at EOF", strings.NewReader("{}\n[]"), nil, false, true, false},
		{"blank physical lines", strings.NewReader("\n{}\n \n{}\n\t"), []int{2, 4}, false, false, false},
		{"reader failure", new(failingReader), nil, false, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, torn, err := ScanJSONL(tt.input, decodeTestLine)
			if !slices.Equal(got, tt.want) || torn != tt.torn || (err != nil) != tt.hard || tt.readFailure && (!errors.Is(err, errReaderFailure) || !strings.Contains(err.Error(), "line 2")) {
				t.Fatalf("ScanJSONL() = %v, torn=%v, err=%v", got, torn, err)
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
func TestScanJSONLLineSizeLimit(t *testing.T) {
	lines, torn, err := ScanJSONL(io.LimitReader(zeroReader{}, maxJSONLLineBytes+1), decodeTestLine)
	if lines != nil || torn || err == nil || !strings.Contains(err.Error(), "line exceeds 67108864 bytes") {
		t.Fatalf("ScanJSONL() = %v, torn=%v, err=%v", lines, torn, err)
	}
}

// scanInPieces reads input as a file that grew to each length in cuts in
// turn: every read continues at the offset and ordinal the last stopped at,
// and only the last decodes its unterminated final line.
func scanInPieces(input string, cuts ...int) (lines []int, torn bool, err error) {
	var offset int64
	next := 1
	var rest []byte
	collect := func(raw []byte, ordinal int) error {
		line, err := decodeTestLine(raw, ordinal)
		lines = append(lines, line)
		return err
	}
	for _, cut := range append(cuts, len(input)) {
		consumed, after, unread, err := ScanWholeJSONL(strings.NewReader(input[offset:cut]), next, collect)
		if err != nil {
			return nil, false, err
		}
		offset, next, rest = offset+consumed, after, unread
	}
	line, ok, torn, err := DecodeFinalJSONL(rest, next, decodeTestLine)
	if err != nil {
		return nil, false, err
	}
	if ok {
		lines = append(lines, line)
	}
	return lines, torn, nil
}

// Reading a file in pieces, cut anywhere, gives what reading it whole gives.
func TestScanWholeJSONLResumesAnywhere(t *testing.T) {
	for _, input := range []string{
		"{}\n{}\n",
		"{}\n{}",
		"{}\n{",
		"\n{}\n \n{}\n\t",
		"{}\r\n{\"a\":1}\r\n",
		"{}\n{\n{}\n",
		"{}\n[]",
		"",
	} {
		want, wantTorn, wantErr := ScanJSONL(strings.NewReader(input), decodeTestLine)
		for first := 0; first <= len(input); first++ {
			for second := first; second <= len(input); second++ {
				got, torn, err := scanInPieces(input, first, second)
				if !slices.Equal(got, want) || torn != wantTorn || (err != nil) != (wantErr != nil) {
					t.Errorf("%q cut at %d and %d = %v, torn=%v, err=%v; whole = %v, torn=%v, err=%v",
						input, first, second, got, torn, err, want, wantTorn, wantErr)
				}
			}
		}
	}
}

func TestScanWholeJSONLLeavesTheUnterminatedLine(t *testing.T) {
	consumed, next, rest, err := ScanWholeJSONL(strings.NewReader("{}\n\n{\"a\""), 1, func([]byte, int) error { return nil })
	if err != nil || consumed != 4 || next != 3 || string(rest) != `{"a"` {
		t.Fatalf("ScanWholeJSONL() = consumed %d, next %d, rest %q, err %v", consumed, next, rest, err)
	}
	if _, _, rest, _ := ScanWholeJSONL(strings.NewReader("{}\n"), 1, func([]byte, int) error { return nil }); rest != nil {
		t.Errorf("a reader ending in a newline left %q", rest)
	}
}
