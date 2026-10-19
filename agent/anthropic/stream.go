package anthropic

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The Messages API streams a message as server-sent events: message_start
// (the message with empty content and its first usage), for each content
// block content_block_start, content_block_delta events and
// content_block_stop, then message_delta (the stop reason and the final
// usage) and message_stop. ping events keep the connection alive and an error
// event ends the stream with a failure.

// errStreamEnded is a stream that stopped before message_stop.
var errStreamEnded = errors.New("the response stream ended before the message was complete")

// streamError is an error event's payload.
type streamError struct {
	Type    string
	Message string
}

func (e *streamError) Error() string { return "stream error " + e.Type }

// streamed is what a stream delivered: the message once complete, and what
// was known before it broke (started says message_start arrived).
type streamed struct {
	message  apiResponse
	started  bool
	complete bool
}

type streamBlock struct {
	fields  map[string]json.RawMessage
	strings map[string]*strings.Builder
	input   *strings.Builder
	stopped bool
	encoded json.RawMessage
	// incomplete marks a block closed by the stop reason rather than by its
	// own content_block_stop, or whose streamed tool input is not JSON.
	incomplete bool
}

func (b *streamBlock) appendString(field, text string) {
	builder := b.strings[field]
	if builder == nil {
		builder = &strings.Builder{}
		var start string
		_ = json.Unmarshal(b.fields[field], &start)
		builder.WriteString(start)
		b.strings[field] = builder
	}
	builder.WriteString(text)
}

// encode is the finished block: its start fields with the streamed text and
// input in place. Tool input that is not JSON, as a response cut off at its
// output limit leaves it, becomes {} and marks the block incomplete; whether
// that is allowed is decided by the stop reason at the end of the message.
func (b *streamBlock) encode() json.RawMessage {
	for field, builder := range b.strings {
		encoded, _ := json.Marshal(builder.String())
		b.fields[field] = encoded
	}
	if b.input != nil {
		input := strings.TrimSpace(b.input.String())
		if input == "" {
			input = "{}"
		}
		if !json.Valid([]byte(input)) {
			input, b.incomplete = "{}", true
		}
		b.fields["input"] = json.RawMessage(input)
	}
	encoded, _ := json.Marshal(b.fields)
	return encoded
}

// cutShort reports a stop reason with which the model may leave its last
// block incomplete: the output limit, or safeguards declining mid-response.
func cutShort(reason string) bool { return reason == "max_tokens" || reason == "refusal" }

// finish completes the message from its blocks. A block left open or with
// tool input that is not JSON is accepted only when the stop reason says the
// response was cut short; the caller then handles that reason.
func finish(out *streamed, blocks []*streamBlock) error {
	cut := cutShort(out.message.StopReason)
	out.message.Content = nil
	for _, b := range blocks {
		if !b.stopped {
			if !cut {
				return malformed("the message stopped with a content block still open")
			}
			b.stopped, b.encoded, b.incomplete = true, b.encode(), true
		}
		if b.incomplete && !cut {
			return malformed("a tool call's streamed input is not JSON")
		}
		out.message.Content = append(out.message.Content, b.encoded)
	}
	out.complete = true
	return nil
}

// readStream reads a stream into a message. It stops at message_stop, at an
// error event, at the end of the stream and at the first event it cannot
// read; out holds what arrived either way.
func readStream(body io.Reader, out *streamed) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), maxDocumentBytes)
	var blocks []*streamBlock
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if data.Len() == 0 {
				continue
			}
			done, err := applyEvent([]byte(data.String()), out, &blocks)
			data.Reset()
			if err != nil || done {
				return err
			}
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		// "event:" names the type the data repeats; comments and other
		// fields are ignored.
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return malformed(fmt.Sprintf("a stream line is over %d bytes", maxDocumentBytes))
		}
		return err
	}
	if data.Len() > 0 {
		if done, err := applyEvent([]byte(data.String()), out, &blocks); err != nil || done {
			return err
		}
	}
	// A stream that ends after message_delta named a cut-short stop reason
	// has delivered its final usage; the missing message_stop changes nothing.
	if out.started && cutShort(out.message.StopReason) {
		return finish(out, blocks)
	}
	return errStreamEnded
}

// malformed is a stream event that does not follow the protocol.
type malformed string

func (m malformed) Error() string { return string(m) }

