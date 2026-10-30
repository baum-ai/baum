package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/decisionlog"
	"github.com/baum-ai/baum/internal/fix"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/planusage"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/prupdate"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/sessions/collector"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
	"github.com/baum-ai/baum/sessions/session"
)

var t0 = time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)

func at(d time.Duration) string { return t0.Add(d).Format("2006-01-02T15:04:05.000Z") }

// claudeTranscript is a small Claude Code session in dir: two human prompts
// and a wrapper-only slash command, a failed and a successful command (a git
// commit), three model calls and a 26-minute pause.
func claudeTranscript(dir, id string) string {
	line := func(format string, args ...any) string {
		base := fmt.Sprintf(`"cwd":%q,"gitBranch":"feature","sessionId":%q,"version":"2.1.220","isSidechain":false,`, dir, id)
		return "{" + base + fmt.Sprintf(format, args...) + "}\n"
	}
	usage := `"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":1000}`
	return line(`"type":"user","uuid":"u1","parentUuid":null,"timestamp":%q,"message":{"role":"user","content":"Fix the parser"},"promptId":"P1","promptSource":"typed","origin":{"kind":"human"}`, at(0)) +
		line(`"type":"assistant","uuid":"u2","parentUuid":"u1","timestamp":%q,"requestId":"req_A","message":{"id":"msg_A","role":"assistant","model":"claude-sonnet-4-5","stop_reason":"tool_use",%s,"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]}`, at(time.Minute), usage) +
		line(`"type":"user","uuid":"u3","parentUuid":"u2","timestamp":%q,"message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"FAIL","is_error":true}]},"promptId":"P1","sourceToolAssistantUUID":"u2"`, at(2*time.Minute)) +
		line(`"type":"assistant","uuid":"u4","parentUuid":"u3","timestamp":%q,"requestId":"req_B","message":{"id":"msg_B","role":"assistant","model":"claude-sonnet-4-5","stop_reason":"tool_use",%s,"content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"git commit -am 'Fix the parser'"}}]}`, at(3*time.Minute), usage) +
		line(`"type":"user","uuid":"u5","parentUuid":"u4","timestamp":%q,"message":{"role":"user","content":[{"tool_use_id":"toolu_2","type":"tool_result","content":"[feature abc1234] Fix the parser\n 1 file changed"}]},"promptId":"P1","sourceToolAssistantUUID":"u4"`, at(4*time.Minute)) +
		line(`"type":"user","uuid":"u6","parentUuid":"u5","timestamp":%q,"message":{"role":"user","content":"<command-name>/clear</command-name>\n<command-message>clear</command-message>"},"promptId":"P2"`, at(29*time.Minute)) +
		line(`"type":"user","uuid":"u7","parentUuid":"u6","timestamp":%q,"message":{"role":"user","content":"Now push it"},"promptId":"P3","promptSource":"typed","origin":{"kind":"human"}`, at(30*time.Minute)) +
		line(`"type":"assistant","uuid":"u8","parentUuid":"u7","timestamp":%q,"requestId":"req_C","message":{"id":"msg_C","role":"assistant","model":"claude-sonnet-4-5","stop_reason":"end_turn",%s,"content":[{"type":"text","text":"Done."}]}`, at(31*time.Minute), usage)
}

type fixture struct {
	repo, claude string
	index        *Index
	mapped       *int
}

type fakeClones []repos.Clone

func (f fakeClones) List(context.Context) ([]repos.Clone, error) { return f, nil }

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Me", "GIT_AUTHOR_EMAIL=me@example.com", "GIT_COMMITTER_NAME=Me", "GIT_COMMITTER_EMAIL=me@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	repo := filepath.Join(base, "work", "demo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "init", "-q", "-b", "main")
	claude := filepath.Join(base, "claude")
	codex := filepath.Join(base, "codex")
	for _, dir := range []string{claude, codex} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mapped := 0
	index := &Index{
		Path:   filepath.Join(base, "state", "analytics-index.json"),
		Clones: fakeClones{{ID: "c1", Path: repo, Repository: "Acme/Demo", Available: true}},
		Stores: Stores{ClaudeProjectsDir: claude, CodexSessionsDir: codex, CursorStorePaths: []string{filepath.Join(base, "absent.vscdb")}},
	}
	index.mapSession = func(s collector.Session) (*session.Graph, []string, error) {
		mapped++
		return mapSession(s)
	}
	return fixture{repo: repo, claude: claude, index: index, mapped: &mapped}
}

