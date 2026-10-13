package locallink

import (
	"cmp"
	"context"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/lastpersonlabs/baum/bundle"
	"github.com/lastpersonlabs/baum/internal/gitlocal"
	"github.com/lastpersonlabs/baum/linkage/sessioncorrelate"
	"github.com/lastpersonlabs/baum/sessions"
	"github.com/lastpersonlabs/baum/sessions/session"
)

// EvidenceRequest says which pull request commits BuildSessionEvidence links
// sessions to, where it looks for them, and how much of each it keeps.
type EvidenceRequest struct {
	// Dir is the repository root or any directory inside the clone; ""
	// means the current directory.
	Dir string
	// Commits are the pull request's commits as the clone holds them, oldest
	// first: gitlocal.Repo.Commits from the merge base to the head.
	Commits []gitlocal.Commit
	// Sessions narrows or redirects the stores read. Its PreviousPaths are
	// the directories the repository used to live at (--previous-path).
	Sessions sessions.Options
	// MaxPromptsPerSession and MaxCommandEventsPerSession are the bundle caps
	// of the same names, which the source applies: a session keeps its first
	// prompt and its latest MaxPromptsPerSession-1 (none at 0), and its
	// latest MaxCommandEventsPerSession command events (all at 0).
	MaxPromptsPerSession, MaxCommandEventsPerSession int
	// Warn receives one line for each session store file or checkpoint that
	// could not be read. It defaults to discarding them.
	Warn io.Writer
}

// BuildSessionEvidence returns the bundle sessions for the pull request
// commits: every session on this machine matched to one of them, as Resolve
// matches them, except baum's own generation runs (sessions.IsGenerationRun).
// They are ordered by linkage confidence, the highest any of the session's
// edges has, then by start, earliest first and unknown last, then by id.
//
// The sessions are not redacted: the bundle's input redaction (bundle.Redact)
// masks them with the rest of the bundle before it is trimmed or written, as
// for every other source. A command event carries only the paraphrase
// bundle.ParseCommandParaphrase gives and a touched path only a
// repository-relative one, so no raw command line, tool output or absolute
// path is returned.
func BuildSessionEvidence(ctx context.Context, req EvidenceRequest) ([]bundle.Session, error) {
	evidence := []bundle.Session{}
	if len(req.Commits) == 0 {
		return evidence, nil
	}
	dir := req.Dir
	if dir == "" {
		dir = "."
	}
	found, truth, err := loadSessions(ctx, dir, req.Sessions, req.Commits, req.Warn)
	if err != nil {
		return nil, err
	}
	found, truth = withoutGenerationRuns(found, truth)
	ids, latest, edges, err := linkSessions(ctx, found, req.Commits, truth)
	if err != nil {
		return nil, err
	}
	confidence := map[string]float64{}
	for _, edge := range edges {
		if current, seen := confidence[edge.RunID]; !seen || edge.Confidence > current {
			confidence[edge.RunID] = edge.Confidence
		}
	}
	for _, id := range ids {
		linked, ok := confidence[id]
		if !ok {
			continue
		}
		snap := latest[id]
		if item, ok := sessionEvidence(snap.session, snap.correlated, linked, req.MaxPromptsPerSession, req.MaxCommandEventsPerSession); ok {
			evidence = append(evidence, item)
		}
	}
	slices.SortStableFunc(evidence, func(a, b bundle.Session) int {
		if a.Confidence != b.Confidence {
			return cmp.Compare(b.Confidence, a.Confidence)
		}
		if a.HasStartedAt != b.HasStartedAt {
			if a.HasStartedAt {
				return -1
			}
			return 1
		}
		if c := a.StartedAtValue.Compare(b.StartedAtValue); c != 0 {
			return c
		}
		return strings.Compare(a.RootSessionID, b.RootSessionID)
	})
	return evidence, nil
}

// withoutGenerationRuns drops baum's own generation runs before matching, so
// they can neither be linked nor take a link from another session, and from
// the checkpoints' ground truth. A session any reading of which is marked is
// dropped whole.
func withoutGenerationRuns(found []sessions.Session, truth map[string][]string) ([]sessions.Session, map[string][]string) {
	marked := map[string]bool{}
	for _, s := range found {
		if sessions.IsGenerationRun(s.Graph) {
			marked[s.ID] = true
		}
	}
	if len(marked) == 0 {
		return found, truth
	}
	kept := slices.DeleteFunc(slices.Clone(found), func(s sessions.Session) bool { return marked[s.ID] })
	pruned := make(map[string][]string, len(truth))
	for commit, ids := range truth {
		if ids = slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return marked[id] }); len(ids) > 0 {
			pruned[commit] = ids
		}
	}
	return kept, pruned
}

