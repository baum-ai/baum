package decisions

// The renderer turns a session tree into the compact, redacted transcript the
// extractor reads (ARCHITECTURE.md, "decisions").

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/linkage/sessioncorrelate"
	"github.com/baum-ai/baum/sessions/prompttext"
	"github.com/baum-ai/baum/sessions/session"
)

// Redactor masks secrets and identities in free text. bundle.Redactor
// satisfies it.
type Redactor interface {
	RedactString(ctx context.Context, s string) (string, int, error)
}

// Transcript is one root session reduced to what decision extraction needs.
type Transcript struct {
	SessionID string
	Turns     []Turn
}

// Turn is one root-session user input and everything kept from what it caused,
// including its nested sessions.
type Turn struct {
	// Ordinal is the stored UserInput.Ordinal. Decision citations use it, so
	// it is never renumbered; dropped inputs leave gaps.
	Ordinal int `json:"ordinal"`
	// Human is the cleaned prompt, empty when the input was not a human
	// prompt (a system notification, an injected skill body).
	Human  string  `json:"-"`
	Events []Event `json:"-"`
	// EditedFiles are the repo-relative paths edited during the turn, sorted.
	// Decision `files` citations are validated against this list.
	EditedFiles []string `json:"edited_files"`
}

// EventKind names the kinds of content kept from a turn.
type EventKind string

const (
	EventAgentText EventKind = "agent_text"
	EventQuestion  EventKind = "question"
	EventPlan      EventKind = "plan"
	EventDenial    EventKind = "denial"
	EventSubagent  EventKind = "subagent_report"
)

// PlanOutcome is what the human did with a proposed plan.
type PlanOutcome string

const (
	PlanApproved   PlanOutcome = "approved"
	PlanRejected   PlanOutcome = "rejected"
	PlanUnanswered PlanOutcome = "unanswered"
)

// Event is one kept item in a turn, in session order.
type Event struct {
	Kind EventKind
	// Text is the agent text, plan or subagent report.
	Text string
	// Tool is the denied tool's name.
	Tool      string
	Questions []Question
	// Discuss reports that the human chose to talk the questions over
	// instead of answering them.
	Discuss bool
	Outcome PlanOutcome
	// Feedback is what the human said when rejecting a plan, a question or a
	// tool call.
	Feedback string
}

// Question is one question the agent asked the human.
type Question struct {
	Text    string
	Options []string
	// Answer is empty when no answer was recorded. For Cursor the answer, if
	// any, is the next human prompt.
	Answer string
	// Picked reports that Answer is one (or, for multi-select, only) of the
	// offered options rather than free text.
	Picked bool
	// Note is what the human added to their choice.
	Note string
}

const (
	// userSaidMarker and rejectionReasonMarker precede the feedback in Claude
	// Code's rejection text. Recorded ExitPlanMode rejections use the first,
	// recorded Write rejections the second.
	userSaidMarker        = "the user said:\n"
	rejectionReasonMarker = "The user provided the following reason for the rejection:"
	// rejectionPrefix starts the result Claude Code records when the human
	// rejects a tool call.
	rejectionPrefix = "The user doesn't want to proceed with this tool use"
	// clarifyPrefix starts Claude Code's own text when the human chooses to
	// discuss an AskUserQuestion instead of answering it.
	clarifyPrefix = "The user wants to clarify these questions."
	// compactionPrefix starts the summary Claude Code writes as a prompt when
	// it compacts a conversation; the agent wrote it, not the human.
	compactionPrefix = "This session is being continued from a previous conversation"
)

var (
	commandName = regexp.MustCompile(`(?s)<command-name>(.*?)</command-name>`)
	commandArgs = regexp.MustCompile(`(?s)<command-args>(.*?)</command-args>`)
	taskID      = regexp.MustCompile(`(?s)<task-id>(.*?)</task-id>`)
	taskResult  = regexp.MustCompile(`(?s)<result>(.*?)</result>`)
	// bashMode marks a Claude Code `!` command and its output, recorded as a
	// prompt. Raw command lines and output never enter the transcript.
	bashMode = regexp.MustCompile(`<bash-(?:input|stdout|stderr)>`)
	// homeOwner captures the account name of a recorded path that starts in a
	// home directory.
	homeOwner = regexp.MustCompile(`^(?:[A-Za-z]:)?[/\\](?:home|Users)[/\\]([^/\\]+)`)
	// localPath is an absolute path under a filesystem root that is never part
	// of a repository: home and temporary directories, mounts, Windows drives.
	// Paths inside the repository have already been made relative. Group 1
	// keeps the character before the path, which must not be part of a word
	// or URL. Punctuation ending the match is restored by maskLocalPaths.
	localPath = regexp.MustCompile(`(^|[\s"'(\[=,;:])(?:~/|/(?:home|Users|tmp|private|var|opt|mnt|Volumes|workspaces?|root|srv|nix)\b|[A-Za-z]:\\)[^\s"'<>()\[\]` + "`" + `]*`)
)

