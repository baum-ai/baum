package assign

import _ "embed"

// PublicVersion is the published single-item prompt version, and
// PublicBatchVersion its batch version.
const (
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
	map[string]Version{PublicVersion: {Prompt: publicPrompt, Skips: true, Lists: true}},
	map[string]Batch{PublicBatchVersion: {
		Single: PublicVersion, Frame: publicBatchFrame, Seeded: PublicBatchVersion + "-2", SeededNote: publicSeededNote,
	}},
)