// sessionEvidence projects one linked session tree into its bundle session.
// Prompts are the top-level session's human prompts; nested sessions' file
// touches count toward the root prompt that spawned them, at any depth, and
// command events are taken from the whole tree. ok is false for a graph
// without a top-level session.
func sessionEvidence(s sessions.Session, correlated sessioncorrelate.Result, confidence float64, maxPrompts, maxCommandEvents int) (bundle.Session, bool) {
	root := s.Root()
	if root == nil {
		return bundle.Session{}, false
	}
	graph := s.Graph
	item := bundle.Session{
		RootSessionID: s.ID, Vendor: s.Vendor, Outcome: string(root.Outcome),
		Models: models(graph), Confidence: confidence,
	}
	if root.Title != nil {
		item.Title = *root.Title
	}
	if root.GitBranch != nil {
		item.Branch = *root.GitBranch
	}
	if item.Branch == "" && len(correlated.Branches) > 0 {
		item.Branch = correlated.Branches[0]
	}
	if root.StartedAt != nil {
		started := *root.StartedAt
		item.StartedAt, item.HasStartedAt, item.StartedAtValue = &started, true, started
	}
	if root.EndedAt != nil {
		ended := *root.EndedAt
		item.EndedAt = &ended
	}
	item.Prompts = prompts(graph, root.ID, maxPrompts)
	attachTouchedPaths(graph, s.Vendor, item.Prompts)
	item.CommandEvents = commandEvents(graph, maxCommandEvents)
	return item, true
}

// models are the distinct models the tree's invocations name, sorted.
func models(graph *session.Graph) []string {
	var names []string
	for _, invocation := range graph.ModelInvocations {
		if invocation.Model != nil && *invocation.Model != "" && !slices.Contains(names, *invocation.Model) {
			names = append(names, *invocation.Model)
		}
	}
	slices.Sort(names)
	return names
}

// prompts are the top-level session's human prompts and slash commands with
// text, in order: the first, and the latest limit-1, each with the latest
// non-blank reply the model gave it. A limit of 0 or less keeps none.
func prompts(graph *session.Graph, rootID string, limit int) []bundle.Prompt {
	if limit <= 0 {
		return nil
	}
	var human []session.UserInput
	for _, input := range graph.UserInputs {
		if input.SessionID == rootID && input.Origin == session.OriginHuman &&
			(input.Kind == session.KindPrompt || input.Kind == session.KindSlashCommand) && strings.TrimSpace(input.Text) != "" {
			human = append(human, input)
		}
	}
	slices.SortStableFunc(human, func(a, b session.UserInput) int { return cmp.Compare(a.Ordinal, b.Ordinal) })
	var selected []bundle.Prompt
	for i, input := range human {
		if i != 0 && len(human)-i >= limit {
			continue
		}
		prompt := bundle.Prompt{Index: input.Ordinal, ID: input.ID, Text: input.Text, Reply: reply(graph, input.ID)}
		if input.TriggeredAt != nil {
			at := *input.TriggeredAt
			prompt.At = &at
		}
		selected = append(selected, prompt)
	}
	return selected
}

// reply is the latest non-blank assistant text attributed to the input.
func reply(graph *session.Graph, inputID string) string {
	var latest *session.AssistantMessage
	for i := range graph.AssistantMessages {
		message := &graph.AssistantMessages[i]
		if message.UserInputID != inputID || message.Text == nil || strings.TrimSpace(*message.Text) == "" {
			continue
		}
		if latest == nil || newerMessage(message, latest) {
			latest = message
		}
	}
	if latest == nil {
		return ""
	}
	return *latest.Text
}

// newerMessage orders messages by timestamp, an unknown one oldest, then id.
func newerMessage(a, b *session.AssistantMessage) bool {
	if c := compareTimes(a.Timestamp, b.Timestamp); c != 0 {
		return c > 0
	}
	return a.ID > b.ID
}

// attachTouchedPaths gives each prompt the distinct repository-relative paths
// read or edited under it, including by the sessions it spawned at any depth,
// sorted. A path that cannot be made repository-relative is left out.
func attachTouchedPaths(graph *session.Graph, vendor string, prompts []bundle.Prompt) {
	if len(prompts) == 0 {
		return
	}
	byID := make(map[string]*bundle.Prompt, len(prompts))
	for i := range prompts {
		byID[prompts[i].ID] = &prompts[i]
	}
	owners := newPromptOwners(graph)
	resolver := sessioncorrelate.NewPathResolver(graph, vendor)
	touched := map[string]map[string]bool{}
	for _, touch := range graph.FileTouches {
		if touch.Kind != session.TouchRead && touch.Kind != session.TouchEdit {
			continue
		}
		owner, ok := owners.of(touch.UserInputID)
		if !ok || byID[owner] == nil {
			continue
		}
		path, ok := resolver.Touch(touch)
		if !ok {
			continue
		}
		if touched[owner] == nil {
			touched[owner] = map[string]bool{}
		}
		touched[owner][path] = true
	}
	for owner, paths := range touched {
		prompt := byID[owner]
		for path := range paths {
			prompt.TouchedPaths = append(prompt.TouchedPaths, path)
		}
		slices.Sort(prompt.TouchedPaths)
	}
}

