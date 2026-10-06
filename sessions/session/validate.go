package session

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Validate checks a Graph against every invariant of the session model
// (see ARCHITECTURE.md), reporting all violations at once. It
// runs before anything is written: the failures it catches — a consequence
// attributed to the wrong session, a second result for one tool call, two
// entities sharing an id — would otherwise be a wrong answer in the database
// rather than an error in the log.
func Validate(g *Graph) error {
	v := &validator{}

	sessions := index(g.Sessions, func(s Session) string { return s.ID }, v, "session")
	userInputs := index(g.UserInputs, func(u UserInput) string { return u.ID }, v, "user input")
	invocations := index(g.ModelInvocations, func(m ModelInvocation) string { return m.ID }, v, "model invocation")
	toolUses := index(g.ToolUses, func(t ToolUse) string { return t.ID }, v, "tool use")
	index(g.AssistantMessages, func(a AssistantMessage) string { return a.ID }, v, "assistant message")
	index(g.ToolResults, func(r ToolResult) string { return r.ID }, v, "tool result")
	index(g.FileTouches, func(f FileTouch) string { return f.ID }, v, "file touch")
	index(g.TokenUsage, func(u TokenUsage) string { return u.ID }, v, "token usage")
	index(g.Attributes, func(a SessionAttribute) string { return a.ID }, v, "session attribute")

	v.sessions(g, sessions, toolUses)
	v.userInputs(g, sessions)
	v.invocations(g, userInputs)
	v.messages(g, userInputs, invocations)
	v.toolUses(g, userInputs, invocations)
	v.toolResults(g, toolUses)
	v.fileTouches(g, userInputs, toolUses)
	v.tokenUsage(g, sessions, userInputs)
	v.attributes(g, sessions)
	v.vendorIDs(g, ownerSessions(g))

	return v.err()
}

// ownerSessions maps every entity to the session it belongs to, so a vendor
// mapping row can be checked against its owner.
func ownerSessions(g *Graph) map[EntityType]map[string]string {
	out := map[EntityType]map[string]string{
		EntitySession:          make(map[string]string, len(g.Sessions)),
		EntityUserInput:        make(map[string]string, len(g.UserInputs)),
		EntityModelInvocation:  make(map[string]string, len(g.ModelInvocations)),
		EntityAssistantMessage: make(map[string]string, len(g.AssistantMessages)),
		EntityToolUse:          make(map[string]string, len(g.ToolUses)),
		EntityToolResult:       make(map[string]string, len(g.ToolResults)),
		EntityFileTouch:        make(map[string]string, len(g.FileTouches)),
		EntitySessionAttribute: make(map[string]string, len(g.Attributes)),
		EntityTokenUsage:       make(map[string]string, len(g.TokenUsage)),
	}
	for _, s := range g.Sessions {
		out[EntitySession][s.ID] = s.ID
	}
	for _, u := range g.UserInputs {
		out[EntityUserInput][u.ID] = u.SessionID
	}
	for _, m := range g.ModelInvocations {
		out[EntityModelInvocation][m.ID] = m.SessionID
	}
	for _, a := range g.AssistantMessages {
		out[EntityAssistantMessage][a.ID] = a.SessionID
	}
	for _, t := range g.ToolUses {
		out[EntityToolUse][t.ID] = t.SessionID
	}
	for _, r := range g.ToolResults {
		out[EntityToolResult][r.ID] = r.SessionID
	}
	for _, f := range g.FileTouches {
		out[EntityFileTouch][f.ID] = f.SessionID
	}
	for _, a := range g.Attributes {
		out[EntitySessionAttribute][a.ID] = a.SessionID
	}
	for _, u := range g.TokenUsage {
		out[EntityTokenUsage][u.ID] = u.SessionID
	}
	return out
}

type validator struct{ problems []error }

func (v *validator) addf(format string, args ...any) {
	v.problems = append(v.problems, fmt.Errorf(format, args...))
}

func (v *validator) err() error {
	if len(v.problems) == 0 {
		return nil
	}
	return errors.Join(v.problems...)
}

// index builds an id set and reports duplicates. Postgres cannot be relied on
// for this: an upserting write collapses two rows sharing an id silently, and
// the symptom is a missing entity rather than an error.
func index[T any](rows []T, id func(T) string, v *validator, what string) map[string]bool {
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		key := id(row)
		if key == "" {
			v.addf("%s has an empty id", what)
			continue
		}
		if seen[key] {
			v.addf("%s id %s appears more than once", what, key)
		}
		seen[key] = true
	}
	return seen
}

