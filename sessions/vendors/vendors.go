// Package vendors holds the canonical agent slugs the platform stores and
// groups by. A slug names the agent that ran — never how its session reached
// us, which is `agent_session_ingests.source`.
//
// It is deliberately tiny and has no dependencies: it is a shared spelling, not
// a registry. The mappers do not import it (a mapper knows only its own
// vendor's name, and coupling four packages to one table would make adding a
// fifth a change to all of them); the local discovery, upload and ingest path
// does, because those three have to agree on the same string for a session to
// survive the trip.
package vendors

// Slug is a canonical agent identifier: the value stored in
// `agent_session_ingests.vendor` and the value the analytics `source` filter
// accepts.
type Slug string

const (
	ClaudeCode Slug = "claude_code"
	Codex      Slug = "codex"
	Cursor     Slug = "cursor"
)

// Valid reports whether s is a slug the local discovery and upload path can
// produce. Sessions of other agents reach us through Entire checkpoints, whose
// slugs `entiresession` derives from what Entire recorded; they are not
// enumerated here because nothing in this path can collect one.
func (s Slug) Valid() bool {
	switch s {
	case ClaudeCode, Codex, Cursor:
		return true
	}
	return false
}

// String makes a slug printable without a conversion at every call site.
func (s Slug) String() string { return string(s) }
