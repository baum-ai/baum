package automatic

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/reviewgroups"
)

// Groups is the review group store as the scheduler reads it.
type Groups interface {
	List(context.Context) ([]reviewgroups.Group, error)
	Summary(context.Context, string) (reviewgroups.Summary, error)
}

// groupState is one stack group's automatic review. Its heads are the
// members' heads as jobs.Prepared.Members encodes them ("7@<sha>,9@<sha>").
type groupState struct {
	// Done is the last heads queued, found already reviewed or refused.
	Done string `json:"done,omitempty"`
	// Heads is what Attempts and RetryAt count for.
	Heads    string    `json:"heads,omitempty"`
	Attempts int       `json:"attempts,omitempty"`
	RetryAt  time.Time `json:"retry_at,omitzero"`
	// Retried and Refused are as a PR's: the heads whose interrupted run
	// was put back once, and the last refused job put back.
	Retried string `json:"retried,omitempty"`
	Refused string `json:"refused,omitempty"`
}

type groupWork struct {
	name  string
	repo  repository
	group reviewgroups.Group
	heads map[int]string
	key   string
	// first: the pass saw the group for the first time, as a baseline sees a
	// PR; members already reviewed then are not caught up.
	first bool
}

// memberHeads is the group's heads when every member is open, eligible and
// has no automatic job still to submit. Called with mu held.
func memberHeads(rs *repoState, members []int) (map[int]string, string, bool) {
	heads := map[int]string{}
	parts := make([]string, 0, len(members))
	for _, number := range members {
		pr := rs.PRs[number]
		if pr == nil || pr.Head == "" || !pr.Eligible || pr.Pending != "" {
			return nil, "", false
		}
		heads[number] = pr.Head
		parts = append(parts, strconv.Itoa(number)+"@"+pr.Head)
	}
	return heads, strings.Join(parts, ","), true
}

// passGroups queues the review of each stack group of a repository with
// automatic generation on, once every member has a saved review of its
// current head and the group's latest review does not already cover those
// heads. Each set of heads is submitted once, like a PR's head; a run a
// daemon stop interrupted is retried once, and one refused before it started
// is submitted again.
func (s *Scheduler) passGroups(ctx context.Context, v view, interrupted, refused []jobs.Job, paused map[string]bool, now time.Time) {
	if s.Groups == nil {
		return
	}
	groups, err := s.Groups.List(ctx)
	if err != nil {
		s.log().Warn("automatic group reviews skipped", "error", err)
		return
	}
	s.mu.Lock()
	state := s.load()
	changed := false
	stateOf := func(j jobs.Job) *groupState {
		if j.Group == "" {
			return nil
		}
		if rs := state.Repositories[strings.ToLower(j.Repository)]; rs != nil && !j.CreatedAt.Before(rs.Since) {
			if gs := rs.Groups[j.Group]; gs != nil && gs.Done == j.Members {
				return gs
			}
		}
		return nil
	}
	for _, j := range interrupted {
		if gs := stateOf(j); gs != nil && gs.Retried != j.Members {
			gs.Done, gs.Retried, changed = "", j.Members, true
		}
	}
	for _, j := range refused {
		if gs := stateOf(j); gs != nil && gs.Refused != j.ID {
			gs.Done, gs.Refused, changed = "", j.ID, true
		}
	}
	live := map[string]bool{}
	var due []groupWork
	for _, g := range groups {
		rs := state.Repositories[g.Repository]
		if !g.Stack || rs == nil || rs.AwaitingBaseline {
			continue
		}
		live[g.ID] = true
		gs := rs.Groups[g.ID]
		first := gs == nil
		if first {
			gs = &groupState{}
			rs.Groups[g.ID] = gs
			changed = true
		}
		r := v.repos[g.Repository]
		heads, key, ok := memberHeads(rs, g.Members)
		if !ok || !r.clone.Available || paused[g.Repository] {
			continue
		}
		if gs.Done == key || (gs.Heads == key && now.Before(gs.RetryAt)) {
			continue
		}
		due = append(due, groupWork{g.Repository, r, g, heads, key, first})
	}
	for _, rs := range state.Repositories {
		for id := range rs.Groups {
			if !live[id] {
				delete(rs.Groups, id)
				changed = true
			}
		}
	}
	if changed {
		s.save()
	}
	s.mu.Unlock()
	for _, w := range due {
		if ctx.Err() != nil || (s.Hold != nil && s.Hold()) {
			return
		}
		if paused[w.name] {
			continue
		}
		switch s.submitGroup(ctx, w, now) {
		case stopAll:
			return
		case stopRepository:
			paused[w.name] = true
		}
	}
}

