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
	var out []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		out = append(out, chunk...)
		if len(out) > maxJSONLLineBytes {
			return nil, fmt.Errorf("line exceeds %d bytes", maxJSONLLineBytes)
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err != nil:
			return bytes.TrimRight(out, "\r\n"), err
		default:
			return bytes.TrimRight(out, "\r\n"), nil
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
