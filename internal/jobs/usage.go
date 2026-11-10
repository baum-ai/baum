package jobs

import (
	"reflect"
	"time"

	"github.com/baum-ai/baum/agent"
)

// Usage is a running job's usage so far, with the job's state and phase, by
// which a client tells whether its copy of the job is still current.
type Usage struct {
	ID            string            `json:"id"`
	State         string            `json:"state"`
	Phase         string            `json:"phase"`
	Calls         int               `json:"calls"`
	Tokens        int               `json:"tokens"`
	Repairs       int               `json:"repairs"`
	ActualCostUSD *float64          `json:"actual_cost_usd"`
	CostReason    string            `json:"cost_reason"`
	Accounting    *agent.Accounting `json:"accounting"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

func usageOf(j Job) Usage {
	return Usage{ID: j.ID, State: j.State, Phase: j.Phase, Calls: j.Calls, Tokens: j.Tokens, Repairs: j.Repairs,
		ActualCostUSD: j.ActualCostUSD, CostReason: j.CostReason, Accounting: j.Accounting, UpdatedAt: j.UpdatedAt}
}

// SetUsageChanged hands a running job's reports that change nothing but its
// usage to f, instead of to the callback Open was given. A running job
// reports its usage every few seconds; the listeners of that callback rebuild
// what they show from every change, which f's need not.
func (q *Queue) SetUsageChanged(f func(Usage)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.usage = f
}

// usageOnly reports whether a job changed only its usage between before and
// after: its calls, tokens, repairs, accounting and cost.
func usageOnly(before, after Job) bool {
	for _, j := range []*Job{&before, &after} {
		j.Calls, j.Tokens, j.Repairs, j.Accounting = 0, 0, 0, nil
		j.ActualCostUSD, j.CostReason, j.UpdatedAt = nil, "", time.Time{}
	}
	return reflect.DeepEqual(before, after)
}
