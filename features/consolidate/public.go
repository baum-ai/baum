package consolidate

import _ "embed"

// PublicVersion is the published prompt version.
const PublicVersion = "feature-consolidate-public-v1"

// RepositoryVersion permits repair of a small semantically mixed local group.
const RepositoryVersion = "feature-consolidate-repository-v1"

var (
	//go:embed prompts/feature-merge-public-v1.txt
	publicMergePrompt string
	//go:embed prompts/feature-split-repository-v1.txt
	repositorySplitPrompt string
	//go:embed prompts/feature-split-public-v1.txt
	publicSplitPrompt string
)

// Public are the published prompt versions: one whose merge call sees each
// feature's digest and its pull requests' summaries.
var Public = Prompts{
	PublicVersion:     {Merge: publicMergePrompt, Split: publicSplitPrompt, Contextual: true},
	RepositoryVersion: {Merge: publicMergePrompt, Split: repositorySplitPrompt, Contextual: true},
}
