package summary

import _ "embed"

// PublicVersion is the published prompt version.
const PublicVersion = "feature-summary-public-v1"

// RepositoryVersion requires semantic relevance for local highlights.
const RepositoryVersion = "feature-summary-repository-v1"

var (
	//go:embed prompts/feature-summary-public-v1.txt
	publicFeaturePrompt string
	//go:embed prompts/pr-summary-public-v1.txt
	publicPullRequestPrompt string
)

// Public are the published prompt versions: one whose feature call reads
// the feature's current text.
var Public = Prompts{
	PublicVersion:     {Feature: publicFeaturePrompt, PullRequest: publicPullRequestPrompt, CurrentText: true},
	RepositoryVersion: {Feature: publicFeaturePrompt + repositoryNote, PullRequestVersion: PublicVersion, CurrentText: true},
}

const repositoryNote = `

# Local significance and relevance
Only decisions in key_eligible_decisions may be highlights. A highlight must explain a consequential product, architecture, behavior, interface or engineering tradeoff directly relevant to this feature. Preserve explicit human choices and their citations. Routine agent activity (running or publishing tests, lint/build checks, preparing a PR, bookkeeping, restating completion) is not a key decision. A testing/tooling policy that deliberately changes the repository may be significant; merely performing checks is not. Do not fill a quota. Return zero key decisions when none qualifies, even if decisions or pull requests exist. Do not substitute a decision from another outcome merely because it cites a shared file.`
