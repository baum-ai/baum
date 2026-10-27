package jobs

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
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

// ReasonDailyCap is why a queued automatic job was cancelled before it
// started: the repository's automatic spending that day, with this job's
// estimate, would exceed its current daily cap. The job never ran, so it does
// not use up its head: Refused lists it, and the scheduler makes the head
// pending again, where the cap pauses the repository as it does at submission.
// A job whose pull request turned draft while it waited is cancelled the same
// way with ReasonDraft, by the recheck before any model call; its head is
// pending again once the PR is ready for review.
const ReasonDailyCap = "daily_cap"

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
	now := q.clock0()
	j := Job{ID: id(), Prepared: p, Automatic: true, Trigger: r.Trigger, State: "queued", Phase: "queued", CreatedAt: now, UpdatedAt: now}
	// Settings are read again here, under the mutex the worker and
	// CancelDisallowedAutomatic hold, so a pass that read them before an off
	// cannot queue after it.
	if q.gate != nil {
		if a := q.gate(j); a.Reason != "" {
			return Job{}, &NotAllowedError{a.Reason}
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

// Admission is the automatic gate's answer for one job, from the current
// settings.
type Admission struct {
	// Reason is why the job may not run now (automatic generation turned off
	// globally or for its repository, cursor-agent no longer acknowledged),
	// or "" when it may.
	Reason string
	// DailyCap is the repository's current daily cap, nil for none. Before
	// a queued job starts, the queue checks it against the automatic spending
	// of the calendar day that starts at Day.
	DailyCap *float64
	Day      time.Time
}

// SetAutomaticGate installs the check of current settings that every
// automatic job passes when it is submitted and again before it starts. It is
// called with the queue's mutex held and must not call the queue. Before a
// job starts, the queue also checks the returned daily cap itself, under the
// same mutex, so spending reported or a cap lowered while the job waited
// stops it (ReasonDailyCap). Until a gate is installed no automatic job
// starts, so a journaled one cannot run between Open and this call; confirmed
// jobs are not held.
func (q *Queue) SetAutomaticGate(gate func(Job) Admission) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.gate = gate
	q.signal()
}

// refuseAutomatic cancels (or, for an unreadable gate, interrupts) the
// queued job at i if it is automatic and the gate refuses it or its start
// would take the repository past its current daily cap, recording the
// reason. Called with mu held; the caller publishes the returned job after
// unlocking.
func (q *Queue) refuseAutomatic(i int) (Job, bool) {
	j := &q.jobs[i]
	if !j.Automatic || j.State != "queued" || q.gate == nil {
		return Job{}, false
	}
	a := q.gate(*j)
	reason := a.Reason
	if reason == "" && a.DailyCap != nil && !q.startWithinCap(*j, *a.DailyCap, a.Day) {
		reason = ReasonDailyCap
	}
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

// startWithinCap reports whether starting the queued job j keeps its
// repository's automatic spending on the day starting at day within limit:
// the jobs that already started or finished that day (actual cost when
// reported, else the estimate) plus j's estimate. Other queued jobs are not
// counted; each is checked when its turn comes. An unknown amount is over
// the cap, as at submission. Called with mu held.
func (q *Queue) startWithinCap(j Job, limit float64, day time.Time) bool {
	if j.EstimateUSD == nil {
		return false
	}
	spent, known := q.spend(j.Repository, day, func(o Job) bool { return o.State == "queued" })
	return known && spent+*j.EstimateUSD <= limit+1e-9
}

// CancelDisallowedAutomatic cancels every queued, not yet started automatic
// job the gate, or the daily cap, now refuses, with its reason. Running jobs
// keep running.
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
		// A fix of the head is not its review.
		if j.HeadSHA == head && !unsettled(j) && j.Kind != KindFix {
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

// unsettled: an automatic job cancelled before any model call for a reason
// that says nothing about its head (ReasonDailyCap, ReasonDraft,
// ReasonNeedsClone). It does not
// count as the head's job.
func unsettled(j Job) bool {
	return j.Automatic && j.State == "cancelled" && (j.Reason == ReasonDailyCap || j.Reason == ReasonDraft || j.Reason == ReasonNeedsClone)
}

// headKey identifies a pull request head, case-insensitively by repository.
func headKey(j Job) string {
	return strings.ToLower(j.Repository) + "#" + strconv.Itoa(j.PR) + "@" + j.HeadSHA
}

// Refused lists automatic jobs cancelled before any model call for a reason
// that does not use up their head (ReasonDailyCap, ReasonDraft,
// ReasonNeedsClone), when that is
// the latest job for the head; the scheduler makes such a head pending again
// while the PR is eligible.
func (q *Queue) Refused() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	latest := map[string]int{}
	for i, j := range q.jobs {
		if j.Kind != KindFix {
			latest[headKey(j)] = i
		}
	}
	var out []Job
	for i, j := range q.jobs {
		if unsettled(j) && latest[headKey(j)] == i {
			j.Accounting = j.Accounting.Clone()
			out = append(out, j)
		}
	}
	return out
}

// Interrupted lists automatic jobs that a daemon stop or restart interrupted
// and that may be retried once: the only job ever recorded for their head.
func (q *Queue) Interrupted() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	count := map[string]int{}
	for _, j := range q.jobs {
		if !unsettled(j) && j.Kind != KindFix {
			count[headKey(j)]++
		}
	}
	var out []Job
	for _, j := range q.jobs {
		if j.Automatic && j.State == "interrupted" && count[headKey(j)] == 1 {
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
	return q.spend(repository, day, nil)
}

// spend is automaticSpend leaving out the jobs skip selects. Called with mu
// held.
func (q *Queue) spend(repository string, day time.Time, skip func(Job) bool) (float64, bool) {
	end := day.AddDate(0, 0, 1)
	total, known := 0.0, true
	for _, j := range q.jobs {
		if !j.Automatic || !strings.EqualFold(j.Repository, repository) || j.CreatedAt.Before(day) || !j.CreatedAt.Before(end) || (skip != nil && skip(j)) {
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

// SetClock replaces the clock that stamps automatic jobs' creation time, which
// the daily cap's calendar day is measured against. The scheduler's clock and
// this one must agree; tests inject the same fake clock into both.
func (q *Queue) SetClock(now func() time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.clock = now
}

// clock0 reads the clock; the caller holds q.mu.
func (q *Queue) clock0() time.Time {
	if q.clock != nil {
		return q.clock().UTC()
	}
	return time.Now().UTC()
}
