// Package transcript shapes one session graph into the transcript the
// viewer's session card draws as a conversation: the session's human prompts
// in order, what each of them caused and the agent's text replies to it, and
// per prompt the agent activity (its messages, model calls and tool calls
// with their input and output) that the hosted service serves one prompt at a
// time and baum view serves in the same answer.
//
// It bounds the result, so a session of many megabytes does not stall the
// page drawing it: every text is cut to a limit, each prompt lists a limited
// number of messages and calls, and once the encoded transcript reaches its
// byte budget nothing after it is listed. Each cut is reported, so the reader
// is told. It reads the graph as recorded and passes each text it keeps,
// before cutting it, through the caller's Clean (a redactor), so a secret is
// never cut in two where the redactor can no longer see it; the caller
// redacts the encoded result as a whole as well.
//
// The prompt and ownership rules are the hosted service's: a prompt is a
// top-level user input of kind prompt or slash command from a human with text
// left once the wrapper constructs are stripped (prompttext), and a nested
// session's activity belongs to the prompt whose tool call spawned it.
// Activity under any other input belongs to the session and to no prompt.
package transcript

import (
	"bytes"
	"cmp"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lastpersonlabs/baum/sessions/prompttext"
	"github.com/lastpersonlabs/baum/sessions/session"
)

// Limits bound a transcript. Text limits are in bytes, cut at a character.
type Limits struct {
	// Bytes is the budget for the transcript as Encode writes it.
	Bytes int
	// Prompt, Message and Tool cut one prompt, one agent message, and one
	// tool call's input or output.
	Prompt, Message, Tool int
	// Messages and ToolCalls cap how many of each one prompt's activity
	// lists.
	Messages, ToolCalls int
	// Reply cuts the preview of one text reply, and Replies caps how many
	// replies (a prompt's last) a prompt carries. A reply's whole text, up to
	// Message, is among the activity's messages.
	Reply, Replies int
	// Field cuts each of the session's own texts (title, branch, working
	// directory, agent and model names), and Models caps how many model
	// names it lists, so the part of the transcript outside the prompts is
	// bounded too.
	Field, Models int
}

// DefaultLimits keeps a transcript to a few megabytes of JSON, which a
// browser parses and draws without a pause.
var DefaultLimits = Limits{
	Bytes:     4 << 20,
	Prompt:    32 << 10,
	Message:   16 << 10,
	Tool:      4 << 10,
	Messages:  100,
	ToolCalls: 200,
	Reply:     4 << 10,
	Replies:   10,
	Field:     1 << 10,
	Models:    50,
}

// Meta is what the graph does not say about a session: which agent ran it
// and how it is linked to the pull request.
type Meta struct {
	ID, Vendor, Branch string
	// Commits is how many of the pull request's commits it is linked to, and
	// Confidence the strongest of those links.
	Commits    int
	Confidence float64
}

// Transcript is the answer for one session. Its JSON names follow the
// viewer's transcript view model (oss/viewer/src/transcript.ts).
type Transcript struct {
	Session Session `json:"session"`
	// Activity is each listed prompt's activity, by its turn id.
	Activity map[string]Activity `json:"activity"`
	// Truncated says some prompts, messages, calls or text were left out or
	// cut; LimitBytes is the budget that was applied to the encoded size.
	Truncated  bool `json:"truncated"`
	LimitBytes int  `json:"limit_bytes"`
}

// Session is the session card's session.
type Session struct {
	RunID      string     `json:"run_id"`
	Source     string     `json:"source"`
	Title      string     `json:"title"`
	GitBranch  string     `json:"git_branch"`
	WorkingDir string     `json:"working_dir"`
	Models     []string   `json:"models"`
	StartedAt  *time.Time `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at"`
	Outcome    string     `json:"outcome"`
	// UserEmail is always empty: this machine does not say who typed.
	UserEmail        string   `json:"user_email"`
	TurnCount        int      `json:"turn_count"`
	CommitCount      int      `json:"commit_count"`
	Confidence       float64  `json:"confidence"`
	Prompts          []Prompt `json:"prompts"`
	PromptsTotal     int      `json:"prompts_total"`
	PromptsTruncated bool     `json:"prompts_truncated"`
	// RepliesIncluded is always true: every prompt carries its replies.
	RepliesIncluded bool     `json:"replies_included"`
	Totals          *Effects `json:"totals"`
	Unattributed    *Effects `json:"unattributed"`
	Tokens          int64    `json:"tokens"`
	EstimatedTokens int64    `json:"estimated_tokens"`
	// CostPerPrompt is false when usage was reported only for the whole
	// session; the name is the card's. No cost is ever given (CostUSD is
	// null and CostIsFloor false): this machine has no prices.
	CostPerPrompt bool     `json:"cost_per_prompt"`
	CostUSD       *float64 `json:"cost_usd"`
	CostIsFloor   bool     `json:"cost_is_floor"`
	HasInnerTrace bool     `json:"has_inner_trace"`
}

