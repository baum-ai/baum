package jobs

import (
	"os"
	"slices"
	"time"
)

// ArchiveAfter is how long after a job ended (its UpdatedAt) it leaves the
// queue's history: List, the API and memory. Its row stays in the journal,
// archived, and job_heads keeps counting it as its head's run. It covers
// analytics' longest window, which counts jobs created in the last 90 days.
const ArchiveAfter = 120 * 24 * time.Hour

// archiveInterval is how often a running queue archives; a variable for
// tests.
var archiveInterval = 24 * time.Hour

// headRuns are the archived jobs that count for a head: runs are those
// automaticConflict counts, others the chat and details jobs that only
// Interrupted counts.
type headRuns struct{ runs, others int }

// archivedRun is what an archived job adds to its head's counts (headKey),
// with the filters of automaticConflict and Interrupted: a fix, or an
// unsettled job, counts for neither.
func archivedRun(j Job) (string, headRuns, bool) {
	switch {
	case j.Kind == KindFix || unsettled(j):
		return "", headRuns{}, false
	case j.Kind == KindChat || j.Kind == KindDetails:
		return headKey(j), headRuns{others: 1}, true
	}
	return headKey(j), headRuns{runs: 1}, true
}

// archiveLoop archives every archiveInterval until the queue stops.
func (q *Queue) archiveLoop() {
	defer q.wg.Done()
	for {
		timer := time.NewTimer(archiveInterval)
		select {
		case <-q.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		_ = q.archive()
	}
}

// archive moves the jobs that ended more than ArchiveAfter ago out of the
// history. One transaction marks their rows archived, adds them to their
// heads' counts and writes those changed since their last write; only after
// it commits are they dropped from memory and their marks cleared, so save
// never deletes their rows. A failed write keeps them for the next pass.
func (q *Queue) archive() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	cutoff := q.clock0().Add(-ArchiveAfter)
	candidates := map[string]bool{}
	encoded := map[string][]byte{}
	byHead := map[string][]Job{}
	for _, j := range q.jobs {
		if j.Kind != KindFix {
			byHead[headKey(j)] = append(byHead[headKey(j)], j)
		}
		if j.State == "queued" || j.State == "running" || q.cancels[j.ID] != nil || !j.UpdatedAt.Before(cutoff) {
			continue
		}
		if q.dirty[j.ID] {
			raw, _, err := encodeJob(j)
			if err != nil {
				// It stays until it can be written.
				continue
			}
			encoded[j.ID] = raw
		}
		candidates[j.ID] = true
	}
	// Refused lists an unsettled job while it is the latest non-fix job of
	// its head. Archiving keeps that answer: the latest job stays when it is
	// unsettled, and so does every job after the last one staying when that
	// one is unsettled, so an unsettled job never becomes the latest.
	for _, jobs := range byHead {
		if latest := jobs[len(jobs)-1]; unsettled(latest) {
			delete(candidates, latest.ID)
		}
		for n := len(jobs) - 1; n >= 0; n-- {
			if candidates[jobs[n].ID] {
				continue
			}
			if unsettled(jobs[n]) {
				for _, later := range jobs[n+1:] {
					delete(candidates, later.ID)
				}
			}
			break
		}
	}
	var ids []string
	var rows []journalRow
	heads := map[string]headRuns{}
	for _, j := range q.jobs {
		if !candidates[j.ID] {
			continue
		}
		if raw, ok := encoded[j.ID]; ok {
			rows = append(rows, journalRow{j, raw})
		}
		ids = append(ids, j.ID)
		if key, h, ok := archivedRun(j); ok {
			sum := heads[key]
			sum.runs, sum.others = sum.runs+h.runs, sum.others+h.others
			heads[key] = sum
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if err := archiveJournal(q.path, rows, ids, heads); err != nil {
		if q.log != nil {
			q.log.Error("job archive failed", "error", err)
		}
		return err
	}
	archived := map[string]bool{}
	for _, id := range ids {
		archived[id] = true
		delete(q.dirty, id)
		delete(q.drafts, id)
		delete(q.superseded, id)
		delete(q.cancels, id)
		if err := os.Remove(transcriptPath(q.transcripts, id)); err != nil && !os.IsNotExist(err) && q.log != nil {
			q.log.Warn("archived job's transcript not removed", "job", id, "error", err)
		}
	}
	q.jobs = slices.DeleteFunc(q.jobs, func(j Job) bool { return archived[j.ID] })
	for key, h := range heads {
		sum := q.heads[key]
		sum.runs, sum.others = sum.runs+h.runs, sum.others+h.others
		q.heads[key] = sum
	}
	return nil
}

// archivedRuns counts the archived runs automaticConflict compares: those of
// a pull request's head, or of a group's member heads. mu is held.
func (q *Queue) archivedRuns(repository string, pr int, group, head string) int {
	// A group job has no pull request of its own.
	if group != "" && pr != 0 {
		return 0
	}
	key := headKey(Job{Prepared: Prepared{Item: Item{PR: pr, Group: group}, Repository: repository, HeadSHA: head, Members: head}})
	return q.heads[key].runs
}
