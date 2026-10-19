// Package githubapi holds GitHub API plumbing every host shares: the GraphQL
// transport that recognises primary and secondary rate limits, and the
// GraphQL scalars GitHub's schema adds. It makes no requests of its own.
package githubapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimitError is GitHub refusing a GraphQL request for a primary or
// secondary rate limit. ResumeAt is when GitHub said to try again, and zero
// when it did not say.
type RateLimitError struct {
	ResumeAt time.Time
}

func (e *RateLimitError) Error() string {
	return "github graphql rate limit exceeded"
}

// RateLimitTransport preserves rate-limit information that the GraphQL
// client otherwise reduces to a generic HTTP error: a limited response
// becomes a *RateLimitError. It inspects at most a small prefix and streams
// the remainder of larger successful responses unchanged.
func RateLimitTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return rateLimitTransport{base: base}
}

type rateLimitTransport struct {
	base http.RoundTripper
}

func (t rateLimitTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil || response == nil {
		return response, err
	}

	resumeAt := rateLimitResumeAt(response.Header, time.Now())
	switch response.StatusCode {
	case http.StatusTooManyRequests:
		_ = response.Body.Close()
		return nil, &RateLimitError{ResumeAt: resumeAt}
	case http.StatusForbidden:
		if rateLimitHeaders(response.Header) {
			_ = response.Body.Close()
			return nil, &RateLimitError{ResumeAt: resumeAt}
		}
		body, complete, readErr := inspectResponse(response)
		if readErr != nil {
			return nil, readErr
		}
		if complete && rateLimitBody(body) {
			_ = response.Body.Close()
			return nil, &RateLimitError{ResumeAt: resumeAt}
		}
	case http.StatusOK, http.StatusCreated:
		body, complete, readErr := inspectResponse(response)
		if readErr != nil {
			return nil, readErr
		}
		if complete && applicationRateLimit(body) {
			_ = response.Body.Close()
			return nil, &RateLimitError{ResumeAt: resumeAt}
		}
	}
	return response, nil
}

const responseInspectionLimit = 64 << 10

type replayReadCloser struct {
	io.Reader
	io.Closer
}

// inspectResponse makes a small response available for classification and
// restores it for the GraphQL client. A normal data response larger than
// the inspection limit is replayed as a stream without buffering the rest.
func inspectResponse(response *http.Response) ([]byte, bool, error) {
	original := response.Body
	prefix, err := io.ReadAll(io.LimitReader(original, responseInspectionLimit+1))
	if err != nil {
		_ = original.Close()
		return nil, false, fmt.Errorf("reading github graphql response: %w", err)
	}
	if len(prefix) > responseInspectionLimit {
		response.Body = replayReadCloser{
			Reader: io.MultiReader(bytes.NewReader(prefix), original),
			Closer: original,
		}
		return nil, false, nil
	}
	_ = original.Close()
	response.Body = io.NopCloser(bytes.NewReader(prefix))
	return prefix, true, nil
}

func rateLimitHeaders(header http.Header) bool {
	return header.Get("Retry-After") != "" || header.Get("X-RateLimit-Remaining") == "0"
}

func rateLimitBody(body []byte) bool {
	var payload struct {
		Message          string `json:"message"`
		DocumentationURL string `json:"documentation_url"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(payload.Message), "rate limit") ||
		strings.Contains(strings.ToLower(payload.DocumentationURL), "rate-limit")
}

func applicationRateLimit(body []byte) bool {
	var payload struct {
		Errors []struct {
			Type       string         `json:"type"`
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	for _, graphError := range payload.Errors {
		if strings.EqualFold(graphError.Type, "RATE_LIMITED") ||
			strings.EqualFold(fmt.Sprint(graphError.Extensions["type"]), "RATE_LIMITED") ||
			strings.EqualFold(fmt.Sprint(graphError.Extensions["code"]), "RATE_LIMITED") {
			return true
		}
	}
	return false
}

// secondaryLimitResume is how long a limit that names no time waits. A limit
// with quota left is a secondary one, which a short pause lifts.
const secondaryLimitResume = time.Minute

// rateLimitResumeAt is when GitHub says to try again. X-RateLimit-Reset is the
// primary window's reset and arrives on every response, so it counts only when
// X-RateLimit-Remaining is 0; a secondary limit waits Retry-After, or a minute
// when it names nothing.
func rateLimitResumeAt(header http.Header, now time.Time) time.Time {
	var resumeAt time.Time
	if seconds, err := strconv.ParseInt(header.Get("Retry-After"), 10, 64); err == nil {
		resumeAt = now.Add(time.Duration(seconds) * time.Second)
	} else if parsed, err := http.ParseTime(header.Get("Retry-After")); err == nil {
		resumeAt = parsed
	}
	exhausted := header.Get("X-RateLimit-Remaining") == "0"
	if exhausted {
		if unix, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			if reset := time.Unix(unix, 0); reset.After(resumeAt) {
				resumeAt = reset
			}
		}
	}
	if resumeAt.IsZero() && !exhausted {
		resumeAt = now.Add(secondaryLimitResume)
	}
	return resumeAt
}
