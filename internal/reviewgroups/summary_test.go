package reviewgroups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/decisions"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/store"
)

// summaryFixture is a stack of #1 <- #3 <- #2 (a chain whose numbers are not
// in stack order) with fake member reviews and a fake model.
type summaryFixture struct {
	store    *Store
	group    Group
	versions map[int]int
	risks    map[int]string
	calls    []decisions.ModelRequest
	fail     error
	reply    string
	s        *Summarizer
	now      time.Time
}

func newSummaryFixture(t *testing.T) *summaryFixture {
	t.Helper()
	ctx := context.Background()
	f := &summaryFixture{store: openStore(t), versions: map[int]int{}, risks: map[int]string{}, reply: `{"summary":"The stack moves the queue to the new store.  It starts with #1."}`,
		now: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	prs := []discovery.PullRequest{open(1, "a", "main"), open(3, "c", "a"), open(2, "b", "c")}
	if _, err := f.store.SyncStacks(ctx, "acme/demo", prs, nil); err != nil {
		t.Fatal(err)
	}
	list, err := f.store.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("groups %+v %v", list, err)
	}
	f.group = list[0]
	f.s = &Summarizer{
		Store: f.store,
		Index: func(context.Context) (reviewindex.Snapshot, error) { return f.snapshot(), nil },
		Read: func(_ context.Context, clone string, number int) (store.Version, error) {
			if clone != "c1" || f.versions[number] == 0 {
				return store.Version{}, store.ErrNotFound
			}
			doc := fmt.Sprintf(`{"summary":{"title":"PR %d","text":"Summary of #%d."},"sections":[{"title":"Current part","derived":{"state":"current"}},{"title":"Old part","derived":{"state":"stale"}}]}`, number, number)
			return store.Version{Metadata: store.Metadata{Version: f.versions[number]}, Document: []byte(doc)}, nil
		},
		Complete: func(_ context.Context, g Group, req decisions.ModelRequest, check func([]byte) error) ([]byte, string, error) {
			f.calls = append(f.calls, req)
			if f.fail != nil {
				return nil, "", f.fail
			}
			return []byte(f.reply), "claude-haiku-5-5", check([]byte(f.reply))
		},
		now: func() time.Time { return f.now },
	}
	return f
}

func (f *summaryFixture) snapshot() reviewindex.Snapshot {
	var entries []reviewindex.Entry
	for _, number := range []int{1, 2, 3} {
		entry := reviewindex.Entry{PullRequest: discovery.PullRequest{Repository: "acme/demo", Number: number}, CloneID: "c1"}
		if v := f.versions[number]; v > 0 {
			entry.Latest = &store.Metadata{Version: v}
			if risk := f.risks[number]; risk != "" {
				entry.RiskScore = json.RawMessage(risk)
			}
		}
		entries = append(entries, entry)
	}
	return reviewindex.Snapshot{Entries: entries}
}

func TestStackOrderFollowsTheChain(t *testing.T) {
	f := newSummaryFixture(t)
	if got := stackOrder(f.group); !slices.Equal(got, []int{1, 3, 2}) {
		t.Fatalf("order %v", got)
	}
}

