package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/compose"
	tilelibrary "github.com/baum-ai/baum/tiles/library"
)

// ComposePrompts supplies the instructions of the call that arranges a
// review's tiles (ComposeRun).
type ComposePrompts interface {
	// ComposeInstructions returns the composition prompt for a backend. Its
	// Version and Digest are recorded in the composition's generator.
	ComposeInstructions(ctx context.Context, backend string) (Prompt, error)
}

// ComposeRequest is one composition of a review's tiles.
type ComposeRequest struct {
	// Completer makes the call; nil falls back to the template at once.
	Completer Completer
	// Backend names the completer's backend, for the prompt.
	Backend string
	Prompts ComposePrompts
	// Document is the tile document tiles.FromReview converted from the
	// review.
	Document *tiles.Document
	// Fills are the library tiles the analysis selected, the ready ones with
	// their tiles (compose.FillOf); Authored are the authored tiles it wrote
	// that passed the host's checks. The composition holds both, whatever
	// the composer replies: a tile it leaves out gets a default place
	// (tiles.PlaceExtras). Previous is the previous version's stored
	// composition, nil without one; Preferences are the repository's.
	Fills       []compose.Fill
	Authored    []compose.Authored
	Previous    *tiles.Composition
	Preferences compose.Preferences
	// Saved are the definition digests of the person's saved tile types the
	// host offered the analysis (its tile library): their fills' tiles are
	// placed beside the built-in types'.
	Saved []string
	// Redactor masks secrets in the replies before they are read; nil uses
	// bundle.NewRedactor.
	Redactor *bundle.Redactor
	// CallTimeout bounds each call; zero leaves only the stage's deadline.
	CallTimeout time.Duration
	// Timeout bounds the whole stage when ctx has no deadline; zero is
	// DefaultComposeTimeout.
	Timeout time.Duration
	// MaxOutputTokens caps each reply; zero is DefaultComposeOutputTokens.
	MaxOutputTokens int
	// Now stamps the composition; nil is time.Now.
	Now func() time.Time
	// MaxCalls bounds the calls, the repair included: zero is two, and one
	// makes no repair, an invalid reply then falling back at once.
	MaxCalls int
	// Skip, when set, is why the host leaves the composer out, such as a run
	// whose budgets are spent (FallbackBudget): ComposeRun makes no call and
	// returns the template with this reason.
	Skip string

	// redact masks a reply; nil is RedactTaskOutput. Tests replace it.
	redact func(context.Context, *bundle.Redactor, []byte, *Outcome) ([]byte, error)
}

// DefaultComposeTimeout bounds the compose stage, its repair included, when
// the caller's context has no deadline. A composition call takes 3 to 10
// seconds; past this the template is as good as a late layout.
const DefaultComposeTimeout = 90 * time.Second

// ErrComposeInterrupted is returned when the caller's context ended before or
// during the stage. The outcome then counts what was spent but holds no
// composition, not even the fallback: a host must not store one for a run it
// stopped.
var ErrComposeInterrupted = errors.New("agent: the composition was interrupted")

// DefaultComposeOutputTokens caps a composition reply: a layout of a large
// review with a reason per lead is a few thousand tokens.
const DefaultComposeOutputTokens = 8000

// The reasons a composition fell back to the template, which its generator's
// problems record. They are the host's words and quote nothing a model wrote.
const (
	FallbackNoCompleter = "the backend cannot make a single structured call"
	FallbackPrompt      = "the composition prompt could not be loaded"
	FallbackInventory   = "the inventory could not be built"
	FallbackProvider    = "the composition call failed"
	FallbackInvalid     = "the composer's reply did not match its schema after one repair"
	FallbackRedaction   = "the composer's reply could not be checked for secrets"
	FallbackUnusable    = "the composer's layout could not be applied"
	// FallbackBudget is a host's reason to skip the call
	// (ComposeRequest.Skip).
	FallbackBudget = "the call or token budget left nothing to arrange the tiles"
)

// ComposeOutcome is what ComposeRun did. Its Outcome counts the calls, tokens,
// usage and repairs, as a stage's does.
type ComposeOutcome struct {
	Outcome
	// Composition is normalised, its generator set: the composer's, or the
	// template's when Fallback is set.
	Composition tiles.Composition
	// Report lists what normalising the composer's layout fixed; a fallback's
	// fixes are the template's own, normally none.
	Report tiles.Report
	// Inventory is the user message the composer read.
	Inventory string
	// Reply is the last reply, masked; nil when no call answered.
	Reply []byte
	// Fallback says the composition is the template; FallbackReason says why.
	Fallback       bool
	FallbackReason string
	// Repaired says a second call was made after an invalid reply.
	Repaired bool
}

