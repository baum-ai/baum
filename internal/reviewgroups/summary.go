package reviewgroups

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/store"
)

// StackSummaryPrompt names the published prompt that writes a stack's
// summary.
const StackSummaryPrompt = "stack-summary-public-v1"

//go:embed prompts/stack-summary-public-v1.txt
var stackSummaryPrompt string

// Bounds of one stack summary call.
const (
	// maxSummaryInputBytes bounds the members' part of the prompt; each
	// member gets an equal share, at least minMemberShare.
	maxSummaryInputBytes = 64 << 10
	minMemberShare       = 600
	// maxSummaryOutputTokens caps the reply, reasoning included.
	maxSummaryOutputTokens = 4096
	// maxSummaryRunes bounds the stored text; a longer reply is refused.
	maxSummaryRunes = 2000
	// maxSectionTitles is the most section titles a member contributes.
	maxSectionTitles = 12
)

// StackSummary is a stack group's generated summary: a few sentences about
// the whole stack written from its open members' saved reviews (their
// summaries and section titles), never the code.
type StackSummary struct {
	Text string `json:"text"`
	// Members are the member reviews it read, ascending: comparing them with
	// the members' latest saved reviews tells whether it is current without
	// reading any document.
	Members   []SummarizedMember `json:"members"`
	Model     string             `json:"model"`
	Prompt    string             `json:"prompt"`
	CreatedAt time.Time          `json:"created_at"`
}

// SummarizedMember is a member review a stack summary read.
type SummarizedMember struct {
	Number        int `json:"number"`
	ReviewVersion int `json:"review_version"`
	// Clone is the clone whose saved review it read: clones keep separate
	// histories, so a version number alone does not name one review.
	Clone string `json:"clone,omitempty"`
}

// Overview is what a stack group's page shows beside its pull requests,
// worked out from the members' saved reviews.
type Overview struct {
	// Riskiest is the open member whose saved review has the highest risk
	// score, the lowest in the stack on a tie; 0 when no review has a score.
	// The view's risk_score is its review's.
	Riskiest int `json:"riskiest,omitempty"`
	// Reviewed counts the open members with a saved review.
	Reviewed int `json:"reviewed"`
	// Summary is the latest stack summary, or null before there is one.
	Summary *StackSummary `json:"summary"`
	// Current is a summary that read every open member's latest saved
	// review.
	Current bool `json:"current"`
}

// createSummaryTable adds the stack_summaries table: one row per stack group,
// replaced when the summary is written again. It is not a review version:
// a summary has no history and no document.
func createSummaryTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS stack_summaries(group_id TEXT PRIMARY KEY, summary BLOB NOT NULL)`)
	return err
}

// StackSummary returns the group's summary, nil when it has none.
func (s *Store) StackSummary(ctx context.Context, id string) (*StackSummary, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT summary FROM stack_summaries WHERE group_id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out StackSummary
	return &out, json.Unmarshal(raw, &out)
}

// stackSummaries reads the summaries of every live stack group.
func (s *Store) stackSummaries(ctx context.Context) (map[string]StackSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT group_id, summary FROM stack_summaries WHERE group_id IN (SELECT id FROM groups WHERE deleted=0 AND stack=1)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]StackSummary{}
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var summary StackSummary
		if err := json.Unmarshal(raw, &summary); err != nil {
			return nil, err
		}
		out[id] = summary
	}
	return out, rows.Err()
}

// PutStackSummary stores the group's summary in place of the one before. A
// group that is gone or no longer a stack group keeps none.
func (s *Store) PutStackSummary(ctx context.Context, id string, summary StackSummary) error {
	raw, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO stack_summaries(group_id, summary) SELECT ?, ? WHERE EXISTS(SELECT 1 FROM groups WHERE id=? AND deleted=0 AND stack=1)
ON CONFLICT(group_id) DO UPDATE SET summary=excluded.summary`, id, raw, id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return ErrNotFound
	}
	return nil
}