// Prompt is one human prompt.
type Prompt struct {
	TurnID          string     `json:"turn_id"`
	Ordinal         int        `json:"ordinal"`
	StartedAt       *time.Time `json:"started_at"`
	Text            string     `json:"text"`
	TextLength      int        `json:"text_length"`
	Truncated       bool       `json:"truncated"`
	Effects         *Effects   `json:"effects"`
	CostUSD         *float64   `json:"cost_usd"`
	CostIsFloor     bool       `json:"cost_is_floor"`
	Tokens          int64      `json:"tokens"`
	EstimatedTokens int64      `json:"estimated_tokens"`
	// Replies are the prompt's last text replies, oldest first, each cut to
	// Limits.Reply; RepliesOmitted counts the earlier ones left out.
	Replies        []Message `json:"replies"`
	RepliesOmitted int       `json:"replies_omitted"`
}

// Effects is what a prompt, or the session, caused.
type Effects struct {
	FilesRead    int `json:"files_read"`
	FilesEdited  int `json:"files_edited"`
	Reads        int `json:"reads"`
	FilesChanged int `json:"files_changed"`
	DiffHunks    int `json:"diff_hunks"`
	AddedLines   int `json:"added_lines"`
	RemovedLines int `json:"removed_lines"`
	ToolCalls    int `json:"tool_calls"`
	ExecuteCalls int `json:"execute_calls"`
	FailedCalls  int `json:"failed_calls"`
}

func (e *Effects) add(other Effects) {
	e.FilesRead += other.FilesRead
	e.FilesEdited += other.FilesEdited
	e.Reads += other.Reads
	e.FilesChanged += other.FilesChanged
	e.DiffHunks += other.DiffHunks
	e.AddedLines += other.AddedLines
	e.RemovedLines += other.RemovedLines
	e.ToolCalls += other.ToolCalls
	e.ExecuteCalls += other.ExecuteCalls
	e.FailedCalls += other.FailedCalls
}

// Activity is what the agent did for one prompt.
type Activity struct {
	Messages          []Message `json:"messages"`
	MessagesTruncated bool      `json:"messages_truncated"`
	// Invocations holds the first model call only, which is all the card
	// reads; InvocationsTotal counts them.
	Invocations        []Invocation `json:"invocations"`
	InvocationsTotal   int          `json:"invocations_total"`
	TokenUsage         TokenUsage   `json:"token_usage"`
	ToolCalls          []ToolCall   `json:"tool_calls"`
	ToolCallsTruncated bool         `json:"tool_calls_truncated"`
}

// Message is one agent message with text, or a prompt's reply.
type Message struct {
	Text       string     `json:"text"`
	TextLength int        `json:"text_length"`
	Truncated  bool       `json:"truncated"`
	Timestamp  *time.Time `json:"timestamp"`
	Model      string     `json:"model"`
}

// Invocation is one model call.
type Invocation struct {
	Model            string     `json:"model"`
	InputTokens      int64      `json:"input_tokens"`
	OutputTokens     int64      `json:"output_tokens"`
	CacheReadTokens  int64      `json:"cache_read_tokens"`
	CacheWriteTokens int64      `json:"cache_write_tokens"`
	StartedAt        *time.Time `json:"started_at"`
}

