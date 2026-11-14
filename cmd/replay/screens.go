package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/pricing"
	"github.com/baum-ai/baum/internal/replay/screens"
)

// parseWithOne parses flags around exactly one positional argument, which
// may come before or after them.
func parseWithOne(fs *flag.FlagSet, args []string, usage string) (string, error) {
	var positional string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		if positional != "" {
			return "", fmt.Errorf("unexpected argument %q (write a flag's value as --flag=value); usage: %s", fs.Arg(0), usage)
		}
		positional, rest = fs.Arg(0), fs.Args()[1:]
		if len(rest) == 0 {
			break
		}
	}
	if positional == "" {
		return "", errors.New("usage: " + usage)
	}
	return positional, nil
}

// pairing loads a run's pictures and, when against names another run, that
// run's.
func pairing(dir, against string) (screens.Pairing, error) {
	run, err := screens.Load(dir)
	if err != nil {
		return screens.Pairing{}, err
	}
	var other *screens.Run
	if against != "" {
		if other, err = screens.Load(against); err != nil {
			return screens.Pairing{}, err
		}
	}
	return screens.Pairs(run, other), nil
}

// runSheet implements `replay sheet <run-dir> [--against <run-dir>] [--judge <report>]`.
func runSheet(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("replay sheet", flag.ContinueOnError)
	fs.SetOutput(stderr)
	against := fs.String("against", "", "another run of the same cases: compare the two compositions instead of the run's composition with its template")
	judge := fs.String("judge", "", "a judge report of the same pairing whose verdicts the sheet shows")
	out := fs.String("out", "", "the sheet's path; default is the run's screens directory")
	dir, err := parseWithOne(fs, args, "replay sheet <run-dir> [--against <run-dir>] [--judge <report>]")
	if err != nil {
		return err
	}
	p, err := pairing(dir, *against)
	if err != nil {
		return err
	}
	var verdicts map[string]screens.PairVerdict
	if *judge != "" {
		report, err := screens.ReadReport(*judge)
		if err != nil {
			return err
		}
		if report.XRun != p.X.Run.Name() || report.YRun != p.Y.Run.Name() || report.X != p.X.Label() || report.Y != p.Y.Label() {
			return fmt.Errorf("%s judged %s against %s, not this pairing", *judge, report.X, report.Y)
		}
		verdicts = report.Pairs
	}
	path := *out
	if path == "" {
		path = filepath.Join(dir, screens.Dir, screens.SheetName(p))
	}
	if err := screens.WriteSheet(p, path, verdicts); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "wrote %s (%d cases)\n", path, len(p.Cases()))
	return nil
}