// stackOrder is the group's open members in stack order: from the base up,
// a parent before its children, siblings ascending. Members the saved chain
// does not place follow, ascending.
func stackOrder(g Group) []int {
	children := map[int][]int{}
	var roots []int
	for _, entry := range g.chain {
		if entry.Parent == nil {
			roots = append(roots, entry.Number)
		} else {
			children[*entry.Parent] = append(children[*entry.Parent], entry.Number)
		}
	}
	open := map[int]bool{}
	for _, number := range g.Members {
		open[number] = true
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(g.Members))
	var visit func(int)
	visit = func(number int) {
		if seen[number] {
			return
		}
		seen[number] = true
		if open[number] {
			out = append(out, number)
		}
		kids := children[number]
		slices.Sort(kids)
		for _, kid := range kids {
			visit(kid)
		}
	}
	slices.Sort(roots)
	for _, root := range roots {
		visit(root)
	}
	for _, number := range g.Members {
		if !slices.Contains(out, number) {
			out = append(out, number)
		}
	}
	return out
}

// memberReview is an open member's latest saved review as the review index
// lists it.
type memberReview struct {
	Clone   string
	Version int
	Risk    json.RawMessage
}

// memberReviews finds each open member's latest saved review in the index:
// the highest version any clone of the repository holds.
func memberReviews(g Group, snapshot reviewindex.Snapshot) map[int]memberReview {
	open := map[int]bool{}
	for _, number := range g.Members {
		open[number] = true
	}
	out := map[int]memberReview{}
	for _, entry := range snapshot.Entries {
		pr := entry.PullRequest
		if entry.Latest == nil || !open[pr.Number] || !strings.EqualFold(pr.Repository, g.Repository) {
			continue
		}
		if found, ok := out[pr.Number]; ok && found.Version >= entry.Latest.Version {
			continue
		}
		out[pr.Number] = memberReview{Clone: entry.CloneID, Version: entry.Latest.Version, Risk: entry.RiskScore}
	}
	return out
}

// summarized are the member reviews a summary would read now, ascending.
func summarized(reviews map[int]memberReview) []SummarizedMember {
	out := make([]SummarizedMember, 0, len(reviews))
	for number, review := range reviews {
		out = append(out, SummarizedMember{Number: number, ReviewVersion: review.Version, Clone: review.Clone})
	}
	slices.SortFunc(out, func(a, b SummarizedMember) int { return a.Number - b.Number })
	return out
}

// riskScore is the part of a review's summary.risk_score the overview reads.
type riskScore struct {
	Score *int `json:"score"`
}

// overview works out a stack group's overview from the index's member
// reviews and its stored summary. risk is the riskiest member's
// summary.risk_score, as the review stored it.
func overview(g Group, reviews map[int]memberReview, summary *StackSummary) (Overview, json.RawMessage) {
	out := Overview{Reviewed: len(reviews), Summary: summary}
	best := 0
	var risk json.RawMessage
	for _, number := range stackOrder(g) {
		review, ok := reviews[number]
		if !ok || len(review.Risk) == 0 {
			continue
		}
		var score riskScore
		if json.Unmarshal(review.Risk, &score) != nil || score.Score == nil {
			continue
		}
		if *score.Score > best {
			best, out.Riskiest, risk = *score.Score, number, review.Risk
		}
	}
	out.Current = summary != nil && slices.Equal(summary.Members, summarized(reviews))
	return out, risk
}

// summaryMember is one pull request of the summary call's input.
type summaryMember struct {
	Number   int      `json:"number"`
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	Sections []string `json:"sections"`
}