// Option configures Render.
type Option func(*settings)

type settings struct {
	vendor   string
	files    []sessioncorrelate.FileEvidence
	filesSet bool
}

// WithVendor selects the vendor's attribute spellings for working
// directories, as for sessioncorrelate.Correlate.
func WithVendor(vendor string) Option {
	return func(s *settings) { s.vendor = vendor }
}

// WithSessionFiles supplies the session's file evidence as
// sessioncorrelate.Correlate made it: repo-relative files for the session's
// repository, possibly built with evidence the graph does not hold, such as
// Entire's files_touched. They anchor relativization, and an edit counts only when
// Correlate also reports it as an edit, so files in another repository and
// paths Correlate could not prove stay out. A path a working directory cut
// short takes the spelling of the one stored edit it names (storedEdit).
func WithSessionFiles(files ...sessioncorrelate.FileEvidence) Option {
	return func(s *settings) { s.files, s.filesSet = append(s.files, files...), true }
}

// Render builds the transcript of graph's root session. Text is redacted with
// redactor after local paths are made repo-relative.
func Render(ctx context.Context, graph *session.Graph, redactor Redactor, opts ...Option) (*Transcript, error) {
	r, err := NewTree(graph, opts...)
	if err != nil {
		return nil, fmt.Errorf("render session: %w", err)
	}
	root := graph.Root()
	transcript := &Transcript{SessionID: root.ID}
	for _, input := range graph.UserInputs {
		if input.SessionID != root.ID {
			continue
		}
		turn, keep := r.turn(input)
		if !keep {
			continue
		}
		if err := r.redactTurn(ctx, redactor, &turn); err != nil {
			return nil, fmt.Errorf("render session %s turn %d: %w", root.ID, input.Ordinal, err)
		}
		transcript.Turns = append(transcript.Turns, turn)
	}
	return transcript, nil
}

// Tree is a session tree prepared for rendering: its paths resolved against
// the tree's roots and working directories, the account names its paths
// reveal, the calls the human rejected, and the order of its rows. Render
// builds the decisions transcript from one. Its exported methods let another
// render of the same tree, such as a validation transcript, share
// these rules rather than copy them.
type Tree struct {
	graph  *session.Graph
	rootID string
	paths  *sessioncorrelate.PathResolver
	dirs   []string
	// names matches the account names in the tree's recorded paths, wherever
	// they appear in text.
	names *regexp.Regexp
	// owner maps every session to the root input that caused it: a nested
	// session's rows belong to the turn of the spawn that started it.
	owner        map[string]string
	childOfSpawn map[string]string
	results      map[string]*session.ToolResult
	invocation   map[string]int
	// agentTasks are the Claude Code subagent ids in the tree. A task
	// notification for one of them carries an async subagent's report;
	// notifications for other tasks (background shells) carry tool output.
	agentTasks map[string]bool
	// notified maps the subagent ids whose report a task notification
	// carries to that report.
	notified map[string]string
	// edits restricts edited files to Correlate's stored edits when the
	// session's file evidence was supplied, and supplies their repo-relative
	// spelling (storedEdit). Agent worktree prefixes are already removed.
	edits []string
	// rejected are the tool calls the human rejected (denial). Their file
	// touches record what the agent proposed, not what it wrote.
	rejected map[string]bool
	position map[string]sourcePosition
}

// sourcePosition is where a row was read in its vendor file, from its vendor
// ids. It orders rows whose timestamps are missing.
type sourcePosition struct {
	file        string
	line, block int
}

// NewTree prepares graph for rendering. It fails when graph has no root
// session.
func NewTree(graph *session.Graph, opts ...Option) (*Tree, error) {
	if graph == nil || graph.Root() == nil {
		return nil, fmt.Errorf("graph has no root session")
	}
	var s settings
	for _, opt := range opts {
		opt(&s)
	}
	return newTree(graph, graph.Root().ID, s), nil
}