func TestSummaryWaitsForTwoReviewsThenFollowsThem(t *testing.T) {
	ctx := context.Background()
	f := newSummaryFixture(t)
	f.versions[1] = 1
	f.s.pass(ctx)
	if len(f.calls) != 0 {
		t.Fatal("one reviewed member was summed up")
	}
	f.versions[2] = 4
	f.s.pass(ctx)
	if len(f.calls) != 1 {
		t.Fatalf("calls %d", len(f.calls))
	}
	var input struct {
		Name         string          `json:"name"`
		PullRequests []summaryMember `json:"pull_requests"`
	}
	if err := json.Unmarshal([]byte(f.calls[0].User), &input); err != nil {
		t.Fatal(err)
	}
	if len(input.PullRequests) != 2 || input.PullRequests[0].Number != 1 || input.PullRequests[1].Number != 2 ||
		!slices.Equal(input.PullRequests[0].Sections, []string{"Current part"}) || input.PullRequests[0].Summary != "Summary of #1." {
		t.Fatalf("input %+v", input)
	}
	summary, err := f.store.StackSummary(ctx, f.group.ID)
	if err != nil || summary == nil || summary.Text != "The stack moves the queue to the new store. It starts with #1." || summary.Model != "claude-haiku-5-5" ||
		!slices.Equal(summary.Members, []SummarizedMember{{1, 1}, {2, 4}}) || summary.Prompt != StackSummaryPrompt {
		t.Fatalf("summary %+v %v", summary, err)
	}
	// Nothing changed: no call.
	f.s.pass(ctx)
	if len(f.calls) != 1 {
		t.Fatalf("calls %d", len(f.calls))
	}
	// A third member's review, then a new version of one: written again each time.
	f.versions[3] = 2
	f.s.pass(ctx)
	f.versions[1] = 2
	f.s.pass(ctx)
	if len(f.calls) != 3 {
		t.Fatalf("calls %d", len(f.calls))
	}
	if err := json.Unmarshal([]byte(f.calls[1].User), &input); err != nil || len(input.PullRequests) != 3 || input.PullRequests[1].Number != 3 {
		t.Fatalf("stack order %+v %v", input, err)
	}
}

func TestFailedSummaryBacksOffAndStops(t *testing.T) {
	ctx := context.Background()
	f := newSummaryFixture(t)
	f.versions[1], f.versions[2] = 1, 1
	f.fail = errors.New("unavailable")
	next := f.s.pass(ctx)
	if len(f.calls) != 1 || !next.Equal(f.now.Add(2*time.Minute)) {
		t.Fatalf("calls %d next %v", len(f.calls), next)
	}
	f.s.pass(ctx)
	if len(f.calls) != 1 {
		t.Fatal("retried before the backoff ended")
	}
	for range 3 {
		f.now = f.now.Add(time.Hour)
		f.s.pass(ctx)
	}
	if len(f.calls) != summaryAttempts {
		t.Fatalf("calls %d", len(f.calls))
	}
	// New reviews start over.
	f.fail = nil
	f.versions[2] = 2
	f.s.pass(ctx)
	if summary, err := f.store.StackSummary(ctx, f.group.ID); err != nil || summary == nil {
		t.Fatalf("summary %+v %v", summary, err)
	}
}

func TestUnusableReplyIsNotStored(t *testing.T) {
	ctx := context.Background()
	f := newSummaryFixture(t)
	f.versions[1], f.versions[2] = 1, 1
	f.reply = `{"summary":"  "}`
	f.s.pass(ctx)
	if summary, err := f.store.StackSummary(ctx, f.group.ID); err != nil || summary != nil {
		t.Fatalf("summary %+v %v", summary, err)
	}
	if _, err := parseSummary([]byte(`{"summary":"` + strings.Repeat("a", maxSummaryRunes+1) + `"}`)); err == nil {
		t.Fatal("a long reply was accepted")
	}
}

