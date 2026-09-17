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