func newTree(graph *session.Graph, rootID string, s settings) *Tree {
	var anchors []string
	for _, file := range s.files {
		anchors = append(anchors, file.Path)
	}
	r := &Tree{
		graph: graph, rootID: rootID,
		paths:        sessioncorrelate.NewPathResolver(graph, s.vendor, sessioncorrelate.WithFileAnchors(anchors...)),
		owner:        map[string]string{},
		childOfSpawn: map[string]string{},
		results:      map[string]*session.ToolResult{},
		invocation:   map[string]int{},
		agentTasks:   map[string]bool{},
		notified:     map[string]string{},
		rejected:     map[string]bool{},
		position:     sourcePositions(graph.VendorIDs),
		names:        namesPattern(localNames(graph)),
	}
	if s.filesSet {
		r.edits = []string{}
		for _, file := range s.files {
			if file.Kind != session.TouchEdit {
				continue
			}
			if path, ok := sessioncorrelate.WithoutAgentWorktree(file.Path); ok {
				r.edits = append(r.edits, path)
			}
		}
	}
	// Roots come first, longest first, then working directories, so a path
	// under a recorded root reads repo-relative, as in the edited-file list.
	// Text is cut by these directories alone: a path a subdirectory cwd cut
	// short reads src/x.ts even when storedEdit lists it as web/src/x.ts.
	// Decision files are cited from the edited-file list, and
	// ValidateCitations drops any other spelling.
	for _, group := range [][]string{r.paths.Roots(), r.paths.Dirs()} {
		var dirs []string
		for _, dir := range group {
			// A shallow cwd such as "/" or "/repo" would rewrite unrelated text.
			if strings.Count(strings.TrimSuffix(dir, "/"), "/") >= 2 {
				dirs = append(dirs, strings.TrimSuffix(dir, "/"))
			}
		}
		sort.SliceStable(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
		r.dirs = append(r.dirs, dirs...)
	}
	spawnInput := map[string]string{}
	for _, use := range graph.ToolUses {
		spawnInput[use.ID] = use.UserInputID
	}
	// Sessions are ordered parent-first, so a parent's owner is known first.
	for _, s := range graph.Sessions {
		if s.SpawnedByToolUseID == nil {
			continue
		}
		r.childOfSpawn[*s.SpawnedByToolUseID] = s.ID
		input := spawnInput[*s.SpawnedByToolUseID]
		if s.ParentSessionID != nil && *s.ParentSessionID != rootID {
			input = r.owner[*s.ParentSessionID]
		}
		r.owner[s.ID] = input
	}
	for i := range graph.ToolResults {
		r.results[graph.ToolResults[i].ToolUseID] = &graph.ToolResults[i]
	}
	for _, use := range graph.ToolUses {
		if id := claudeAgent(use, r.results[use.ID]).AgentID; id != "" {
			r.agentTasks[id] = true
		}
		if _, denied := denial(use, r.results[use.ID]); denied {
			r.rejected[use.ID] = true
		}
	}
	for _, input := range graph.UserInputs {
		if report := r.notificationReport(input); input.SessionID == rootID && report != "" {
			r.notified[firstGroup(taskID, input.Text)] = report
		}
	}
	for i, invocation := range graph.ModelInvocations {
		r.invocation[invocation.ID] = i
	}
	return r
}

// turnOf returns the root input a row with this session and input belongs to:
// the input itself in the root session, else the root input whose call
// spawned the row's nested session (whatever inputID is).
func (r *Tree) turnOf(sessionID, inputID string) string {
	if sessionID == r.rootID {
		return inputID
	}
	return r.owner[sessionID]
}

type ordered struct {
	id         string
	timestamp  *time.Time
	invocation int
	// rank puts a reply's text before its tool calls when nothing else
	// orders them.
	rank, index int
	event       Event
}

func (r *Tree) turn(input session.UserInput) (Turn, bool) {
	turn := Turn{Ordinal: input.Ordinal, Human: HumanText(input), EditedFiles: []string{}}
	if report := r.notificationReport(input); report != "" {
		turn.Events = append(turn.Events, Event{Kind: EventSubagent, Text: report})
	}
	var items []ordered
	for i, message := range r.graph.AssistantMessages {
		if message.SessionID != r.rootID || message.UserInputID != input.ID || message.Text == nil {
			continue
		}
		if text := strings.TrimSpace(*message.Text); text != "" {
			items = append(items, ordered{message.ID, message.Timestamp, r.invocation[message.ModelInvocationID], 0, i, Event{Kind: EventAgentText, Text: text}})
		}
	}
	for i, use := range r.graph.ToolUses {
		if use.SessionID != r.rootID || use.UserInputID != input.ID {
			continue
		}
		if event, ok := r.ToolEvent(use); ok {
			items = append(items, ordered{use.ID, use.Timestamp, r.invocation[use.ModelInvocationID], 1, i, event})
		}
	}
	r.sortItems(items)
	for _, item := range items {
		turn.Events = append(turn.Events, item.event)
	}
	turn.EditedFiles = r.editedFiles(input.ID)
	return turn, turn.Human != "" || len(turn.Events) > 0 || len(turn.EditedFiles) > 0
}

// sortItems orders a turn's rows by where they were read from the vendor
// file when every row has that position, else by timestamp when every row has
// one, else by model invocation. Stored rows without timestamps come back
// from the database in id order, which is no order at all.
func (r *Tree) sortItems(items []ordered) {
	sortInOrder(r, items, func(item ordered) ordered { return item })
}

// sortInOrder is sortItems over any row type, ordered by its key.
func sortInOrder[T any](r *Tree, items []T, key func(T) ordered) {
	byPosition, byTime := true, true
	for _, item := range items {
		position, ok := r.position[key(item).id]
		byPosition = byPosition && ok && position.file == r.position[key(items[0]).id].file
		byTime = byTime && key(item).timestamp != nil
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := key(items[i]), key(items[j])
		switch {
		case byPosition:
			pa, pb := r.position[a.id], r.position[b.id]
			if pa.line != pb.line {
				return pa.line < pb.line
			}
			if pa.block != pb.block {
				return pa.block < pb.block
			}
		case byTime:
			if !a.timestamp.Equal(*b.timestamp) {
				return a.timestamp.Before(*b.timestamp)
			}
		default:
			if a.invocation != b.invocation {
				return a.invocation < b.invocation
			}
		}
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		return a.index < b.index
	})
}