// ComposeRun arranges a review's tiles with one structured call through a
// Completer, with no tools and no files: the composer reads an inventory of
// the converted document (compose.BuildInventory) and returns a layout
// (compose.Reply). A reply that is not valid JSON of the reply schema gets one
// repair call, which resends the inventory with the problems; problems the
// host can fix (unknown tiles, sizes, groups) are left to normalisation. The
// layout's IDs are stabilised against the previous composition, the
// repository's pins stamped, the result normalised, and the generator set
// with the prompt, the model, the template and the fixes by kind.
//
// Whatever fails (no completer, the prompt, the call, a reply invalid after
// the repair, redaction), the composition is the template of the change kind
// (tiles.Template), with Fallback set and the reason in the generator's
// problems. Every reply is masked for secrets before it is read, as a task's
// output is (RedactTaskOutput). A layout that normalises to an empty
// composition falls back too.
//
// Without a deadline on ctx the stage is bounded by Timeout
// (DefaultComposeTimeout); when that bound passes, the composition falls
// back. When ctx itself ends, before or during the stage, ComposeRun makes no
// further call and returns ErrComposeInterrupted with no composition. The
// only other error is for a document that takes no composition at all.
func ComposeRun(ctx context.Context, r ComposeRequest) (ComposeOutcome, error) {
	var out ComposeOutcome
	if r.Document == nil || r.Document.Origin.Kind != tiles.OriginConverted {
		return out, errors.New("agent: a composition applies only to a converted tile document")
	}
	parent := ctx
	interrupted := func() (ComposeOutcome, error) {
		return out, fmt.Errorf("%w: %w", ErrComposeInterrupted, parent.Err())
	}
	if parent.Err() != nil {
		return interrupted()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cmp.Or(r.Timeout, DefaultComposeTimeout))
		defer cancel()
	}
	now := r.Now
	if now == nil {
		now = time.Now
	}
	var prompt Prompt
	fallback := func(reason string) (ComposeOutcome, error) {
		return r.fallback(out, prompt, reason, now)
	}
	if r.Skip != "" {
		return fallback(r.Skip)
	}
	if r.Completer == nil {
		return fallback(FallbackNoCompleter)
	}
	if r.Prompts == nil {
		return fallback(FallbackPrompt)
	}
	var err error
	if prompt, err = r.Prompts.ComposeInstructions(ctx, r.Backend); err != nil {
		return fallback(FallbackPrompt)
	}
	inventory, err := compose.BuildInventory(r.Document, r.Fills, r.Previous, r.Preferences, r.Authored...)
	if err != nil {
		return fallback(FallbackInventory)
	}
	if out.Inventory, err = inventory.Message(); err != nil {
		return fallback(FallbackInventory)
	}
	redactor := r.Redactor
	if redactor == nil {
		if redactor, err = bundle.NewRedactor(); err != nil {
			return fallback(FallbackRedaction)
		}
	}

	request := CompleteRequest{System: prompt.Body, User: out.Inventory, Schema: compose.ReplySchema(),
		MaxOutputTokens: cmp.Or(r.MaxOutputTokens, DefaultComposeOutputTokens)}
	var reply compose.Reply
	for attempt := 0; ; attempt++ {
		raw, err := r.call(ctx, &out.Outcome, request, redactor)
		if parent.Err() != nil {
			return interrupted()
		}
		if err != nil {
			if errors.Is(err, errRedaction) {
				return fallback(FallbackRedaction)
			}
			return fallback(FallbackProvider)
		}
		out.Reply = raw
		var problems []string
		if reply, problems = compose.ParseReply(raw); problems == nil {
			break
		}
		if attempt == 1 || r.MaxCalls == 1 {
			out.Findings = strings.Join(problems, "\n")
			return fallback(FallbackInvalid)
		}
		out.Repaired = true
		out.ValidationRepairs++
		request.User = repairMessage(out.Inventory, raw, problems)
	}

	c := inventory.Composition(reply)
	c.Library, c.Authored = r.extras()
	tiles.PlaceExtras(&c, r.Document)
	c = tiles.StabilizeIDs(r.Previous, &c)
	pin(&c, r.Document, r.Preferences.Pins)
	if c.Empty() {
		return fallback(FallbackUnusable)
	}
	n, report := tiles.NormalizeComposition(r.Document, &c)
	if report.Counts()[tiles.FixUnusable] > 0 || n.Empty() {
		return fallback(FallbackUnusable)
	}
	out.Composition, out.Report = n, report
	if err := out.Composition.SetGenerator(r.generator(prompt, out.Model, report.Counts(), false, "", now)); err != nil {
		return fallback(FallbackUnusable)
	}
	return out, nil
}