func (v *validator) sessions(g *Graph, sessions, toolUses map[string]bool) {
	roots := 0
	// seen carries the parent-first ordering check; spawnedSession catches one tool
	// use claiming to have spawned two sessions.
	seen := make(map[string]bool, len(g.Sessions))
	spawnedSession := make(map[string]string, len(g.Sessions))

	sessionOfToolUse := make(map[string]string, len(g.ToolUses))
	verbOfToolUse := make(map[string]ToolVerb, len(g.ToolUses))
	for _, t := range g.ToolUses {
		sessionOfToolUse[t.ID] = t.SessionID
		verbOfToolUse[t.ID] = t.Verb
	}

	for _, s := range g.Sessions {
		if !s.Outcome.Valid() {
			v.addf("session %s has unknown outcome %q", s.ID, s.Outcome)
		}
		if (s.ParentSessionID == nil) != (s.SpawnedByToolUseID == nil) {
			v.addf("session %s sets only one of parent_session_id and spawned_by_tool_use_id", s.ID)
		}
		if s.TopLevel() {
			roots++
			seen[s.ID] = true
			continue
		}

		parent := *s.ParentSessionID
		if !sessions[parent] {
			v.addf("session %s names parent session %s, which is not in the graph", s.ID, parent)
		} else if !seen[parent] {
			v.addf("session %s appears before its parent %s; sessions must be ordered parent-first", s.ID, parent)
		}
		seen[s.ID] = true

		if s.SpawnedByToolUseID == nil {
			continue
		}
		toolUseID := *s.SpawnedByToolUseID
		switch {
		case !toolUses[toolUseID]:
			v.addf("session %s is spawned by tool use %s, which is not in the graph", s.ID, toolUseID)
		case verbOfToolUse[toolUseID] != VerbSpawnSubagent:
			v.addf("session %s is spawned by tool use %s, whose verb is %q, not %q",
				s.ID, toolUseID, verbOfToolUse[toolUseID], VerbSpawnSubagent)
		case sessionOfToolUse[toolUseID] != parent:
			v.addf("session %s is spawned by tool use %s in session %s, but its parent is %s",
				s.ID, toolUseID, sessionOfToolUse[toolUseID], parent)
		}
		if other, ok := spawnedSession[toolUseID]; ok {
			v.addf("tool use %s spawns both session %s and session %s", toolUseID, other, s.ID)
		}
		spawnedSession[toolUseID] = s.ID
	}

	if len(g.Sessions) > 0 && roots != 1 {
		v.addf("graph has %d top-level sessions, want exactly 1", roots)
	}
}

func (v *validator) userInputs(g *Graph, sessions map[string]bool) {
	last := map[string]int{}
	for _, u := range g.UserInputs {
		if !sessions[u.SessionID] {
			v.addf("user input %s names session %s, which is not in the graph", u.ID, u.SessionID)
		}
		if !u.Kind.Valid() {
			v.addf("user input %s has unknown kind %q", u.ID, u.Kind)
		}
		if !u.Origin.Valid() {
			v.addf("user input %s has unknown origin %q", u.ID, u.Origin)
		}
		if u.Ordinal < 1 {
			v.addf("user input %s has ordinal %d, want a positive integer", u.ID, u.Ordinal)
		} else if prev, seen := last[u.SessionID]; seen && u.Ordinal <= prev {
			v.addf("user input %s has ordinal %d after %d in session %s; ordinals must strictly increase",
				u.ID, u.Ordinal, prev, u.SessionID)
		}
		last[u.SessionID] = u.Ordinal
		v.jsonObject(u.Metadata, "user input %s metadata", u.ID)
	}
}

func (v *validator) invocations(g *Graph, userInputs map[string]bool) {
	sessionByInput := v.sessionByInput(g)
	seen := make(map[string]bool, len(g.ModelInvocations))
	owner := make(map[string]string, len(g.ModelInvocations))

	for _, m := range g.ModelInvocations {
		owner[m.ID] = m.UserInputID
		v.consequence(consequenceRef{
			what: "model invocation", id: m.ID,
			userInputID: m.UserInputID, sessionID: m.SessionID,
		}, userInputs, sessionByInput)

		for name, count := range map[string]int64{
			"input_tokens": m.InputTokens, "output_tokens": m.OutputTokens,
			"cache_read_tokens": m.CacheReadTokens, "cache_write_tokens": m.CacheWriteTokens,
			"total_tokens": m.TotalTokens,
		} {
			if count < 0 {
				v.addf("model invocation %s has %s %d, want a non-negative count", m.ID, name, count)
			}
		}
		if m.TokenBasis != "" && !m.TokenBasis.Valid() {
			v.addf("model invocation %s has unknown token basis %q", m.ID, m.TokenBasis)
		}

		if m.ParentModelInvocationID != nil {
			parent := *m.ParentModelInvocationID
			switch {
			case !seen[parent]:
				v.addf("model invocation %s names parent %s, which does not precede it in the graph", m.ID, parent)
			case owner[parent] != m.UserInputID:
				v.addf("model invocation %s is chained to parent %s under a different user input", m.ID, parent)
			}
		}
		seen[m.ID] = true
	}
}