func (f fixture) write(t *testing.T, dir, id, content string) string {
	t.Helper()
	path := filepath.Join(f.claude, claudecode.EncodeProjectDir(dir), id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const sessionID = "aaaaaaaa-0000-4000-8000-000000000001"

func TestIndexCountsASessionByTheHostedRules(t *testing.T) {
	f := newFixture(t)
	f.write(t, f.repo, sessionID, claudeTranscript(f.repo, sessionID))
	if err := f.index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, progress := f.index.Snapshot()
	if len(entries) != 1 || !progress.Complete || progress.Done != 1 || progress.Total != 1 {
		t.Fatalf("entries = %+v, progress = %+v", entries, progress)
	}
	e := entries[0]
	if e.Source != "claude_code" || e.SessionID != sessionID || e.Repository != "acme/demo" || e.Clone != "c1" || e.Branch != "feature" {
		t.Errorf("identity = %+v", e)
	}
	// The wrapper-only slash command is not a prompt.
	if e.Prompts != 2 || e.ToolCalls != 2 || e.FailedTools != 1 || e.ModelCalls != 3 {
		t.Errorf("counts: prompts %d, tools %d, failed %d, calls %d", e.Prompts, e.ToolCalls, e.FailedTools, e.ModelCalls)
	}
	// Four minutes of work, a 26-minute pause that is not, then one more.
	if e.ActiveSeconds == nil || *e.ActiveSeconds != 300 {
		t.Errorf("active = %v, want 300", e.ActiveSeconds)
	}
	if len(e.Commits) != 1 || e.Commits[0] != "abc1234" {
		t.Errorf("commits = %v", e.Commits)
	}
	if len(e.Usage) != 1 || e.Usage[0].Calls != 3 || e.Usage[0].Input != 300 || e.Usage[0].CacheRead != 3000 || e.TokensUnknown {
		t.Errorf("usage = %+v (unknown %v)", e.Usage, e.TokensUnknown)
	}
	raw, err := os.ReadFile(f.index.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Fix the parser", "Now push it", "go test", "FAIL", "Done."} {
		if strings.Contains(string(raw), text) {
			t.Errorf("the index holds session text %q", text)
		}
	}
}

func TestIndexReadsOnlyWhatChangedAndPrunes(t *testing.T) {
	f := newFixture(t)
	path := f.write(t, f.repo, sessionID, claudeTranscript(f.repo, sessionID))
	// A session outside every registered clone is never read.
	elsewhere := filepath.Join(filepath.Dir(f.repo), "elsewhere")
	f.write(t, elsewhere, "bbbbbbbb-0000-4000-8000-000000000002", claudeTranscript(elsewhere, "bbbbbbbb-0000-4000-8000-000000000002"))
	ctx := context.Background()
	if err := f.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if *f.mapped != 1 {
		t.Fatalf("mapped %d sessions, want only the clone's", *f.mapped)
	}
	if err := f.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if *f.mapped != 1 {
		t.Fatalf("an unchanged session was read again")
	}
	// A restarted daemon reads the saved index and nothing again.
	again := &Index{Path: f.index.Path, Clones: f.index.Clones, Stores: f.index.Stores, mapSession: f.index.mapSession}
	if err := again.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if entries, _ := again.Snapshot(); *f.mapped != 1 || len(entries) != 1 {
		t.Fatalf("after reload: mapped %d, entries %d", *f.mapped, len(entries))
	}
	// A session that grew is read again.
	more := strings.Replace(claudeTranscript(f.repo, sessionID), "Now push it", "Now push it to origin", 1)
	f.write(t, f.repo, sessionID, more)
	if err := again.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if *f.mapped != 2 {
		t.Fatalf("a changed session was not read again (mapped %d)", *f.mapped)
	}
	// Removed, it leaves the index.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := again.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if entries, _ := again.Snapshot(); len(entries) != 0 {
		t.Fatalf("a deleted session stayed: %+v", entries)
	}
}

func TestIndexDropsSessionsOlderThanTheRetentionAndOfUnregisteredClones(t *testing.T) {
	f := newFixture(t)
	f.write(t, f.repo, sessionID, claudeTranscript(f.repo, sessionID))
	ctx := context.Background()
	if err := f.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	f.index.Now = func() time.Time { return time.Now().Add(Retention + time.Hour) }
	if err := f.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if entries, _ := f.index.Snapshot(); len(entries) != 0 {
		t.Fatalf("an old session stayed: %+v", entries)
	}
	f.index.Now = nil
	if err := f.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	// An unavailable clone keeps its entries; an unregistered one does not.
	f.index.Clones = fakeClones{{ID: "c1", Path: f.repo, Repository: "acme/demo"}}
	if err := f.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if entries, _ := f.index.Snapshot(); len(entries) != 1 {
		t.Fatalf("an unavailable clone's session was dropped")
	}
	f.index.Clones = fakeClones{}
	if err := f.index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if entries, _ := f.index.Snapshot(); len(entries) != 0 {
		t.Fatalf("an unregistered clone's session stayed")
	}
}

func TestIndexSkipsBaumsOwnRuns(t *testing.T) {
	f := newFixture(t)
	run := strings.Replace(claudeTranscript(f.repo, sessionID), `"content":"Fix the parser"`, `"content":"<!-- baum:generation-run -->\nReview this"`, 1)
	f.write(t, f.repo, sessionID, run)
	if err := f.index.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, _ := f.index.Snapshot()
	if len(entries) != 1 || !entries[0].Generation || entries[0].Prompts != 0 {
		t.Fatalf("entries = %+v", entries)
	}
	if got := latestReadings(entries); len(got) != 0 {
		t.Fatalf("a generation run counts: %+v", got)
	}
}

// fakeRegistry is the report's view of the registered repositories.
type fakeRegistry struct {
	clones []repos.Clone
	picked []repos.Repository
}

func (f fakeRegistry) List(context.Context) ([]repos.Clone, error) { return f.clones, nil }
func (f fakeRegistry) Repositories(context.Context) ([]repos.Repository, error) {
	return f.picked, nil
}

func ptr[T any](v T) *T { return &v }

func preloaded(entries ...Entry) *Index {
	x := &Index{loaded: true, entries: map[string]Entry{}, bands: pricing.Active().PromptBands(), progress: Progress{Done: 1, Total: 3}}
	for i, e := range entries {
		x.entries[fmt.Sprint(i)] = e
	}
	return x
}

func TestReportCountsRootSessionsOnceAndNeverPricesUnknownModels(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	start := now.Add(-2 * time.Hour)
	priced := Usage{Model: "claude-sonnet-4-5", Single: true, Calls: 2, Input: 1000, Output: 500, MinPrompt: 1000, MaxPrompt: 1000}
	unknown := Usage{Model: "made-up-model-9", Single: true, Calls: 1, Input: 10, Output: 10, MinPrompt: 10, MaxPrompt: 10}
	entry := func(id, source string, usage ...Usage) Entry {
		return Entry{Source: source, SessionID: id, Repository: "acme/demo", Clone: "c1", StartedAt: &start, ModifiedAt: start,
			ActiveSeconds: ptr(int64(600)), Hours: map[int64]int64{start.Unix() / 3600: 600}, Prompts: 2, ToolCalls: 4, FailedTools: 1, Subagents: 1, Usage: usage}
	}
	older := entry("s1", "claude_code", priced)
	older.ModifiedAt = start.Add(-time.Hour)
	older.Prompts = 9
	latest := entry("s1", "claude_code", priced)
	latest.ModifiedAt = start.Add(time.Minute)
	mixed := entry("s2", "codex", unknown)
	none := entry("s3", "cursor")
	none.TokensUnknown, none.ActiveSeconds, none.Hours = true, nil, nil
	ancient := entry("s4", "claude_code", priced)
	ancient.StartedAt = ptr(now.AddDate(0, 0, -40))
	s := &Service{
		Registry: fakeRegistry{clones: []repos.Clone{{ID: "c1", Repository: "acme/demo"}}, picked: []repos.Repository{{Repository: "acme/demo"}}},
		Index:    preloaded(older, latest, mixed, none, ancient),
		Location: time.UTC, Now: func() time.Time { return now },
	}
	report, err := s.Report(context.Background(), Query{Days: 7})
	if err != nil {
		t.Fatal(err)
	}
	sum := report.Summary
	// s1 is counted once, from its latest reading; s4 started before the window.
	if sum.Sessions != 3 || sum.Prompts != 6 || sum.Subagents != 3 {
		t.Fatalf("summary = %+v", sum)
	}
	if sum.ActiveSeconds == nil || *sum.ActiveSeconds != 1200 || !sum.ActiveIncomplete {
		t.Errorf("active = %v incomplete %v", sum.ActiveSeconds, sum.ActiveIncomplete)
	}
	wantUSD, _ := pricing.Active().Cost(pricing.Active().Normalize("claude-sonnet-4-5", "claude_code"), pricing.Usage{Vendor: "claude_code", InputTokens: 1000, OutputTokens: 500, SingleRequest: true})
	if sum.Cost.USD == nil || *sum.Cost.USD != wantUSD || !sum.Cost.Incomplete || len(sum.Cost.Reasons) == 0 {
		t.Errorf("cost = %+v, want %v and incomplete", sum.Cost, wantUSD)
	}
	byAgent := map[string]Row{}
	for _, row := range report.ByAgent {
		byAgent[row.Key] = row
	}
	if codex := byAgent["codex"]; codex.Cost.USD != nil || !codex.Cost.Incomplete || codex.Tokens == nil || *codex.Tokens != 20 {
		t.Errorf("an unpriced model's row = %+v, want no cost, incomplete, its tokens", codex)
	}
	if cursor := byAgent["cursor"]; cursor.Tokens != nil || !cursor.TokensIncomplete || cursor.ActiveSeconds != nil || cursor.Cost.USD != nil {
		t.Errorf("a session without usage = %+v, want unknowns", cursor)
	}
	if claude := byAgent["claude_code"]; claude.FailedRate == nil || *claude.FailedRate != 0.25 {
		t.Errorf("failed rate = %v", claude.FailedRate)
	}
	if len(report.Daily) != 7 || report.Daily[6].Date != "2026-10-27" || report.Daily[6].Agents["claude_code"].Sessions != 1 || report.Daily[6].Agents["claude_code"].ActiveSeconds != 600 {
		t.Errorf("daily = %+v", report.Daily)
	}
	if report.Hours[10].ActiveSeconds != 1200 {
		t.Errorf("hours = %+v", report.Hours)
	}
	if len(report.Longest) != 2 {
		t.Errorf("longest = %+v", report.Longest)
	}
	if report.Indexing.Complete || report.Indexing.Total != 3 {
		t.Errorf("indexing = %+v", report.Indexing)
	}
	// Only the agents seen are offered.
	if strings.Join(report.Options.Agents, ",") != "claude_code,codex,cursor" || strings.Join(report.Options.Repositories, ",") != "acme/demo" {
		t.Errorf("options = %+v", report.Options)
	}
	filtered, err := s.Report(context.Background(), Query{Days: 7, Agent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if filtered.Summary.Sessions != 1 || filtered.Summary.Cost.USD != nil {
		t.Errorf("codex only = %+v", filtered.Summary)
	}
}

func TestShareCountsEachCommitOnce(t *testing.T) {
	start := t0
	commits := []branchCommit{
		{OID: "abc1234ffff", At: t0.Add(time.Hour)},
		{OID: "bbb0000", At: t0.Add(time.Hour), Agents: []string{"codex"}},
		{OID: "ccc0000", At: t0.Add(time.Hour), Checkpoint: true},
		{OID: "ddd0000", At: t0.Add(time.Hour)},
		{OID: "eee0000", At: t0.Add(-time.Hour), Agents: []string{"claude"}},
	}
	// Two clones of one repository hold the same commits.
	clones := []repos.Clone{{ID: "a", Repository: "acme/demo"}, {ID: "b", Repository: "Acme/Demo"}}
	byClone := map[string]cloneCommits{"a": {commits: commits}, "b": {commits: commits}}
	entries := []Entry{{Source: "claude_code", Repository: "acme/demo", Commits: []string{"abc1234"}}}
	got := share(byClone, clones, entries, start, "")
	if !got.Available || got.Commits != 4 || got.Agent != 3 || got.BySession != 1 || got.ByCheck != 1 || got.ByTrailer != 1 || got.Share == nil || *got.Share != 0.75 {
		t.Fatalf("share = %+v", got)
	}
	if len(got.Repository) != 1 || got.Repository[0].Commits != 4 {
		t.Errorf("by repository = %+v", got.Repository)
	}
	// Under an agent filter, a checkpoint names no agent: it stays in the
	// denominator only.
	codex := share(byClone, clones, entries, start, "codex")
	if codex.Commits != 4 || codex.Agent != 1 || codex.ByTrailer != 1 {
		t.Errorf("codex share = %+v", codex)
	}
	unreadable := share(map[string]cloneCommits{"a": {reason: "no email"}}, clones[:1], nil, start, "")
	if unreadable.Available || unreadable.Share != nil || len(unreadable.Reasons) != 1 {
		t.Errorf("unreadable = %+v", unreadable)
	}
}

func TestShareReadsTheClonesCommits(t *testing.T) {
	f := newFixture(t)
	runGit(t, f.repo, "config", "user.email", "me@example.com")
	runGit(t, f.repo, "commit", "-q", "--allow-empty", "-m", "Mine\n\nCo-Authored-By: Claude <noreply@anthropic.com>")
	runGit(t, f.repo, "commit", "-q", "--allow-empty", "-m", "Also mine")
	var reader commitReader
	got := reader.read(context.Background(), repos.Clone{ID: "c1", Path: f.repo, Repository: "acme/demo"}, time.Now())
	if got.reason != "" || len(got.commits) != 2 {
		t.Fatalf("commits = %+v", got)
	}
	result := share(map[string]cloneCommits{"c1": got}, []repos.Clone{{ID: "c1", Repository: "acme/demo"}}, nil, time.Now().Add(-time.Hour), "")
	if result.Commits != 2 || result.Agent != 1 {
		t.Errorf("share = %+v", result)
	}
}

type fakeSearch struct {
	pulls   []github.AuthoredPullRequest
	release chan struct{}
	err     error
}

func (f *fakeSearch) AuthoredPullRequests(ctx context.Context, _ github.Repository, _ time.Time) ([]github.AuthoredPullRequest, error) {
	if f.release != nil {
		<-f.release
	}
	return f.pulls, f.err
}

func TestDeliveryAnswersAtOnceThenFromTheSearch(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	created := now.Add(-3 * day)
	search := &fakeSearch{release: make(chan struct{}), pulls: []github.AuthoredPullRequest{
		{Number: 1, CreatedAt: created, MergedAt: ptr(created.Add(2 * time.Hour)), FirstReviewAt: ptr(created.Add(time.Hour))},
		{Number: 2, CreatedAt: created, MergedAt: ptr(created.Add(4 * time.Hour)), FirstReviewAt: ptr(created.Add(3 * time.Hour))},
		{Number: 3, CreatedAt: created, Conflict: true},
		{Number: 4, CreatedAt: created, Draft: true},
		{Number: 5, CreatedAt: now.Add(-20 * day), MergedAt: ptr(now)},
	}}
	s := &Service{
		Registry: fakeRegistry{picked: []repos.Repository{{Repository: "acme/demo"}}},
		Delivery: &Delivery{Search: search, Now: func() time.Time { return now }},
		Location: time.UTC, Now: func() time.Time { return now },
	}
	report, err := s.Report(context.Background(), Query{Days: 7})
	if err != nil {
		t.Fatal(err)
	}
	if report.Delivery.Status != "loading" || report.Delivery.Opened != 0 {
		t.Fatalf("first answer = %+v, want loading", report.Delivery)
	}
	close(search.release)
	deadline := time.Now().Add(5 * time.Second)
	for report.Delivery.Status == "loading" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		report, _ = s.Report(context.Background(), Query{Days: 7})
	}
	d := report.Delivery
	if d.Status != "ready" || d.Opened != 3 || d.Drafts != 1 || d.Merged != 2 || d.Reviewed != 2 || d.ConflictOrFailing != 1 {
		t.Fatalf("delivery = %+v", d)
	}
	if d.MedianMergeSeconds == nil || *d.MedianMergeSeconds != 3*3600 || d.MedianFirstReviewSeconds == nil || *d.MedianFirstReviewSeconds != 2*3600 {
		t.Errorf("medians = %v %v", d.MedianMergeSeconds, d.MedianFirstReviewSeconds)
	}
	if d.ConflictOrFailingShare == nil || *d.ConflictOrFailingShare != 1.0/3 {
		t.Errorf("share = %v", d.ConflictOrFailingShare)
	}
	month, _ := s.Report(context.Background(), Query{Days: 30})
	if month.Delivery.Opened != 4 {
		t.Errorf("30 days opened = %d", month.Delivery.Opened)
	}
}

func TestDeliveryFailureIsAReasonInWords(t *testing.T) {
	now := time.Now()
	s := &Service{
		Registry: fakeRegistry{picked: []repos.Repository{{Repository: "acme/demo"}}},
		Delivery: &Delivery{Search: &fakeSearch{err: fmt.Errorf("upstream said: secret-token")}},
		Location: time.UTC, Now: func() time.Time { return now },
	}
	var d DeliveryReport
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		report, _ := s.Report(context.Background(), Query{Days: 7})
		if d = report.Delivery; d.Status != "loading" {
			break
		}
	}
	if d.Status != "unavailable" || len(d.Reasons) != 1 || strings.Contains(d.Reasons[0], "secret") {
		t.Fatalf("delivery = %+v", d)
	}
}

func TestBaumWorkCountsJobsAndActions(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Hour)
	job := func(kind, state, reason string, cost *float64) jobs.Job {
		j := jobs.Job{State: state, Reason: reason, ActualCostUSD: cost, CreatedAt: recent, Tokens: 10, Calls: 1}
		j.Kind, j.Repository, j.Backend = kind, "acme/demo", "claude"
		return j
	}
	pushed := job(jobs.KindFix, "completed", "", ptr(0.5))
	pushed.Commit = "abc"
	pushed.ThreadOutcomes = []fix.ThreadOutcome{{Outcome: fix.ThreadResolved}, {Outcome: fix.ThreadResolved}, {Outcome: fix.ThreadSkipped}}
	group := job("", "completed", "", ptr(0.25))
	group.Group = "g1"
	old := job("", "completed", "", ptr(9.0))
	old.CreatedAt = now.AddDate(0, 0, -30)
	list := []jobs.Job{
		job("", "completed", "", ptr(1.0)), job("", "failed", "budget_exceeded", nil), job("", "failed", "budget_exceeded", nil),
		pushed, job(jobs.KindFix, "failed", "push_rejected", nil), group, old,
		// A feature build is the decision log's spend, not baum's work.
		job(jobs.KindFeatureBuild, "failed", "backend_unavailable", ptr(3.0)),
	}
	dir := t.TempDir()
	actions := &Actions{Path: filepath.Join(dir, "actions.json"), Now: func() time.Time { return recent }}
	for _, kind := range []string{ActionMerge, ActionMerge, ActionRerunChecks} {
		if err := actions.Record(kind, "Acme/Demo", 7); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{Registry: fakeRegistry{picked: []repos.Repository{{Repository: "acme/demo"}}}, Jobs: func() []jobs.Job { return list }, Actions: actions, Location: time.UTC, Now: func() time.Time { return now }}
	report, err := s.Report(context.Background(), Query{Days: 7})
	if err != nil {
		t.Fatal(err)
	}
	b := report.Baum
	if b.Reviews != 3 || b.GroupReviews != 1 || b.Fixes != 2 || b.Failed != 3 || b.Completed != 3 || b.FixesPushed != 1 || b.FixesFailed != 1 || b.ThreadsResolved != 2 {
		t.Fatalf("baum = %+v", b)
	}
	if b.Cost.USD == nil || *b.Cost.USD != 1.75 || !b.Cost.Incomplete {
		t.Errorf("cost = %+v", b.Cost)
	}
	if len(b.Failures) != 2 || b.Failures[0].Reason != "budget_exceeded" || b.Failures[0].Count != 2 {
		t.Errorf("failures = %+v", b.Failures)
	}
	if b.Merges == nil || *b.Merges != 2 || *b.CheckReruns != 1 || *b.BranchUpdates != 0 {
		t.Errorf("actions = %v %v %v", b.Merges, b.BranchUpdates, b.CheckReruns)
	}
	// The ledger survives a restart.
	again := &Actions{Path: actions.Path}
	if got := again.Since(recent.Add(-time.Minute)); len(got) != 3 || got[0].Repository != "acme/demo" {
		t.Errorf("reloaded = %+v", got)
	}
	codex, _ := s.Report(context.Background(), Query{Days: 7, Agent: "codex"})
	if codex.Baum.Reviews+codex.Baum.Fixes != 0 {
		t.Errorf("a claude job counted under codex: %+v", codex.Baum)
	}
}

func TestPlanUsageSeriesCountLimitHits(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "plans.json")
	samples := []planusage.Sample{
		{At: now.Add(-5 * time.Hour), Provider: "claude", Window: "5-hour", UsedPercent: 50},
		{At: now.Add(-4 * time.Hour), Provider: "claude", Window: "5-hour", UsedPercent: 100},
		{At: now.Add(-3 * time.Hour), Provider: "claude", Window: "5-hour", UsedPercent: 100},
		{At: now.Add(-2 * time.Hour), Provider: "claude", Window: "5-hour", UsedPercent: 5},
		{At: now.Add(-time.Hour), Provider: "claude", Window: "5-hour", UsedPercent: 100},
		{At: now.Add(-time.Hour), Provider: "codex", Window: "weekly", UsedPercent: 40},
	}
	raw, _ := json.Marshal(samples)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Service{Registry: fakeRegistry{}, Plans: &planusage.History{Path: path, Now: func() time.Time { return now }}, Location: time.UTC, Now: func() time.Time { return now }}
	report, _ := s.Report(context.Background(), Query{Days: 7})
	p := report.PlanUsage
	if !p.Available || len(p.Series) != 2 || p.Series[0].Provider != "claude" || p.Series[0].LimitHits != 2 || p.Series[0].Peak != 100 || len(p.Series[0].Points) != 5 {
		t.Fatalf("plan usage = %+v", p)
	}
	codex, _ := s.Report(context.Background(), Query{Days: 7, Agent: "codex"})
	if len(codex.PlanUsage.Series) != 1 || codex.PlanUsage.Series[0].Provider != "codex" {
		t.Errorf("codex only = %+v", codex.PlanUsage.Series)
	}
}

