package decisions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Model makes one structured-output call. Provider clients implement it.
type Model interface {
	Complete(ctx context.Context, req ModelRequest) (ModelResponse, error)
}

// ModelRequest is one call: a system prompt, one user message and the JSON
// Schema the reply must match.
type ModelRequest struct {
	System string
	User   string
	// SchemaName names Schema for providers that require one.
	SchemaName string
	// Schema is a JSON Schema object that satisfies OpenAI's strict structured
	// outputs: every property required, additionalProperties false.
	Schema map[string]any
	// MaxOutputTokens caps the reply. Zero means the provider client's default.
	MaxOutputTokens int64
}

// ModelResponse is the reply to a ModelRequest.
type ModelResponse struct {
	// JSON is the reply object, expected to match the request's Schema.
	JSON []byte
	// Model is the model the provider reports having used.
	Model string
	Usage Usage
}

// BilledError is a call the provider answered, and so billed, whose reply
// cannot be used: truncated, refused, failed or malformed. Model and Usage
// are what the provider reported for it.
type BilledError struct {
	Model string
	Usage Usage
	Err   error
}

func (e *BilledError) Error() string { return e.Err.Error() }

func (e *BilledError) Unwrap() error { return e.Err }

// Usage is the token usage of one call. InputTokens includes
// CachedInputTokens, as OpenAI reports it.
type Usage struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
}

// Extraction is the result for one session.
type Extraction struct {
	SessionDecisions
	// PromptVersion is the Extractor's PromptVersion.
	PromptVersion string    `json:"prompt_version"`
	Model         string    `json:"model"`
	Usage         Usage     `json:"usage"`
	Dropped       []Dropped `json:"dropped,omitempty"`
}

// ExtractOptions configures Extract.
type ExtractOptions struct {
	// MaxInputTokens caps the estimated prompt size; a larger transcript
	// returns ErrTranscriptTooLarge without calling the model. Zero means no cap.
	MaxInputTokens int64
	// MaxOutputTokens is passed to ModelRequest.MaxOutputTokens; zero means
	// the provider client's default.
	MaxOutputTokens int64
	// Existing and Cutoff extend an earlier extraction of the same session
	// instead of starting over, so its decisions and their IDs stay as they
	// were. Existing are its decisions, shown to the model by handle (d1, d2,
	// ...) and returned first; Cutoff is the last turn whose decisions they
	// cover, 0 for none. Either one set extends. A cutoff is usually the turn
	// before the earlier extraction's last turn (ExtensionCutoff), since an
	// open last turn keeps growing.
	Existing []Decision
	Cutoff   int
}

// ErrTranscriptTooLarge reports a transcript over ExtractOptions.MaxInputTokens.
var ErrTranscriptTooLarge = errors.New("transcript exceeds the input token cap")

// ExtractSchemaName names ExtractSchema in ModelRequest.SchemaName.
const ExtractSchemaName = "session_decisions"

// EstimateTokens is the prompt size Extract compares with
// ExtractOptions.MaxInputTokens: the bytes of the system prompt and the
// transcript divided by four, rounded up. It ignores the schema and message
// framing, and overestimates for long words; it only needs to be cheap and
// deterministic.
func EstimateTokens(system, user string) int64 {
	return (int64(len(system)) + int64(len(user)) + 3) / 4
}

// FramingTokens allows for what a request adds around its text: message and
// role framing, and the tool definition Anthropic wraps the schema in.
const FramingTokens = 1024

// Extractor asks a model for the decisions in a transcript with one system
// prompt, and validates the reply. The host supplies the prompt, so a private
// hosted prompt and the published one (NewPublicExtractor) share the schema,
// parser and validator.
type Extractor struct {
	system  string
	version string
}

// NewExtractor returns an Extractor that sends systemPrompt.
func NewExtractor(systemPrompt string) *Extractor {
	schema, err := json.Marshal(ExtractSchema())
	if err != nil {
		panic(fmt.Sprintf("marshal extraction schema: %v", err))
	}
	sum := sha256.Sum256(append([]byte(systemPrompt), schema...))
	return &Extractor{system: systemPrompt, version: hex.EncodeToString(sum[:])[:12]}
}

// PromptVersion identifies the prompt and schema: the first 12 hex characters
// of the SHA-256 of the system prompt followed by the marshalled
// ExtractSchema. It changes whenever either changes, so cached extractions can
// be keyed on it.
func (e *Extractor) PromptVersion() string { return e.version }

// SystemPrompt is the system prompt the Extractor sends.
func (e *Extractor) SystemPrompt() string { return e.system }

// EstimateTokens is EstimateTokens of the request Extract would send for
// transcript and opts, the figure ExtractOptions.MaxInputTokens is compared
// with.
func (e *Extractor) EstimateTokens(transcript *Transcript, opts ExtractOptions) int64 {
	return EstimateTokens(e.system, userMessage(transcript, opts))
}

// existingHeader starts the user message of an extension.
const existingHeader = "# Existing decisions\n"

