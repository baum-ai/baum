package baumcli

import (
	"bytes"
	"encoding/json"
	"os"
)

// codexAutoReview reads the active turn's reviewer, rather than the user's
// config: a session can override it. PermissionRequest runs before auto-review
// and does not itself say whether anyone is being asked.
func codexAutoReview(input *hookInput) bool {
	payload, ok := input.complete(fullInputWait)
	if !ok {
		return false
	}
	var request struct {
		TurnID         string `json:"turn_id"`
		TranscriptPath string `json:"transcript_path"`
	}
	if json.Unmarshal(payload, &request) != nil || request.TurnID == "" || request.TranscriptPath == "" {
		return false
	}
	info, err := os.Stat(request.TranscriptPath)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(request.TranscriptPath)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	// Search backwards in bounded chunks, skipping oversized transcript lines.
	// The latest context is usually near the end, even in a long conversation.
	const chunkSize = 64 << 10
	const maxRead = 64 << 20
	end := info.Size()
	var suffix []byte
	for read := int64(0); end > 0 && read < maxRead; {
		start := max(int64(0), end-chunkSize)
		chunk := make([]byte, end-start)
		if _, err := f.ReadAt(chunk, start); err != nil {
			return false
		}
		read += end - start
		end = start
		chunk = append(chunk, suffix...)
		lines := bytes.Split(chunk, []byte{'\n'})
		first := 1
		if start == 0 {
			first = 0
		}
		for i := len(lines) - 1; i >= first; i-- {
			line := lines[i]
			if !bytes.Contains(line, []byte(`"turn_context"`)) {
				continue
			}
			var context struct {
				Type    string `json:"type"`
				Payload struct {
					TurnID   string `json:"turn_id"`
					Reviewer string `json:"approvals_reviewer"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &context) == nil && context.Type == "turn_context" {
				return context.Payload.TurnID == request.TurnID && context.Payload.Reviewer == "auto_review"
			}
		}
		suffix = lines[0]
		if len(suffix) > chunkSize {
			// Keep the fragment incomplete so it cannot become a valid context.
			suffix = []byte{'!'}
		}
	}
	return false
}