func applyEvent(data []byte, out *streamed, blocks *[]*streamBlock) (done bool, err error) {
	var event struct {
		Type         string          `json:"type"`
		Index        int             `json:"index"`
		Message      json.RawMessage `json:"message"`
		ContentBlock json.RawMessage `json:"content_block"`
		Delta        json.RawMessage `json:"delta"`
		Usage        *apiUsage       `json:"usage"`
		Error        *streamError    `json:"error"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return false, malformed("a stream event is not JSON")
	}
	if event.Type != "message_start" && event.Type != "ping" && event.Type != "error" && !out.started {
		return false, malformed("the stream did not start with message_start")
	}
	block := func() (*streamBlock, error) {
		if event.Index < 0 || event.Index >= len(*blocks) || (*blocks)[event.Index].stopped {
			return nil, malformed(fmt.Sprintf("a stream event names content block %d, which is not open", event.Index))
		}
		return (*blocks)[event.Index], nil
	}
	switch event.Type {
	case "ping":
	case "error":
		if event.Error == nil {
			return false, malformed("an error event without an error")
		}
		return false, event.Error
	case "message_start":
		if out.started {
			return false, malformed("a second message_start")
		}
		if err := json.Unmarshal(event.Message, &out.message); err != nil {
			return false, malformed("message_start carries no message")
		}
		out.message.Content = nil
		out.started = true
	case "content_block_start":
		if event.Index != len(*blocks) {
			return false, malformed("content blocks out of order")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(event.ContentBlock, &fields); err != nil || fields == nil {
			return false, malformed("content_block_start carries no block")
		}
		*blocks = append(*blocks, &streamBlock{fields: fields, strings: map[string]*strings.Builder{}})
	case "content_block_delta":
		b, err := block()
		if err != nil {
			return false, err
		}
		var delta struct {
			Type        string  `json:"type"`
			Text        *string `json:"text"`
			Thinking    *string `json:"thinking"`
			Signature   *string `json:"signature"`
			PartialJSON *string `json:"partial_json"`
		}
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			return false, malformed("a content delta is not an object")
		}
		switch {
		case delta.Type == "text_delta" && delta.Text != nil:
			b.appendString("text", *delta.Text)
		case delta.Type == "thinking_delta" && delta.Thinking != nil:
			b.appendString("thinking", *delta.Thinking)
		case delta.Type == "signature_delta" && delta.Signature != nil:
			b.appendString("signature", *delta.Signature)
		case delta.Type == "input_json_delta" && delta.PartialJSON != nil:
			if b.input == nil {
				b.input = &strings.Builder{}
			}
			b.input.WriteString(*delta.PartialJSON)
		default:
			return false, malformed("an unknown content delta")
		}
	case "content_block_stop":
		b, err := block()
		if err != nil {
			return false, err
		}
		b.stopped, b.encoded = true, b.encode()
	case "message_delta":
		var delta struct {
			StopReason string `json:"stop_reason"`
		}
		if err := json.Unmarshal(event.Delta, &delta); err != nil {
			return false, malformed("message_delta carries no delta")
		}
		if delta.StopReason != "" {
			out.message.StopReason = delta.StopReason
		}
		out.message.Usage = mergeUsage(out.message.Usage, event.Usage)
	case "message_stop":
		return true, finish(out, *blocks)
	}
	// Event types this backend does not know are skipped, as the protocol
	// allows new ones.
	return false, nil
}

// mergeUsage applies a message_delta's usage, whose counts are cumulative, to
// message_start's: each field it reports replaces the earlier value.
func mergeUsage(start, delta *apiUsage) *apiUsage {
	if delta == nil {
		return start
	}
	if start == nil {
		return delta
	}
	merged := *start
	for _, field := range []struct{ to, from **int64 }{
		{&merged.Input, &delta.Input}, {&merged.Output, &delta.Output},
		{&merged.CacheRead, &delta.CacheRead}, {&merged.CacheCreate, &delta.CacheCreate},
	} {
		if *field.from != nil {
			*field.to = *field.from
		}
	}
	if delta.CacheCreation != nil {
		merged.CacheCreation = delta.CacheCreation
	}
	if delta.Speed != nil {
		merged.Speed = delta.Speed
	}
	return &merged
}

// countingReader fails a body over its limit and tells watch about every
// read that made progress.
type countingReader struct {
	r     io.Reader
	n     int64
	watch func()
}

var errTooLarge = errors.New("the response is too large")

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.n += int64(n)
		c.watch()
		if c.n > maxResponseBytes {
			return n, errTooLarge
		}
	}
	return n, err
}