func (v *validator) messages(g *Graph, userInputs, invocations map[string]bool) {
	sessionByInput := v.sessionByInput(g)
	invocationInput := make(map[string]string, len(g.ModelInvocations))
	for _, m := range g.ModelInvocations {
		invocationInput[m.ID] = m.UserInputID
	}

	for _, a := range g.AssistantMessages {
		v.consequence(consequenceRef{
			what: "assistant message", id: a.ID,
			userInputID: a.UserInputID, sessionID: a.SessionID,
		}, userInputs, sessionByInput)

		if a.Text == nil && len(a.Content) == 0 {
			v.addf("assistant message %s has neither text nor content", a.ID)
		}
		if !invocations[a.ModelInvocationID] {
			v.addf("assistant message %s names model invocation %s, which is not in the graph", a.ID, a.ModelInvocationID)
		} else if got := invocationInput[a.ModelInvocationID]; got != a.UserInputID {
			v.addf("assistant message %s is under user input %s but its model invocation is under %s",
				a.ID, a.UserInputID, got)
		}
	}
}

func (v *validator) toolUses(g *Graph, userInputs, invocations map[string]bool) {
	sessionByInput := v.sessionByInput(g)
	invocationInput := make(map[string]string, len(g.ModelInvocations))
	for _, m := range g.ModelInvocations {
		invocationInput[m.ID] = m.UserInputID
	}
	resulted := make(map[string]bool, len(g.ToolResults))
	for _, r := range g.ToolResults {
		resulted[r.ToolUseID] = true
	}

	for _, t := range g.ToolUses {
		v.consequence(consequenceRef{
			what: "tool use", id: t.ID,
			userInputID: t.UserInputID, sessionID: t.SessionID,
		}, userInputs, sessionByInput)

		if t.Name == "" {
			v.addf("tool use %s has no name", t.ID)
		}
		if !t.Verb.Valid() {
			v.addf("tool use %s has unknown verb %q", t.ID, t.Verb)
		}
		if !t.Status.Valid() {
			v.addf("tool use %s has unknown status %q", t.ID, t.Status)
		}
		if hasResult := resulted[t.ID]; hasResult == (t.Status == StatusNoResult) {
			v.addf("tool use %s has status %q but %s", t.ID, t.Status,
				map[bool]string{true: "has a tool result", false: "has no tool result"}[hasResult])
		}
		v.jsonValue(t.Input, "tool use %s input", t.ID)
		v.jsonObject(t.Metadata, "tool use %s metadata", t.ID)

		if !invocations[t.ModelInvocationID] {
			v.addf("tool use %s names model invocation %s, which is not in the graph", t.ID, t.ModelInvocationID)
		} else if got := invocationInput[t.ModelInvocationID]; got != t.UserInputID {
			v.addf("tool use %s is under user input %s but its model invocation is under %s",
				t.ID, t.UserInputID, got)
		}
	}
}

func (v *validator) toolResults(g *Graph, toolUses map[string]bool) {
	use := make(map[string]ToolUse, len(g.ToolUses))
	for _, t := range g.ToolUses {
		use[t.ID] = t
	}
	seen := make(map[string]string, len(g.ToolResults))

	for _, r := range g.ToolResults {
		if !r.Status.Valid() {
			v.addf("tool result %s has unknown status %q", r.ID, r.Status)
		}
		if other, ok := seen[r.ToolUseID]; ok {
			v.addf("tool use %s has two tool results, %s and %s", r.ToolUseID, other, r.ID)
		}
		seen[r.ToolUseID] = r.ID

		if !toolUses[r.ToolUseID] {
			v.addf("tool result %s names tool use %s, which is not in the graph", r.ID, r.ToolUseID)
			continue
		}
		parent := use[r.ToolUseID]
		if r.SessionID != parent.SessionID {
			v.addf("tool result %s is in session %s but its tool use is in %s", r.ID, r.SessionID, parent.SessionID)
		}
		if r.UserInputID != parent.UserInputID {
			v.addf("tool result %s is under user input %s but its tool use is under %s",
				r.ID, r.UserInputID, parent.UserInputID)
		}
		if r.Status != parent.Status {
			v.addf("tool result %s has status %q but its tool use has %q", r.ID, r.Status, parent.Status)
		}
	}
}

