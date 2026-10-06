package steps

import "github.com/lastpersonlabs/baum/linkage"

// Default returns the built-in steps in linkage.DefaultMatchSteps order, one
// per id. The pipeline (the linkage package) can't import this package back (it
// would cycle, since these steps import linkage), so callers assemble the
// registry here and pass it into linkage.MatchCommit / linkage.MatchCommits
// as availableSteps.
func Default() []linkage.Step {
	return []linkage.Step{
		CommitRefStep{},
		TimeframeStep{},
		CoauthorAgentStep{},
		UserEmailStep{},
		StartCommitStep{},
		BranchStep{},
		FilesOverlapStep{},
	}
}
