package agent

import (
	"context"
	"fmt"
)

// Prompt is one immutable set of review instructions for one backend.
type Prompt struct {
	// Version and Digest identify the instructions in the document's generator
	// block; Digest is the sha256 of Body. The JSON tags are the hosted run
	// header's wire format.
	Version string `json:"version"`
	Body    string `json:"body"`
	Digest  string `json:"digest"`
	// Repair and RepairEnding replace RepairText(backend) when they are not
	// empty: the text that opens and closes a validation repair prompt. A host
	// whose instructions name tools differently sets both. RepairEnding takes
	// two %d verbs, the attempt number and the attempt limit. They
	// are not part of the wire format.
	Repair       string `json:"-"`
	RepairEnding string `json:"-"`
}

// Prompts supplies the review instructions to the loop.
type Prompts interface {
	// Instructions returns the review instructions for a backend. The loop
	// ignores Version and Digest: the host records the version in the
	// document's generator.prompt_version and keeps the digest. The loop
	// appends the offered tile guidance (tilecatalogue.Instructions) to the
	// body itself.
	Instructions(ctx context.Context, backend string) (Prompt, error)
}

// StaticPrompts is a fixed Prompts for tests and simple hosts: the prompt for
// a backend by name, with the prompt under the empty name serving every other
// backend.
type StaticPrompts map[string]Prompt

// Instructions returns the prompt named for the backend, else the one under
// the empty name, else an error.
func (s StaticPrompts) Instructions(_ context.Context, backend string) (Prompt, error) {
	if prompt, ok := s[backend]; ok {
		return prompt, nil
	}
	if prompt, ok := s[""]; ok {
		return prompt, nil
	}
	return Prompt{}, fmt.Errorf("no prompt for backend %q", backend)
}