// userMessage is the user message Extract sends: the transcript's text, or
// for an extension "# Existing decisions" with one line per existing decision
// by handle, then "# Transcript" and the text with a marker line before the
// first turn after a positive cutoff. Decision IDs never appear in it. A
// host's prompt explains the extension form to the model.
func userMessage(transcript *Transcript, opts ExtractOptions) string {
	if len(opts.Existing) == 0 && opts.Cutoff <= 0 {
		return transcript.Text()
	}
	var b strings.Builder
	b.WriteString(existingHeader)
	for i, decision := range opts.Existing {
		turns := make([]string, len(decision.Turns))
		for j, turn := range decision.Turns {
			turns[j] = strconv.Itoa(turn)
		}
		state := ""
		if decision.Superseded {
			state = "; superseded"
		}
		text := decision.Summary
		if decision.Title != "" {
			text = decision.Title + ": " + text
		}
		fmt.Fprintf(&b, "- %s (turns %s%s): %s\n", existingHandle(i), strings.Join(turns, ", "), state, strings.Join(strings.Fields(text), " "))
	}
	if len(opts.Existing) == 0 {
		b.WriteString("(none)\n")
	}
	b.WriteString("\n# Transcript\n\n")
	b.WriteString(transcript.text(opts.Cutoff))
	return b.String()
}

// existingHandle is the handle of the i-th existing decision in the user
// message: d1, d2, ...
func existingHandle(i int) string {
	return "d" + strconv.Itoa(i+1)
}

// Request is the request Extract sends for user, Extract's user message
// (userMessage): a rendered transcript's text, extended or not.
func (e *Extractor) Request(user string, maxOutputTokens int64) ModelRequest {
	return ModelRequest{
		System:          e.system,
		User:            user,
		SchemaName:      ExtractSchemaName,
		Schema:          ExtractSchema(),
		MaxOutputTokens: maxOutputTokens,
	}
}

// IsRequest reports whether req carries this Extractor's system prompt and
// the schema name, as Request builds it. A Model that sends only the
// transcript and lets the far side rebuild the request checks this first.
func (e *Extractor) IsRequest(req ModelRequest) bool {
	return req.System == e.system && req.SchemaName == ExtractSchemaName
}

// MaxTokens is an upper bound on the prompt tokens of the request Extract
// would send for transcript and opts, for reserving its cost before the call.
// Byte-level BPE tokenizers never produce more tokens than the text has
// bytes, so it counts one token per byte of the system prompt, the user
// message and the marshalled schema, plus FramingTokens. It is about four times
// EstimateTokens for English text, and holds for text with fewer bytes per
// token, such as CJK.
func (e *Extractor) MaxTokens(transcript *Transcript, opts ExtractOptions) int64 {
	return MaxRequestTokens(ModelRequest{System: e.system, User: userMessage(transcript, opts), Schema: ExtractSchema()})
}

// MaxRequestTokens is the same upper bound for any request: one token per
// byte of its system prompt, user message and marshalled schema, plus
// FramingTokens.
func MaxRequestTokens(req ModelRequest) int64 {
	schema, err := json.Marshal(req.Schema)
	if err != nil {
		panic(fmt.Sprintf("marshal request schema: %v", err))
	}
	return int64(len(req.System)) + int64(len(req.User)) + int64(len(schema)) + FramingTokens
}

// Extract asks model for the decisions in transcript and validates their
// citations against it (ValidateCitations). A transcript with no turns has no
// new decisions and does not call the model. An extension returns
// opts.Existing first, unchanged but for Superseded, then the new decisions,
// which have no IDs; a new decision none of whose turns is after opts.Cutoff
// is dropped.
func (e *Extractor) Extract(ctx context.Context, model Model, transcript *Transcript, opts ExtractOptions) (Extraction, error) {
	if transcript == nil {
		return Extraction{}, errors.New("transcript is nil")
	}
	if len(transcript.Turns) == 0 {
		return Extraction{
			SessionDecisions: SessionDecisions{SessionID: transcript.SessionID, Decisions: extended(opts.Existing, nil, []Decision{})},
			PromptVersion:    e.version,
		}, nil
	}
	user := userMessage(transcript, opts)
	if estimate := EstimateTokens(e.system, user); opts.MaxInputTokens > 0 && estimate > opts.MaxInputTokens {
		return Extraction{}, fmt.Errorf("%w: about %d tokens, cap %d", ErrTranscriptTooLarge, estimate, opts.MaxInputTokens)
	}
	response, err := model.Complete(ctx, e.Request(user, opts.MaxOutputTokens))
	if err != nil {
		return Extraction{}, err
	}
	decisions, superseded, err := ParseReply(response.JSON)
	if err != nil {
		return Extraction{}, err
	}
	kept, dropped := ValidateCitations(decisions, transcript, opts.Cutoff)
	return Extraction{
		SessionDecisions: SessionDecisions{SessionID: transcript.SessionID, Decisions: extended(opts.Existing, superseded, kept)},
		PromptVersion:    e.version,
		Model:            response.Model,
		Usage:            response.Usage,
		Dropped:          dropped,
	}, nil
}

// extended is existing, with Superseded set on those whose handle is in
// superseded, followed by found. A handle is read from its leading
// d<digits>, in any case; unknown ones are ignored. Without a new decision
// nothing replaces an existing one, so nothing is superseded.
func extended(existing []Decision, superseded []string, found []Decision) []Decision {
	if len(existing) == 0 {
		return found
	}
	decisions := slices.Clone(existing)
	for _, handle := range superseded {
		match := handlePattern.FindStringSubmatch(strings.ToLower(handle))
		if match == nil || len(found) == 0 {
			continue
		}
		if n, err := strconv.Atoi(match[1]); err == nil && n >= 1 && n <= len(decisions) {
			decisions[n-1].Superseded = true
		}
	}
	return append(decisions, found...)
}

// handlePattern is an existing decision's handle at the start of a reply's
// superseded_existing entry.
var handlePattern = regexp.MustCompile(`^d(\d+)`)