// sourcePositions reads "<file>#<line>" source refs and the block index of
// block-qualified vendor ids ("<id>:<index>"), keeping each row's earliest.
func sourcePositions(ids []session.VendorID) map[string]sourcePosition {
	positions := map[string]sourcePosition{}
	for _, id := range ids {
		if id.SourceRef == nil {
			continue
		}
		file, lineText, ok := strings.Cut(*id.SourceRef, "#")
		line, err := strconv.Atoi(lineText)
		if !ok || err != nil {
			continue
		}
		block := 0
		if strings.HasSuffix(id.VendorIDKind, ".block") {
			if i := strings.LastIndex(id.VendorID, ":"); i >= 0 {
				block, _ = strconv.Atoi(id.VendorID[i+1:])
			}
		}
		position := sourcePosition{file: file, line: line, block: block}
		if known, seen := positions[id.OurID]; !seen || line < known.line || (line == known.line && block < known.block) {
			positions[id.OurID] = position
		}
	}
	return positions
}

func (r *Tree) editedFiles(inputID string) []string {
	seen := map[string]bool{}
	files := []string{}
	for _, touch := range r.graph.FileTouches {
		if r.turnOf(touch.SessionID, touch.UserInputID) != inputID {
			continue
		}
		path, ok := r.EditedPath(touch)
		if !ok {
			continue
		}
		if !seen[path] {
			seen[path] = true
			files = append(files, path)
		}
	}
	sort.Strings(files)
	return files
}

// EditedPath is the repo-relative path an edit touch wrote, as the edited-file
// list shows it. It reports false for a read, for a touch of a call the human
// rejected (the mappers record the file the agent proposed to write), for a
// path nothing relativizes, and, when the session's file evidence was
// supplied, for a path that names no stored edit or several.
func (r *Tree) EditedPath(touch session.FileTouch) (string, bool) {
	if touch.Kind != session.TouchEdit || (touch.ToolUseID != nil && r.rejected[*touch.ToolUseID]) {
		return "", false
	}
	path, ok := r.paths.Touch(touch)
	if ok && r.edits != nil {
		path, ok = storedEdit(path, r.edits)
	}
	return path, ok
}

// storedEdit returns the stored edit that path names, in its stored
// spelling, or false when Correlate stored no such edit or path could name
// more than one. A path the agent's working directory cut short (src/x.ts
// for web/src/x.ts, when no repository root was recorded) names every stored
// edit it is a path-segment suffix of: one such edit is the file, several are
// ambiguous. A stored spelling equal to path wins over longer ones, so a
// root-level README.md stays itself when docs/README.md was also edited.
// Rows stored before roots were preferred can themselves be cut short; a
// shorter stored spelling still confirms path but supplies no better one.
func storedEdit(path string, stored []string) (string, bool) {
	longer, confirmed, ambiguous := "", false, false
	for _, spelling := range stored {
		switch {
		case spelling == path:
			return path, true
		case strings.HasSuffix(spelling, "/"+path):
			ambiguous = ambiguous || (longer != "" && longer != spelling)
			longer = spelling
		case strings.HasSuffix(path, "/"+spelling):
			confirmed = true
		}
	}
	switch {
	case ambiguous:
		return "", false
	case longer != "":
		return longer, true
	default:
		return path, confirmed
	}
}

