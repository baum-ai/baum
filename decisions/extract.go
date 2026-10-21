package decisions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
// prompt, and validates the reply. This package ships no prompt: the host
// supplies its own, so a private hosted prompt and a public one share the
// schema, parser and validator.
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
// transcript, the figure ExtractOptions.MaxInputTokens is compared with.
func (e *Extractor) EstimateTokens(transcript *Transcript) int64 {
	return EstimateTokens(e.system, transcript.Text())
}

// Request is the request Extract sends for user, a rendered transcript's
// text.
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
// would send for transcript, for reserving its cost before the call.
// Byte-level BPE tokenizers never produce more tokens than the text has
// bytes, so it counts one token per byte of the system prompt, the transcript
// and the marshalled schema, plus FramingTokens. It is about four times
// EstimateTokens for English text, and holds for text with fewer bytes per
// token, such as CJK.
func (e *Extractor) MaxTokens(transcript *Transcript) int64 {
	schema, err := json.Marshal(ExtractSchema())
	if err != nil {
		panic(fmt.Sprintf("marshal extraction schema: %v", err))
	}
	return int64(len(e.system)) + int64(len(transcript.Text())) + int64(len(schema)) + FramingTokens
}

// Extract asks model for the decisions in transcript and validates their
// citations against it (ValidateCitations). A transcript with no turns has no
// decisions and does not call the model.
func (e *Extractor) Extract(ctx context.Context, model Model, transcript *Transcript, opts ExtractOptions) (Extraction, error) {
	if transcript == nil {
		return Extraction{}, errors.New("transcript is nil")
	}
	if len(transcript.Turns) == 0 {
		return Extraction{
			SessionDecisions: SessionDecisions{SessionID: transcript.SessionID, Decisions: []Decision{}},
			PromptVersion:    e.version,
		}, nil
	}
	user := transcript.Text()
	if estimate := EstimateTokens(e.system, user); opts.MaxInputTokens > 0 && estimate > opts.MaxInputTokens {
		return Extraction{}, fmt.Errorf("%w: about %d tokens, cap %d", ErrTranscriptTooLarge, estimate, opts.MaxInputTokens)
	}
	response, err := model.Complete(ctx, e.Request(user, opts.MaxOutputTokens))
	if err != nil {
		return Extraction{}, err
	}
	decisions, err := ParseReply(response.JSON)
	if err != nil {
		return Extraction{}, err
	}
	kept, dropped := ValidateCitations(decisions, transcript)
	return Extraction{
		SessionDecisions: SessionDecisions{SessionID: transcript.SessionID, Decisions: kept},
		PromptVersion:    e.version,
		Model:            response.Model,
		Usage:            response.Usage,
		Dropped:          dropped,
	}, nil
}
