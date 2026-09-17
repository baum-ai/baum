package linkage

import "strings"

// NormalizeAgent maps an agent string to its configured canonical slug,
// falling back to a trimmed lowercase copy when it is unmapped.
func NormalizeAgent(raw string, aliases map[string]string) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	if mapped, ok := aliases[key]; ok {
		return mapped
	}
	return key
}

// TrailerAttribution returns the commit's normalized, order-stable agent slugs.
func TrailerAttribution(commit EnrichedCommit, cfg Config) []string {
	agents := make([]string, 0, len(commit.CoauthoredAgents))
	seen := make(map[string]bool, len(commit.CoauthoredAgents))
	for _, raw := range commit.CoauthoredAgents {
		agent := NormalizeAgent(raw, cfg.AgentAliases)
		if !seen[agent] {
			agents = append(agents, agent)
			seen[agent] = true
		}
	}
	return agents
}

// TrailerEdge attributes an unmatched outcome to its co-author trailers.
func TrailerEdge(unmatched Edge, agents []string, cfg Config) Edge {
	unmatched.RunID = ""
	unmatched.Method = MethodTrailer
	unmatched.MatchedBy = string(MethodTrailer)
	unmatched.Confidence = cfg.TrailerConfidence
	unmatched.TrailerAgents = agents
	return unmatched
}
