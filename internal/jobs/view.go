package jobs

import (
	"errors"
	"strconv"
	"strings"
)

// Jobs are read in three views so that no client reads the whole history on
// every refresh (GET /api/jobs). All are copies, taken under q.mu once.

// ErrUnknownJob answers a page cursor naming no loaded job.
var ErrUnknownJob = errors.New("unknown job")

// jobSubject is what a job is about: a review group, or a pull request
// (repository folded to lower case, as the app matches it) and, for a
// repository's feature build, pull request 0. It is the app's notion of "a
// PR's job" (ofPR and groupJob in oss/app/src/api.ts).
func jobSubject(j Job) string {
	if j.Group != "" {
		return "group:" + j.Group
	}
	return strings.ToLower(j.Repository) + "#" + strconv.Itoa(j.PR)
}

func cloneJobs(jobs []Job) []Job {
	out := make([]Job, len(jobs))
	copy(out, jobs)
	for i := range out {
		out[i].Accounting = out[i].Accounting.Clone()
	}
	return out
}

// Current is the jobs a client needs on every refresh, oldest first: every
// queued or running job, every failed job not yet seen, and the latest job of
// each subject (pull request, group, or repository for a feature build) for
// each kind. That is what the app's reviewJob, detailsJob, fixJob, groupJob
// and buildJobOf, its active-job count and the tray read.
func (q *Queue) Current() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	latest := map[[2]string]int{}
	for i, j := range q.jobs {
		latest[[2]string{jobSubject(j), j.Kind}] = i
	}
	var out []Job
	for i, j := range q.jobs {
		if j.State == "queued" || j.State == "running" || (j.State == "failed" && j.SeenAt == nil) || latest[[2]string{jobSubject(j), j.Kind}] == i {
			out = append(out, j)
		}
	}
	return cloneJobs(out)
}

// OfPullRequest is every loaded job of one pull request, oldest first: not
// a group's, with the repository matched without regard to case.
func (q *Queue) OfPullRequest(repository string, pr int) []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []Job
	for _, j := range q.jobs {
		if j.Group == "" && j.PR == pr && strings.EqualFold(j.Repository, repository) {
			out = append(out, j)
		}
	}
	return cloneJobs(out)
}

// Page is up to limit jobs, newest first, starting just before the job
// named by before (from the newest when it is empty). next is the id to pass
// as before for the following page, empty when no older job is loaded.
func (q *Queue) Page(before string, limit int) (jobs []Job, next string, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	end := len(q.jobs)
	if before != "" {
		end = -1
		for i, j := range q.jobs {
			if j.ID == before {
				end = i
				break
			}
		}
		if end < 0 {
			return nil, "", ErrUnknownJob
		}
	}
	start := max(end-limit, 0)
	page := cloneJobs(q.jobs[start:end])
	for i, j := 0, len(page)-1; i < j; i, j = i+1, j-1 {
		page[i], page[j] = page[j], page[i]
	}
	if start > 0 {
		next = q.jobs[start].ID
	}
	return page, next, nil
}

// Find is the loaded job with the id.
func (q *Queue) Find(id string) (Job, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, j := range q.jobs {
		if j.ID == id {
			j.Accounting = j.Accounting.Clone()
			return j, true
		}
	}
	return Job{}, false
}
