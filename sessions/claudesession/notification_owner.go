package claudesession

import "github.com/baumai/baum/sessions/session"

// Resolve after building the whole tree: a completion delivered to the parent
// can name a tool in a child transcript. Keep every consequence in its own
// session; analytics follows the existing spawn links from there.
func attributeNotifications(g *session.Graph) {
	r := notificationOwners{
		inputs: map[string]session.UserInput{}, sessions: map[string]session.Session{},
		tools: map[string]session.ToolUse{}, vendorTools: map[string]map[string]bool{},
		resolved: map[string]string{}, visiting: map[string]bool{},
	}
	for _, input := range g.UserInputs {
		r.inputs[input.ID] = input
	}
	for _, s := range g.Sessions {
		r.sessions[s.ID] = s
	}
	for _, tool := range g.ToolUses {
		r.tools[tool.ID] = tool
	}
	for _, v := range g.VendorIDs {
		if v.Vendor != Vendor || v.OurType != session.EntityToolUse || v.VendorIDKind != "tool_use.id" {
			continue
		}
		if r.vendorTools[v.VendorID] == nil {
			r.vendorTools[v.VendorID] = map[string]bool{}
		}
		r.vendorTools[v.VendorID][v.OurID] = true
	}
	for _, input := range g.UserInputs {
		r.resolve(input.ID)
	}
	reassign := func(id *string) {
		if owner := r.resolved[*id]; owner != "" {
			*id = owner
		}
	}
	lastReply := map[string]string{}
	for i := range g.ModelInvocations {
		reply := &g.ModelInvocations[i]
		reassign(&reply.UserInputID)
		if previous, ok := lastReply[reply.UserInputID]; ok {
			reply.ParentModelInvocationID = &previous
		}
		lastReply[reply.UserInputID] = reply.ID
	}
	for i := range g.AssistantMessages {
		reassign(&g.AssistantMessages[i].UserInputID)
	}
	for i := range g.ToolUses {
		reassign(&g.ToolUses[i].UserInputID)
	}
	for i := range g.ToolResults {
		reassign(&g.ToolResults[i].UserInputID)
	}
	for i := range g.FileTouches {
		reassign(&g.FileTouches[i].UserInputID)
	}
	for i := range g.TokenUsage {
		if g.TokenUsage[i].UserInputID != nil {
			owner := *g.TokenUsage[i].UserInputID
			reassign(&owner)
			g.TokenUsage[i].UserInputID = &owner
		}
	}
}

type notificationOwners struct {
	inputs      map[string]session.UserInput
	sessions    map[string]session.Session
	tools       map[string]session.ToolUse
	vendorTools map[string]map[string]bool
	resolved    map[string]string
	visiting    map[string]bool
}

func (r *notificationOwners) resolve(id string) string {
	if owner, done := r.resolved[id]; done {
		return owner
	}
	if r.visiting[id] {
		return ""
	}
	r.visiting[id] = true
	defer delete(r.visiting, id)
	input := r.inputs[id]
	owner := ""
	if (input.Kind == session.KindPrompt || input.Kind == session.KindSlashCommand) &&
		(input.Origin == session.OriginHuman || input.Origin == session.OriginAgent) {
		owner = id
	} else if input.Kind == session.KindOther && input.Origin == session.OriginSystem {
		// Multiple wrappers or references cannot establish one causal owner.
		if len(notificationWrapper.FindAllStringSubmatch(input.Text, -1)) == 1 &&
			len(notificationField["toolUseID"].FindAllStringSubmatch(input.Text, -1)) == 1 {
			if n, ok := parseNotification(input.Text); ok && n.ToolUseID != "" {
				candidate, matches := "", 0
				for toolID := range r.vendorTools[n.ToolUseID] {
					if local := r.localOwner(toolID, input); local != "" {
						candidate = local
						matches++
					}
				}
				if matches == 1 {
					owner = r.resolve(candidate)
				}
			}
		}
	}
	r.resolved[id] = owner
	return owner
}

// Only this session or its descendants can provide evidence. Sibling/ancestor
// tool IDs must never move activity across a session boundary.
func (r *notificationOwners) localOwner(toolID string, input session.UserInput) string {
	for steps := 0; steps <= len(r.sessions); steps++ {
		tool, ok := r.tools[toolID]
		if !ok || (tool.Timestamp != nil && input.TriggeredAt != nil && tool.Timestamp.After(*input.TriggeredAt)) {
			return ""
		}
		if tool.SessionID == input.SessionID {
			owner, ok := r.inputs[tool.UserInputID]
			if ok && owner.SessionID == input.SessionID && owner.Ordinal < input.Ordinal {
				return owner.ID
			}
			return ""
		}
		s := r.sessions[tool.SessionID]
		if s.SpawnedByToolUseID == nil {
			return ""
		}
		toolID = *s.SpawnedByToolUseID
	}
	return ""
}
