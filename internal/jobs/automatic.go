package jobs

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
)

// Why an automatic job was queued.
const (
	TriggerNewHead         = "new_head"
	TriggerReviewRequested = "review_requested"
	TriggerReadyForReview  = "ready_for_review"
	// TriggerRetry re-runs an automatic job a daemon stop or restart
	// interrupted, once.
	TriggerRetry = "retry"
)

// Reasons SubmitAutomatic refuses an item for good: a person has to generate
// it, if anyone does.
const (
	ReasonIsolation          = "isolation_confirmation_required"
	ReasonCursorAgentConsent = "cursor_agent_acknowledgement_required"
	ReasonDraft              = "draft"
)

// ReasonSettingsUnreadable is the gate's answer when it could not read the
// settings. It says nothing about the job, so a queued job it stops is
// recorded as interrupted, not cancelled: its head stays eligible for the one
// retry an interrupted automatic run gets.
const ReasonSettingsUnreadable = "settings_unreadable"

var (
	// ErrBusy: a job for the pull request is queued or running. Retry once it
	// finishes; one PR never has two jobs at a time.
	ErrBusy = errors.New("a job for this pull request is queued or running")
	// ErrDuplicate: a job for this head already exists, in any state other
	// than one automatic run a daemon stop or restart interrupted.
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
// It never supersedes or joins another job of the PR and runs a head once
// (one interrupted automatic run is retried once), and it is refused with
// ErrRestarting while the daemon drains.
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
	if p.Draft {
		return Job{}, &NotAutomaticError{ReasonDraft}
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
	now := time.Now().UTC()
	j := Job{ID: id(), Prepared: p, Automatic: true, Trigger: r.Trigger, State: "queued", Phase: "queued", CreatedAt: now, UpdatedAt: now}
	// Settings are read again here, under the mutex the worker and
	// CancelDisallowedAutomatic hold, so a pass that read them before an off
	// cannot queue after it.
	if q.gate != nil {
		if reason := q.gate(j); reason != "" {
			return Job{}, &NotAllowedError{reason}
		}
	}
	if r.DailyCap != nil {
		spent, known := q.automaticSpend(p.Repository, r.Day)
		if !known || p.EstimateUSD == nil || spent+*p.EstimateUSD > *r.DailyCap+1e-9 {
			return Job{}, ErrDailyCap
		}
	}
	q.jobs = append(q.jobs, j)
	if err := q.save(); err != nil {
		q.jobs = q.jobs[:len(q.jobs)-1]
		return Job{}, err
	}
	notification = &j
	q.signal()
	return j, nil
}

// NotAllowedError refuses an automatic job because automatic generation is
// off for it now (the gate's Reason); it is not a verdict on the PR.
type NotAllowedError struct{ Reason string }

func (e *NotAllowedError) Error() string { return "automatic generation not allowed: " + e.Reason }

// SetAutomaticGate installs the check of current settings that every
// automatic job passes when it is submitted and again before it starts: it
// returns why the job may not run (automatic generation turned off globally
// or for its repository, cursor-agent no longer acknowledged), or "". It is
// called with the queue's mutex held and must not call the queue. Until a
// gate is installed no automatic job starts, so a journaled one cannot run
// between Open and this call; confirmed jobs are not held.
func (q *Queue) SetAutomaticGate(gate func(Job) string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.gate = gate
	q.signal()
}

// refuseAutomatic cancels (or, for an unreadable gate, interrupts) the queued job at i if it is automatic and the gate
// refuses it, recording the reason. Called with mu held; the caller
// publishes the returned job after unlocking.
func (q *Queue) refuseAutomatic(i int) (Job, bool) {
	j := &q.jobs[i]
	if !j.Automatic || j.State != "queued" || q.gate == nil {
		return Job{}, false
	}
	reason := q.gate(*j)
	if reason == "" {
		return Job{}, false
	}
	old := *j
	j.State, j.Phase, j.Reason, j.UpdatedAt = "cancelled", "finished", reason, time.Now().UTC()
	if reason == ReasonSettingsUnreadable {
		j.State = "interrupted"
	}
	if q.save() != nil {
		*j = old
		return Job{}, false
	}
	return *j, true
}

// CancelDisallowedAutomatic cancels every queued, not yet started automatic
// job the gate now refuses, with its reason. Running jobs keep running.
func (q *Queue) CancelDisallowedAutomatic() []Job {
	q.mu.Lock()
	var out []Job
	for i := range q.jobs {
		if j, ok := q.refuseAutomatic(i); ok {
			out = append(out, j)
		}
	}
	q.mu.Unlock()
	for _, j := range out {
		q.notify(j)
	}
	return out
}

// automaticConflict is called with mu held.
func (q *Queue) automaticConflict(repository string, pr int, head string) error {
	if q.draining {
		return ErrRestarting
	}
	if q.ctx.Err() != nil {
		return errors.New("job queue stopped")
	}
	sameHead := 0
	retryable := false
	for _, j := range q.jobs {
		if !strings.EqualFold(j.Repository, repository) || j.PR != pr {
			continue
		}
		if j.State == "queued" || j.State == "running" {
			return ErrBusy
		}
		if j.HeadSHA == head {
			sameHead++
			retryable = j.Automatic && j.State == "interrupted"
		}
	}
	// A head runs once, except that one automatic run a daemon stop or
	// restart interrupted is retried once.
	if sameHead > 1 || (sameHead == 1 && !retryable) {
		return ErrDuplicate
	}
	return nil
}

// Interrupted lists automatic jobs that a daemon stop or restart interrupted
// and that may be retried once: the only job ever recorded for their head.
func (q *Queue) Interrupted() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	count := map[string]int{}
	key := func(j Job) string { return strings.ToLower(j.Repository) + "#" + strconv.Itoa(j.PR) + "@" + j.HeadSHA }
	for _, j := range q.jobs {
		count[key(j)]++
	}
	var out []Job
	for _, j := range q.jobs {
		if j.Automatic && j.State == "interrupted" && count[key(j)] == 1 {
			j.Accounting = j.Accounting.Clone()
			out = append(out, j)
		}
	}
	return out
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
