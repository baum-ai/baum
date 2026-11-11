package jobs

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/generate"
)

// Why an automatic job was queued.
const (
	TriggerNewHead         = "new_head"
	TriggerReviewRequested = "review_requested"
	TriggerReadyForReview  = "ready_for_review"
	// TriggerRetry re-runs an automatic job a daemon stop or restart
	// interrupted, once.
	TriggerRetry = "retry"
	// TriggerStackReviewed generates a stack group's review once every
	// member has a saved review of its current head.
	TriggerStackReviewed = "stack_reviewed"
	// TriggerSetup reviews one of the pull requests open when desktop setup
	// finished (automatic.Scheduler.CatchUp).
	TriggerSetup = "setup"
)

// Reasons SubmitAutomatic refuses an item for good: a person has to generate
// it, if anyone does.
const (
	ReasonIsolation = "isolation_confirmation_required"
	ReasonDraft     = "draft"
)

// ReasonSettingsUnreadable is the gate's answer when it could not read the
// settings. It says nothing about the job, so a queued job it stops is
// recorded as interrupted, not cancelled: its head stays eligible for the one
// retry an interrupted automatic run gets.
const ReasonSettingsUnreadable = "settings_unreadable"

// A job whose pull request turned draft while it waited is cancelled before
// any model call with ReasonDraft, by the executor's recheck. It never ran,
// so it does not use up its head: Refused lists it, and the scheduler makes
// the head pending again once the PR is ready for review. A job an older baum
// cancelled with the reason "daily_cap" (a spending cap baum no longer has)
// stays an ordinary cancelled job.

var (
	// ErrBusy: a job for the pull request is queued or running, other than
	// a queued automatic review of another head, which a new head takes over.
	// Retry once it finishes; one PR never has two jobs at a time.
	ErrBusy = errors.New("a job for this pull request is queued or running")
	// ErrDuplicate: a job for this head already exists, in any state other
	// than one automatic run a daemon stop or restart interrupted.
	ErrDuplicate = errors.New("a job for this pull request head already exists")
	// ErrHeadChanged: GitHub reports a different head than the one that
	// triggered or confirmed the job; the newer head triggers its own job.
	ErrHeadChanged = generate.ErrHeadChanged
)

// NotAutomaticError refuses an item automatic generation may never run:
// Reason is ReasonIsolation, ReasonDraft or the preparation's blocked reason.
type NotAutomaticError struct{ Reason string }

func (e *NotAutomaticError) Error() string { return "not generated automatically: " + e.Reason }

// AutomaticRequest is one job the daemon's scheduler asks for.
type AutomaticRequest struct {
	Item
	// Repository, when set, lets the journal refuse a busy or duplicate
	// request before the item is prepared against GitHub.
	Repository string
	// Head is the head that triggered the job; a different current head is
	// ErrHeadChanged. A group job's head is its members' heads, as
	// Prepared.Members encodes them.
	Head    string
	Trigger string
}

// SubmitAutomatic queues one job nobody confirmed. It prepares the item
// exactly as a person's start does, so the job runs with the same budgets,
// recheck and isolation as a confirmed one, and refuses what a person would
// have to confirm: a fork or an author without known write access. Nothing
// about the price stops it. It runs a head once (one interrupted automatic
// run is retried once), and it is refused with ErrRestarting while the
// daemon drains. It never joins another job of the PR. A new head of a PR
// whose automatic review has not started replaces that review in place: the
// waiting job takes the new head and keeps its id and its place in line, so
// a PR pushed more often than the queue moves is still reviewed, at its
// newest head. Any other queued or running job of the PR is ErrBusy.
func (q *Queue) SubmitAutomatic(ctx context.Context, r AutomaticRequest) (Job, error) {
	// Refuse what the journal alone decides before asking GitHub.
	if r.Repository != "" {
		q.mu.Lock()
		_, err := q.automaticConflict(r.Repository, r.PR, r.Group, r.Head)
		q.mu.Unlock()
		if err != nil {
			return Job{}, err
		}
	}
	p, _, err := q.executor.Prepare(ctx, r.Item)
	if err != nil {
		return Job{}, err
	}
	if p.head() != r.Head {
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
	var notifications []Job
	defer func() {
		q.mu.Unlock()
		for _, j := range notifications {
			q.notify(j)
		}
	}()
	replace, err := q.automaticConflict(p.Repository, p.PR, p.Group, p.head())
	if err != nil {
		return Job{}, err
	}
	now := q.clock0()
	j := Job{ID: id(), Prepared: p, Automatic: true, Trigger: r.Trigger, State: "queued", Phase: "queued", CreatedAt: now, UpdatedAt: now}
	if replace >= 0 {
		// The waiting job takes the new head: same id, same place in line,
		// and queued since it first was.
		j.ID, j.CreatedAt = q.jobs[replace].ID, q.jobs[replace].CreatedAt
	}
	// Settings are read again here, under the mutex the worker and
	// CancelDisallowedAutomatic hold, so a pass that read them before an off
	// cannot queue after it.
	if q.gate != nil {
		if a := q.gate(j); a.Reason != "" {
			return Job{}, &NotAllowedError{a.Reason}
		}
	}
	before := append([]Job(nil), q.jobs...)
	// A review of a new head replaces a retry of the old head's details.
	var superseded []Job
	var stop []string
	if p.Group == "" {
		superseded, stop = q.supersedeDetails(p.Repository, p.PR, p.HeadSHA)
	}
	if replace >= 0 {
		q.jobs[replace] = j
	} else {
		q.jobs = append(q.jobs, j)
	}
	q.mark(j.ID)
	if err := q.save(); err != nil {
		q.jobs = before
		return Job{}, err
	}
	q.stopSuperseded(stop)
	notifications = append(superseded, j)
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
	// globally or for its repository), or "" when it may.
	Reason string
}

// SetAutomaticGate installs the check of current settings that every
// automatic job passes when it is submitted and again before it starts. It is
// called with the queue's mutex held and must not call the queue. Until a
// gate is installed no automatic job
// starts, so a journaled one cannot run between Open and this call; confirmed
// jobs are not held.
func (q *Queue) SetAutomaticGate(gate func(Job) Admission) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.gate = gate
	q.signal()
}

