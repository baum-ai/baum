// Command replay runs review generation again on the frozen cases of a dataset
// directory, with no GitHub access, and saves what it generates. Its metrics
// subcommand measures a finished run. See README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
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

// triState is a flag that is true, false or not given; a bare flag is true.
type triState struct{ value *bool }

func (t *triState) String() string {
	if t == nil || t.value == nil {
		return ""
	}
	return strconv.FormatBool(*t.value)
}

func (t *triState) IsBoolFlag() bool { return true }

func (t *triState) Set(v string) error {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return errors.New("use true or false")
	}
	t.value = &b
	return nil
}

// readCaseFile reads case ids, one per line; blank lines and # comments are
// ignored.
func readCaseFile(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, line := range strings.Split(string(raw), "\n") {
		line, _, _ = strings.Cut(line, "#")
		if id := strings.TrimSpace(line); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	switch {
	case err == nil || errors.Is(err, flag.ErrHelp):
	default:
		fmt.Fprintln(os.Stderr, "replay:", err)
		stop()
		os.Exit(1)
	}
}

// errFailed is returned, after the summary is written, by a run in which a
// case failed or the run was stopped before it finished.
type errFailed struct{ failed, skipped int }

func (e errFailed) Error() string {
	return fmt.Sprintf("%d case(s) failed and %d were not run; see the summary above", e.failed, e.skipped)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "metrics" {
		return runMetrics(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		dataset                = fs.String("dataset", os.Getenv("BAUM_REPLAY_DATASET"), "dataset directory (or BAUM_REPLAY_DATASET)")
		out                    = fs.String("out", envOr("BAUM_REPLAY_OUT", defaultOut()), "directory that receives runs (or BAUM_REPLAY_OUT); default is baum/replay-runs under the user cache directory")
		name                   = fs.String("name", "", "run name; default is the start time. --resume needs it")
		cases, sizes, agentEnv list
		overrides, genOptions  list
		split                  = fs.String("split", "", "calibration or held_out")
		representative         = fs.Bool("representative", false, "only each pull request's representative case")
		primary                = fs.Bool("primary", false, "only each revision's primary case")
		sessions               = fs.String("with-sessions", "", "claude_code, codex or none")
		limit                  = fs.Int("limit", 0, "run at most this many cases")
		seed                   = fs.String("shuffle-seed", "", "shuffle the selection with this seed before --limit")
		backend                = fs.String("backend", "claude", "claude or codex")
		model                  = fs.String("model", "", "model; empty is the agent's own")
		effort                 = fs.String("effort", "", "reasoning effort; empty is the agent's own")
		staged                 = fs.Bool("staged", true, "write the review in stages, as the desktop app does")
		noStaged               = fs.Bool("no-staged", false, "write the review in one pass (wins over --staged)")
		reuse                  = fs.Bool("reuse", false, "reuse the previous version's section details as the pipeline does; by default every section is generated")
		claudeDir              = fs.String("claude-config-dir", "", "CLAUDE_CONFIG_DIR for Claude Code, as the app sets it; default is the ambient login")
		codexHome              = fs.String("codex-home", "", "CODEX_HOME for Codex, as the app sets it; default is the ambient login")
		resumeAnyway           = fs.Bool("resume-anyway", false, "with --resume, continue a run whose saved settings differ")
		concurrency            = fs.Int("concurrency", 1, "cases run at once")
		maxCost                = fs.Float64("max-cost-usd", 0, "stop starting cases once this much has been spent (0 is no guard)")
		maxCases               = fs.Int("max-cases", 20, "refuse a selection of more cases (0 is no guard)")
		timeLimit              = fs.Duration("time-limit", 0, "time limit of one case; default is the desktop's (10m)")
		resume                 = fs.Bool("resume", false, "skip cases that already succeeded in --name's run")
		dryRun                 = fs.Bool("dry-run", false, "list what would run and call no model")
		noTranscript           = fs.Bool("no-transcript", false, "do not keep the agent's transcript")
		caseFile               = fs.String("case-file", "", "file with one case id per line (# starts a comment); like --case")
		detector               = fs.String("detector", "", "keep cases a structural detector matches: schema, api, any or none")
		generatorName          = fs.String("generator", "review", "what to generate: "+strings.Join(replay.GeneratorNames(), ", "))
		detectorReport         = fs.Bool("detector-report", false, "write the case ids by kind of detector evidence into <out>/<name>/detectors (for labelling a sample)")
		chain                  = fs.Bool("chain", false, "replay each pull request's revisions in order, each reading the previous one's generated document as its previous version")
		composeFromStore       = fs.Bool("compose-from-store", false, "run only the compose stage over the dataset's saved reviews of the selected cases (the compose generator)")
		composeOnly            = fs.Bool("compose-only", false, "run only the compose stage over the review documents of --from-run (the compose generator)")
		fromRun                = fs.String("from-run", "", "with --compose-only, the earlier run directory whose cases/<id>/document.json are composed")
		images, threads, prev  triState
	)
	fs.Var(&images, "with-images", "keep cases whose bundle has images (--with-images=false: has none)")
	fs.Var(&threads, "with-threads", "keep cases whose bundle has review threads (=false: none)")
	fs.Var(&prev, "with-previous", "keep cases whose bundle has a previous version (=false: none)")
	fs.Var(&cases, "case", "case id; repeatable")
	fs.Var(&sizes, "size", "size bucket xs, s, m, l or xl; repeatable or comma separated")
	fs.Var(&agentEnv, "agent-env", "KEY=VALUE set for the agent, as the app's agent_env does; repeatable")
	fs.Var(&genOptions, "generator-option", "KEY=VALUE for the generator; repeatable (the review generator takes none)")
	fs.Var(&overrides, "prompt-override", "<id>=<file> replaces a staged prompt, or with the compose stage the composition prompt; repeatable. ids: "+promptIDs())
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noPositional(fs, args); err != nil {
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
	if *caseFile != "" {
		ids, err := readCaseFile(*caseFile)
		if err != nil {
			return err
		}
		cases = append(cases, ids...)
		if len(ids) == 0 {
			return fmt.Errorf("--case-file %s names no case", *caseFile)
		}
	}
	sel := replay.Selection{IDs: cases, Split: *split, Representative: *representative, Primary: *primary, Sessions: *sessions, Limit: *limit,
		Images: images.value, Threads: threads.value, Previous: prev.value, Detector: *detector}
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
	genConfig := replay.GeneratorConfig{Staged: *staged, Reuse: *reuse}
	settings := replay.Settings{Backend: config.Backend(*backend), Model: *model, Effort: *effort, TimeLimit: *timeLimit, NoTranscript: *noTranscript,
		ClaudeConfigDir: *claudeDir, CodexHome: *codexHome}
	for _, kv := range agentEnv {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return fmt.Errorf("--agent-env %q is not KEY=VALUE", kv)
		}
		if settings.AgentEnv == nil {
			settings.AgentEnv = map[string]string{}
		}
		settings.AgentEnv[name] = value
	}
	for _, o := range overrides {
		id, file, ok := strings.Cut(o, "=")
		if !ok || id == "" || file == "" {
			return fmt.Errorf("--prompt-override %q is not <id>=<file>", o)
		}
		text, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if genConfig.PromptOverrides == nil {
			genConfig.PromptOverrides = map[string]string{}
		}
		genConfig.PromptOverrides[id] = string(text)
	}
	for _, kv := range genOptions {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return fmt.Errorf("--generator-option %q is not KEY=VALUE", kv)
		}
		if genConfig.Options == nil {
			genConfig.Options = map[string]string{}
		}
		genConfig.Options[key] = value
	}
	switch {
	case *composeFromStore && *composeOnly:
		return errors.New("use --compose-from-store or --compose-only, not both")
	case *fromRun != "" && !*composeOnly:
		return errors.New("--from-run goes with --compose-only")
	case *composeFromStore || *composeOnly:
		if *chain {
			return errors.New("the compose stage runs on reviews that exist; it does not take --chain")
		}
		if len(genConfig.Options) > 0 {
			return errors.New("--compose-from-store and --compose-only set the generator's options themselves")
		}
		*generatorName = "compose"
		genConfig.Options = map[string]string{"source": replay.ComposeFromStore, "dataset": *dataset}
		if *composeOnly {
			genConfig.Options = map[string]string{"source": replay.ComposeFromRun, "run": *fromRun}
		}
	}
	if settings.Generator, err = replay.NewGenerator(*generatorName, genConfig); err != nil {
		return err
	}
	summary, err := replay.Execute(ctx, replay.Plan{Dataset: d, OutDir: *out, Name: *name, Selection: sel, Settings: settings, Concurrency: *concurrency,
		MaxCostUSD: *maxCost, MaxCases: *maxCases, Chain: *chain, DetectorReport: *detectorReport, Resume: *resume, ResumeAnyway: *resumeAnyway, DryRun: *dryRun, Out: stdout})
	if err != nil {
		return err
	}
	if summary.Totals.Failed > 0 || summary.Totals.Skipped > 0 || summary.Stopped != "" {
		return errFailed{summary.Totals.Failed, summary.Totals.Skipped}
	}
	return nil
}

// noPositional refuses an argument that is not a flag. The flag package stops
// at the first one and drops every flag after it, so "--with-images false
// --dry-run" would run for real with the filter on.
func noPositional(fs *flag.FlagSet, args []string) error {
	if fs.NArg() == 0 {
		return nil
	}
	arg := fs.Arg(0)
	hint := ""
	if i := slices.Index(args, arg); i > 0 && strings.HasPrefix(args[i-1], "-") && !strings.Contains(args[i-1], "=") {
		hint = fmt.Sprintf("; if %s is meant to have the value %s, write %s=%s", args[i-1], arg, args[i-1], arg)
	}
	return fmt.Errorf("unexpected argument %q: flags after it would be ignored%s (a boolean flag takes its value as --flag=false)", arg, hint)
}

func promptIDs() string {
	var ids []string
	for _, id := range append(prompt.ReviewIDs(false, true), prompt.IDReviewTilesCompose) {
		ids = append(ids, string(id))
	}
	return strings.Join(ids, ", ")
}

// defaultOut is replay-runs under the user cache directory, which is never
// inside a repository; a run's files hold private content.
func defaultOut() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "baum", "replay-runs")
	}
	return "replay-runs"
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
