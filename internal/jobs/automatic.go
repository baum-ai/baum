package jobs

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
)

// Why an automatic job was queued.
const (
	TriggerNewHead         = "new_head"
	TriggerReviewRequested = "review_requested"
)

// Reasons SubmitAutomatic refuses an item for good: a person has to generate
// it, if anyone does.
const (
	ReasonIsolation          = "isolation_confirmation_required"
	ReasonCursorAgentConsent = "cursor_agent_acknowledgement_required"
)

var (
	// ErrBusy: a job for the pull request is queued or running. Retry once it
	// finishes; one PR never has two jobs at a time.
	ErrBusy = errors.New("a job for this pull request is queued or running")
	// ErrDuplicate: a job for this head already exists, in any state.
	ErrDuplicate = errors.New("a job for this pull request head already exists")
	// ErrHeadChanged: GitHub reports a different head than the one that
	// triggered the job; the newer head triggers its own job.
	ErrHeadChanged = errors.New("pull request head changed")
	// ErrDailyCap: the job would take the repository's automatic spending
	// today past its daily cap.
	ErrDailyCap = errors.New("daily automatic spending cap reached")
)

// NotAutomaticError refuses an item automatic generation may never run:
// Reason is ReasonIsolation, ReasonCursorAgentConsent or the estimate's
// blocked reason.
type NotAutomaticError struct{ Reason string }

func (e *NotAutomaticError) Error() string { return "not generated automatically: " + e.Reason }

// AutomaticRequest is one job the daemon's scheduler asks for.
type AutomaticRequest struct {
	Item
	// Repository, when set, lets the journal refuse a busy or duplicate
	// request before the item is prepared against GitHub.
	Repository string
	// Head is the head that triggered the job; a different current head is
	// ErrHeadChanged.
	Head    string
	Trigger string
	// AllowUnpriced is the user's one-time acknowledgement that cursor-agent
	// (or another backend billed outside the price list) runs without an
	// estimate. Without it such an item is refused.
	AllowUnpriced bool
	// DailyCap, when set, limits the repository's automatic spending on the
	// calendar day starting at Day.
	DailyCap *float64
	Day      time.Time
}

// SubmitAutomatic queues one job nobody confirmed. It prepares the item
// exactly as an estimate does, so the job runs with the same budgets, price
// list, recheck and isolation as a confirmed one, and refuses whatever a
// person would have to confirm: a fork or an author without known write
// access, an unpriced model, and cursor-agent without the acknowledgement.
// It never supersedes or joins another job of the PR and never runs a head
// twice, and it is refused with ErrRestarting while the daemon drains.
func (q *Queue) SubmitAutomatic(ctx context.Context, r AutomaticRequest) (Job, error) {
	// Refuse what the journal alone decides before asking GitHub.
	if r.Repository != "" {
		q.mu.Lock()
		err := q.automaticConflict(r.Repository, r.PR, r.Head)
		q.mu.Unlock()
		if err != nil {
			return Job{}, err
		}
	}
	p, _, err := q.executor.Prepare(ctx, r.Item)
	if err != nil {
		return Job{}, err
	}
	if p.HeadSHA != r.Head {
		return Job{}, ErrHeadChanged
	}
	if p.RequiresIsolationConfirmation {
		return Job{}, &NotAutomaticError{ReasonIsolation}
	}
	unpriced := p.Backend == agent.BackendCursorAgent || (p.EstimateUSD == nil && p.BlockedReason == "")
	if unpriced && !r.AllowUnpriced {
		return Job{}, &NotAutomaticError{ReasonCursorAgentConsent}
	}
	if p.BlockedReason != "" {
		return Job{}, &NotAutomaticError{p.BlockedReason}
	}
	q.mu.Lock()
	var notification *Job
	defer func() {
		q.mu.Unlock()
		if notification != nil {
			q.notify(*notification)
		}
	}()
	if err := q.automaticConflict(p.Repository, p.PR, p.HeadSHA); err != nil {
		return Job{}, err
	}
	if r.DailyCap != nil {
		spent, known := q.automaticSpend(p.Repository, r.Day)
		if !known || p.EstimateUSD == nil || spent+*p.EstimateUSD > *r.DailyCap+1e-9 {
			return Job{}, ErrDailyCap
		}
	}
	now := time.Now().UTC()
	j := Job{ID: id(), Prepared: p, Automatic: true, Trigger: r.Trigger, State: "queued", Phase: "queued", CreatedAt: now, UpdatedAt: now}
	q.jobs = append(q.jobs, j)
	if err := q.save(); err != nil {
		q.jobs = q.jobs[:len(q.jobs)-1]
		return Job{}, err
	}
	notification = &j
	q.signal()
	return j, nil
}

// automaticConflict is called with mu held.
func (q *Queue) automaticConflict(repository string, pr int, head string) error {
	if q.draining {
		return ErrRestarting
	}
	if q.ctx.Err() != nil {
		return errors.New("job queue stopped")
	}
	for _, j := range q.jobs {
		if !strings.EqualFold(j.Repository, repository) || j.PR != pr {
			continue
		}
		if j.State == "queued" || j.State == "running" {
			return ErrBusy
		}
		if j.HeadSHA == head {
			return ErrDuplicate
		}
	}
	return nil
}

// AutomaticSpend sums the repository's automatic jobs created on the day
// starting at day: actual cost when reported, else the confirmed estimate.
// It is false when a job has neither, so the total is a lower bound.
func (q *Queue) AutomaticSpend(repository string, day time.Time) (float64, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.automaticSpend(repository, day)
}

func (q *Queue) automaticSpend(repository string, day time.Time) (float64, bool) {
	end := day.AddDate(0, 0, 1)
	total, known := 0.0, true
	for _, j := range q.jobs {
		if !j.Automatic || !strings.EqualFold(j.Repository, repository) || j.CreatedAt.Before(day) || !j.CreatedAt.Before(end) {
			continue
		}
		switch {
		case j.ActualCostUSD != nil && j.Phase == "finished":
			total += *j.ActualCostUSD
		case j.State == "cancelled" && j.Accounting == nil && j.Calls == 0:
			// Cancelled before it called a model: nothing spent.
		case j.EstimateUSD != nil:
			total += *j.EstimateUSD
		default:
			known = false
		}
	}
	return total, known
}