func TestHandlerValidatesItsParameters(t *testing.T) {
	s := &Service{Registry: fakeRegistry{picked: []repos.Repository{{Repository: "acme/demo"}}}, Location: time.UTC}
	h := s.Handler()
	for query, want := range map[string]int{
		"":                         200,
		"?days=90&agent=codex":     200,
		"?repo=Acme/Demo":          200,
		"?days=5":                  400,
		"?days=x":                  400,
		"?agent=copilot":           400,
		"?repo=acme/elsewhere":     400,
		"?repo=../../etc":          400,
		"?repo=acme/demo/../other": 400,
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/api/analytics"+query, nil))
		if w.Code != want {
			t.Errorf("%q: status %d, want %d: %s", query, w.Code, want, w.Body)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/analytics", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status %d", w.Code)
	}
	// Unknown figures are null, not zero.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/analytics?days=7", nil))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	summary := body["summary"].(map[string]any)
	for _, key := range []string{"active_seconds", "agent_share"} {
		if v, ok := summary[key]; !ok || v != nil {
			t.Errorf("summary.%s = %v, want null", key, v)
		}
	}
	if cost := summary["cost"].(map[string]any); cost["usd"] != nil {
		t.Errorf("cost = %v", cost)
	}
	if body["baum"].(map[string]any)["merges"] != nil || body["delivery"].(map[string]any)["status"] != "unavailable" {
		t.Errorf("unavailable sections = %v %v", body["baum"], body["delivery"])
	}
}

