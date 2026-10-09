package redact

import (
	"context"
	"errors"
)

// RedactJSON masks what RedactContext masks in data, a JSON text, but only
// within its string literals and never in an escape sequence. Secrets are
// still found over the whole text, so a key and its value in separate
// strings are caught together, but the quotes, backslashes and structure
// around them are left alone: the result is valid JSON of the same shape
// whenever data is. A mask over the whole text alone can turn a quote or an
// escape's backslash into a mask character and leave invalid JSON.
func (e *Engine) RedactJSON(ctx context.Context, data []byte) ([]byte, Summary, error) {
	masked, summary, err := e.RedactContext(ctx, string(data))
	if err != nil {
		return nil, Summary{}, err
	}
	if len(masked) != len(data) {
		return nil, Summary{}, errors.New("redact: the mask changed the length of the JSON text")
	}
	out := []byte(masked)
	inString := false
	for i := 0; i < len(data); i++ {
		b := data[i]
		switch {
		case !inString:
			out[i] = b
			inString = b == '"'
		case b == '"':
			inString = false
		case b == '\\':
			// \" \\ \/ \b \f \n \r \t, or \u and four hex digits.
			end := i + 2
			if i+1 < len(data) && data[i+1] == 'u' {
				end = i + 6
			}
			end = min(end, len(data))
			copy(out[i:end], data[i:end])
			i = end - 1
		}
		if b == '"' {
			out[i] = b
		}
	}
	return out, summary, nil
}