// promptOwners finds the top-level input a user input works for: a top-level
// session's input owns itself, and a nested session's inputs belong to the
// input that owns the tool use which spawned it.
type promptOwners struct {
	inputs   map[string]session.UserInput
	sessions map[string]session.Session
	toolUses map[string]session.ToolUse
	owner    map[string]string
	resolved map[string]bool
}

func newPromptOwners(graph *session.Graph) *promptOwners {
	owners := &promptOwners{
		inputs: map[string]session.UserInput{}, sessions: map[string]session.Session{}, toolUses: map[string]session.ToolUse{},
		owner: map[string]string{}, resolved: map[string]bool{},
	}
	for _, input := range graph.UserInputs {
		owners.inputs[input.ID] = input
	}
	for _, s := range graph.Sessions {
		owners.sessions[s.ID] = s
	}
	for _, use := range graph.ToolUses {
		owners.toolUses[use.ID] = use
	}
	return owners
}

// of returns the top-level input that owns inputID, and false when the chain
// of spawns does not reach a top-level session.
func (o *promptOwners) of(inputID string) (string, bool) {
	input, ok := o.inputs[inputID]
	if !ok {
		return "", false
	}
	sessionOwner, ok := o.sessionOwner(input.SessionID, 0)
	if !ok {
		return "", false
	}
	if sessionOwner == "" {
		return input.ID, true
	}
	return sessionOwner, true
}

// sessionOwner is "" for a top-level session and the owning top-level input
// for a nested one. depth guards against a malformed cycle of spawns.
func (o *promptOwners) sessionOwner(sessionID string, depth int) (string, bool) {
	if o.resolved[sessionID] {
		owner, ok := o.owner[sessionID]
		return owner, ok
	}
	s, ok := o.sessions[sessionID]
	if !ok || depth > len(o.sessions) {
		return "", false
	}
	owner, found := "", true
	if !s.TopLevel() {
		spawn, known := o.toolUses[derefString(s.SpawnedByToolUseID)]
		if !known || spawn.SessionID != *s.ParentSessionID {
			found = false
		} else if parentOwner, ok := o.sessionOwner(*s.ParentSessionID, depth+1); !ok {
			found = false
		} else if parentOwner != "" {
			owner = parentOwner
		} else {
			owner = spawn.UserInputID
		}
	}
	o.resolved[sessionID] = true
	if found {
		o.owner[sessionID] = owner
	}
	return owner, found
}

// commandEvents are the tree's execute tool uses that carry a command line,
// paraphrased: the latest limit of them (all when limit is 0 or less), in
// time order, each with the outcome its tool result records ("unknown"
// without one).
func commandEvents(graph *session.Graph, limit int) []bundle.CommandEvent {
	type call struct {
		use  session.ToolUse
		text string
	}
	var calls []call
	for _, use := range graph.ToolUses {
		if use.Verb != session.VerbExecute {
			continue
		}
		if text := bundle.CommandText(use.Input); text != "" {
			calls = append(calls, call{use: use, text: text})
		}
	}
	if limit > 0 && len(calls) > limit {
		// Newest first, an unknown time oldest, to pick the latest.
		slices.SortStableFunc(calls, func(a, b call) int {
			if c := compareTimes(b.use.Timestamp, a.use.Timestamp); c != 0 {
				return c
			}
			return strings.Compare(b.use.ID, a.use.ID)
		})
		calls = calls[:limit]
	}
	// Time order, an unknown time last.
	slices.SortStableFunc(calls, func(a, b call) int {
		if (a.use.Timestamp == nil) != (b.use.Timestamp == nil) {
			if a.use.Timestamp == nil {
				return 1
			}
			return -1
		}
		if a.use.Timestamp != nil {
			if c := a.use.Timestamp.Compare(*b.use.Timestamp); c != 0 {
				return c
			}
		}
		return strings.Compare(a.use.ID, b.use.ID)
	})
	statuses := map[string]session.ToolStatus{}
	for _, result := range graph.ToolResults {
		if _, seen := statuses[result.ToolUseID]; !seen {
			statuses[result.ToolUseID] = result.Status
		}
	}
	events := make([]bundle.CommandEvent, 0, len(calls))
	for _, c := range calls {
		event := bundle.CommandEvent{
			ID: c.use.ID, Outcome: bundle.CommandOutcome(string(statuses[c.use.ID])),
			Commands: bundle.ParseCommandParaphrase(c.text),
		}
		if c.use.Timestamp != nil {
			at := *c.use.Timestamp
			event.Time = &at
		}
		events = append(events, event)
	}
	return events
}

// compareTimes orders two optional times, an unknown one before any known one.
func compareTimes(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	return a.Compare(*b)
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
