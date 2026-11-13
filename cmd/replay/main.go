// Command replay runs review generation again on the frozen cases of a dataset
// directory, with no GitHub access, and saves what it generates. See
// README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/replay"
)

// list is a repeatable flag.
type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	var (
		dataset        = fs.String("dataset", os.Getenv("BAUM_REPLAY_DATASET"), "dataset directory (or BAUM_REPLAY_DATASET)")
		out            = fs.String("out", envOr("BAUM_REPLAY_OUT", "replay-runs"), "directory that receives runs (or BAUM_REPLAY_OUT)")
		name           = fs.String("name", "", "run name; default is the start time. --resume needs it")
		cases, sizes   list
		overrides      list
		split          = fs.String("split", "", "calibration or held_out")
		representative = fs.Bool("representative", false, "only each pull request's representative case")
		primary        = fs.Bool("primary", false, "only each revision's primary case")
		sessions       = fs.String("with-sessions", "", "claude_code, codex or none")
		limit          = fs.Int("limit", 0, "run at most this many cases")
		seed           = fs.String("shuffle-seed", "", "shuffle the selection with this seed before --limit")
		backend        = fs.String("backend", "claude", "claude or codex")
		model          = fs.String("model", "", "model; empty is the agent's own")
		effort         = fs.String("effort", "", "reasoning effort; empty is the agent's own")
		staged         = fs.Bool("staged", true, "write the review in stages, as the desktop app does")
		noStaged       = fs.Bool("no-staged", false, "write the review in one pass")
		concurrency    = fs.Int("concurrency", 1, "cases run at once")
		maxCost        = fs.Float64("max-cost-usd", 0, "stop starting cases once this much has been spent (0 is no guard)")
		maxCases       = fs.Int("max-cases", 20, "refuse a selection of more cases (0 is no guard)")
		timeLimit      = fs.Duration("time-limit", 0, "time limit of one case; default is the desktop's (10m)")
		resume         = fs.Bool("resume", false, "skip cases that already succeeded in --name's run")
		dryRun         = fs.Bool("dry-run", false, "list what would run and call no model")
		noTranscript   = fs.Bool("no-transcript", false, "do not keep the agent's transcript")
	)
	fs.Var(&cases, "case", "case id; repeatable")
	fs.Var(&sizes, "size", "size bucket xs, s, m, l or xl; repeatable or comma separated")
	fs.Var(&overrides, "prompt-override", "<id>=<file> replaces a staged prompt; repeatable. ids: "+promptIDs())
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dataset == "" {
		return errors.New("--dataset (or BAUM_REPLAY_DATASET) is required")
	}
	if *resume && *name == "" {
		return errors.New("--resume needs --name")
	}
	if *noStaged {
		*staged = false
	}
	d, err := replay.OpenDataset(*dataset)
	if err != nil {
		return err
	}
	sel := replay.Selection{IDs: cases, Split: *split, Representative: *representative, Primary: *primary, Sessions: *sessions, Limit: *limit}
	for _, s := range sizes {
		sel.Sizes = append(sel.Sizes, strings.Split(s, ",")...)
	}
	if *seed != "" {
		n, err := strconv.ParseUint(*seed, 10, 64)
		if err != nil {
			return fmt.Errorf("--shuffle-seed: %w", err)
		}
		sel.ShuffleSeed = &n
	}
	settings := replay.Settings{Backend: config.Backend(*backend), Model: *model, Effort: *effort, Staged: *staged, TimeLimit: *timeLimit, NoTranscript: *noTranscript}
	for _, o := range overrides {
		id, file, ok := strings.Cut(o, "=")
		if !ok || id == "" || file == "" {
			return fmt.Errorf("--prompt-override %q is not <id>=<file>", o)
		}
		text, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if settings.PromptOverrides == nil {
			settings.PromptOverrides = prompt.Overrides{}
		}
		settings.PromptOverrides[prompt.ID(id)] = string(text)
	}
	_, err = replay.Execute(ctx, replay.Plan{Dataset: d, OutDir: *out, Name: *name, Selection: sel, Settings: settings, Concurrency: *concurrency,
		MaxCostUSD: *maxCost, MaxCases: *maxCases, Resume: *resume, DryRun: *dryRun, Out: os.Stdout})
	return err
}

func promptIDs() string {
	var ids []string
	for _, id := range prompt.ReviewIDs(false, true) {
		ids = append(ids, string(id))
	}
	return strings.Join(ids, ", ")
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