type fakeWrites struct{ fail bool }

func (f fakeWrites) result() error {
	if f.fail {
		return fmt.Errorf("refused")
	}
	return nil
}
func (f fakeWrites) MergeOptions(context.Context, github.Repository, int) (github.MergeOptions, error) {
	return github.MergeOptions{}, nil
}
func (f fakeWrites) MergePullRequest(context.Context, github.Repository, int, string, string) error {
	return f.result()
}
func (f fakeWrites) UpdateOptions(context.Context, github.Repository, int) (prupdate.UpdateOptions, error) {
	return prupdate.UpdateOptions{}, nil
}
func (f fakeWrites) UpdateBranch(context.Context, github.Repository, int, string) error {
	return f.result()
}
func (f fakeWrites) RerunOptions(context.Context, github.Repository, int) (prupdate.RerunOptions, error) {
	return prupdate.RerunOptions{}, nil
}
func (f fakeWrites) RerunFailedChecks(context.Context, github.Repository, int, string) (prupdate.RerunResult, error) {
	return prupdate.RerunResult{}, f.result()
}
func (f fakeWrites) ReviewThreads(context.Context, github.Repository, int, int) (prupdate.Threads, error) {
	return prupdate.Threads{}, nil
}
func (f fakeWrites) ResolveThread(context.Context, github.Repository, int, string) error {
	return f.result()
}
func (f fakeWrites) RerunJob(context.Context, github.Repository, int, string, int64) error {
	return f.result()
}
func (f fakeWrites) FailingChecks(context.Context, github.Repository, int, string) (prupdate.Checks, error) {
	return prupdate.Checks{}, nil
}

