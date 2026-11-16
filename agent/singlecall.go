package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/tiles"
	tilelibrary "github.com/baum-ai/baum/tiles/library"
	"github.com/baum-ai/baum/tiles/view"
)

// The single structured calls (the view call) share how a call is made,
// masked and repaired.

// The reasons a single call fell back, beside the call's own, which its
// generator's problems record. They are the host's words and quote nothing a
// model wrote.
const (
	FallbackNoCompleter = "the backend cannot make a single structured call"
	// FallbackBudget is a host's reason to skip the call
	// (ViewRequest.Skip).
	FallbackBudget = "the call or token budget left nothing to arrange the tiles"
)

// extras are the library tiles of the ready fills, of built-in types or of
// saved types the host offered (saved, by digest), and the authored tiles.
func extras(fills []view.Fill, authored []view.Authored, saved []string) ([]tiles.LibraryTile, []tiles.AuthoredTile) {
	var library []tiles.LibraryTile
	for _, f := range fills {
		// Only a built-in library type's tile, or one of a saved type the
		// host offered, is placed: defence in depth behind the host that
		// made it.
		if f.Tile != nil && (builtinLibrary(f.Tile.Definition) || offered(saved, f.Tile.Definition)) {
			library = append(library, *f.Tile)
		}
	}
	var out []tiles.AuthoredTile
	for _, a := range authored {
		out = append(out, a.Tile)
	}
	return library, out
}

// offered reports whether a definition is one of the saved types the host
// offered: its digest, computed from its content, is one of them.
func offered(saved []string, definition json.RawMessage) bool {
	if len(saved) == 0 {
		return false
	}
	digest, err := tiles.DefinitionDigest(definition)
	return err == nil && slices.Contains(saved, digest)
}

// builtinLibrary is library.IsBuiltin; tests replace it.
var builtinLibrary = tilelibrary.IsBuiltin

// errRedaction is a reply that could not be masked.
var errRedaction = errors.New("agent: masking the reply failed")

// completeOnce makes one completion of a single structured call (the
// composition, the view call), adds what it spent to o and returns the
// reply, masked with redact (nil is RedactTaskOutput).
func completeOnce(ctx context.Context, completer Completer, callTimeout time.Duration, redact func(context.Context, *bundle.Redactor, []byte, *Outcome) ([]byte, error),
	o *Outcome, request CompleteRequest, redactor *bundle.Redactor) ([]byte, error) {
	callCtx := ctx
	if callTimeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, callTimeout)
		defer cancel()
	}
	completion, err := completer.Complete(callCtx, request)
	usage := completion.Usage
	if usage.IsZero() {
		usage.Unreported = true
	}
	tokens := ChargedTokens(int(usage.Input), int(usage.Output), int(usage.CacheCreate))
	o.Calls++
	o.ProviderAttempts++
	o.Tokens += tokens
	o.Usage.Add(usage)
	o.StructuredOutputRejections += completion.Rejections
	if completion.Model != "" {
		o.Model = completion.Model
	}
	if o.Accounting == nil {
		o.Accounting = &Accounting{}
	}
	o.Accounting.Usage.Add(usage)
	o.Accounting.ChargedTokens += tokens
	o.Accounting.Attempts = append(o.Accounting.Attempts, UsageAttempt{Model: completion.Model, Usage: usage, Calls: 1, ChargedTokens: tokens, Failed: err != nil})
	if err != nil {
		o.ErroredAttempts++
		return nil, err
	}
	// Masking the reply masks the outcome's model and accounting strings too.
	if redact == nil {
		redact = RedactTaskOutput
	}
	masked, err := redact(ctx, redactor, completion.Document, o)
	if err != nil {
		return nil, errRedaction
	}
	return masked, nil
}

// maxRepairEcho bounds the rejected reply a repair resends.
const maxRepairEcho = 16 << 10

// repairMessage is the user message of the repair call: the inventory again,
// the rejected reply as data and what was wrong with it.
func repairMessage(inventory string, rejected []byte, problems []string) string {
	var b strings.Builder
	b.WriteString(inventory)
	b.WriteString("\n\nYour previous reply was rejected. It did not match the reply schema:\n")
	for _, problem := range problems {
		b.WriteString("- " + problem + "\n")
	}
	b.WriteString("\nThe rejected reply, for reference:\n")
	b.WriteString(boundedBytes(string(rejected), maxRepairEcho))
	b.WriteString("\n\nReply again with the whole object, matching the schema exactly. This is the only repair.")
	return b.String()
}
