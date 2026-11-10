package jobs

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

// usageExecutor reports a phase, then usage alone, then a draft.
type usageExecutor struct{ *fakeExecutor }

func (u usageExecutor) Execute(_ context.Context, _ Prepared, progress func(Progress)) (*float64, error) {
	progress(Progress{Phase: "generating", Calls: 1, Tokens: 10})
	progress(Progress{Phase: "generating", Calls: 2, Tokens: 30, CostReason: "speed_not_reported"})
	progress(Progress{Phase: "generating", Calls: 2, Tokens: 30, Draft: []byte(`{}`)})
	return nil, nil
}

// A report that changes only a running job's usage goes to the usage
// callback alone; one that changes anything else goes to changed.
func TestUsageOnlyReportsSkipTheChangedCallback(t *testing.T) {
	var mu sync.Mutex
	var changed []Job
	var usage []Usage
	f := &fakeExecutor{head: "first"}
	q, err := Open(filepath.Join(privateDir(t), "jobs.json"), usageExecutor{f}, 1, func(j Job) {
		mu.Lock()
		defer mu.Unlock()
		changed = append(changed, j)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	q.SetUsageChanged(func(u Usage) {
		mu.Lock()
		defer mu.Unlock()
		usage = append(usage, u)
	})
	e := estimate(t, q, "single", Item{Clone: "clone", PR: 1})
	submitted, err := q.Submit(context.Background(), e.ID, true, false)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, q, submitted[0].ID, "completed")
	mu.Lock()
	defer mu.Unlock()
	if len(usage) != 1 {
		t.Fatalf("usage reports %+v", usage)
	}
	u := usage[0]
	if u.ID != submitted[0].ID || u.State != "running" || u.Phase != "generating" || u.Calls != 2 || u.Tokens != 30 || u.CostReason != "speed_not_reported" || u.UpdatedAt.IsZero() {
		t.Fatalf("usage %+v", u)
	}
	var phases []string
	for _, j := range changed {
		phases = append(phases, j.State+"/"+j.Phase)
		if j.Tokens == 30 && j.Draft == 0 && j.State == "running" {
			t.Fatalf("the usage-only report reached changed: %+v", j)
		}
	}
	// Queued, started, the phase, the draft, finished.
	if len(changed) != 5 || changed[2].Phase != "generating" || changed[3].Draft != 1 {
		t.Fatalf("changed %v", phases)
	}
}

func TestUsageOnlyComparesEverythingButUsage(t *testing.T) {
	cost := 1.5
	before := Job{ID: "a", State: "running", Phase: "generating", Calls: 1}
	after := before
	after.Calls, after.Tokens, after.Repairs, after.ActualCostUSD, after.CostReason = 2, 40, 1, &cost, "lower_bound"
	if !usageOnly(before, after) {
		t.Fatal("usage counted as a change")
	}
	for name, change := range map[string]func(*Job){
		"phase":      func(j *Job) { j.Phase = "repairing" },
		"state":      func(j *Job) { j.State = "completed" },
		"draft":      func(j *Job) { j.Draft = 1 },
		"transcript": func(j *Job) { j.HasTranscript = true },
		"model":      func(j *Job) { j.Model = "other" },
		"sections":   func(j *Job) { j.FailedSections = []string{"risks"} },
	} {
		changed := after
		change(&changed)
		if usageOnly(before, changed) {
			t.Fatal(name, "taken for usage")
		}
	}
}
