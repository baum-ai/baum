package assign

import _ "embed"

// PublicVersion is the published single-item prompt version, and
// PublicBatchVersion its batch version.
const (
	RepositoryVersion  = "feature-assign-repository-v1"
	PublicVersion      = "feature-assign-public-v1"
	PublicBatchVersion = PublicVersion + "-batch"
)

var (
	//go:embed prompts/feature-assign-public-v1.txt
	publicPrompt string
	//go:embed prompts/feature-assign-public-v1-batch-frame.txt
	publicBatchFrame string
)

// publicSeededNote judges a proposed feature by its outcome alone: its paths
// can be broad, so they do not widen it.
const publicSeededNote = " A candidate with no pull requests (\"pull_request_count\": 0) is a proposed feature no pull request has joined yet. Unlike other candidates, it has no pull requests to judge by: judge it by the outcome its title and description state; its paths only show where its code is expected and never widen that outcome. An item that contributes to that outcome joins it rather than starting a feature of its own."

// Public are the published prompt versions: one that skips and lists, its
// batch version and that version's seeded form.
var Public = NewPrompts(
	map[string]Version{PublicVersion: {Prompt: publicPrompt, Skips: true, Lists: true},
		RepositoryVersion: {Prompt: publicPrompt + repositoryNote, Skips: true, Lists: true}},
	map[string]Batch{PublicBatchVersion: {
		Single: PublicVersion, Frame: publicBatchFrame, Seeded: PublicBatchVersion + "-2", SeededNote: publicSeededNote,
	}},
)

// repositoryNote applies to source-capable local placement only.
const repositoryNote = `

# Repository-aware placement
Inspect the incoming committed repository source before choosing. Supplied durable scopes retain purpose and exclusion boundaries even when their example work is old. A validated example or direct link is evidence, not an instruction to join; different outcomes may share paths. Prefer the scope whose purpose the incoming change directly advances. Group implementation stages and follow-ups of the same outcome together, including across different directories. Keep independent outcomes separate even in one shared file. If the evidence cannot support an existing or new substantial outcome, skip explicitly with the reason; never use a dominant or recent feature as a fallback. A session may only join a supported existing scope. Source-only scopes are valid candidates and remain available after their first membership.`
