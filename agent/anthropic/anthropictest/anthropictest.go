// Package anthropictest serves Messages API responses as the event streams the
// anthropic backend reads, for stub servers in tests.
package anthropictest

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// WriteMessage writes a complete message, given as the JSON of a non-streamed
// Messages API response, as a 200 event stream: message_start with the usage
// and an output count of 1, each content block in two deltas, a ping,
// message_delta with the stop reason and the final output count, and
// message_stop. A message without usage streams none.
func WriteMessage(w http.ResponseWriter, message string) error {
	var m struct {
		ID         string            `json:"id"`
		Type       string            `json:"type"`
		Model      string            `json:"model"`
		Content    []json.RawMessage `json:"content"`
		StopReason json.RawMessage   `json:"stop_reason"`
		Usage      map[string]any    `json:"usage"`
	}
	if err := json.Unmarshal([]byte(message), &m); err != nil {
		return err
	}
	w.Header().Set("content-type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	send := func(event map[string]any) {
		encoded, _ := json.Marshal(event)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], encoded)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	start := map[string]any{"id": m.ID, "type": m.Type, "role": "assistant", "model": m.Model, "content": []any{}, "stop_reason": nil}
	var output any
	if m.Usage != nil {
		first := map[string]any{}
		for key, value := range m.Usage {
			first[key] = value
		}
		output = m.Usage["output_tokens"]
		if output != nil {
			first["output_tokens"] = 1
		}
		start["usage"] = first
	}
	send(map[string]any{"type": "message_start", "message": start})
	for i, raw := range m.Content {
		var block map[string]json.RawMessage
		if err := json.Unmarshal(raw, &block); err != nil {
			return err
		}
		var kind string
		_ = json.Unmarshal(block["type"], &kind)
		halves := func(s string) []string { return []string{s[:len(s)/2], s[len(s)/2:]} }
		var deltas []map[string]any
		opened := map[string]any{}
		for key, value := range block {
			opened[key] = value
		}
		switch kind {
		case "text":
			var text string
			_ = json.Unmarshal(block["text"], &text)
			opened["text"] = ""
			for _, part := range halves(text) {
				deltas = append(deltas, map[string]any{"type": "text_delta", "text": part})
			}
		case "tool_use":
			if input, ok := block["input"]; ok {
				opened["input"] = map[string]any{}
				for _, part := range halves(string(input)) {
					deltas = append(deltas, map[string]any{"type": "input_json_delta", "partial_json": part})
				}
			}
		case "thinking":
			var thinking, signature string
			_ = json.Unmarshal(block["thinking"], &thinking)
			_ = json.Unmarshal(block["signature"], &signature)
			opened["thinking"], opened["signature"] = "", ""
			if thinking != "" {
				deltas = append(deltas, map[string]any{"type": "thinking_delta", "thinking": thinking})
			}
			deltas = append(deltas, map[string]any{"type": "signature_delta", "signature": signature})
		}
		send(map[string]any{"type": "content_block_start", "index": i, "content_block": opened})
		for j, delta := range deltas {
			if i == 0 && j == 1 {
				send(map[string]any{"type": "ping"})
			}
			send(map[string]any{"type": "content_block_delta", "index": i, "delta": delta})
		}
		send(map[string]any{"type": "content_block_stop", "index": i})
	}
	final := map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": m.StopReason}}
	if output != nil {
		final["usage"] = map[string]any{"output_tokens": output}
	}
	send(final)
	send(map[string]any{"type": "message_stop"})
	return nil
}
