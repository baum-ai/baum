package githubapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func respond(status int, header http.Header, body []byte) http.RoundTripper {
	if header == nil {
		header = make(http.Header)
	}
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
	})
}

func TestRateLimitTransportPreservesResponsesItDoesNotClassify(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   []byte
	}{
		{name: "ordinary forbidden", status: http.StatusForbidden, body: []byte(`{"message":"Forbidden"}`)},
		{name: "large success", status: http.StatusOK, body: bytes.Repeat([]byte("x"), responseInspectionLimit+100)},
		{name: "application error", status: http.StatusOK, body: []byte(`{"errors":[{"type":"NOT_FOUND","message":"Could not resolve"}]}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := RateLimitTransport(respond(tc.status, nil, tc.body)).RoundTrip(&http.Request{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(body, tc.body) {
				t.Fatalf("body length = %d, want %d unchanged", len(body), len(tc.body))
			}
		})
	}
}

func TestRateLimitTransportClassifiesLimits(t *testing.T) {
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	for _, tc := range []struct {
		name   string
		status int
		header http.Header
		body   string
		resume time.Time
	}{
		{name: "too many requests", status: http.StatusTooManyRequests, header: http.Header{"Retry-After": {"30"}}},
		{name: "primary limit", status: http.StatusForbidden, header: http.Header{
			"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {strconv.FormatInt(reset.Unix(), 10)},
		}, resume: reset},
		{name: "secondary limit message", status: http.StatusForbidden, body: `{"message":"You have exceeded a secondary rate limit."}`},
		{name: "application rate limit", status: http.StatusOK, body: `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RateLimitTransport(respond(tc.status, tc.header, []byte(tc.body))).RoundTrip(&http.Request{})
			var limited *RateLimitError
			if !errors.As(err, &limited) {
				t.Fatalf("err = %v, want *RateLimitError", err)
			}
			if !tc.resume.IsZero() && !limited.ResumeAt.Equal(tc.resume) {
				t.Errorf("ResumeAt = %v, want %v", limited.ResumeAt, tc.resume)
			}
		})
	}
}

func TestRateLimitResumeAt(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	reset := func(d time.Duration) string { return strconv.FormatInt(now.Add(d).Unix(), 10) }
	for _, tc := range []struct {
		name   string
		header http.Header
		want   time.Time
	}{
		{name: "exhausted quota prefers the later signal", header: http.Header{
			"Retry-After": {"60"}, "X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {reset(10 * time.Second)},
		}, want: now.Add(time.Minute)},
		{name: "exhausted quota waits for the reset", header: http.Header{
			"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {reset(50 * time.Minute)},
		}, want: now.Add(50 * time.Minute)},
		{name: "secondary limit ignores the primary reset", header: http.Header{
			"Retry-After": {"60"}, "X-Ratelimit-Remaining": {"4000"}, "X-Ratelimit-Reset": {reset(50 * time.Minute)},
		}, want: now.Add(time.Minute)},
		{name: "secondary limit naming nothing waits a minute", header: http.Header{
			"X-Ratelimit-Remaining": {"4000"}, "X-Ratelimit-Reset": {reset(50 * time.Minute)},
		}, want: now.Add(time.Minute)},
		{name: "no headers waits a minute", header: http.Header{}, want: now.Add(time.Minute)},
		{name: "exhausted quota naming nothing", header: http.Header{"X-Ratelimit-Remaining": {"0"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rateLimitResumeAt(tc.header, now); !got.Equal(tc.want) {
				t.Errorf("resume = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBigIntDecodesStringsNumbersAndNull(t *testing.T) {
	var got struct{ A, B, C BigInt }
	if err := json.Unmarshal([]byte(`{"A":"9007199254740993","B":42,"C":null}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.A != 9007199254740993 || got.B != 42 || got.C != 0 {
		t.Errorf("decoded %+v", got)
	}
}