// HumanText is the prompt a person typed, cleaned as the transcript shows it,
// or empty for inputs nobody typed.
func HumanText(input session.UserInput) string {
	if input.Origin != session.OriginHuman || isMeta(input.Metadata) || bashMode.MatchString(input.Text) ||
		strings.HasPrefix(strings.TrimSpace(input.Text), compactionPrefix) {
		return ""
	}
	switch input.Kind {
	case session.KindInterrupt:
		return "[interrupted the agent]"
	case session.KindSlashCommand:
		name := firstGroup(commandName, input.Text)
		if name == "" {
			return prompttext.Strip(input.Text)
		}
		return strings.TrimSpace(name + " " + firstGroup(commandArgs, input.Text))
	}
	return prompttext.Strip(input.Text)
}

func isMeta(metadata json.RawMessage) bool {
	var fields struct {
		IsMeta bool `json:"isMeta"`
	}
	return len(metadata) > 0 && json.Unmarshal(metadata, &fields) == nil && fields.IsMeta
}

func firstGroup(pattern *regexp.Regexp, text string) string {
	if match := pattern.FindStringSubmatch(text); match != nil {
		return strings.TrimSpace(match[1])
	}
	return ""
}

// ToolEvent is what the transcript keeps of a root-session tool call:
// question, plan or denial dialogue, or a subagent report. It reports false
// for a call the transcript drops.
func (r *Tree) ToolEvent(use session.ToolUse) (Event, bool) {
	result := r.results[use.ID]
	switch use.Name {
	case "AskUserQuestion", "request_user_input", "AskQuestion":
		return questionEvent(use, result), true
	case "ExitPlanMode", "CreatePlan":
		return planEvent(use, result)
	}
	if child, ok := r.childOfSpawn[use.ID]; ok {
		// An async subagent's report is shown where its notification arrived.
		if r.notified[claudeAgent(use, result).AgentID] != "" {
			return Event{}, false
		}
		return r.subagentEvent(child)
	}
	if ReportOnCall(use) {
		// The mapper put the subagent's report on its call
		// (session.SubagentReport); a notification that carries the same
		// report already shows it.
		text := strings.TrimSpace(resultText(result))
		if text == "" || result.Status != session.StatusOK || r.notified[claudeAgent(use, result).AgentID] == text {
			return Event{}, false
		}
		return Event{Kind: EventSubagent, Text: text}, true
	}
	if event, ok := uncapturedSubagent(use, result); ok {
		return event, true
	}
	if feedback, denied := denial(use, result); denied {
		return Event{Kind: EventDenial, Tool: use.Name, Feedback: feedback}, true
	}
	return Event{}, false
}

// subagentEvent keeps only a nested session's final report: its last visible
// assistant text, or, for a session that only delegated, the report of its
// latest nested session.
func (r *Tree) subagentEvent(childID string) (Event, bool) {
	var messages, spawns []ordered
	for i, message := range r.graph.AssistantMessages {
		if message.SessionID == childID && message.Text != nil && strings.TrimSpace(*message.Text) != "" {
			messages = append(messages, ordered{message.ID, message.Timestamp, r.invocation[message.ModelInvocationID], 0, i,
				Event{Kind: EventSubagent, Text: strings.TrimSpace(*message.Text)}})
		}
	}
	if len(messages) > 0 {
		r.sortItems(messages)
		return messages[len(messages)-1].event, true
	}
	for i, use := range r.graph.ToolUses {
		if child, ok := r.childOfSpawn[use.ID]; ok && use.SessionID == childID {
			spawns = append(spawns, ordered{use.ID, use.Timestamp, r.invocation[use.ModelInvocationID], 1, i, Event{Text: child}})
		}
	}
	r.sortItems(spawns)
	for _, spawn := range slices.Backward(spawns) {
		if event, ok := r.subagentEvent(spawn.event.Text); ok {
			return event, true
		}
	}
	return Event{}, false
}

