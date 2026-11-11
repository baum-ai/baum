package claudetranscript

import (
	"bytes"
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

// readInPieces reads data as a transcript that had grown to cut when it was
// first read and to its full length when read again.
func readInPieces(t *testing.T, data []byte, cut int, name, agentID string) (*File, error) {
	t.Helper()
	r := NewReading(name, name, agentID)
	consumed, _, err := r.Continue(bytes.NewReader(data[:cut]))
	if err != nil {
		return nil, err
	}
	more, rest, err := r.Continue(bytes.NewReader(data[consumed:]))
	if err != nil {
		return nil, err
	}
	if sum := crc32.Checksum(data[:consumed+more], traceprocessing.Castagnoli); r.Sum() != sum {
		t.Fatalf("cut at %d: Sum %08x, want the checksum of the %d bytes consumed, %08x", cut, r.Sum(), consumed+more, sum)
	}
	return r.File(rest)
}

// withoutRaw is a whole read's file as a Reading keeps it.
func withoutRaw(file *File) *File {
	out := *file
	out.Lines = make([]Line, len(file.Lines))
	for i, line := range file.Lines {
		line.Raw = nil
		out.Lines[i] = line
	}
	return &out
}

func TestReadingMatchesAWholeRead(t *testing.T) {
	dir := filepath.Join("testdata", "11111111-2222-3333-4444-555555555555")
	for _, tt := range []struct{ path, agentID string }{
		{dir + ".jsonl", ""},
		{filepath.Join(dir, "subagents", "agent-a1.jsonl"), "a1"},
		{filepath.Join(dir, "subagents", "agent-a2.jsonl"), "a2"},
	} {
		data, err := os.ReadFile(tt.path)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(tt.path)
		// A torn final line, as a writer leaves it mid-write.
		for _, content := range [][]byte{data, append(bytes.Clone(data), `{"type":"user","uuid":"x`...)} {
			whole, err := decodeFile(bytes.NewReader(content), name, name, tt.agentID)
			if err != nil {
				t.Fatal(err)
			}
			want := withoutRaw(whole)
			for _, cut := range cutPoints(content) {
				got, err := readInPieces(t, content, cut, name, tt.agentID)
				if err != nil {
					t.Fatalf("%s cut at %d: %v", name, cut, err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s cut at %d differs from a whole read", name, cut)
				}
			}
		}
	}
}

// The ids a final line without a newline carries are checked but not kept:
// the next read decodes that line again.
func TestReadingChecksIDsAcrossReads(t *testing.T) {
	first := strings.Replace(envelope, `"uuid":"a"`, `"uuid":"one"`, 1)
	second := strings.Replace(envelope, `"uuid":"a"`, `"uuid":"two"`, 1)

	r := NewReading("t.jsonl", "t.jsonl", "")
	_, rest, err := r.Continue(strings.NewReader(first + "\n" + second))
	if err != nil {
		t.Fatal(err)
	}
	if file, err := r.File(rest); err != nil || len(file.Lines) != 2 {
		t.Fatalf("File = %v, %v; want both lines", file, err)
	}
	_, rest, err = r.Continue(strings.NewReader(second + "\n"))
	if err != nil || rest != nil {
		t.Fatalf("the final line read again: rest %q, err %v", rest, err)
	}
	if file, err := r.File(rest); err != nil || len(file.Lines) != 2 {
		t.Fatalf("File = %v, %v; want both lines", file, err)
	}

	// A uuid repeated in a later read is caught as in a whole read.
	if _, _, err := r.Continue(strings.NewReader(first + "\n")); err == nil || !strings.Contains(err.Error(), "repeats the uuid of line 1") {
		t.Errorf("a repeated uuid in a later read: %v", err)
	}
}