func TestPutStackSummaryOnlyForLiveStackGroups(t *testing.T) {
	ctx := context.Background()
	f := newSummaryFixture(t)
	mine, err := f.store.Create(ctx, "acme/demo", "Mine", []int{7, 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.PutStackSummary(ctx, mine.ID, StackSummary{Text: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a person's group took a summary: %v", err)
	}
	if err := f.store.PutStackSummary(ctx, f.group.ID, StackSummary{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.PutStackSummary(ctx, f.group.ID, StackSummary{Text: "y"}); err != nil {
		t.Fatal(err)
	}
	if summary, err := f.store.StackSummary(ctx, f.group.ID); err != nil || summary.Text != "y" {
		t.Fatalf("summary %+v %v", summary, err)
	}
}

func TestMemberBriefFitsItsShare(t *testing.T) {
	sections := make([]string, 0, 20)
	for i := range 20 {
		sections = append(sections, fmt.Sprintf(`{"title":"Section %d with a fairly long title"}`, i))
	}
	doc := `{"summary":{"title":"T","text":"` + strings.Repeat("word ", 400) + `"},"sections":[` + strings.Join(sections, ",") + `]}`
	member, err := memberBrief(4, []byte(doc), minMemberShare)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(member)
	if len(encoded) > minMemberShare || len(member.Sections) != 0 || member.Summary == "" {
		t.Fatalf("%d bytes: %+v", len(encoded), member)
	}
	full, _ := memberBrief(4, []byte(doc), maxSummaryInputBytes)
	if len(full.Sections) != maxSectionTitles {
		t.Fatalf("sections %d", len(full.Sections))
	}
}

func TestStackViewOverOverHTTP(t *testing.T) {
	ctx := context.Background()
	f := newSummaryFixture(t)
	// An earlier release's combined review of the stack stays stored but is
	// not reported.
	if err := f.store.Put(ctx, f.group.ID, 0, groupReview(t, "acme/demo", 1, ReviewedMember{Number: 1, HeadSHA: "aaaaaaa", ReviewVersion: 1}, ReviewedMember{Number: 2, HeadSHA: "bbbbbbb", ReviewVersion: 1})); err != nil {
		t.Fatal(err)
	}
	f.versions[1], f.versions[2], f.versions[3] = 1, 1, 1
	f.risks[1] = `{"score":2,"status":"assessed","reasons":[]}`
	f.risks[3] = `{"score":4,"status":"assessed","reasons":[{"section":"s1","text":"It changes the lock order."}]}`
	f.risks[2] = `{"score":4,"status":"assessed","reasons":[]}`
	api := &API{Store: f.store, Registry: fakeRegistry{}, Index: func(context.Context) (reviewindex.Snapshot, error) { return f.snapshot(), nil }}
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	fixture := &apiFixture{server: server}
	read := func() View {
		t.Helper()
		status, raw := fixture.do(t, "GET", "/api/groups", "")
		var body struct{ Groups []View }
		if err := json.Unmarshal(raw, &body); status != 200 || err != nil || len(body.Groups) != 1 {
			t.Fatalf("%d %s %v", status, raw, err)
		}
		_, one := fixture.do(t, "GET", "/api/groups/"+f.group.ID, "")
		var single View
		if err := json.Unmarshal(one, &single); err != nil || (single.Overview == nil) != (body.Groups[0].Overview == nil) {
			t.Fatalf("one %s %v", one, err)
		}
		return body.Groups[0]
	}
	v := read()
	// #3 comes before #2 in the stack, so it wins the tie.
	if v.VersionCount != 0 || v.Latest != nil || v.Outdated || v.Overview == nil || v.Overview.Riskiest != 3 || v.Overview.Reviewed != 3 ||
		v.Overview.Summary != nil || !strings.Contains(string(v.RiskScore), "lock order") {
		t.Fatalf("view %+v %s", v, v.RiskScore)
	}
	f.s.pass(ctx)
	if v = read(); v.Overview.Summary == nil || !v.Overview.Current {
		t.Fatalf("view %+v", v.Overview)
	}
	f.versions[2] = 2
	if v = read(); v.Overview.Summary == nil || v.Overview.Current {
		t.Fatalf("a new member review left it current: %+v", v.Overview)
	}
	if status, _ := fixture.do(t, "GET", "/api/groups/"+f.group.ID+"/document", ""); status != 404 {
		t.Fatalf("a stack's combined review was served: %d", status)
	}
	if status, raw := fixture.do(t, "GET", "/api/groups/"+f.group.ID+"/versions", ""); status != 200 || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("versions %d %s", status, raw)
	}
	// Taken over by the person, its combined review shows again.
	name := "Mine now"
	if _, err := f.store.Update(ctx, f.group.ID, &name, nil); err != nil {
		t.Fatal(err)
	}
	if v = read(); v.Overview != nil || v.VersionCount != 1 {
		t.Fatalf("view %+v", v)
	}
}