// uncapturedSubagent keeps a Claude Code subagent's report from the spawn
// tool's result when its transcript was not captured, as in Entire-delegated
// sessions where the spawn stays verb "other".
func uncapturedSubagent(use session.ToolUse, result *session.ToolResult) (Event, bool) {
	if (use.Name != "Agent" && use.Name != "Task") || result == nil || result.Status != session.StatusOK || result.OutputText == nil {
		return Event{}, false
	}
	// An async subagent's result holds only a status line; its report
	// arrives in a task notification (notificationReport).
	if claudeAgent(use, result).IsAsync {
		return Event{}, false
	}
	text := strings.TrimSpace(*result.OutputText)
	if text == "" || strings.HasPrefix(text, "Async agent launched") {
		return Event{}, false
	}
	return Event{Kind: EventSubagent, Text: text}, true
}

// ReportOnCall reports whether a spawning call's result is its subagent's
// final report (session.SubagentReport).
func ReportOnCall(use session.ToolUse) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(use.Metadata, &fields) != nil {
		return false
	}
	var detail session.SubagentDetail
	return json.Unmarshal(fields[session.SubagentDetailKey], &detail) == nil && detail == session.SubagentReport
}

type agentResult struct {
	AgentID string `json:"agentId"`
	IsAsync bool   `json:"isAsync"`
}

// claudeAgent reads the subagent fields Claude Code records on an Agent or
// Task tool result.
func claudeAgent(use session.ToolUse, result *session.ToolResult) agentResult {
	var fields agentResult
	if (use.Name == "Agent" || use.Name == "Task") && result != nil && len(result.Output) > 0 {
		_ = json.Unmarshal(result.Output, &fields)
	}
	return fields
}

// NotificationReport is the report in a task notification for one of the
// tree's Claude Code subagents, with the id of the call that started that
// subagent; both are empty for any other input.
func (r *Tree) NotificationReport(input session.UserInput) (report, spawnToolUseID string) {
	report = r.notificationReport(input)
	if report == "" {
		return "", ""
	}
	id := firstGroup(taskID, input.Text)
	for _, use := range r.graph.ToolUses {
		if claudeAgent(use, r.results[use.ID]).AgentID == id {
			return report, use.ID
		}
	}
	return report, ""
}

// notificationReport is the report in a task notification for one of the
// tree's subagents, or empty.
func (r *Tree) notificationReport(input session.UserInput) string {
	if !strings.HasPrefix(strings.TrimSpace(input.Text), "<task-notification>") {
		return ""
	}
	if !r.agentTasks[firstGroup(taskID, input.Text)] {
		return ""
	}
	return firstGroup(taskResult, input.Text)
}

// resultText is the error text of a failed result, else its output text.
func resultText(result *session.ToolResult) string {
	if result == nil {
		return ""
	}
	for _, text := range []*string{result.ErrorText, result.OutputText} {
		if text != nil && strings.TrimSpace(*text) != "" {
			return *text
		}
	}
	return ""
}

// denial reports whether the human rejected the tool call, with any feedback
// they gave. Only a failed call whose result starts with Claude Code's
// rejection text counts, so tool output that merely quotes it is not a
// denial. The tool's input is never kept: it can be a raw command line.
func denial(use session.ToolUse, result *session.ToolResult) (string, bool) {
	var metadata struct {
		UserDecision string `json:"userDecision"`
	}
	if len(use.Metadata) > 0 && json.Unmarshal(use.Metadata, &metadata) == nil && metadata.UserDecision == "rejected" {
		return "", true
	}
	if result == nil || (result.Status != session.StatusInterrupted && result.Status != session.StatusError) {
		return "", false
	}
	text := strings.TrimPrefix(strings.TrimSpace(resultText(result)), "Error: ")
	if !strings.HasPrefix(text, rejectionPrefix) {
		return "", false
	}
	// The first marker wins: feedback can itself quote the other one.
	start := -1
	for _, marker := range []string{userSaidMarker, rejectionReasonMarker} {
		if i := strings.Index(text, marker); i >= 0 && (start < 0 || i < start) {
			start = i + len(marker)
		}
	}
	if start < 0 {
		return "", true
	}
	return strings.TrimSpace(text[start:]), true
}

func planEvent(use session.ToolUse, result *session.ToolResult) (Event, bool) {
	var input struct {
		Plan string `json:"plan"`
	}
	_ = json.Unmarshal(use.Input, &input)
	event := Event{Kind: EventPlan, Text: strings.TrimSpace(input.Plan), Outcome: PlanUnanswered}
	if event.Text == "" {
		return Event{}, false
	}
	if feedback, denied := denial(use, result); denied {
		event.Outcome, event.Feedback = PlanRejected, feedback
	} else if strings.HasPrefix(resultText(result), "User has approved") {
		event.Outcome = PlanApproved
	}
	return event, true
}

