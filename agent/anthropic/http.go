package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
)

// Category names why a call failed. Each is a fixed word, recorded as the
// status of the run's error event, so a host may report it where the message
// may not be reported.
type Category string

const (
	// CategoryAuthentication is a missing or rejected key (HTTP 401 or 403).
	CategoryAuthentication Category = "authentication_error"
	// CategoryRateLimit is HTTP 429 after the retries.
	CategoryRateLimit Category = "rate_limit"
	// CategoryOverloaded is HTTP 529 after the retries.
	CategoryOverloaded Category = "provider_overloaded"
	// CategoryServer is another 5xx (or 408) after the retries.
	CategoryServer Category = "provider_error"
	// CategoryTransport is a connection that failed after the retries.
	CategoryTransport Category = "transport_error"
	// CategoryInvalidRequest is a request the API refused (400, 404, 413 and
	// the other 4xx statuses), such as an unknown model.
	CategoryInvalidRequest Category = "invalid_request"
	// CategoryMalformed is a response that does not follow the Messages API:
	// not JSON, too large, without usage or with an unknown stop reason.
	CategoryMalformed Category = "malformed_response"
	// CategoryRefusal is a response the model's safeguards declined.
	CategoryRefusal Category = "refusal"
	// CategoryContextWindow is a conversation the model could not continue.
	CategoryContextWindow Category = "context_window_exceeded"
	// CategoryNoDocument is a run that ended without submitting.
	CategoryNoDocument Category = "no_document"
)

// Error is a named failure of the backend. Its text never holds the API key
// or text the model wrote; the API's own message is kept, bounded, only for a
// request it refused.
type Error struct {
	Category Category
	// StatusCode is the HTTP status, zero when there was none.
	StatusCode int
	// Attempts is how many times the call was sent.
	Attempts int
	Detail   string
}

func (e *Error) Error() string {
	text := "anthropic: " + string(e.Category)
	if e.StatusCode != 0 {
		text += fmt.Sprintf(": HTTP %d", e.StatusCode)
	}
	if e.Detail != "" {
		text += ": " + e.Detail
	}
	if e.Attempts > 1 {
		text += fmt.Sprintf(" (after %d attempts)", e.Attempts)
	}
	return text
}

type apiResponse struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Model      string            `json:"model"`
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      *apiUsage         `json:"usage"`
}

