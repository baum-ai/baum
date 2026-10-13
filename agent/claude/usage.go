package claude

import "encoding/json"

// claudeUsage is one report of a model call's usage in Claude Code's stream.
type claudeUsage struct {
	Input       int `json:"input_tokens"`
	Output      int `json:"output_tokens"`
	CacheRead   int `json:"cache_read_input_tokens"`
	CacheCreate int `json:"cache_creation_input_tokens"`
	// CacheCreation is the TTL split and Speed the service speed. A value of
	// the wrong type reads as not reported, and never fails the event.
	CacheCreation cacheSplit    `json:"cache_creation"`
	Speed         lenientString `json:"speed"`
}

// cacheSplit is usage.cache_creation: Hour is ephemeral_1h_input_tokens, nil
// when absent or not an integer.
type cacheSplit struct{ Hour *int }

func (c *cacheSplit) UnmarshalJSON(b []byte) error {
	var v struct {
		Hour json.RawMessage `json:"ephemeral_1h_input_tokens"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var n int
	if json.Unmarshal(v.Hour, &n) == nil {
		c.Hour = &n
	}
	return nil
}

// lenientString is a string that reads as empty when it is anything else.
type lenientString string

func (l *lenientString) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*l = lenientString(s)
	}
	return nil
}

// Cache1h is the 1-hour part of the cache writes, nil when not reported.
func (u claudeUsage) Cache1h() *int { return u.CacheCreation.Hour }

const (
	speedStandard = "standard"
	speedFast     = "fast"
)

func pointerTo[T any](v T) *T { return &v }

func intOr(p *int, fallback int) int {
	if p == nil {
		return fallback
	}
	return *p
}