type rawQuestion struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Prompt   string `json:"prompt"`
	Header   string `json:"header"`
	Options  []struct {
		Label string `json:"label"`
	} `json:"options"`
}

func questionEvent(use session.ToolUse, result *session.ToolResult) Event {
	var input struct {
		Questions []rawQuestion `json:"questions"`
	}
	_ = json.Unmarshal(use.Input, &input)
	answers, notes := recordedAnswers(result)
	event := Event{Kind: EventQuestion}
	for _, raw := range input.Questions {
		question := Question{Text: firstNonEmpty(raw.Question, raw.Prompt, raw.Header)}
		for _, option := range raw.Options {
			question.Options = append(question.Options, option.Label)
		}
		question.Answer = firstNonEmpty(answers[raw.Question], answers[question.Text], answers[raw.ID])
		question.Picked = picked(question.Answer, question.Options)
		question.Note = firstNonEmpty(notes[raw.Question], notes[question.Text])
		event.Questions = append(event.Questions, question)
	}
	if feedback, denied := denial(use, result); denied {
		if strings.HasPrefix(feedback, clarifyPrefix) {
			event.Discuss = true
		} else {
			event.Feedback = feedback
		}
	}
	return event
}

// recordedAnswers reads answers keyed by question text (Claude Code's
// toolUseResult.answers) or by question id (Codex request_user_input output),
// and the notes Claude Code records beside a choice.
func recordedAnswers(result *session.ToolResult) (map[string]string, map[string]string) {
	answers, notes := map[string]string{}, map[string]string{}
	if result == nil {
		return answers, notes
	}
	var claude struct {
		Answers     map[string]string `json:"answers"`
		Annotations map[string]struct {
			Notes string `json:"notes"`
		} `json:"annotations"`
	}
	if len(result.Output) > 0 && json.Unmarshal(result.Output, &claude) == nil {
		maps.Copy(answers, claude.Answers)
		for question, annotation := range claude.Annotations {
			notes[question] = annotation.Notes
		}
	}
	var codex struct {
		Answers map[string]struct {
			Answers []string `json:"answers"`
		} `json:"answers"`
	}
	if result.OutputText != nil && json.Unmarshal([]byte(*result.OutputText), &codex) == nil {
		for key, value := range codex.Answers {
			answers[key] = strings.Join(value.Answers, ", ")
		}
	}
	return answers, notes
}

