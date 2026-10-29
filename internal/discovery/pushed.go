package discovery

import (
	"context"
	"time"

	"github.com/baum-ai/baum/internal/github"
)

// PushedDelays are the waits between the polls after baum itself pushed a
// pull request's head (a fix). A poll right after the push finds the head
// before GitHub has queued its checks: no checks and a clean merge state,
// which reads as ready to merge. The checks usually appear within seconds,
// and neither the probe (check results do not move a pull request's update
// time) nor the settle poll (the background scheduler does not see a poll it
// did not make) would notice them before the usual backoff.
var PushedDelays = []time.Duration{0, 5 * time.Second, 10 * time.Second, 15 * time.Second, 30 * time.Second}

// AfterPush polls repository at PushedDelays (delays, in tests) until the
// pull request shows head with its checks reported, or is no longer open,
// and calls changed after each poll that succeeded. A repository without
// checks reports none for every poll, so the polls stop with the delays.
// poll is Service.PollNow, which still honors the shared rate budgets.
func AfterPush(ctx context.Context, poll func(context.Context, github.Repository) (Snapshot, error), repository github.Repository, number int, head string, changed func(), delays []time.Duration) {
	if delays == nil {
		delays = PushedDelays
	}
	for _, delay := range delays {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		snapshot, err := poll(ctx, repository)
		if err != nil {
			continue
		}
		changed()
		if checksReported(snapshot, number, head) {
			return
		}
	}
}

// checksReported is whether the snapshot settles the pushed head: the pull
// request shows it with checks running or finished, or is no longer open.
func checksReported(snapshot Snapshot, number int, head string) bool {
	for _, pr := range snapshot.PullRequests {
		if pr.Number != number {
			continue
		}
		if pr.HeadSHA != head {
			return false
		}
		switch pr.Checks {
		case "running", "passing", "failing":
			return true
		}
		return false
	}
	return true
}
