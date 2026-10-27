package consolidate

import _ "embed"

// PublicVersion is the published prompt version.
const PublicVersion = "feature-consolidate-public-v1"

var (
	//go:embed prompts/feature-merge-public-v1.txt
	publicMergePrompt string
	//go:embed prompts/feature-split-public-v1.txt
	publicSplitPrompt string
)

// Public are the published prompt versions: one whose merge call sees each
// feature's digest and its pull requests' summaries.
var Public = Prompts{
	PublicVersion: {Merge: publicMergePrompt, Split: publicSplitPrompt, Contextual: true},
}
