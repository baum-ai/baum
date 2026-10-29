package anthropic

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/baum-ai/baum/agent"
)

// completeOutputTokens is a completion's reply cap when the request sets none.
const completeOutputTokens = 16384

const completeToolDescription = "Submit the finished reply, once. The reply's top-level fields are this call's arguments."

// Complete makes one Messages call that offers only the StructuredOutput tool,
// whose input schema is the request's (agent.Completer). There are no file
// tools, no nudge and no repair: a reply without a valid StructuredOutput call
// fails, with the usage it was billed. The current models refuse a forced
// tool_choice while they think, so the choice is auto, limited to one call,
// and the system prompt asks for the call. A rate limit, an overload or a
// server error before the API answered is retried as in Run.
func (b *Backend) Complete(ctx context.Context, request agent.CompleteRequest) (completion agent.Completion, err error) {
	completion.Model = b.model()
	key := b.APIKey
	if key == "" {
		return completion, &Error{Category: CategoryAuthentication, Detail: "no API key; set ANTHROPIC_API_KEY"}
	}
	defer func() {
		if err != nil {
			var named *Error
			if errors.As(err, &named) {
				named.Detail = scrub(named.Detail, key)
			}
		}
	}()
	var schema jsonschema.Schema
	if err := json.Unmarshal(request.Schema, &schema); err != nil {
		return completion, fmt.Errorf("anthropic: the output schema is not JSON Schema: %w", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return completion, fmt.Errorf("anthropic: the output schema does not resolve: %w", err)
	}
	tool, err := json.Marshal(struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}{structuredOutputTool, completeToolDescription, request.Schema})
	if err != nil {
		return completion, err
	}
	user, err := json.Marshal(textBlock{Type: "text", Text: request.User})
	if err != nil {
		return completion, err
	}
	body := requestBody{
		Stream:     true,
		Model:      b.model(),
		MaxTokens:  cmp.Or(request.MaxOutputTokens, completeOutputTokens),
		System:     []textBlock{{Type: "text", Text: request.System + "\n\nSubmit the reply by calling the StructuredOutput tool exactly once; never write it as text."}},
		Messages:   []message{{Role: "user", Content: []json.RawMessage{user}}},
		Tools:      []json.RawMessage{tool},
		ToolChoice: &toolChoice{Type: "auto", DisableParallelToolUse: true},
	}
	if effort := b.effort(); effort != "" {
		body.OutputConfig = &outputConfig{Effort: effort}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return completion, err
	}
	var result agent.Result
	endpoint := agent.Endpoint{BaseURL: cmp.Or(b.BaseURL, DefaultBaseURL), Key: key}
	// Refused requests count as calls; the retries need room for them.
	response, err := b.send(ctx, endpoint, encoded, maxRetries+1, &result, func(agent.Event) {})
	completion.Usage = result.Usage
	if result.Model != "" {
		completion.Model = result.Model
	}
	if err != nil {
		return completion, err
	}
	completion.Model = cmp.Or(response.Model, completion.Model)
	switch response.StopReason {
	case "end_turn", "tool_use", "stop_sequence":
	case "max_tokens":
		return completion, &Error{Category: CategoryMalformed, Detail: "the reply was cut off at the output token limit"}
	case "refusal":
		return completion, &Error{Category: CategoryRefusal, Detail: "the model declined the request (stop_reason refusal)"}
	default:
		return completion, &Error{Category: CategoryMalformed, Detail: "unexpected stop_reason " + stopStatus(response.StopReason)}
	}
	blocks, err := parseContent(response.Content)
	if err != nil {
		return completion, err
	}
	for _, block := range blocks {
		if block.Type != "tool_use" || block.Name != structuredOutputTool {
			continue
		}
		document, problem := submitted(block.Input, resolved)
		if problem != "" {
			return completion, &Error{Category: CategoryMalformed, Detail: boundedText(problem, 512)}
		}
		completion.Document = document
		return completion, nil
	}
	return completion, &Error{Category: CategoryNoDocument, Detail: "the reply has no StructuredOutput call"}
}
