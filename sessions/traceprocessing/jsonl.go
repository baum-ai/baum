package traceprocessing

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const maxJSONLLineBytes = 64 << 20

// ReadJSONLLine reads one bounded line, accumulating across the reader's buffer.
func ReadJSONLLine(reader *bufio.Reader) ([]byte, error) {
	line, _, err := readJSONLLine(reader)
	return line, err
}

// readJSONLLine is ReadJSONLLine that also returns the bytes the line took in
// the reader, its newline included.
func readJSONLLine(reader *bufio.Reader) ([]byte, int, error) {
	var out []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		out = append(out, chunk...)
		if len(out) > maxJSONLLineBytes {
			return nil, 0, fmt.Errorf("line exceeds %d bytes", maxJSONLLineBytes)
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err != nil:
			return bytes.TrimRight(out, "\r\n"), len(out), err
		default:
			return bytes.TrimRight(out, "\r\n"), len(out), nil
		}
	}
}

// ScanJSONL decodes every nonblank record while preserving physical ordinals.
func ScanJSONL[T any](r io.Reader, decode func([]byte, int) (T, error)) (lines []T, torn bool, err error) {
	reader := bufio.NewReaderSize(r, 1<<20)
	for ordinal := 1; ; ordinal++ {
		raw, readErr := ReadJSONLLine(reader)
		atEOF := errors.Is(readErr, io.EOF)
		if readErr != nil && !atEOF {
			return nil, false, fmt.Errorf("reading line %d: %w", ordinal, readErr)
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			if atEOF {
				return lines, false, nil
			}
			continue
		}
		line, decodeErr := decode(trimmed, ordinal)
		if decodeErr != nil {
			if atEOF && !json.Valid(trimmed) {
				return lines, true, nil
			}
			return nil, false, decodeErr
		}
		lines = append(lines, line)
		if atEOF {
			return lines, false, nil
		}
	}
}

// ScanWholeJSONL reads the lines of r that end in a newline and passes each
// nonblank one to decode, numbering physical lines from first. It is the
// resumable half of ScanJSONL: reading a file in pieces, each continuing at
// the byte offset and ordinal the last one stopped at, gives the same lines
// and ordinals as reading it whole.
//
// It stops at a final line without a newline, which a writer may still be
// writing. consumed is the bytes of the lines read, newlines included; next is
// the ordinal of the line after them; rest is that final line, nil when r
// ended with a newline. DecodeFinalJSONL decodes rest as ScanJSONL would.
func ScanWholeJSONL(r io.Reader, first int, decode func([]byte, int) error) (consumed int64, next int, rest []byte, err error) {
	reader := bufio.NewReaderSize(r, 1<<20)
	for ordinal := first; ; ordinal++ {
		raw, n, readErr := readJSONLLine(reader)
		if errors.Is(readErr, io.EOF) {
			if n == 0 {
				return consumed, ordinal, nil, nil
			}
			return consumed, ordinal, raw, nil
		}
		if readErr != nil {
			return 0, 0, nil, fmt.Errorf("reading line %d: %w", ordinal, readErr)
		}
		consumed += int64(n)
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			continue
		}
		if err := decode(trimmed, ordinal); err != nil {
			return 0, 0, nil, err
		}
	}
}

// DecodeFinalJSONL decodes the final line ScanWholeJSONL left unread as
// ScanJSONL decodes a file's last line: a blank one is no line (ok false), one
// that does not decode and is not valid JSON is torn, a partial write, and
// one that is valid JSON but does not decode is an error.
func DecodeFinalJSONL[T any](rest []byte, ordinal int, decode func([]byte, int) (T, error)) (line T, ok, torn bool, err error) {
	trimmed := bytes.TrimSpace(rest)
	if len(trimmed) == 0 {
		return line, false, false, nil
	}
	line, err = decode(trimmed, ordinal)
	if err != nil {
		var zero T
		if !json.Valid(trimmed) {
			return zero, false, true, nil
		}
		return zero, false, false, err
	}
	return line, true, false, nil
}