func (v *validator) fileTouches(g *Graph, userInputs, toolUses map[string]bool) {
	use := make(map[string]ToolUse, len(g.ToolUses))
	for _, t := range g.ToolUses {
		use[t.ID] = t
	}
	sessionByInput := v.sessionByInput(g)

	for _, f := range g.FileTouches {
		v.consequence(consequenceRef{
			what: "file touch", id: f.ID,
			userInputID: f.UserInputID, sessionID: f.SessionID,
		}, userInputs, sessionByInput)

		if f.Path == "" {
			v.addf("file touch %s has no path", f.ID)
		}
		if !f.Kind.Valid() {
			v.addf("file touch %s has unknown kind %q", f.ID, f.Kind)
		}
		if f.StartLine != nil && *f.StartLine < 1 {
			v.addf("file touch %s has start_line %d, want a positive integer", f.ID, *f.StartLine)
		}
		if f.EndLine != nil && *f.EndLine < 1 {
			v.addf("file touch %s has end_line %d, want a positive integer", f.ID, *f.EndLine)
		}

		if f.ToolUseID == nil {
			continue
		}
		toolUseID := *f.ToolUseID
		if !toolUses[toolUseID] {
			v.addf("file touch %s names tool use %s, which is not in the graph", f.ID, toolUseID)
			continue
		}
		parent := use[toolUseID]
		if f.SessionID != parent.SessionID {
			v.addf("file touch %s is in session %s but its tool use is in %s", f.ID, f.SessionID, parent.SessionID)
		}
		if f.UserInputID != parent.UserInputID {
			v.addf("file touch %s is under user input %s but its tool use is under %s",
				f.ID, f.UserInputID, parent.UserInputID)
		}
	}
}

func (v *validator) attributes(g *Graph, sessions map[string]bool) {
	// from is microseconds, the precision timestamptz keeps: checking at a finer
	// one would pass a graph the insert then rejects.
	type slot struct {
		session string
		key     string
		from    int64
	}
	seen := map[slot]string{}

	for _, a := range g.Attributes {
		if !sessions[a.SessionID] {
			v.addf("session attribute %s names session %s, which is not in the graph", a.ID, a.SessionID)
		}
		if a.Key == "" {
			v.addf("session attribute %s has no key", a.ID)
		}
		if len(a.Value) == 0 || !json.Valid(a.Value) {
			v.addf("session attribute %s (%s) has no valid JSON value", a.ID, a.Key)
		}
		if a.ValidFrom.IsZero() {
			v.addf("session attribute %s (%s) has no valid_from", a.ID, a.Key)
		}
		at := slot{a.SessionID, a.Key, a.ValidFrom.UnixMicro()}
		if other, ok := seen[at]; ok {
			v.addf("session attributes %s and %s share key %q and valid_from %s in session %s",
				other, a.ID, a.Key, a.ValidFrom.Format("2006-01-02T15:04:05.999999Z07:00"), a.SessionID)
		}
		seen[at] = a.ID
	}
}

func (v *validator) tokenUsage(g *Graph, sessions, userInputs map[string]bool) {
	sessionByInput := v.sessionByInput(g)
	for _, u := range g.TokenUsage {
		if !u.Scope.Valid() {
			v.addf("token usage %s has unknown scope %q", u.ID, u.Scope)
		}
		if !sessions[u.SessionID] {
			v.addf("token usage %s names session %s, which is not in the graph", u.ID, u.SessionID)
		}
		switch u.Scope {
		case UsageScopeInput:
			if u.UserInputID == nil {
				v.addf("input-scoped token usage %s has no user input", u.ID)
			} else if !userInputs[*u.UserInputID] {
				v.addf("token usage %s names user input %s, which is not in the graph", u.ID, *u.UserInputID)
			} else if owner := sessionByInput[*u.UserInputID]; owner != u.SessionID {
				v.addf("token usage %s is in session %s but its user input is in %s", u.ID, u.SessionID, owner)
			}
		case UsageScopeSession:
			if u.UserInputID != nil {
				v.addf("session-scoped token usage %s names user input %s", u.ID, *u.UserInputID)
			}
		}

		counts := map[string]*int64{
			"input_tokens": u.InputTokens, "output_tokens": u.OutputTokens,
			"cache_read_tokens": u.CacheReadTokens, "cache_write_tokens": u.CacheWriteTokens,
			"total_tokens": u.TotalTokens,
		}
		reported := false
		for name, count := range counts {
			if count == nil {
				continue
			}
			reported = true
			if *count < 0 {
				v.addf("token usage %s has %s %d, want a non-negative count", u.ID, name, *count)
			}
		}
		if !reported {
			v.addf("token usage %s has no reported counts", u.ID)
		}
		if u.APICallCount != nil && *u.APICallCount < 0 {
			v.addf("token usage %s has api_call_count %d, want a non-negative count", u.ID, *u.APICallCount)
		}
		if u.TokenBasis != "" && !u.TokenBasis.Valid() {
			v.addf("token usage %s has unknown token basis %q", u.ID, u.TokenBasis)
		}
	}
}