// extras are the library tiles of the ready fills and the authored tiles.
func (r ComposeRequest) extras() ([]tiles.LibraryTile, []tiles.AuthoredTile) {
	var library []tiles.LibraryTile
	for _, f := range r.Fills {
		// Only a built-in library type's tile, or one of a saved type the
		// host offered, is placed: defence in depth behind the host that
		// made it.
		if f.Tile != nil && (builtinLibrary(f.Tile.Definition) || r.offered(f.Tile.Definition)) {
			library = append(library, *f.Tile)
		}
	}
	var authored []tiles.AuthoredTile
	for _, a := range r.Authored {
		authored = append(authored, a.Tile)
	}
	return library, authored
}

// offered reports whether a definition is one of the saved types the host
// offered (Saved): its digest, computed from its content, is one of them.
func (r ComposeRequest) offered(definition json.RawMessage) bool {
	if len(r.Saved) == 0 {
		return false
	}
	digest, err := tiles.DefinitionDigest(definition)
	return err == nil && slices.Contains(r.Saved, digest)
}

// builtinLibrary is library.IsBuiltin; tests replace it.
var builtinLibrary = tilelibrary.IsBuiltin

// errRedaction is a reply that could not be masked.
var errRedaction = errors.New("agent: masking the composer's reply failed")

// call makes one completion, adds what it spent to o and returns the reply,
// masked.
func (r ComposeRequest) call(ctx context.Context, o *Outcome, request CompleteRequest, redactor *bundle.Redactor) ([]byte, error) {
	return completeOnce(ctx, r.Completer, r.CallTimeout, r.redact, o, request, redactor)
}

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

// fallback sets the outcome's composition to the template of the change
// kind, with the reason.
func (r ComposeRequest) fallback(out ComposeOutcome, prompt Prompt, reason string, now func() time.Time) (ComposeOutcome, error) {
	library, authored := r.extras()
	n, report, err := TemplateCompositionWith(r.Document, r.Preferences.Pins, &tiles.Composition{Library: library, Authored: authored})
	if err != nil {
		return out, err
	}
	out.Composition, out.Report = n, report
	out.Fallback, out.FallbackReason = true, reason
	if err := out.Composition.SetGenerator(r.generator(prompt, out.Model, report.Counts(), true, reason, now)); err != nil {
		return out, err
	}
	return out, nil
}

// TemplateComposition is the layout a composition falls back to: the change
// kind's template (tiles.Template) with the repository's pins stamped,
// normalised, without a generator. A host lays out a draft with it while the
// composer has not answered, so a composer that keeps the template moves
// nothing on the page.
func TemplateComposition(doc *tiles.Document, pins []string) (tiles.Composition, tiles.Report, error) {
	return TemplateCompositionWith(doc, pins, nil)
}

// TemplateCompositionWith is TemplateComposition with a composition's tiles
// (library, promoted and authored; its layout is not used), which the
// template places after the leading tiles.
func TemplateCompositionWith(doc *tiles.Document, pins []string, content *tiles.Composition) (tiles.Composition, tiles.Report, error) {
	template, _ := tiles.Template(doc, content)
	template.Generator = nil // the host sets it
	pin(&template, doc, pins)
	n, report := tiles.NormalizeComposition(doc, &template)
	if report.Counts()[tiles.FixUnusable] > 0 {
		return n, report, fmt.Errorf("agent: the template does not apply: %s", strings.Join(report.Messages(), "; "))
	}
	return n, report, nil
}

func (r ComposeRequest) generator(prompt Prompt, model string, fixes map[tiles.FixKind]int, fallback bool, reason string, now func() time.Time) tiles.Generator {
	g := tiles.Generator{PromptVersion: prompt.Version, PromptDigest: prompt.Digest, Model: boundedBytes(model, 200),
		ComposedAt: now().UTC().Format(time.RFC3339), Template: tiles.TemplateName(changeKind(r.Document)), Fallback: fallback, Fixes: fixes}
	if reason != "" {
		g.Problems = []string{reason}
	}
	return g
}

// changeKind returns the change kind the overview tile holds.
func changeKind(doc *tiles.Document) string {
	for _, tile := range doc.Tiles {
		if tile.Type == tiles.TypeOverview {
			var data tiles.OverviewData
			_ = json.Unmarshal(tile.Data, &data)
			return data.ChangeKind
		}
	}
	return ""
}

// pin stamps the repository's pins on a composition before it is normalised:
// a pinned tile's entry is pinned and leaves its group, and a pinned tile the
// layout omits is added, pinned, at the front. Pins naming no tile of the
// document are ignored.
func pin(c *tiles.Composition, doc *tiles.Document, pins []string) {
	c.Layout.Entries = tiles.PinEntries(c.Layout.Entries, doc, pins)
}