// refuseAutomatic cancels (or, for an unreadable gate, interrupts) the
// queued job at i if it is automatic and the gate refuses it, recording the
// reason. Called with mu held; the caller publishes the returned job after
// unlocking.
func (q *Queue) refuseAutomatic(i int) (Job, bool) {
	j := &q.jobs[i]
	if !j.Automatic || j.State != "queued" || q.gate == nil {
		return Job{}, false
	}
	reason := q.gate(*j).Reason
	if reason == "" {
		return Job{}, false
	}
	old := *j
	j.State, j.Phase, j.Reason, j.UpdatedAt = "cancelled", "finished", reason, time.Now().UTC()
	if reason == ReasonSettingsUnreadable {
		j.State = "interrupted"
	}
	q.mark(j.ID)
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

// automaticConflict refuses an automatic job of a pull request's (or a
// group's) head with ErrRestarting, ErrBusy or ErrDuplicate, or returns the
// index of the queued job that takes the new head, -1 for none: the pull
// request's automatic review of another head that has not started. A
// group's queued job is not taken over: it waits for its members, and its
// recheck follows their moves (requeueGroup). mu is held.
func (q *Queue) automaticConflict(repository string, pr int, group, head string) (int, error) {
	if q.draining {
		return -1, ErrRestarting
	}
	if q.ctx.Err() != nil {
		return -1, errors.New("job queue stopped")
	}
	replace := -1
	sameHead := 0
	retryable := false
	for i, j := range q.jobs {
		// A retry of a version's details is not a review of its head, and a
		// review of another head supersedes it.
		if !strings.EqualFold(j.Repository, repository) || j.PR != pr || j.Group != group || j.Kind == KindChat || j.Kind == KindDetails {
			continue
		}
		if j.State == "queued" && j.Automatic && j.Kind == "" && group == "" && j.head() != head && replace < 0 {
			replace = i
			continue
		}
		if j.State == "queued" || j.State == "running" {
			return -1, ErrBusy
		}
		// A fix of the head is not its review.
		if j.head() == head && !unsettled(j) && j.Kind != KindFix {
			sameHead++
			retryable = j.Automatic && j.State == "interrupted"
		}
	}
	// A head runs once, except that one automatic run a daemon stop or
	// restart interrupted is retried once. An archived run is never retried.
	if archived := q.archivedRuns(repository, pr, group, head); archived > 0 {
		sameHead, retryable = sameHead+archived, false
	}
	if sameHead > 1 || (sameHead == 1 && !retryable) {
		return -1, ErrDuplicate
	}
	return replace, nil
}

// unsettled: an automatic job cancelled before any model call for a reason
// that says nothing about its head (ReasonDraft, ReasonNeedsClone). It does
// not count as the head's job.
func unsettled(j Job) bool {
	return j.Automatic && j.State == "cancelled" && (j.Reason == ReasonDraft || j.Reason == ReasonNeedsClone)
}

// headKey identifies a pull request head, or a group's member heads,
// case-insensitively by repository.
func headKey(j Job) string {
	if j.Group != "" {
		return strings.ToLower(j.Repository) + "#group:" + j.Group + "@" + j.Members
	}
	return strings.ToLower(j.Repository) + "#" + strconv.Itoa(j.PR) + "@" + j.HeadSHA
}

// head is what an automatic job runs once: the pull request's head, or a
// group's member heads.
func (p Prepared) head() string {
	if p.Group != "" {
		return p.Members
	}
	return p.HeadSHA
}

// Refused lists automatic jobs cancelled before any model call for a reason
// that does not use up their head (ReasonDraft, ReasonNeedsClone), when that
// is the latest job for the head; the scheduler makes such a head pending
// again while the PR is eligible.
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
// and that may be retried once: the only job ever recorded for their head,
// archived ones included.
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
		key := headKey(j)
		if j.Automatic && j.State == "interrupted" && count[key]+q.heads[key].runs+q.heads[key].others == 1 {
			j.Accounting = j.Accounting.Clone()
			out = append(out, j)
		}
	}
	return out
}

// QueuedAutomatic lists the automatic jobs waiting in the queue, so the
// scheduler can keep their number bounded and knows which pull requests'
// waiting reviews a new head takes over.
func (q *Queue) QueuedAutomatic() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []Job
	for _, j := range q.jobs {
		if j.Automatic && j.State == "queued" {
			j.Accounting = j.Accounting.Clone()
			out = append(out, j)
		}
	}
	return out
}

// SetClock replaces the clock that stamps automatic jobs' creation time, which
// the scheduler compares with when it enabled a repository and when the
// signed-in account last changed. The scheduler's clock and this one must
// agree; tests inject the same fake clock into both.
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