// runJudge implements `replay judge <run-dir> [--against <run-dir>] [flags]`.
func runJudge(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("replay judge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	against := fs.String("against", "", "another run of the same cases: judge the two compositions instead of the run's composition against its template")
	backend := fs.String("backend", "claude", "the judge: claude or codex")
	model := fs.String("model", "", "the judge's model; empty is the agent's own")
	effort := fs.String("effort", "", "the judge's reasoning effort; empty is the agent's own")
	name := fs.String("name", "", "the report's name; default names the judge and the pairing")
	concurrency := fs.Int("concurrency", 4, "calls at once")
	limit := fs.Int("limit", 0, "judge at most this many cases (0 is all)")
	only := fs.String("only", "", "only the cases whose id contains this text")
	repeat := fs.Float64("repeat", 0.1, "ask again this share of the calls, in the same order, to measure the judge's noise")
	seed := fs.Uint64("seed", 1, "seed choosing the repeated calls")
	phone := fs.Bool("phone", true, "show the judge the phone pictures too")
	timeout := fs.Duration("timeout", 5*time.Minute, "time limit of one call")
	maxCost := fs.Float64("max-cost-usd", 0, "refuse to start when the estimate is above this (0 is no guard); the estimate is the number of calls times --cost-per-call")
	perCall := fs.Float64("cost-per-call", 0.08, "the cost of one call the estimate assumes")
	claudeDir := fs.String("claude-config-dir", "", "CLAUDE_CONFIG_DIR for Claude Code; default is the ambient login")
	codexHome := fs.String("codex-home", "", "CODEX_HOME for Codex; default is the ambient login")
	dir, err := parseWithOne(fs, args, "replay judge <run-dir> [--against <run-dir>] [--backend claude|codex] [--model m]")
	if err != nil {
		return err
	}
	p, err := pairing(dir, *against)
	if err != nil {
		return err
	}
	cases := 0
	for _, id := range p.Cases() {
		if strings.Contains(id, *only) {
			cases++
		}
	}
	if *limit > 0 {
		cases = min(cases, *limit)
	}
	calls := int(math.Ceil(float64(cases) * 2 * (1 + *repeat)))
	if *maxCost > 0 && float64(calls)**perCall > *maxCost {
		return fmt.Errorf("about %d calls at $%.2f each is above --max-cost-usd %.2f", calls, *perCall, *maxCost)
	}
	cfg := config.Default()
	cfg.Backend, cfg.Model, cfg.Effort, cfg.ClaudeConfigDir, cfg.CodexHome = config.Backend(*backend), *model, *effort, *claudeDir, *codexHome
	if !config.ValidEffort(*effort) {
		return fmt.Errorf("unknown reasoning effort %q", *effort)
	}
	selected, err := generate.SelectDesktop(ctx, cfg.Backend, cfg.Model, generate.DefaultBackendsFor(cfg), io.Discard)
	if err != nil {
		return err
	}
	completer := generate.CompleterOf(selected.Backend)
	if completer == nil {
		return fmt.Errorf("the %s backend cannot make a single structured call", selected.Backend.Name())
	}
	prices := pricing.Active()
	backendName := selected.Backend.Name()
	reportName := *name
	if reportName == "" {
		reportName = screens.JudgeName(p, backendName, *model)
	}
	_, _ = fmt.Fprintf(stdout, "judging %d cases of %s against %s with %s %s, both orders (about %d calls)\n", cases, p.X.Label(), p.Y.Label(), backendName, *model, calls)
	report, err := screens.Judge(ctx, p, screens.JudgeOptions{Completer: completer, Backend: backendName, Model: *model, Concurrency: *concurrency, Limit: *limit, Only: *only,
		Repeat: *repeat, Seed: *seed, Phone: *phone, Progress: stdout, Timeout: *timeout,
		Price: func(m string, usage agent.Usage) *float64 {
			return prices.BackendOutcomeCost(backendName, m, agent.Outcome{Model: m, Usage: usage, Calls: 1})
		}})
	report.Name = reportName
	path := filepath.Join(dir, screens.Dir, reportName+".json")
	if werr := screens.WriteReport(path, report); werr != nil {
		return errors.Join(err, werr)
	}
	if err != nil {
		return err
	}
	printReport(stdout, report)
	_, _ = fmt.Fprintf(stdout, "wrote %s\n", path)
	return nil
}

func printReport(w io.Writer, r screens.Report) {
	s := r.Summary
	_, _ = fmt.Fprintf(w, "\nX = %s, Y = %s, judged by %s %s (%s)\n", r.X, r.Y, r.Backend, r.Model, r.RubricVersion)
	_, _ = fmt.Fprintf(w, "pairs %d: X wins %d, ties %d, Y wins %d\n", s.Pairs, s.WinsX, s.Ties, s.WinsY)
	_, _ = fmt.Fprintf(w, "X's share of decisive pairs %.2f (95%% Wilson %.2f–%.2f), sign test p = %.3g\n", s.WinShareX, s.WilsonLow, s.WilsonHi, s.SignP)
	_, _ = fmt.Fprintf(w, "the two orders agreed on %d of %d pairs; repeated calls agreed %d of %d\n", s.OrdersAgree, s.Pairs, r.RepeatsAgree, r.Repeats)
	for _, size := range []string{"xs", "s", "m", "l", "xl", ""} {
		if row, ok := s.BySize[size]; ok {
			label := size
			if label == "" {
				label = "(no size)"
			}
			_, _ = fmt.Fprintf(w, "  size %s: %d / %d / %d\n", label, row[0], row[1], row[2])
		}
	}
	if r.IncompletePair > 0 {
		_, _ = fmt.Fprintf(w, "%d pairs are missing an order and are left out\n", r.IncompletePair)
	}
	_, _ = fmt.Fprintf(w, "cost $%.2f (%d calls unpriced)\n", r.CostUSD, r.UnpricedCalls)
}

// runLabels implements `replay labels make|agree`.
func runLabels(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "make" && args[0] != "agree") {
		return errors.New("usage: replay labels make <run-dir> --out <dir> [--against <run-dir>] [--n 15] [--seed 1], or replay labels agree <labels-dir> <judge-report>...")
	}
	if args[0] == "agree" {
		return runLabelsAgree(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("replay labels make", flag.ContinueOnError)
	fs.SetOutput(stderr)
	against := fs.String("against", "", "another run of the same cases: label the two compositions instead of composition against template")
	out := fs.String("out", "", "the directory that receives index.html, key.json and labels.txt (outside any repository)")
	n := fs.Int("n", 15, "pairs")
	seed := fs.Uint64("seed", 1, "seed choosing the pairs and their order")
	dir, err := parseWithOne(fs, args[1:], "replay labels make <run-dir> --out <dir>")
	if err != nil {
		return err
	}
	if *out == "" {
		return errors.New("replay labels make needs --out <dir>")
	}
	p, err := pairing(dir, *against)
	if err != nil {
		return err
	}
	key, err := screens.LabelSet(p, *n, *seed, *out)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "wrote %d pairs: %s, %s and %s\n", len(key.Pairs), filepath.Join(*out, "index.html"), filepath.Join(*out, "key.json"), filepath.Join(*out, "labels.txt"))
	return nil
}

func runLabelsAgree(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("replay labels agree", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return errors.New("usage: replay labels agree <labels-dir> <judge-report>...")
	}
	dir := fs.Arg(0)
	key, err := screens.ReadKey(filepath.Join(dir, "key.json"))
	if err != nil {
		return err
	}
	labels, err := screens.ReadLabels(filepath.Join(dir, "labels.txt"))
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "%d of %d pairs labelled\n", len(labels), len(key.Pairs))
	for _, path := range fs.Args()[1:] {
		report, err := screens.ReadReport(path)
		if err != nil {
			return err
		}
		if report.XRun != key.XRun || report.YRun != key.YRun || report.X != key.X || report.Y != key.Y {
			return fmt.Errorf("%s judged %s against %s; the labels compare %s against %s", path, report.X, report.Y, key.X, key.Y)
		}
		a := screens.AgreeLabels(key, labels, report)
		var differ []string
		for _, pair := range a.Pairs {
			if pair.Owner != pair.Judge {
				differ = append(differ, fmt.Sprintf("%s (owner %s, judge %s)", pair.Pair, pair.Owner, pair.Judge))
			}
		}
		_, _ = fmt.Fprintf(stdout, "%s (%s %s): agrees on %d of %d labelled pairs; on %d of %d both call decisive\n",
			filepath.Base(path), report.Backend, report.Model, a.Same, a.Labelled, a.DecisiveSame, a.Decisive)
		if len(differ) > 0 {
			_, _ = fmt.Fprintf(stdout, "  differs: %s\n", strings.Join(differ, ", "))
		}
	}
	return nil
}

// runJudgeAgree implements `replay judge agree <report> <report>`: how often
// two judges of the same pairing, such as two model families, agree.
func runJudgeAgree(args []string, stdout io.Writer) error {
	if len(args) != 2 {
		return errors.New("usage: replay judge agree <judge-report> <judge-report>")
	}
	a, err := screens.ReadReport(args[0])
	if err != nil {
		return err
	}
	b, err := screens.ReadReport(args[1])
	if err != nil {
		return err
	}
	if a.XRun != b.XRun || a.YRun != b.YRun || a.X != b.X || a.Y != b.Y {
		return errors.New("the two reports judged different pairings")
	}
	agreement := screens.Agree(a, b)
	_, _ = fmt.Fprintf(stdout, "%s %s and %s %s: same verdict on %d of %d pairs; of the %d both call decisive, %d agree\n",
		a.Backend, a.Model, b.Backend, b.Model, agreement.Same, agreement.Cases, agreement.BothDecisive, agreement.DecisiveSame)
	return nil
}
