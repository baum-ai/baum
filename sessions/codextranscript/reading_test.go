package codextranscript

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/baum-ai/baum/sessions/traceprocessing"
)

// cutPoints are offsets in data a growing file can be read at: each line's
// start, its second byte and its middle.
func cutPoints(data []byte) []int {
	var cuts []int
	for start := 0; start < len(data); {
		end := bytes.IndexByte(data[start:], '\n')
		if end < 0 {
			end = len(data) - start
		}
		cuts = append(cuts, start, start+1, start+end/2)
		start += end + 1
	}
	return append(cuts, len(data))
}

func readInPieces(data []byte, cut int, label string) (*File, error) {
	r := NewReading(label)
	consumed, _, err := r.Continue(bytes.NewReader(data[:cut]))
	if err != nil {
		return nil, err
	}
	more, rest, err := r.Continue(bytes.NewReader(data[consumed:]))
	if err != nil {
		return nil, err
	}
	if sum := crc32.Checksum(data[:consumed+more], traceprocessing.Castagnoli); r.Sum() != sum {
		return nil, fmt.Errorf("cut at %d: Sum %08x, want the checksum of the %d bytes consumed, %08x", cut, r.Sum(), consumed+more, sum)
	}
	return r.File(rest)
}

// slimmed is a whole read's file as a Reading keeps it.
func slimmed(file *File) *File {
	out := *file
	out.Lines = make([]Line, len(file.Lines))
	for i, line := range file.Lines {
		out.Lines[i] = slim(line)
	}
	out.Meta = out.Lines[0].SessionMeta
	return &out
}

func TestReadingMatchesAWholeRead(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "store", "sessions", "*", "*", "*", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	more, err := filepath.Glob(filepath.Join("..", "codexsession", "testdata", "*", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, more...)
	more, err = filepath.Glob(filepath.Join("..", "codexsession", "testdata", "*", "sessions", "*", "*", "*", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, more...)
	if len(paths) < 10 {
		t.Fatalf("fixtures = %v", paths)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, content := range [][]byte{data, append(bytes.Clone(data), `{"timestamp":"2026-`...)} {
			whole, err := Read(bytes.NewReader(content), path)
			if err != nil {
				t.Fatal(err)
			}
			want := slimmed(whole)
			for _, cut := range cutPoints(content) {
				got, err := readInPieces(content, cut, path)
				if err != nil {
					t.Fatalf("%s cut at %d: %v", path, cut, err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s cut at %d differs from a whole read", path, cut)
				}
			}
		}
	}
}

// Whether the rollout is paginated is read from its first line, and holds for
// the lines read after it.
func TestReadingKeepsThePaginationOfTheFirstLine(t *testing.T) {
	meta := `{"timestamp":"2026-07-01T10:00:00.000Z","ordinal":0,"type":"session_meta","payload":{"id":"019f0000-0000-7000-8000-000000000009","history_mode":"paginated"}}`
	message := `{"timestamp":"2026-07-01T10:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`
	r := NewReading("rollout.jsonl")
	if _, _, err := r.Continue(strings.NewReader(meta + "\n")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Continue(strings.NewReader(message + "\n")); err == nil || !strings.Contains(err.Error(), "has no paginated ordinal") {
		t.Errorf("an unnumbered line after a paginated session_meta: %v", err)
	}
}
