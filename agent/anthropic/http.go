package anthropic

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync/atomic"
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

// Timeouts of one call, both within the run's own deadline: headers must
// arrive within defaultHeaderTimeout of sending, and once the stream has
// started, the gap between two reads must stay below defaultIdleTimeout. A
// healthy stream can be silent for minutes (a tool input the API buffers,
// thinking that is not displayed), so the idle limit is the SDKs' read
// timeout of ten minutes: only a connection that is really dead trips it.
// A call cut off mid-stream is billed with unknown usage, and the shared
// loop retries the run.
const (
	defaultHeaderTimeout = 2 * time.Minute
	defaultIdleTimeout   = 10 * time.Minute
)

// send makes one streamed model call. Before the API has answered, a rate
// limit, an overload, a server error, a failed connection or a missing
// response is retried up to maxRetries times; waits count against the
// context's deadline, and a wait that would outlast it ends the call with the
// last failure instead. Once a 200 response has started, the call is never
// sent again: a stream that breaks fails the call.
//
// Every request that was sent counts as a call against maxCalls, whether or
// not it was answered; a call without a complete usage report leaves the
// run's usage unreported, since the model may have run and billed it.
func (b *Backend) send(ctx context.Context, endpoint agent.Endpoint, body []byte, maxCalls int, result *agent.Result, record func(agent.Event)) (*apiResponse, error) {
	url := strings.TrimRight(endpoint.BaseURL, "/") + "/v1/messages"
	var last *Error
	for attempt := 0; ; attempt++ {
		if result.Calls >= maxCalls {
			return nil, errors.New(agent.GenerationBudgetExceededReason("model calls"))
		}
		callCtx, cancel := context.WithCancel(ctx)
		var stalled atomic.Bool
		watchdog := time.AfterFunc(cmp.Or(b.headerTimeout, defaultHeaderTimeout), func() {
			stalled.Store(true)
			cancel()
		})
		var wrote atomic.Bool
		trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) }}
		req, err := http.NewRequestWithContext(httptrace.WithClientTrace(callCtx, trace), http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			watchdog.Stop()
			cancel()
			return nil, &Error{Category: CategoryInvalidRequest, Detail: "the API base URL is not usable"}
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("accept", "text/event-stream")
		req.Header.Set("x-api-key", endpoint.Key)
		req.Header.Set("anthropic-version", APIVersion)
		response, err := b.httpClient().Do(req)
		if err == nil && response.StatusCode == http.StatusOK {
			idle := cmp.Or(b.idleTimeout, defaultIdleTimeout)
			watchdog.Reset(idle)
			message, streamErr := b.receive(response, func() { watchdog.Reset(idle) })
			watchdog.Stop()
			cancel()
			if streamErr != nil && ctx.Err() == nil && stalled.Load() {
				streamErr = &Error{Category: CategoryTransport, Detail: fmt.Sprintf("no stream event for %s; the connection was dropped", idle)}
			}
			return b.charge(ctx, message, streamErr, result, record)
		}
		var retryAfter time.Duration
		var raw []byte
		if err == nil {
			raw, _ = io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			retryAfter = parseRetryAfter(response.Header)
			// Every request the API answered is a call against the budget,
			// refused ones included; an error status bills no tokens, so the
			// usage stays known.
			result.Calls++
		}
		watchdog.Stop()
		cancel()
		if err != nil {
			if wrote.Load() {
				// The request reached the network: the model may have run it.
				result.Calls++
				unreported(result)
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			detail := "the connection to the API failed"
			if stalled.Load() {
				detail = fmt.Sprintf("no response within %s", cmp.Or(b.headerTimeout, defaultHeaderTimeout))
			}
			last = &Error{Category: CategoryTransport, Attempts: attempt + 1, Detail: detail}
		} else {
			switch status := response.StatusCode; {
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
		}
		if attempt == maxRetries {
			return nil, last
		}
		if result.Calls >= maxCalls {
			// No call is left to retry with: the budget, not the provider,
			// ends the run, and the last failure says why it was spent.
			return nil, fmt.Errorf("%s (the last call failed: %s)", agent.GenerationBudgetExceededReason("model calls"), last.Error())
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

// receive reads a 200 response's event stream; watch is told of every read.
func (b *Backend) receive(response *http.Response, watch func()) (streamed, error) {
	defer func() { _ = response.Body.Close() }()
	var out streamed
	if media := response.Header.Get("content-type"); !strings.HasPrefix(media, "text/event-stream") {
		return out, malformed("the response is not an event stream")
	}
	err := readStream(&countingReader{r: response.Body, watch: watch}, &out)
	return out, err
}

// charge counts a call that the API answered, complete or not, and returns
// its message or why there is none. Only a complete message with a usable
// usage report is charged its tokens; any other leaves the usage unreported.
func (b *Backend) charge(ctx context.Context, got streamed, streamErr error, result *agent.Result, record func(agent.Event)) (*apiResponse, error) {
	result.Calls++
	if got.started && got.message.Model != "" {
		result.Model, result.ModelSource = got.message.Model, "provider_reported"
	}
	if streamErr != nil || !got.complete {
		unreported(result)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		var named *Error
		var event *streamError
		var bad malformed
		switch {
		case errors.As(streamErr, &named):
			return nil, named
		case errors.As(streamErr, &event):
			return nil, streamFailure(event)
		case errors.As(streamErr, &bad):
			return nil, &Error{Category: CategoryMalformed, Detail: string(bad)}
		case errors.Is(streamErr, errTooLarge):
			return nil, &Error{Category: CategoryMalformed, Detail: fmt.Sprintf("the response is over %d bytes", maxResponseBytes)}
		case errors.Is(streamErr, errStreamEnded) && !got.started:
			return nil, &Error{Category: CategoryMalformed, Detail: "the response carried no message"}
		}
		return nil, &Error{Category: CategoryTransport, Detail: errStreamEnded.Error()}
	}
	response := got.message
	if response.Type != "message" {
		unreported(result)
		return nil, &Error{Category: CategoryMalformed, Detail: "the response is not a message"}
	}
	usage, ok := response.Usage.usage()
	if !ok {
		unreported(result)
		return nil, &Error{Category: CategoryMalformed, Detail: "the response has no usable usage report"}
	}
	charged := agent.ChargedTokens(int(usage.Input), int(usage.Output), int(usage.CacheCreate))
	result.Tokens += charged
	result.Usage.Add(usage)
	result.UsageAttempts = append(result.UsageAttempts, agent.UsageAttempt{ModelSource: "provider_reported", Model: response.Model, Usage: usage, Calls: 1, ChargedTokens: charged})
	record(agent.Event{Kind: "model_usage", ID: response.ID, Input: int(usage.Input), Output: int(usage.Output), CacheRead: int(usage.CacheRead), CacheCreate: int(usage.CacheCreate)})
	if response.Model == "" {
		return nil, &Error{Category: CategoryMalformed, Detail: "the response names no model"}
	}
	return &response, nil
}

// streamFailure names an error event that ended a stream. It is not retried
// here: the model may already have run.
func streamFailure(event *streamError) *Error {
	switch event.Type {
	case "overloaded_error":
		return &Error{Category: CategoryOverloaded, Detail: "overloaded during the response"}
	case "rate_limit_error":
		return &Error{Category: CategoryRateLimit, Detail: "rate limited during the response"}
	case "authentication_error", "permission_error":
		return &Error{Category: CategoryAuthentication, Detail: "the API rejected the key during the response"}
	case "invalid_request_error":
		return &Error{Category: CategoryInvalidRequest, Detail: boundedText("invalid_request_error: "+event.Message, 300)}
	}
	return &Error{Category: CategoryServer, Detail: boundedText(event.Type, 64) + " during the response"}
}

// unreported records a call whose usage is unknown.
func unreported(result *agent.Result) {
	usage := agent.Usage{Unreported: true}
	result.Usage.Add(usage)
	result.UsageAttempts = append(result.UsageAttempts, agent.UsageAttempt{ModelSource: "provider_reported", Usage: usage, Calls: 1, Failed: true})
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
