package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
)

type replyCompleter struct{ calls int }

func (c *replyCompleter) Complete(context.Context, CompleteRequest) (Completion, error) {
	c.calls++
	return Completion{Document: []byte(`{}`)}, nil
}

type staticCompose struct{}

func (staticCompose) ComposeInstructions(context.Context, string) (Prompt, error) {
	return Prompt{Version: "v", Body: "arrange"}, nil
}

// A reply that cannot be masked is never read: the composition falls back.
func TestComposeRunRedactionFailureFallsBack(t *testing.T) {
	normalized, err := review.NormalizeStored(schema.Example)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := &replyCompleter{}
	out, err := ComposeRun(context.Background(), ComposeRequest{Completer: c, Prompts: staticCompose{}, Document: doc,
		redact: func(context.Context, *bundle.Redactor, []byte, *Outcome) ([]byte, error) {
			return nil, errors.New("masking failed")
		}})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Fallback || out.FallbackReason != FallbackRedaction || c.calls != 1 || out.Reply != nil {
		t.Fatalf("%v %q %d", out.Fallback, out.FallbackReason, c.calls)
	}
	if _, err := json.Marshal(out.Composition); err != nil {
		t.Fatal(err)
	}
}