func (f fakeWrites) AllChecks(context.Context, github.Repository, int, string) (prupdate.CheckList, error) {
	return prupdate.CheckList{}, nil
}
func (f fakeWrites) Conflicts(context.Context, github.Repository, int) (prupdate.ConflictFiles, error) {
	return prupdate.ConflictFiles{}, nil
}

func TestActionsRecordOnlyWritesThatSucceeded(t *testing.T) {
	actions := &Actions{Path: filepath.Join(t.TempDir(), "actions.json")}
	repo := github.Repository{Host: "github.com", Owner: "acme", Name: "demo"}
	ctx := context.Background()
	for _, w := range []fakeWrites{{}, {fail: true}} {
		_ = actions.RecordMerges(w).MergePullRequest(ctx, repo, 1, "SQUASH", "abc")
		_ = actions.RecordUpdates(w).UpdateBranch(ctx, repo, 2, "abc")
		_, _ = actions.RecordUpdates(w).RerunFailedChecks(ctx, repo, 3, "abc")
	}
	got := actions.Since(time.Time{})
	if len(got) != 3 || got[0].Kind != ActionMerge || got[1].Kind != ActionUpdateBranch || got[2].Kind != ActionRerunChecks || got[2].PR != 3 {
		t.Fatalf("actions = %+v", got)
	}
}

