package locallink

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/baumai/baum/redact"
	"github.com/baumai/baum/sessions/session"
)

// RedactGraph encodes a session graph as JSON and masks every secret and
// e-mail address in it with redact.Engine.RedactJSON: found over the whole
// encoded text, so a tool input's key and value are caught together, and in
// each string as decoded, so a line of a tool's output is read as a line. The
// result stays valid JSON. "baum sessions --dump" prints it indented.
func RedactGraph(ctx context.Context, engine *redact.Engine, graph *session.Graph, indent bool) ([]byte, error) {
	var (
		encoded []byte
		err     error
	)
	if indent {
		encoded, err = json.MarshalIndent(graph, "", "  ")
	} else {
		encoded, err = json.Marshal(graph)
	}
	if err != nil {
		return nil, fmt.Errorf("encoding session graph: %w", err)
	}
	redacted, _, err := engine.RedactJSON(ctx, encoded)
	return redacted, err
}
