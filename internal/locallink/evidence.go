package locallink

import (
	"strings"

	"github.com/baumai/baum/internal/gitlocal"
	"github.com/baumai/baum/linkage"
	"github.com/baumai/baum/linkage/sessioncorrelate"
	"github.com/baumai/baum/sessions"
	"github.com/baumai/baum/sessions/session"
)

// candidateOf projects a session read from this machine into the matcher's
// view of it, the way the hosted service projects a stored session tree.
//
//   - RunID is the vendor's session id, and Agent the vendor that ran, which
//     is what a Co-Authored-By trailer names. Source is the store it came
//     from ("claude_code", "entire", ...), which says nothing about the agent
//     for a session an Entire checkpoint recorded.
//   - Branches, files and commit refs come from sessioncorrelate, which reads
//     them out of the tree without altering it; a ref's role is carried over
//     as is, and the "starting" ref also sets StartingCommitSHA.
//   - IngestedAt is when the store last changed, standing in for the upload
//     time the hosted service uses to bound a session that recorded no end.
//   - UserEmail is left empty: a session on this machine records no author
//     address, and an empty one never rules a commit out.
func candidateOf(s sessions.Session, correlated sessioncorrelate.Result) linkage.RunCandidate {
	candidate := linkage.RunCandidate{
		RunID: s.ID, Agent: s.Vendor, Source: s.Source.String(), Branches: correlated.Branches,
	}
	if !s.ModifiedAt.IsZero() {
		modified := s.ModifiedAt
		candidate.IngestedAt = &modified
	}
	if root := s.Root(); root != nil {
		candidate.StartedAt, candidate.EndedAt = root.StartedAt, root.EndedAt
		if root.GitBranch != nil && *root.GitBranch != "" && !containsString(candidate.Branches, *root.GitBranch) {
			candidate.Branches = append(append([]string(nil), candidate.Branches...), *root.GitBranch)
		}
	}
	for _, ref := range correlated.CommitRefs {
		candidate.CommitRefs = append(candidate.CommitRefs, linkage.CommitRef{SHA: ref.SHA, Role: linkage.Role(ref.Role)})
		if ref.Role == sessioncorrelate.RoleStarting && candidate.StartingCommitSHA == "" {
			candidate.StartingCommitSHA = ref.SHA
		}
	}
	seen := map[string]bool{}
	edited := map[string]bool{}
	for _, file := range correlated.Files {
		if !seen[file.Path] {
			seen[file.Path] = true
			candidate.FilesTouched = append(candidate.FilesTouched, file.Path)
		}
		if file.Kind == session.TouchEdit && !edited[file.Path] {
			edited[file.Path] = true
			candidate.FilesEdited = append(candidate.FilesEdited, file.Path)
		}
	}
	return candidate
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// enrichedOf projects a commit read from the clone into the matcher's view.
//
//   - AncestorOID is the first parent, the commit the work started from, and
//     IsMerge is more than one parent.
//   - FilesTouched are the paths the commit changed, both sides of a rename.
//   - CoauthoredAgents are the agent slugs that Co-Authored-By trailers name by
//     address; a person or an unknown address names none.
//   - BranchNames is empty, which the branch step reads as unknown: a local
//     branch name says nothing about which branch the pull request was
//     opened from, and a wrong name would rule out the session that wrote
//     the commit.
func enrichedOf(c gitlocal.Commit) linkage.EnrichedCommit {
	enriched := linkage.EnrichedCommit{
		Commit:           linkage.Commit{OID: c.OID, AuthorEmail: c.AuthorEmail, CommittedAt: c.CommittedAt},
		IsMerge:          len(c.Parents) > 1,
		CoauthoredAgents: coauthoredAgents(c.Trailers),
		FilesTouched:     c.Files,
		BranchNames:      []string{},
	}
	if len(c.Parents) > 0 {
		enriched.AncestorOID = c.Parents[0]
	}
	if enriched.FilesTouched == nil {
		enriched.FilesTouched = []string{}
	}
	return enriched
}

// Agents are identified by the address in their Co-Authored-By trailer, never
// by the display name: names vary from release to release while the address
// stays fixed, and a name would misclassify a person who shares one.
var (
	agentEmails = map[string]string{
		"noreply@anthropic.com":   "claude",
		"cursoragent@cursor.com":  "cursor",
		"agent@cursor.com":        "cursor",
		"noreply@opencode.ai":     "opencode",
		"openhands@all-hands.dev": "openhands",
		"amp@ampcode.com":         "amp",
	}
	// agentEmailDomains are agent-only domains whose local part varies.
	agentEmailDomains = map[string]string{"aider.chat": "aider"}
	// agentBotSlugs maps GitHub App logins to agents; their addresses read
	// <account-id>+<login>@users.noreply.github.com.
	agentBotSlugs = map[string]string{
		"claude":                  "claude",
		"copilot":                 "copilot",
		"copilot-swe-agent":       "copilot",
		"chatgpt-codex-connector": "codex",
		"devin-ai-integration":    "devin",
		"google-labs-jules":       "jules",
		"amazon-q-developer":      "amazon-q",
	}
)

func coauthoredAgents(trailers []string) []string {
	agents := []string{}
	seen := map[string]bool{}
	for _, trailer := range trailers {
		key, value, ok := strings.Cut(trailer, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "co-authored-by" && key != "coauthored-by" {
			continue
		}
		start, end := strings.LastIndex(value, "<"), strings.LastIndex(value, ">")
		if start < 0 || end < start {
			continue
		}
		if agent := agentFromEmail(value[start+1 : end]); agent != "" && !seen[agent] {
			seen[agent] = true
			agents = append(agents, agent)
		}
	}
	return agents
}

func agentFromEmail(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if agent, found := agentEmails[email]; found {
		return agent
	}
	local, domain, found := strings.Cut(email, "@")
	if !found {
		return ""
	}
	if agent, found := agentEmailDomains[domain]; found {
		return agent
	}
	if domain != "users.noreply.github.com" {
		return ""
	}
	if _, login, found := strings.Cut(local, "+"); found {
		local = login
	}
	return agentBotSlugs[strings.TrimSuffix(local, "[bot]")]
}
