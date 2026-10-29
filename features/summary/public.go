package summary

import _ "embed"

// PublicVersion is the published prompt version.
const PublicVersion = "feature-summary-public-v1"

var (
	//go:embed prompts/feature-summary-public-v1.txt
	publicFeaturePrompt string
	//go:embed prompts/pr-summary-public-v1.txt
	publicPullRequestPrompt string
)

// Public are the published prompt versions: one whose feature call reads
// the feature's current text.
var Public = Prompts{
	PublicVersion: {Feature: publicFeaturePrompt, PullRequest: publicPullRequestPrompt, CurrentText: true},
}
