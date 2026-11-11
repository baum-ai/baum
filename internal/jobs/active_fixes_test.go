package jobs

import "testing"

func TestActiveFixesListsOnlyQueuedAndRunningFixes(t *testing.T) {
	q, _ := newQueue(t)
	job := func(id, kind, state string) Job {
		j := Job{ID: id, State: state}
		j.Kind = kind
		return j
	}
	q.mu.Lock()
	q.jobs = []Job{
		job("done-fix", KindFix, "succeeded"),
		job("queued-fix", KindFix, "queued"),
		job("running-review", "", "running"),
		job("running-fix", KindFix, "running"),
		job("failed-fix", KindFix, "failed"),
	}
	q.mu.Unlock()
	var got []string
	for _, j := range q.ActiveFixes() {
		got = append(got, j.ID)
	}
	if len(got) != 2 || got[0] != "queued-fix" || got[1] != "running-fix" {
		t.Fatalf("ActiveFixes = %v, want the queued and running fixes in order", got)
	}
}
