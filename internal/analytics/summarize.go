package analytics

import (
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/linkage/sessioncorrelate"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/prompttext"
	"github.com/baum-ai/baum/sessions/session"
)

// IdleGap is the longest pause before an agent event (a model call, a reply,
// a tool call or its result) that still counts as the agent working: a longer
// one is the session waiting, for the person or left open, and adds nothing
// to its active time. Time before a person's input never counts.
const IdleGap = 5 * time.Minute

// Entry is what the index keeps of one session tree: counts and token sums,
// never a prompt, a message, a tool's input or output, or a file path.
type Entry struct {
	// Key changes whenever one of the tree's files does (sizes and
	// modification times, or a digest of generated content).
	Key        string `json:"key"`
	Source     string `json:"source"`
	SessionID  string `json:"session_id"`
	Clone      string `json:"clone"`
	Repository string `json:"repository"`
	// Branch is the top-level session's branch, as the agent recorded it.
	Branch     string     `json:"branch,omitempty"`
	ModifiedAt time.Time  `json:"modified_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	// Generation marks one of baum's own runs, which is kept only so it is
	// not read again: it never counts as the person's agent work.
	Generation bool `json:"generation,omitempty"`
	// ActiveSeconds is nil for a session without a transcript (metadata
	// only): its activity is unknown, not zero. Hours holds the same seconds
	// by the unix hour they fell in.
	ActiveSeconds *int64          `json:"active_seconds,omitempty"`
	Hours         map[int64]int64 `json:"hours,omitempty"`
	Prompts       int             `json:"prompts"`
	ToolCalls     int             `json:"tool_calls"`
	FailedTools   int             `json:"failed_tool_calls"`
	Subagents     int             `json:"subagents,omitempty"`
	ModelCalls    int             `json:"model_calls"`
	Usage         []Usage         `json:"usage,omitempty"`
	// TokensUnknown says some of the session's model calls reported no
	// usage, or none was recorded at all, so its token sums are a floor.
	TokensUnknown bool `json:"tokens_unknown,omitempty"`
	// Commits are the commits the session recorded creating, as the agent
	// printed them (possibly abbreviated).
	Commits []string `json:"commits,omitempty"`
}

// Usage sums one model's tokens over requests that a price can be applied to
// together: the same speed, and every prompt on the same side of every
// long-prompt threshold of the price list (Single), or an aggregate the vendor
// reported without its requests, whose prompts are only known to be at most
// MaxPrompt.
type Usage struct {
	Model        string `json:"model"`
	Speed        string `json:"speed,omitempty"`
	Single       bool   `json:"single,omitempty"`
	Band         int    `json:"band,omitempty"`
	Calls        int64  `json:"calls"`
	Input        int64  `json:"input"`
	Output       int64  `json:"output"`
	CacheRead    int64  `json:"cache_read"`
	CacheWrite   int64  `json:"cache_write"`
	CacheWrite1h int64  `json:"cache_write_1h,omitempty"`
	MinPrompt    int64  `json:"min_prompt"`
	MaxPrompt    int64  `json:"max_prompt"`
}

// Tokens is every token the group processed, cache reads included.
func (u Usage) Tokens(vendor string) int64 {
	if vendor == pricing.VendorCodex {
		// Codex's input already counts its cached input.
		return u.Input + u.Output
	}
	return u.Input + u.Output + u.CacheRead + u.CacheWrite
}

// summarize reduces one mapped session tree to its entry. bands are the
// price list's long-prompt thresholds, ascending.
func summarize(s sessions.Session, bands []int64) Entry {
	graph := s.Graph
	e := Entry{Source: s.Source.String(), SessionID: s.ID, ModifiedAt: s.ModifiedAt.UTC()}
	if graph == nil {
		e.TokensUnknown = true
		return e
	}
	if sessions.IsGenerationRun(graph) {
		e.Generation = true
		return e
	}
	root := sessions.Root(graph)
	if root != nil {
		e.StartedAt, e.EndedAt = utc(root.StartedAt), utc(root.EndedAt)
		if root.GitBranch != nil {
			e.Branch = *root.GitBranch
		}
	}
	e.Subagents = max(0, len(graph.Sessions)-1)

	type event struct {
		at    time.Time
		agent bool
	}
	var events []event
	add := func(at *time.Time, agent bool) {
		if at != nil && !at.IsZero() {
			events = append(events, event{at.UTC(), agent})
		}
	}
	for _, input := range graph.UserInputs {
		add(input.TriggeredAt, false)
		if root != nil && input.SessionID == root.ID && humanPrompt(input) {
			e.Prompts++
		}
	}
	for _, m := range graph.AssistantMessages {
		add(m.Timestamp, true)
	}
	for _, r := range graph.ToolResults {
		add(r.Timestamp, true)
	}
	for _, use := range graph.ToolUses {
		add(use.Timestamp, true)
		e.ToolCalls++
		if use.Status == session.StatusError {
			e.FailedTools++
		}
	}
	groups := map[groupKey]*Usage{}
	vendor := e.Source
	for _, call := range graph.ModelInvocations {
		add(call.StartedAt, true)
		e.ModelCalls++
		if call.TokenBasis == session.TokenBasisUnknown ||
			call.InputTokens+call.OutputTokens+call.CacheReadTokens+call.CacheWriteTokens == 0 {
			e.TokensUnknown = true
		}
		var oneHour int64
		if call.CacheWrite1hTokens != nil {
			oneHour = *call.CacheWrite1hTokens
		}
		prompt := pricing.Usage{Vendor: vendor, InputTokens: call.InputTokens, CacheReadTokens: call.CacheReadTokens, CacheWriteTokens: call.CacheWriteTokens}.PromptTokens()
		key := groupKey{model: deref(call.Model), speed: deref(call.Speed), single: true, band: band(prompt, bands)}
		groups[key] = accumulate(groups[key], key, 1, call.InputTokens, call.OutputTokens, call.CacheReadTokens, call.CacheWriteTokens, oneHour, prompt)
	}
	for _, row := range graph.TokenUsage {
		in, out, read, write := deref64(row.InputTokens), deref64(row.OutputTokens), deref64(row.CacheReadTokens), deref64(row.CacheWriteTokens)
		if row.InputTokens == nil && row.OutputTokens == nil {
			e.TokensUnknown = true
		}
		prompt := pricing.Usage{Vendor: vendor, InputTokens: in, CacheReadTokens: read, CacheWriteTokens: write}.PromptTokens()
		calls := int64(0)
		single := row.APICallCount != nil && *row.APICallCount == 1
		if row.APICallCount != nil {
			calls = *row.APICallCount
		}
		key := groupKey{model: deref(row.Model), speed: deref(row.Speed), single: single}
		if single {
			key.band = band(prompt, bands)
		}
		groups[key] = accumulate(groups[key], key, calls, in, out, read, write, 0, prompt)
	}
	if len(graph.ModelInvocations) == 0 && len(graph.TokenUsage) == 0 {
		e.TokensUnknown = true
	}
	for _, g := range groups {
		if !g.Single {
			// An aggregate's requests are each at most its largest prompt
			// and at least nothing.
			g.MinPrompt = 0
		}
		e.Usage = append(e.Usage, *g)
	}
	slices.SortFunc(e.Usage, func(a, b Usage) int {
		return strings.Compare(a.Model+"\x00"+a.Speed, b.Model+"\x00"+b.Speed)
	})

	if sessions.HasTranscript(graph) {
		slices.SortStableFunc(events, func(a, b event) int { return a.at.Compare(b.at) })
		active := int64(0)
		hours := map[int64]int64{}
		for i := 1; i < len(events); i++ {
			gap := events[i].at.Sub(events[i-1].at)
			if !events[i].agent || gap <= 0 || gap > IdleGap {
				continue
			}
			seconds := int64(gap / time.Second)
			active += seconds
			hours[events[i-1].at.Unix()/3600] += seconds
		}
		e.ActiveSeconds = &active
		if len(hours) > 0 {
			e.Hours = hours
		}
		if e.StartedAt == nil && len(events) > 0 {
			e.StartedAt = &events[0].at
		}
		if e.EndedAt == nil && len(events) > 0 {
			e.EndedAt = &events[len(events)-1].at
		}
	}

	seen := map[string]bool{}
	for _, ref := range sessioncorrelate.Correlate(graph, vendor).CommitRefs {
		if ref.Role == sessioncorrelate.RoleCreated && len(ref.SHA) >= 7 && !seen[ref.SHA] {
			seen[ref.SHA] = true
			e.Commits = append(e.Commits, strings.ToLower(ref.SHA))
		}
	}
	return e
}

// humanPrompt is the hosted rule: an input of kind prompt or slash command
// from a person, with text left once wrapper constructs are stripped. A
// parent's instructions to a child agent are not prompts.
func humanPrompt(input session.UserInput) bool {
	return input.Origin == session.OriginHuman &&
		(input.Kind == session.KindPrompt || input.Kind == session.KindSlashCommand) &&
		strings.TrimSpace(prompttext.Strip(input.Text)) != ""
}

type groupKey struct {
	model, speed string
	single       bool
	band         int
}

func accumulate(g *Usage, key groupKey, calls, in, out, read, write, oneHour, prompt int64) *Usage {
	if g == nil {
		g = &Usage{Model: key.model, Speed: key.speed, Single: key.single, Band: key.band, MinPrompt: prompt, MaxPrompt: prompt}
	}
	g.Calls += calls
	g.Input += in
	g.Output += out
	g.CacheRead += read
	g.CacheWrite += write
	g.CacheWrite1h += oneHour
	g.MinPrompt = min(g.MinPrompt, prompt)
	g.MaxPrompt = max(g.MaxPrompt, prompt)
	return g
}

// band is how many thresholds lie below a prompt of that size: requests in
// one band are all on the same side of every threshold.
func band(prompt int64, bands []int64) int {
	n := 0
	for _, threshold := range bands {
		if prompt > threshold {
			n++
		}
	}
	return n
}

func utc(t *time.Time) *time.Time {
	if t == nil || t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func deref64(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}