// apiUsage is the Messages API's usage block. Its buckets are disjoint:
// input_tokens excludes the cache reads and writes.
type apiUsage struct {
	Input         *int64 `json:"input_tokens"`
	Output        *int64 `json:"output_tokens"`
	CacheRead     *int64 `json:"cache_read_input_tokens"`
	CacheCreate   *int64 `json:"cache_creation_input_tokens"`
	CacheCreation *struct {
		Hour *int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	Speed *string `json:"speed"`
}

// usage converts one call's report. The 1-hour part is the reported split;
// when the API reports none it is zero, because this backend only ever asks
// for the 5-minute TTL. The speed is the reported one, else standard, because
// this backend never asks for fast mode.
func (u *apiUsage) usage() (agent.Usage, bool) {
	if u == nil || u.Input == nil || u.Output == nil {
		return agent.Usage{}, false
	}
	value := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	usage := agent.Usage{Input: *u.Input, Output: *u.Output, CacheRead: value(u.CacheRead), CacheCreate: value(u.CacheCreate)}
	if usage.Input < 0 || usage.Output < 0 || usage.CacheRead < 0 || usage.CacheCreate < 0 {
		return agent.Usage{}, false
	}
	hour := int64(0)
	if u.CacheCreation != nil && u.CacheCreation.Hour != nil {
		hour = *u.CacheCreation.Hour
	}
	if hour >= 0 && hour <= usage.CacheCreate {
		usage.CacheCreate1h = &hour
	}
	speed := "standard"
	if u.Speed != nil {
		speed = *u.Speed
	}
	if speed == "standard" || speed == "fast" {
		usage.Speed = &speed
	}
	prompt := usage.Input + usage.CacheRead + usage.CacheCreate
	usage.MinPrompt, usage.MaxPrompt = &prompt, &prompt
	return usage, true
}

// send makes one model call, retrying a rate limit, an overload, a server
// error or a failed connection up to maxRetries times. Waits count against
// the context's deadline: a wait that would outlast it ends the call with the
// last failure instead. A response is charged to result: its call, tokens and
// usage. A request that may have reached the model without a usable answer
// marks the usage unreported.
func (b *Backend) send(ctx context.Context, endpoint agent.Endpoint, body []byte, result *agent.Result, record func(agent.Event)) (*apiResponse, error) {
	url := strings.TrimRight(endpoint.BaseURL, "/") + "/v1/messages"
	var last *Error
	for attempt := 0; ; attempt++ {
		var wrote bool
		trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { wrote = true }}
		req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, &Error{Category: CategoryInvalidRequest, Detail: "the API base URL is not usable"}
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-api-key", endpoint.Key)
		req.Header.Set("anthropic-version", APIVersion)
		var retryAfter time.Duration
		response, err := b.httpClient().Do(req)
		var raw []byte
		if err == nil {
			raw, err = io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
			_ = response.Body.Close()
		}
		if err != nil {
			if wrote {
				// The model may have run and billed this call.
				unreported(result, false)
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			last = &Error{Category: CategoryTransport, Attempts: attempt + 1, Detail: "the connection to the API failed"}
		} else {
			switch status := response.StatusCode; {
			case status == http.StatusOK:
				return b.accept(raw, result, record)
			case status == http.StatusTooManyRequests:
				last = &Error{Category: CategoryRateLimit, StatusCode: status, Attempts: attempt + 1, Detail: "rate limit"}
			case status == 529:
				last = &Error{Category: CategoryOverloaded, StatusCode: status, Attempts: attempt + 1, Detail: "overloaded"}
			case status == http.StatusRequestTimeout || status >= 500:
				last = &Error{Category: CategoryServer, StatusCode: status, Attempts: attempt + 1, Detail: apiErrorType(raw)}
			case status == http.StatusUnauthorized || status == http.StatusForbidden:
				return nil, &Error{Category: CategoryAuthentication, StatusCode: status, Attempts: attempt + 1, Detail: "the API rejected the key; check ANTHROPIC_API_KEY"}
			default:
				return nil, &Error{Category: CategoryInvalidRequest, StatusCode: status, Attempts: attempt + 1, Detail: apiErrorMessage(raw)}
			}
			retryAfter = parseRetryAfter(response.Header)
		}
		if attempt == maxRetries {
			return nil, last
		}
		wait := b.wait(attempt, retryAfter)
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < wait {
			last.Detail += "; waiting to retry would exceed the run's time budget"
			return nil, last
		}
		record(agent.Event{Kind: "retry", Status: string(last.Category)})
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
}

// accept parses a 200 response and charges it.
func (b *Backend) accept(raw []byte, result *agent.Result, record func(agent.Event)) (*apiResponse, error) {
	result.Calls++
	if len(raw) > maxResponseBytes {
		unreported(result, true)
		return nil, &Error{Category: CategoryMalformed, Detail: fmt.Sprintf("the response is over %d bytes", maxResponseBytes)}
	}
	var response apiResponse
	if err := json.Unmarshal(raw, &response); err != nil || response.Type != "message" {
		unreported(result, true)
		return nil, &Error{Category: CategoryMalformed, Detail: "the response is not a message"}
	}
	usage, ok := response.Usage.usage()
	if !ok {
		unreported(result, true)
		return nil, &Error{Category: CategoryMalformed, Detail: "the response has no usable usage report"}
	}
	charged := agent.ChargedTokens(int(usage.Input), int(usage.Output), int(usage.CacheCreate))
	result.Tokens += charged
	result.Model = response.Model
	result.ModelSource = "provider_reported"
	result.Usage.Add(usage)
	result.UsageAttempts = append(result.UsageAttempts, agent.UsageAttempt{ModelSource: "provider_reported", Model: response.Model, Usage: usage, Calls: 1, ChargedTokens: charged})
	record(agent.Event{Kind: "model_usage", ID: response.ID, Input: int(usage.Input), Output: int(usage.Output), CacheRead: int(usage.CacheRead), CacheCreate: int(usage.CacheCreate)})
	if response.Model == "" {
		return nil, &Error{Category: CategoryMalformed, Detail: "the response names no model"}
	}
	return &response, nil
}

// unreported records a call whose usage is unknown. counted says whether it
// is a call the model answered, which counts against the call budget.
func unreported(result *agent.Result, counted bool) {
	usage := agent.Usage{Unreported: true}
	result.Usage.Add(usage)
	attempt := agent.UsageAttempt{ModelSource: "provider_reported", Usage: usage, Failed: true}
	if counted {
		attempt.Calls = 1
	}
	result.UsageAttempts = append(result.UsageAttempts, attempt)
}

func (b *Backend) wait(attempt int, retryAfter time.Duration) time.Duration {
	if b.retryDelay != nil {
		return b.retryDelay(attempt, retryAfter)
	}
	if retryAfter > 0 {
		return min(retryAfter, maxRetryWait)
	}
	base := time.Duration(1<<min(attempt, 5)) * time.Second
	// Up to a quarter more, so parallel runs do not retry in step.
	return min(base+rand.N(base/4+1), maxRetryWait)
}

// parseRetryAfter reads retry-after in seconds or as an HTTP date; zero when
// absent or unreadable.
func parseRetryAfter(header http.Header) time.Duration {
	value := strings.TrimSpace(header.Get("retry-after"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if !(seconds > 0) {
			return 0
		}
		return min(time.Duration(min(seconds, maxRetryWait.Seconds())*float64(time.Second)), maxRetryWait)
	}
	if when, err := http.ParseTime(value); err == nil {
		return min(max(time.Until(when), 0), maxRetryWait)
	}
	return 0
}

type apiError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func apiErrorType(raw []byte) string {
	var e apiError
	if json.Unmarshal(raw, &e) != nil || e.Error.Type == "" {
		return "server error"
	}
	return boundedText(e.Error.Type, 64)
}

// apiErrorMessage is the API's explanation of a refused request, bounded; it
// names a parameter or a model, which a user needs to fix the request.
func apiErrorMessage(raw []byte) string {
	var e apiError
	if json.Unmarshal(raw, &e) != nil || e.Error.Type == "" {
		return "the API refused the request"
	}
	return boundedText(e.Error.Type+": "+e.Error.Message, 300)
}