func (s *Scheduler) submitGroup(ctx context.Context, w groupWork, now time.Time) outcome {
	reviewed := s.Reviewed
	if reviewed == nil {
		reviewed = SavedHead
	}
	var err error
	var job jobs.Job
	if summary, serr := s.Groups.Summary(ctx, w.group.ID); serr == nil && covered(summary.Reviewed, w.heads) {
		err = jobs.ErrDuplicate
	} else {
		// The members' own reviews come first: their completion wakes a pass.
		for _, number := range w.group.Members {
			if head, err := reviewed(ctx, w.repo.clone, number); err != nil || head != w.heads[number] {
				return carryOn
			}
		}
		if w.first {
			err = jobs.ErrDuplicate
		} else {
			job, err = s.Queue.SubmitAutomatic(ctx, jobs.AutomaticRequest{Item: jobs.Item{Clone: w.repo.clone.ID, Group: w.group.ID}, Repository: w.repo.clone.Repository, Head: w.key, Trigger: jobs.TriggerStackReviewed})
		}
	}
	s.mu.Lock()
	rs := s.load().Repositories[w.name]
	var gs *groupState
	if rs != nil {
		gs = rs.Groups[w.group.ID]
	}
	if gs == nil {
		// Disabled or dissolved while this was submitted.
		s.mu.Unlock()
		return carryOn
	}
	result := carryOn
	notice := rs.Notice
	var notAutomatic *jobs.NotAutomaticError
	var notAllowed *jobs.NotAllowedError
	quiet := false
	switch {
	case err == nil:
		s.log().Info("queued an automatic stack review", "repository", w.name, "group", w.group.ID, "job", job.ID)
		gs.Done = w.key
		notice = ""
	case errors.Is(err, jobs.ErrDuplicate):
		gs.Done = w.key
	case errors.Is(err, jobs.ErrHeadChanged), errors.As(err, &notAutomatic) && notAutomatic.Reason == jobs.ReasonDraft:
		// GitHub is ahead of discovery: a member moved on or turned draft.
		// Discovery soon shows it, which changes or holds these heads.
		gs.Heads, gs.RetryAt = w.key, now.Add(time.Minute)
	case errors.Is(err, jobs.ErrBusy):
	case errors.Is(err, jobs.ErrRestarting):
		result = stopAll
	case errors.Is(err, jobs.ErrNeedsClone):
		result, quiet = stopRepository, true
	case errors.As(err, &notAllowed):
		result = stopRepository
	case errors.As(err, &notAutomatic):
		gs.Done = w.key
		notice = notAutomatic.Reason
	default:
		if gs.Heads != w.key {
			gs.Heads, gs.Attempts = w.key, 0
		}
		gs.Attempts++
		if gs.Attempts >= maxAttempts {
			gs.Done = w.key
			notice = NoticeUnavailable
		} else {
			gs.RetryAt = now.Add(time.Duration(1<<gs.Attempts) * time.Minute)
		}
		s.log().Warn("automatic stack review not queued", "repository", w.name, "group", w.group.ID, "attempt", gs.Attempts, "error", err)
	}
	announce := notice != rs.Notice || (result == stopRepository && !quiet)
	rs.Notice = notice
	s.save()
	status := s.status(w.name, w.repo, rs)
	s.mu.Unlock()
	if announce {
		s.notify([]Status{status})
	}
	return result
}

// covered reports that a group review read every current member at its
// current head; members it covered that left the stack (merged, say) do not
// call for another.
func covered(reviewed []reviewgroups.ReviewedMember, heads map[int]string) bool {
	read := map[int]string{}
	for _, member := range reviewed {
		read[member.Number] = member.HeadSHA
	}
	for number, head := range heads {
		if !strings.EqualFold(read[number], head) {
			return false
		}
	}
	return len(heads) > 0
}