// picked reports whether answer names only offered options. Multi-select
// answers join labels with ", ", which a label may itself contain, so the
// answer must split into whole labels.
func picked(answer string, options []string) bool {
	if answer == "" {
		return false
	}
	for _, option := range options {
		if option == "" {
			continue
		}
		if answer == option {
			return true
		}
		if rest, ok := strings.CutPrefix(answer, option+", "); ok && picked(rest, options) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (r *Tree) redactTurn(ctx context.Context, redactor Redactor, turn *Turn) error {
	clean := func(text *string) error {
		if *text == "" {
			return nil
		}
		redacted, _, err := redactor.RedactString(ctx, r.ScrubPaths(*text))
		if err != nil {
			return err
		}
		*text = redacted
		return nil
	}
	if err := clean(&turn.Human); err != nil {
		return err
	}
	for i := range turn.Events {
		event := &turn.Events[i]
		for _, text := range []*string{&event.Text, &event.Feedback, &event.Tool} {
			if err := clean(text); err != nil {
				return err
			}
		}
		for j := range event.Questions {
			question := &event.Questions[j]
			if err := clean(&question.Text); err != nil {
				return err
			}
			if err := clean(&question.Answer); err != nil {
				return err
			}
			if err := clean(&question.Note); err != nil {
				return err
			}
			for k := range question.Options {
				if err := clean(&question.Options[k]); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ScrubPaths rewrites the tree's roots and working directories to
// repo-relative form, masks every other local path, and masks the account
// names the tree's own paths reveal where they are joined to other text: a
// dashed project directory, a hostname, a branch. It masks paths and account
// names only and redacts no secrets or e-mail addresses: its output must
// still go through a Redactor, as Render does, before it is shown or sent.
func (r *Tree) ScrubPaths(text string) string {
	for _, dir := range r.dirs {
		text = strings.ReplaceAll(text, dir+"/", "")
	}
	text = maskLocalPaths(text)
	return maskJoinedNames(r.names, text, "user")
}

// maskLocalPaths replaces each local path with "<local path>", keeping the
// character before it and any punctuation that ends the sentence or list
// after it.
func maskLocalPaths(text string) string {
	return localPath.ReplaceAllStringFunc(text, func(match string) string {
		groups := localPath.FindStringSubmatch(match)
		path := strings.TrimPrefix(match, groups[1])
		trimmed := strings.TrimRight(path, ".,;:!?")
		return groups[1] + "<local path>" + path[len(trimmed):]
	})
}

// genericAccounts are account names too common as words to mask.
var genericAccounts = map[string]bool{
	"dev": true, "user": true, "admin": true, "root": true, "runner": true,
	"ubuntu": true, "app": true, "node": true, "vscode": true, "shared": true,
}

// localNames are the account names in the tree's recorded paths that start
// in a home directory: working directories, roots, cwd history and file
// touches. A name with spaces also yields its parts.
func localNames(graph *session.Graph) []string {
	seen := map[string]bool{}
	var names []string
	add := func(path string) {
		match := homeOwner.FindStringSubmatch(path)
		if match == nil {
			return
		}
		for _, name := range append(strings.Fields(match[1]), match[1]) {
			if key := strings.ToLower(name); len(key) >= 2 && !genericAccounts[key] && !seen[key] {
				seen[key] = true
				names = append(names, name)
			}
		}
	}
	for _, s := range graph.Sessions {
		for _, path := range []*string{s.WorkingDir, s.RepoRoot, s.WorktreeRoot} {
			if path != nil {
				add(*path)
			}
		}
	}
	for _, attribute := range graph.Attributes {
		var value string
		if attribute.Key == "cwd" && json.Unmarshal(attribute.Value, &value) == nil {
			add(value)
		}
	}
	for _, touch := range graph.FileTouches {
		add(touch.Path)
	}
	return names
}

// namesPattern matches any of names as a whole word, ignoring case, with the
// path, host or branch punctuation on either side in groups 1 and 2.
func namesPattern(names []string) *regexp.Regexp {
	if len(names) == 0 {
		return nil
	}
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = regexp.QuoteMeta(name)
	}
	return regexp.MustCompile(`(?i)([-/._@\\]?)\b(?:` + strings.Join(quoted, "|") + `)\b([-/._@\\]?)`)
}

// maskJoinedNames replaces a name only where it is joined to punctuation
// (alice.example.dev, -Users-alice-, alice/feature). A name standing alone
// in prose is a word, not an identifier: account names such as "will" or
// "max" are ordinary words.
func maskJoinedNames(names *regexp.Regexp, text, mask string) string {
	if names == nil {
		return text
	}
	return names.ReplaceAllStringFunc(text, func(match string) string {
		groups := names.FindStringSubmatch(match)
		if groups[1] == "" && groups[2] == "" {
			return match
		}
		return groups[1] + mask + groups[2]
	})
}

// Result is the result of a tool call, or nil when it has none.
func (r *Tree) Result(toolUseID string) *session.ToolResult {
	return r.results[toolUseID]
}

// SpawnedSession is the nested session a tool call spawned.
func (r *Tree) SpawnedSession(toolUseID string) (string, bool) {
	id, ok := r.childOfSpawn[toolUseID]
	return id, ok
}

// Row is one assistant message or tool call to put in session order. Exactly
// one of Message and ToolUse is set. Index is the row's position in the
// graph's AssistantMessages or ToolUses; it breaks ties.
type Row struct {
	Message *session.AssistantMessage
	ToolUse *session.ToolUse
	Index   int
}

// Timestamp is the message's or tool call's timestamp, nil when unknown or
// when the row has neither.
func (row Row) Timestamp() *time.Time {
	switch {
	case row.Message != nil:
		return row.Message.Timestamp
	case row.ToolUse != nil:
		return row.ToolUse.Timestamp
	}
	return nil
}

// SortRows puts rows in session order as the transcript orders a turn: by
// position in the vendor file when every row has one, else by timestamp when
// every row has one, else by model invocation; a message comes before a tool
// call when nothing else orders them. The order depends on which rows are
// given, so sort exactly the rows that will be shown.
func (r *Tree) SortRows(rows []Row) {
	sortInOrder(r, rows, func(row Row) ordered {
		if row.Message != nil {
			return ordered{id: row.Message.ID, timestamp: row.Message.Timestamp, invocation: r.invocation[row.Message.ModelInvocationID], index: row.Index}
		}
		return ordered{id: row.ToolUse.ID, timestamp: row.ToolUse.Timestamp, invocation: r.invocation[row.ToolUse.ModelInvocationID], rank: 1, index: row.Index}
	})
}
