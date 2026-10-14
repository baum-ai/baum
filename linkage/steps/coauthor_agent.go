package steps

import "github.com/baumai/baum/linkage"

const CoauthorAgentStepID = "coauthor_agent"

// agentAgnosticSources name a recorder rather than an agent: "entire" and
// "entire_checkpoint" say a checkpoint carried the session, "upload" says
// somebody sent us their store, and "generic" is metadata-only. None can be
// compared to a Co-Authored-By agent, so a candidate that offers only one of
// these has no evidence to be excluded on and passes to a later step.
//
// A candidate with a vendor is compared on the vendor, which is the whole
// point of RunCandidate.Agent: an Entire-carried Cursor session is Cursor
// evidence, not agent-agnostic evidence.
var agentAgnosticSources = map[string]bool{
	"entire": true, "entire_checkpoint": true, "upload": true, "generic": true,
}

// CoauthorAgentStep keeps candidates whose agent normalizes (via
// cfg.AgentAliases) to one of the commit's Co-Authored-By agents. An empty
// CoauthoredAgents list is a no-op.
type CoauthorAgentStep struct{}

func (CoauthorAgentStep) ID() string { return CoauthorAgentStepID }

func (CoauthorAgentStep) Apply(commit linkage.EnrichedCommit, candidates []linkage.RunCandidate, cfg linkage.Config) linkage.StepResult {
	if len(commit.CoauthoredAgents) == 0 {
		return linkage.Filtered(candidates)
	}

	allowed := make(map[string]bool, len(commit.CoauthoredAgents))
	for _, agent := range commit.CoauthoredAgents {
		allowed[linkage.NormalizeAgent(agent, cfg.AgentAliases)] = true
	}

	kept := make([]linkage.RunCandidate, 0, len(candidates))
	for _, c := range candidates {
		agent := linkage.NormalizeAgent(c.AgentOrSource(), cfg.AgentAliases)
		if allowed[agent] || agentAgnosticSources[agent] {
			kept = append(kept, c)
		}
	}
	return linkage.FilteredWithReason(kept, linkage.ReasonAgentMismatch)
}
