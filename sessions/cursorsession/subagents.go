package cursorsession

import (
	"encoding/json"

	"github.com/lastpersonlabs/baum/sessions/cursortranscript"
	"github.com/lastpersonlabs/baum/sessions/session"
)

func (f *composerState) resolveChildren(tree *cursortranscript.Tree) map[string]*session.SpawnLink {
	listed := map[string]bool{}
	for _, childID := range f.data.SubagentComposerIDs {
		if childID != "" {
			listed[childID] = true
		}
	}
	for _, childID := range tree.DanglingChildren {
		f.b.report.note("composer %s lists dangling subagent %s", f.data.ComposerID, childID)
	}

	byChild := map[string][]*toolEvidence{}
	for _, evidence := range f.toolByBubble {
		record := evidence.bubble.Tool
		if record == nil || record.AdditionalData == nil || record.AdditionalData.SubagentComposerID == "" {
			continue
		}
		childID := record.AdditionalData.SubagentComposerID
		if record.ToolName() == "task_v2" {
			byChild[childID] = append(byChild[childID], evidence)
		}
		if !listed[childID] {
			f.b.report.note("composer %s task tool %s references unlisted subagent %s", f.data.ComposerID, record.ToolCallID, childID)
		}
	}

	links := map[string]*session.SpawnLink{}
	for _, child := range tree.Children {
		childID := child.ComposerID()
		matches := byChild[childID]
		if len(matches) != 1 {
			// A spawn record can be compacted out of the header listing, so
			// the child is unattachable evidence, not a reason to refuse the
			// parent. The caller accounts the skipped subtree.
			f.b.report.note("composer %s subagent %s has %d resolving task_v2 tools and is not stored", f.data.ComposerID, childID, len(matches))
			continue
		}
		evidence := matches[0]
		evidence.row.Verb = session.VerbSpawnSubagent
		links[childID] = &session.SpawnLink{ParentSessionID: f.session.ID, ToolUseID: evidence.row.ID}
		f.checkChildCorroboration(child, evidence.bubble.Tool)
	}
	return links
}

func (f *composerState) checkChildCorroboration(child *cursortranscript.Tree, spawn *cursortranscript.ToolRecord) {
	childID := child.ComposerID()
	if child.Composer.Header == nil || !child.Composer.Header.IsSubagent {
		f.b.report.note("child composer %s has no isSubagent header corroboration", childID)
	}
	var firstUser *cursortranscript.Bubble
	for _, bubble := range child.Composer.Bubbles {
		if bubble.Type == cursortranscript.BubbleTypeUser {
			firstUser = bubble
			break
		}
	}
	if firstUser == nil || firstUser.SubagentSpawnTaskToolCallID == "" {
		f.b.report.note("child composer %s has no child-side spawn tool corroboration", childID)
	} else if firstUser.SubagentSpawnTaskToolCallID != spawn.ToolCallID {
		f.b.report.note("child composer %s names spawn tool %s, parent tool is %s", childID, firstUser.SubagentSpawnTaskToolCallID, spawn.ToolCallID)
	}
	resultID := resultAgentID(spawn.Result)
	if resultID == "" {
		f.b.report.note("spawn tool %s has no result.agentId corroboration for child %s", spawn.ToolCallID, childID)
	} else if resultID != childID {
		f.b.report.note("spawn tool %s result names child %s, listing names %s", spawn.ToolCallID, resultID, childID)
	}
}

func resultAgentID(raw json.RawMessage) string {
	parsed, ok := parsedJSON(raw)
	if !ok {
		return ""
	}
	var result struct {
		AgentID string `json:"agentId"`
	}
	if json.Unmarshal(parsed, &result) != nil {
		return ""
	}
	return result.AgentID
}