// vendorIDs enforces §6 rule 2 — one vendor identifier maps to at most one of
// our ids of a given type — scoped to a session rather than to the whole ingest.
// A Claude prompt id appears on the subagent transcripts a turn spawned as well
// as on the turn itself, so one id legitimately names an ask in each of them.
//
// It is not a database constraint either: a forked session copies its origin's
// lines verbatim, so a unique index would refuse the second session outright.
func (v *validator) vendorIDs(g *Graph, byType map[EntityType]map[string]string) {
	type ref struct {
		session          string
		vendor, kind, id string
		our              EntityType
	}
	claimed := map[ref]string{}

	for _, m := range g.VendorIDs {
		if !m.OurType.Valid() {
			v.addf("vendor mapping for %s has unknown entity type %q", m.OurID, m.OurType)
			continue
		}
		if m.Vendor == "" || m.VendorIDKind == "" || m.VendorID == "" {
			v.addf("vendor mapping for %s (%s) is missing vendor, kind, or id", m.OurID, m.OurType)
		}
		if owner, known := byType[m.OurType][m.OurID]; !known {
			v.addf("vendor mapping names %s %s, which is not in the graph", m.OurType, m.OurID)
		} else if owner != m.SessionID {
			v.addf("vendor mapping for %s %s is filed under session %s but that entity is in %s",
				m.OurType, m.OurID, m.SessionID, owner)
		}
		at := ref{m.SessionID, m.Vendor, m.VendorIDKind, m.VendorID, m.OurType}
		if other, ok := claimed[at]; ok && other != m.OurID {
			v.addf("in session %s, vendor id %s/%s/%s maps to both %s %s and %s",
				m.SessionID, m.Vendor, m.VendorIDKind, m.VendorID, m.OurType, other, m.OurID)
		}
		claimed[at] = m.OurID
	}
}

// consequenceRef is the part of a consequence row the membership rules in §3.3
// apply to, regardless of which entity it is.
type consequenceRef struct {
	what        string
	id          string
	userInputID string
	sessionID   string
}

// consequence checks that a row hangs off a user input that exists and sits in
// the same session. The session check is the one that catches a nested session's
// row wired to its parent, which no foreign key would reject.
func (v *validator) consequence(c consequenceRef, userInputs map[string]bool, sessionByInput map[string]string) {
	if !userInputs[c.userInputID] {
		v.addf("%s %s names user input %s, which is not in the graph", c.what, c.id, c.userInputID)
		return
	}
	if got := sessionByInput[c.userInputID]; got != c.sessionID {
		v.addf("%s %s is in session %s but its user input is in %s", c.what, c.id, c.sessionID, got)
	}
}

// sessionByInput maps each user input id to the session that owns it.
func (v *validator) sessionByInput(g *Graph) map[string]string {
	out := make(map[string]string, len(g.UserInputs))
	for _, u := range g.UserInputs {
		out[u.ID] = u.SessionID
	}
	return out
}

func (v *validator) jsonObject(raw json.RawMessage, format string, args ...any) {
	what := fmt.Sprintf(format, args...)
	if len(raw) == 0 {
		v.addf("%s is empty, want at least an empty JSON object", what)
		return
	}
	var into map[string]any
	if err := json.Unmarshal(raw, &into); err != nil {
		v.addf("%s is not a JSON object: %v", what, err)
	}
}

func (v *validator) jsonValue(raw json.RawMessage, format string, args ...any) {
	if len(raw) == 0 || !json.Valid(raw) {
		v.addf("%s is not valid JSON", fmt.Sprintf(format, args...))
	}
}