// memberBrief reads what the summary call sees of a member's saved review:
// its title, summary and current section titles, cut to share bytes.
func memberBrief(number int, document []byte, share int) (summaryMember, error) {
	var doc struct {
		Summary struct {
			Title string `json:"title"`
			Text  string `json:"text"`
		} `json:"summary"`
		Sections []struct {
			Title   string `json:"title"`
			Derived struct {
				State string `json:"state"`
			} `json:"derived"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		return summaryMember{}, fmt.Errorf("the saved review of #%d: %w", number, err)
	}
	out := summaryMember{Number: number, Title: cut(doc.Summary.Title, 200), Summary: strings.TrimSpace(doc.Summary.Text), Sections: []string{}}
	for _, section := range doc.Sections {
		if section.Derived.State != "stale" && len(out.Sections) < maxSectionTitles {
			out.Sections = append(out.Sections, cut(section.Title, 200))
		}
	}
	size := func() int {
		encoded, _ := json.Marshal(out)
		return len(encoded)
	}
	for size() > share && len(out.Sections) > 0 {
		out.Sections = out.Sections[:len(out.Sections)-1]
	}
	// A cut adds an ellipsis of a few bytes, so it may take another.
	for over := size() - share; over > 0 && out.Summary != ""; over = size() - share {
		out.Summary = cut(out.Summary, max(utf8.RuneCountInString(out.Summary)-over-1, 0))
	}
	return out, nil
}

// cut shortens s to at most n runes, marking a cut with an ellipsis.
func cut(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n <= 1 {
		return ""
	}
	return strings.TrimSpace(string([]rune(s)[:n-1])) + "…"
}

// summaryRequest is the model call that writes a stack's summary from its
// members' saved reviews, in stack order.
func summaryRequest(name string, members []summaryMember) (decisions.ModelRequest, error) {
	input, err := json.Marshal(struct {
		Name         string          `json:"name"`
		PullRequests []summaryMember `json:"pull_requests"`
	}{name, members})
	if err != nil {
		return decisions.ModelRequest{}, err
	}
	return decisions.ModelRequest{
		System:     stackSummaryPrompt,
		User:       string(input),
		SchemaName: "stack_summary",
		Schema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"summary": map[string]any{"type": "string"}},
			"required":             []string{"summary"},
			"additionalProperties": false,
		},
		MaxOutputTokens: maxSummaryOutputTokens,
	}, nil
}

// parseSummary reads the reply: one nonempty text of at most
// maxSummaryRunes, its whitespace tidied.
func parseSummary(data []byte) (string, error) {
	var reply struct {
		Summary string `json:"summary"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reply); err != nil {
		return "", fmt.Errorf("stack summary reply: %w", err)
	}
	text := strings.Join(strings.Fields(reply.Summary), " ")
	if text == "" || utf8.RuneCountInString(text) > maxSummaryRunes {
		return "", errors.New("stack summary reply: empty or too long")
	}
	return text, nil
}

// Summarizer keeps each stack group's summary current: once at least two of
// its open members have a saved review, it writes the summary again whenever
// a member gets a new saved review or the open members change. Woken by
// Wake, it compares the members' latest saved reviews in the review index
// with those the stored summary read, and reads documents and calls the model
// only for a stack whose summary is out of date. One call at a time.
type Summarizer struct {
	Store *Store
	// Index is the review index, for the members' latest saved reviews.
	Index func(context.Context) (reviewindex.Snapshot, error)
	// Read reads the latest saved review of a pull request of the group's
	// repository from the clone the index found it in.
	Read func(ctx context.Context, clone string, number int) (store.Version, error)
	// Complete makes the model call; check validates the reply before the
	// call is settled. It returns the reply and the model that wrote it.
	Complete func(ctx context.Context, g Group, req decisions.ModelRequest, check func([]byte) error) ([]byte, string, error)
	// Changed is called after a summary is stored.
	Changed func()
	Log     *slog.Logger

	once sync.Once
	wake chan struct{}
	// failed are the stacks whose summary call failed, by group, for the
	// members it would have read: retried with a backoff, then left until
	// they change.
	failed map[string]summaryFailure
	now    func() time.Time
}

type summaryFailure struct {
	members  string
	attempts int
	retryAt  time.Time
}

// summaryAttempts is how many times one set of member reviews is tried.
const summaryAttempts = 3

func (s *Summarizer) init() {
	s.once.Do(func() {
		s.wake = make(chan struct{}, 1)
		s.failed = map[string]summaryFailure{}
		if s.now == nil {
			s.now = time.Now
		}
		if s.Log == nil {
			s.Log = slog.Default()
		}
	})
}

// Wake asks for a pass soon; it never blocks.
func (s *Summarizer) Wake() {
	s.init()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run passes over the stack groups on every Wake, and when a backoff ends,
// until ctx ends.
func (s *Summarizer) Run(ctx context.Context) {
	s.init()
	for {
		var retry <-chan time.Time
		var timer *time.Timer
		if next := s.pass(ctx); !next.IsZero() {
			timer = time.NewTimer(max(time.Until(next), time.Second))
			retry = timer.C
		}
		select {
		case <-ctx.Done():
		case <-s.wake:
		case <-retry:
		}
		if timer != nil {
			timer.Stop()
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// pass writes every out-of-date stack summary and returns when the earliest
// backoff ends, zero for none.
func (s *Summarizer) pass(ctx context.Context) time.Time {
	s.init()
	groups, err := s.Store.List(ctx)
	if err != nil {
		s.Log.Warn("stack summaries skipped", "error", err)
		return time.Time{}
	}
	snapshot, err := s.Index(ctx)
	if err != nil {
		s.Log.Warn("stack summaries skipped", "error", err)
		return time.Time{}
	}
	var next time.Time
	live := map[string]bool{}
	for _, g := range groups {
		if ctx.Err() != nil {
			return time.Time{}
		}
		if !g.Stack {
			continue
		}
		live[g.ID] = true
		reviews := memberReviews(g, snapshot)
		if len(reviews) < MinMembers {
			continue
		}
		current, err := s.Store.StackSummary(ctx, g.ID)
		if err != nil {
			s.Log.Warn("stack summary not read", "group", g.ID, "error", err)
			continue
		}
		want := summarized(reviews)
		if current != nil && slices.Equal(current.Members, want) {
			continue
		}
		key, _ := json.Marshal(want)
		failure := s.failed[g.ID]
		if failure.members == string(key) && (failure.attempts >= summaryAttempts || s.now().Before(failure.retryAt)) {
			if failure.attempts < summaryAttempts && (next.IsZero() || failure.retryAt.Before(next)) {
				next = failure.retryAt
			}
			continue
		}
		if err := s.summarize(ctx, g, reviews); err != nil {
			if ctx.Err() != nil {
				return time.Time{}
			}
			if failure.members != string(key) {
				failure = summaryFailure{members: string(key)}
			}
			failure.attempts++
			failure.retryAt = s.now().Add(time.Duration(1<<failure.attempts) * time.Minute)
			s.failed[g.ID] = failure
			if failure.attempts < summaryAttempts && (next.IsZero() || failure.retryAt.Before(next)) {
				next = failure.retryAt
			}
			s.Log.Warn("stack summary not written", "repository", g.Repository, "group", g.ID, "attempt", failure.attempts, "error", err)
			continue
		}
		delete(s.failed, g.ID)
	}
	for id := range s.failed {
		if !live[id] {
			delete(s.failed, id)
		}
	}
	return next
}

// summarize reads the members' saved reviews and writes the group's summary.
func (s *Summarizer) summarize(ctx context.Context, g Group, reviews map[int]memberReview) error {
	order := stackOrder(g)
	share := max(maxSummaryInputBytes/max(len(reviews), 1), minMemberShare)
	var members []summaryMember
	var read []SummarizedMember
	for _, number := range order {
		review, ok := reviews[number]
		if !ok {
			continue
		}
		version, err := s.Read(ctx, review.Clone, number)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		member, err := memberBrief(number, version.Document, share)
		if err != nil {
			return err
		}
		members = append(members, member)
		read = append(read, SummarizedMember{Number: number, ReviewVersion: version.Metadata.Version, Clone: review.Clone})
	}
	if len(members) < MinMembers {
		return nil
	}
	request, err := summaryRequest(g.Name, members)
	if err != nil {
		return err
	}
	var text string
	reply, model, err := s.Complete(ctx, g, request, func(raw []byte) error {
		var err error
		text, err = parseSummary(raw)
		return err
	})
	if err != nil {
		return err
	}
	if text == "" {
		if text, err = parseSummary(reply); err != nil {
			return err
		}
	}
	slices.SortFunc(read, func(a, b SummarizedMember) int { return a.Number - b.Number })
	err = s.Store.PutStackSummary(ctx, g.ID, StackSummary{Text: text, Members: read, Model: model, Prompt: StackSummaryPrompt, CreatedAt: s.now().UTC().Truncate(time.Millisecond)})
	if errors.Is(err, ErrNotFound) {
		// Dissolved or taken over meanwhile.
		return nil
	}
	if err == nil && s.Changed != nil {
		s.Changed()
	}
	return err
}
