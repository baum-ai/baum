// Command seeddata writes the seeded profiling dataset (internal/seeddata)
// into a home directory that holds no baum data yet, and prints what it
// wrote. It is developer tooling, not part of the baum binary;
// `just desktop::isolated <name> --seed-data` runs it in Docker.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/baum-ai/baum/internal/seeddata"
)

func main() {
	d := seeddata.Defaults()
	o := seeddata.Options{}
	flag.Int64Var(&o.Seed, "seed", d.Seed, "the seed: the same seed gives the same data")
	flag.StringVar(&o.Home, "home", os.Getenv("HOME"), "the home directory to write into")
	epoch := flag.String("epoch", d.Epoch.Format(time.RFC3339), "the instant the dataset's times count back from (RFC 3339)")
	sessionsAt := flag.String("sessions-at", "", `when the agent sessions' hooks recorded them: RFC 3339, "now", or empty for the epoch`)
	flag.IntVar(&o.Repositories, "repositories", d.Repositories, "repositories")
	flag.IntVar(&o.PullRequests, "pull-requests", d.PullRequests, "open pull requests across the repositories")
	flag.IntVar(&o.LargeDiffLines, "large-diff", d.LargeDiffLines, "changed lines of the largest pull request")
	flag.IntVar(&o.Inboxes, "inboxes", d.Inboxes, "saved inboxes (at most 20)")
	flag.IntVar(&o.ClaudeSessions, "claude-sessions", d.ClaudeSessions, "Claude Code sessions")
	flag.IntVar(&o.CodexSessions, "codex-sessions", d.CodexSessions, "Codex sessions")
	flag.IntVar(&o.LongTranscriptTurns, "long-transcript", d.LongTranscriptTurns, "prompts in each agent's longest session")
	flag.IntVar(&o.DraftComments, "draft-comments", d.DraftComments, "pull requests with draft review comments")
	verbose := flag.Bool("manifest", false, "print the manifest too")
	flag.Parse()
	var err error
	if o.Epoch, err = time.Parse(time.RFC3339, *epoch); err != nil {
		fail(fmt.Errorf("-epoch: %w", err))
	}
	switch *sessionsAt {
	case "":
	case "now":
		o.SessionsAt = time.Now().UTC().Truncate(time.Minute)
	default:
		if o.SessionsAt, err = time.Parse(time.RFC3339, *sessionsAt); err != nil {
			fail(fmt.Errorf("-sessions-at: %w", err))
		}
	}
	started := time.Now()
	result, err := seeddata.Generate(context.Background(), o)
	if err != nil {
		fail(err)
	}
	if !*verbose {
		result.Manifest = nil
	}
	raw, _ := json.MarshalIndent(result, "", "  ")
	fmt.Printf("%s\n", raw)
	fmt.Fprintf(os.Stderr, "seeddata: wrote seed %d into %s in %s\n", o.Seed, o.Home, time.Since(started).Round(100*time.Millisecond))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "seeddata:", err)
	os.Exit(1)
}