// TokenUsage sums one prompt's model usage.
type TokenUsage struct {
	Tokens              int64    `json:"tokens"`
	EstimatedTokens     int64    `json:"estimated_tokens"`
	UncachedInputTokens int64    `json:"uncached_input_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	CacheReadTokens     int64    `json:"cache_read_tokens"`
	CacheWriteTokens    int64    `json:"cache_write_tokens"`
	UnclassifiedTokens  int64    `json:"unclassified_tokens"`
	APICalls            int64    `json:"api_calls"`
	Models              []string `json:"models"`
	// seen holds Models, so adding one is not a search of the list.
	seen map[string]bool
}

func (u *TokenUsage) add(model string, basis session.TokenBasis, in, out, cacheRead, cacheWrite, total, calls int64) {
	unclassified := int64(0)
	if in+out+cacheRead+cacheWrite == 0 {
		unclassified = total
	}
	tokens := in + out + cacheRead + cacheWrite + unclassified
	u.Tokens += tokens
	if basis == session.TokenBasisEstimated {
		u.EstimatedTokens += tokens
	}
	u.UncachedInputTokens += in
	u.OutputTokens += out
	u.CacheReadTokens += cacheRead
	u.CacheWriteTokens += cacheWrite
	u.UnclassifiedTokens += unclassified
	u.APICalls += calls
	if model != "" && !u.seen[model] {
		if u.seen == nil {
			u.seen = map[string]bool{}
		}
		u.seen[model] = true
		u.Models = append(u.Models, model)
	}
}

// ToolCall is one tool call, with its result's output.
type ToolCall struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Verb            string     `json:"verb"`
	Input           string     `json:"input"`
	InputTruncated  bool       `json:"input_truncated"`
	Output          *string    `json:"output"`
	OutputTruncated bool       `json:"output_truncated"`
	Status          string     `json:"status"`
	Timestamp       *time.Time `json:"timestamp"`
}

// Build shapes g, whose top-level session is the one meta describes, within
// limits. A graph with no top-level session yields a transcript with no
// prompts.
//
// clean, when set, is given each text the transcript keeps before it is cut
// (and cut first to the limit plus cleanMargin), and must return a text of
// the same length, as a redactor does. An error from it ends the build.
func Build(g *session.Graph, meta Meta, limits Limits, clean func(string) (string, error)) (Transcript, error) {
	if clean == nil {
		clean = func(text string) (string, error) { return text, nil }
	}
	t := Transcript{
		Session: Session{
			RunID: meta.ID, Source: meta.Vendor, GitBranch: meta.Branch, Outcome: string(session.OutcomeUnknown),
			Models: []string{}, Prompts: []Prompt{}, CommitCount: meta.Commits, Confidence: meta.Confidence,
			RepliesIncluded: true,
		},
		Activity:   map[string]Activity{},
		LimitBytes: limits.Bytes,
	}
	root := g.Root()
	if root == nil {
		return t, nil
	}
	s := &t.Session
	s.StartedAt, s.EndedAt, s.Outcome = root.StartedAt, root.EndedAt, string(root.Outcome)
	s.Title = deref(root.Title)
	s.WorkingDir = deref(root.WorkingDir)
	if branch := deref(root.GitBranch); branch != "" {
		s.GitBranch = branch
	}
	s.HasInnerTrace = len(g.ToolUses)+len(g.AssistantMessages)+len(g.ModelInvocations)+len(g.FileTouches) > 0

	o := newOwnership(g)
	// bucket is the prompt an input's activity counts toward, or "" for the
	// session's own.
	bucket := func(inputID string) string {
		key := o.key(inputID)
		if o.prompts[key] {
			return key
		}
		return ""
	}

	effects := map[string]*Effects{}
	effectsOf := func(key string) *Effects {
		if effects[key] == nil {
			effects[key] = &Effects{}
		}
		return effects[key]
	}
	type touched struct{ key, path string }
	read, edited := map[touched]bool{}, map[touched]bool{}
	for _, touch := range g.FileTouches {
		key := bucket(touch.UserInputID)
		e := effectsOf(key)
		switch touch.Kind {
		case session.TouchRead:
			e.Reads++
			if !read[touched{key, touch.Path}] {
				read[touched{key, touch.Path}] = true
				e.FilesRead++
			}
		case session.TouchEdit:
			if !edited[touched{key, touch.Path}] {
				edited[touched{key, touch.Path}] = true
				e.FilesEdited++
			}
			e.FilesChanged++
			e.DiffHunks += len(touch.Hunks)
			for _, hunk := range touch.Hunks {
				e.AddedLines += hunk.NewLines
				e.RemovedLines += hunk.OldLines
			}
		}
	}
	for _, use := range g.ToolUses {
		e := effectsOf(bucket(use.UserInputID))
		e.ToolCalls++
		if use.Verb == session.VerbExecute {
			e.ExecuteCalls++
		}
		if use.Status == session.StatusError {
			e.FailedCalls++
		}
	}

	usage := map[string]*TokenUsage{}
	usageOf := func(key string) *TokenUsage {
		if usage[key] == nil {
			usage[key] = &TokenUsage{Models: []string{}}
		}
		return usage[key]
	}
	sessionModels := map[string]bool{}
	addModel := func(model string) {
		if model != "" && !sessionModels[model] {
			sessionModels[model] = true
			s.Models = append(s.Models, model)
		}
	}
	models := map[string]string{}
	for _, call := range g.ModelInvocations {
		model := deref(call.Model)
		models[call.ID] = model
		usageOf(bucket(call.UserInputID)).add(model, call.TokenBasis, call.InputTokens, call.OutputTokens,
			call.CacheReadTokens, call.CacheWriteTokens, call.TotalTokens, 1)
		addModel(model)
	}
	perPrompt := false
	for _, u := range g.TokenUsage {
		key := ""
		if u.Scope == session.UsageScopeInput && u.UserInputID != nil {
			key = bucket(*u.UserInputID)
		}
		basis := u.TokenBasis
		if basis == "" {
			basis = session.TokenBasisAggregateReported
		}
		model := deref(u.Model)
		usageOf(key).add(model, basis, deref(u.InputTokens), deref(u.OutputTokens), deref(u.CacheReadTokens),
			deref(u.CacheWriteTokens), deref(u.TotalTokens), deref(u.APICallCount))
		addModel(model)
	}
	for key, u := range usage {
		s.Tokens += u.Tokens
		s.EstimatedTokens += u.EstimatedTokens
		if key != "" && u.Tokens > 0 {
			perPrompt = true
		}
	}
	s.CostPerPrompt = perPrompt || s.Tokens == 0

	if s.HasInnerTrace {
		s.Totals, s.Unattributed = &Effects{}, &Effects{}
		for key, e := range effects {
			s.Totals.add(*e)
			if key == "" {
				s.Unattributed.add(*e)
			}
		}
	}

	// What each prompt's activity lists: messages from the prompt itself,
	// model and tool calls from it and from the subagents it started.
	messages := map[string][]session.AssistantMessage{}
	for _, message := range g.AssistantMessages {
		if message.SessionID != root.ID || strings.TrimSpace(deref(message.Text)) == "" {
			continue
		}
		messages[message.UserInputID] = append(messages[message.UserInputID], message)
	}
	calls := map[string][]session.ModelInvocation{}
	for _, call := range g.ModelInvocations {
		key := o.key(call.UserInputID)
		calls[key] = append(calls[key], call)
	}
	uses := map[string][]session.ToolUse{}
	for _, use := range g.ToolUses {
		key := o.key(use.UserInputID)
		uses[key] = append(uses[key], use)
	}
	results := map[string]session.ToolResult{}
	for _, result := range g.ToolResults {
		results[result.ToolUseID] = result
	}

	cut := func(full string, limit int) (string, bool, error) {
		window, _ := clip(full, limit+cleanMargin)
		cleaned, err := clean(window)
		if err != nil {
			return "", false, err
		}
		shown, _ := clip(cleaned, limit)
		return shown, len(full) > limit, nil
	}
	// The session's own texts are cut to Field, so the budget below bounds
	// everything else.
	for _, field := range []*string{&s.Title, &s.GitBranch, &s.WorkingDir, &s.Source} {
		shown, fieldCut, err := cut(*field, limits.Field)
		if err != nil {
			return Transcript{}, err
		}
		*field = shown
		t.Truncated = t.Truncated || fieldCut
	}
	if len(s.Models) > limits.Models {
		s.Models = s.Models[:limits.Models]
		t.Truncated = true
	}
	for i, model := range s.Models {
		shown, fieldCut, err := cut(model, limits.Field)
		if err != nil {
			return Transcript{}, err
		}
		s.Models[i] = shown
		t.Truncated = t.Truncated || fieldCut
	}

	s.TurnCount = len(o.ordered)
	// The budget starts with everything but the prompts and their activity,
	// and keeps a little back for the flags a cut sets afterwards.
	b := budget{left: limits.Bytes - encodedSize(t) - budgetReserve}
	for _, input := range o.ordered {
		text := prompttext.Strip(input.Text)
		if strings.TrimSpace(text) == "" {
			continue
		}
		s.PromptsTotal++
		if b.spent() {
			s.PromptsTruncated = true
			t.Truncated = true
			continue
		}
		shown, textCut, err := cut(text, limits.Prompt)
		if err != nil {
			return Transcript{}, err
		}
		prompt := Prompt{
			TurnID: input.ID, Ordinal: input.Ordinal, StartedAt: input.TriggeredAt,
			Text: shown, TextLength: utf8.RuneCountInString(text), Truncated: textCut, Replies: []Message{},
		}
		turnMessages := messages[input.ID]
		sortByTime(turnMessages, func(m session.AssistantMessage) (*time.Time, string) { return m.Timestamp, m.ID })
		// The last replies, as the hosted service sends them: a preview of
		// each, whose whole text is the activity's message at that instant.
		// A preview is not a loss, so it does not mark the transcript cut.
		replies := turnMessages[max(0, len(turnMessages)-limits.Replies):]
		prompt.RepliesOmitted = len(turnMessages) - len(replies)
		for _, message := range replies {
			reply, err := messageOf(message, limits.Reply, models, cut)
			if err != nil {
				return Transcript{}, err
			}
			prompt.Replies = append(prompt.Replies, reply)
		}
		if s.HasInnerTrace {
			e := Effects{}
			if found := effects[input.ID]; found != nil {
				e = *found
			}
			prompt.Effects = &e
		}
		turnUsage := TokenUsage{Models: []string{}}
		if found := usage[input.ID]; found != nil {
			turnUsage = *found
		}
		prompt.Tokens, prompt.EstimatedTokens = turnUsage.Tokens, turnUsage.EstimatedTokens

		activity := Activity{
			Messages: []Message{}, Invocations: []Invocation{}, ToolCalls: []ToolCall{}, TokenUsage: turnUsage,
		}
		turnCalls := calls[input.ID]
		activity.InvocationsTotal = len(turnCalls)
		// A prompt and its empty activity go in together, or neither does.
		if !b.fits(encodedSize(prompt) + 1 + encodedSize(input.ID) + 1 + encodedSize(activity) + 1) {
			s.PromptsTruncated = true
			t.Truncated = true
			continue
		}
		s.Prompts = append(s.Prompts, prompt)
		t.Truncated = t.Truncated || textCut

		// The card only asks whether a prompt made model calls; the first
		// stands for them, and InvocationsTotal and the token usage count them.
		sortByTime(turnCalls, func(c session.ModelInvocation) (*time.Time, string) { return c.StartedAt, c.ID })
		if len(turnCalls) > 0 {
			call := turnCalls[0]
			first := Invocation{
				Model: deref(call.Model), InputTokens: call.InputTokens, OutputTokens: call.OutputTokens,
				CacheReadTokens: call.CacheReadTokens, CacheWriteTokens: call.CacheWriteTokens, StartedAt: call.StartedAt,
			}
			if b.fits(encodedSize(first) + 1) {
				activity.Invocations = append(activity.Invocations, first)
			} else {
				t.Truncated = true
			}
		}
		for i, message := range turnMessages {
			if i >= limits.Messages {
				activity.MessagesTruncated = true
				break
			}
			item, err := messageOf(message, limits.Message, models, cut)
			if err != nil {
				return Transcript{}, err
			}
			if !b.fits(encodedSize(item) + 1) {
				activity.MessagesTruncated = true
				break
			}
			activity.Messages = append(activity.Messages, item)
			t.Truncated = t.Truncated || item.Truncated
		}
		turnUses := uses[input.ID]
		sortByTime(turnUses, func(u session.ToolUse) (*time.Time, string) { return u.Timestamp, u.ID })
		for i, use := range turnUses {
			if i >= limits.ToolCalls {
				activity.ToolCallsTruncated = true
				break
			}
			call, err := toolCall(use, results, limits.Tool, cut)
			if err != nil {
				return Transcript{}, err
			}
			if !b.fits(encodedSize(call) + 1) {
				activity.ToolCallsTruncated = true
				break
			}
			activity.ToolCalls = append(activity.ToolCalls, call)
			t.Truncated = t.Truncated || call.InputTruncated || call.OutputTruncated
		}
		t.Truncated = t.Truncated || activity.MessagesTruncated || activity.ToolCallsTruncated
		t.Activity[input.ID] = activity
	}
	return t, nil
}

// cleanMargin is how far past a text's limit Clean reads, so a secret that
// starts before the cut and runs over it is still recognised and masked.
const cleanMargin = 8 << 10

// budgetReserve absorbs what can change after the budget is spent: the
// truncation flags, and a redaction that rewrites a string.
const budgetReserve = 1 << 10

// Encode writes t as JSON without HTML escaping, the form Bytes bounds.
func Encode(t Transcript) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(t); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodedSize is the length of v as Encode writes it, without its newline.
func encodedSize(v any) int {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return 0
	}
	return buf.Len() - 1
}

// messageOf is message as listed, its text cut to limit.
func messageOf(message session.AssistantMessage, limit int, models map[string]string,
	cut func(string, int) (string, bool, error)) (Message, error) {
	full := deref(message.Text)
	text, textCut, err := cut(full, limit)
	if err != nil {
		return Message{}, err
	}
	return Message{
		Text: text, TextLength: utf8.RuneCountInString(full), Truncated: textCut,
		Timestamp: message.Timestamp, Model: models[message.ModelInvocationID],
	}, nil
}

func toolCall(use session.ToolUse, results map[string]session.ToolResult, limit int,
	cut func(string, int) (string, bool, error)) (ToolCall, error) {
	input, inputCut, err := cut(string(use.Input), limit)
	if err != nil {
		return ToolCall{}, err
	}
	call := ToolCall{
		ID: use.ID, Name: use.Name, Verb: string(use.Verb), Input: input, InputTruncated: inputCut,
		Status: string(use.Status), Timestamp: use.Timestamp,
	}
	if result, ok := results[use.ID]; ok {
		output := deref(result.OutputText)
		if output == "" && len(result.Output) > 0 && string(result.Output) != "null" {
			output = string(result.Output)
		}
		if output == "" {
			output = deref(result.ErrorText)
		}
		if output != "" {
			shown, outputCut, err := cut(output, limit)
			if err != nil {
				return ToolCall{}, err
			}
			call.Output, call.OutputTruncated = &shown, outputCut
		}
	}
	return call, nil
}

// ownership answers which top-level input a piece of activity belongs to.
type ownership struct {
	// owner is, for each nested session, the top-level input whose tool
	// call started it (through any depth of nesting).
	owner map[string]string
	// inputSession is each input's session.
	inputSession map[string]string
	// prompts holds the top-level inputs that are human prompts, and
	// ordered lists them by ordinal.
	prompts map[string]bool
	ordered []session.UserInput
}

func newOwnership(g *session.Graph) *ownership {
	o := &ownership{owner: map[string]string{}, inputSession: map[string]string{}, prompts: map[string]bool{}}
	root := g.Root()
	for _, input := range g.UserInputs {
		o.inputSession[input.ID] = input.SessionID
		if input.SessionID == root.ID && input.Origin == session.OriginHuman &&
			(input.Kind == session.KindPrompt || input.Kind == session.KindSlashCommand) {
			o.prompts[input.ID] = true
			o.ordered = append(o.ordered, input)
		}
	}
	slices.SortStableFunc(o.ordered, func(a, b session.UserInput) int {
		return cmp.Or(cmp.Compare(a.Ordinal, b.Ordinal), strings.Compare(a.ID, b.ID))
	})
	spawnInput := map[string]string{}
	for _, use := range g.ToolUses {
		spawnInput[use.ID] = use.UserInputID
	}
	// Sessions come parent before child, so a child's parent is settled
	// before the child is.
	for _, s := range g.Sessions {
		if s.TopLevel() || s.SpawnedByToolUseID == nil {
			continue
		}
		spawnedBy := spawnInput[*s.SpawnedByToolUseID]
		if owner := o.owner[o.inputSession[spawnedBy]]; owner != "" {
			spawnedBy = owner
		}
		o.owner[s.ID] = spawnedBy
	}
	return o
}

// key is the top-level input that owns an input: the one that started its
// session, or the input itself in the top-level session.
func (o *ownership) key(inputID string) string {
	if owner := o.owner[o.inputSession[inputID]]; owner != "" {
		return owner
	}
	return inputID
}

// budget is what is left of Limits.Bytes.
type budget struct{ left int }

func (b *budget) spent() bool { return b.left <= 0 }

// fits takes n bytes when they fit. Once something does not, nothing more
// does, so what is shown is the start of the session and never a selection
// from it.
func (b *budget) fits(n int) bool {
	if n > b.left {
		b.left = 0
		return false
	}
	b.left -= n
	return true
}

// clip cuts text to at most limit bytes, at a character boundary.
func clip(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut], true
}

func sortByTime[T any](items []T, key func(T) (*time.Time, string)) {
	slices.SortStableFunc(items, func(a, b T) int {
		at, aid := key(a)
		bt, bid := key(b)
		switch {
		case at == nil && bt != nil:
			return 1
		case at != nil && bt == nil:
			return -1
		case at != nil && bt != nil:
			if c := at.Compare(*bt); c != 0 {
				return c
			}
		}
		return strings.Compare(aid, bid)
	})
}

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