// Decision capture's calls count by state; an unknown cost leaves the spend
// incomplete, and only unknown costs leave it unknown, never $0.
func TestDecisionCaptureCounts(t *testing.T) {
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	cost := 0.03
	captures := []decisionlog.LedgerEntry{
		{Repository: "acme/demo", Backend: "claude", State: "succeeded", StartedAt: at, Cost: &cost},
		{Repository: "acme/demo", Backend: "claude", State: "failed", Reason: "timeout", StartedAt: at},
		{Repository: "acme/demo", Backend: "claude", State: "pending", StartedAt: at},
		{Repository: "acme/other", Backend: "codex", State: "failed", Reason: "timeout", StartedAt: at},
		{Repository: "acme/demo", Backend: "claude", State: "abandoned", StartedAt: at, Cost: &cost},
	}
	all := decisionCapture(captures, Query{})
	if all.Attempts != 5 || all.Succeeded != 1 || all.Failed != 2 || all.Abandoned != 1 || all.Cost.USD == nil || *all.Cost.USD != 0.06 || !all.Cost.Incomplete ||
		len(all.Failures) != 1 || all.Failures[0] != (ReasonCount{Reason: "timeout", Count: 2}) {
		t.Fatalf("all %+v", all)
	}
	codex := decisionCapture(captures, Query{Agent: "codex"})
	if codex.Attempts != 1 || codex.Cost.USD != nil || !codex.Cost.Incomplete || len(codex.Cost.Reasons) != 1 {
		t.Fatalf("codex %+v", codex)
	}
	if other := decisionCapture(captures, Query{Repository: "ACME/other"}); other.Attempts != 1 {
		t.Fatalf("by repository %+v", other)
	}
	if none := decisionCapture(nil, Query{}); none.Attempts != 0 || none.Cost.USD != nil || none.Cost.Incomplete || none.Failures == nil {
		t.Fatalf("none %+v", none)
	}
}
