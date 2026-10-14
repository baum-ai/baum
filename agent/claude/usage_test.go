//go:build unix

package claude

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baumai/baum/agent"
)

// Claude Code's stream reports each call's 1-hour cache writes and its speed.
// The run's usage keeps the 1-hour part (summed over calls) and a speed only
// when every call agrees; calls that report neither leave both nil.
func TestClaudeUsageCarriesCacheTTLAndSpeed(t *testing.T) {
	call := func(id, usage string) string {
		return `{"type":"assistant","message":{"id":"` + id + `","model":"fixture","usage":` + usage + `}}`
	}
	fast := func(h int) string {
		return `{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":10,"speed":"fast","cache_creation":{"ephemeral_1h_input_tokens":` +
			string(rune('0'+h)) + `,"ephemeral_5m_input_tokens":0}}`
	}
	result := `{"type":"result","subtype":"success","structured_output":{},"usage":{}}`
	for _, tc := range []struct {
		name   string
		events []string
		hour   *int64
		speed  *string
	}{
		{"every call fast with 1-hour writes", []string{call("m1", fast(4)), call("m2", fast(3)), result}, new(int64(7)), new("fast")},
		{"calls disagree on speed", []string{call("m1", fast(4)),
			call("m2", `{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":10,"speed":"standard","cache_creation":{"ephemeral_1h_input_tokens":3}}`), result}, new(int64(7)), nil},
		{"nothing reported", []string{call("m1", `{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":10}`), result}, nil, nil},
		{"a call that wrote to the cache without a split makes it unknown", []string{
			call("m1", `{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":10}`), call("m2", fast(5)), result}, nil, nil},
		{"a call with no usage decides neither", []string{call("m1", fast(4)), call("m2", `{"input_tokens":0,"output_tokens":0}`), result}, new(int64(4)), new("fast")},
		{"a larger result total without a split makes it unknown", []string{call("m1", fast(4)),
			`{"type":"result","subtype":"success","structured_output":{},"usage":{"cache_creation_input_tokens":20}}`}, nil, new("fast")},
		{"a larger result total with a split is used", []string{call("m1", fast(4)),
			`{"type":"result","subtype":"success","structured_output":{},"usage":{"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_1h_input_tokens":15}}}`}, new(int64(15)), new("fast")},
		{"wrong types are not reported and do not end the run", []string{
			call("m1", `{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":10,"speed":1,"cache_creation":{"ephemeral_1h_input_tokens":"x"}}`), result}, nil, nil},
		{"split above the total is dropped", []string{call("m1",
			`{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":2,"cache_creation":{"ephemeral_1h_input_tokens":9}}`), result}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "claude")
			if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\ncat <<'EVENTS'\n"+strings.Join(tc.events, "\n")+"\nEVENTS\n"), 0700); err != nil {
				t.Fatal(err)
			}
			got, err := (&Backend{Binary: script, Profile: Isolated, DefaultEndpoint: testEndpoint}).Run(context.Background(), agent.Request{Directory: t.TempDir(), Schema: []byte(`{}`), MaxCalls: 5, MaxTokens: 100000})
			if err != nil {
				t.Fatal(err)
			}
			if (got.Usage.CacheCreate1h == nil) != (tc.hour == nil) || tc.hour != nil && *got.Usage.CacheCreate1h != *tc.hour {
				t.Errorf("1-hour writes %v, want %v", got.Usage.CacheCreate1h, tc.hour)
			}
			if (got.Usage.Speed == nil) != (tc.speed == nil) || tc.speed != nil && *got.Usage.Speed != *tc.speed {
				t.Errorf("speed %v, want %v", got.Usage.Speed, tc.speed)
			}
		})
	}
}

// The run's per-call prompt range is known only when every call reported
// usage and the run's own total adds nothing the calls do not.
func TestClaudeUsageCarriesThePerCallPromptRange(t *testing.T) {
	call := func(id string, input, read int) string {
		return `{"type":"assistant","message":{"id":"` + id + `","model":"fixture","usage":{"input_tokens":` + fmt.Sprint(input) +
			`,"output_tokens":1,"cache_read_input_tokens":` + fmt.Sprint(read) + `}}}`
	}
	result := func(usage string) string {
		return `{"type":"result","subtype":"success","structured_output":{},"usage":` + usage + `}`
	}
	for _, tc := range []struct {
		name     string
		events   []string
		min, max *int64
	}{
		{"two calls", []string{call("m1", 10, 90), call("m2", 5, 20), result(`{}`)}, new(int64(25)), new(int64(100))},
		{"a call with no usage", []string{call("m1", 10, 90), call("m2", 0, 0), result(`{}`)}, nil, nil},
		{"a total above the calls", []string{call("m1", 10, 90), result(`{"input_tokens":50,"cache_read_input_tokens":90}`)}, nil, nil},
		{"a total equal to the calls", []string{call("m1", 10, 90), result(`{"input_tokens":10,"cache_read_input_tokens":90}`)}, new(int64(100)), new(int64(100))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := filepath.Join(t.TempDir(), "claude")
			if err := os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\ncat <<'EVENTS'\n"+strings.Join(tc.events, "\n")+"\nEVENTS\n"), 0700); err != nil {
				t.Fatal(err)
			}
			got, err := (&Backend{Binary: script, Profile: Isolated, DefaultEndpoint: testEndpoint}).Run(context.Background(), agent.Request{Directory: t.TempDir(), Schema: []byte(`{}`), MaxCalls: 5, MaxTokens: 100000})
			if err != nil {
				t.Fatal(err)
			}
			same := func(a, b *int64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
			if !same(got.Usage.MinPrompt, tc.min) || !same(got.Usage.MaxPrompt, tc.max) {
				t.Errorf("range %v..%v, want %v..%v", got.Usage.MinPrompt, got.Usage.MaxPrompt, tc.min, tc.max)
			}
		})
	}
}
