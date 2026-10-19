package baumcli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/hosted/agenthooks"
)

var entireGitHookArgumentCounts = map[string]int{
	"commit-msg":         1,
	"post-commit":        0,
	"post-rewrite":       1,
	"pre-push":           1,
	"prepare-commit-msg": 2,
}

func newHooksCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "hooks",
		Short:  "Run baum integration hooks",
		Hidden: true,
	}
	gitCommand := &cobra.Command{
		Use:                "git HOOK [args...]",
		Short:              "Run a baum Git hook",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] == "--check" {
				if len(args) != 2 {
					return errors.New("usage: baum hooks git --check HOOK")
				}
				if _, ok := entireGitHookArgumentCounts[args[1]]; !ok {
					return fmt.Errorf("unsupported Git hook %q", args[1])
				}
				return hostedHooksEnabled(cmd.Context(), ".")
			}
			entireArgs, ok := entireGitHookArgs(args)
			if !ok {
				return nil
			}
			entire, err := resolveEntireBinary()
			if err != nil {
				_, _ = fmt.Fprintln(stderr, "[baum] Entire CLI is not installed or not on PATH. Skipping Entire Git hook; continuing.")
				return nil
			}
			entireCommand := exec.CommandContext(cmd.Context(), entire, entireArgs...)
			entireCommand.Stdin = cmd.InOrStdin()
			entireCommand.Stdout = stdout
			entireCommand.Stderr = stderr
			if err := entireCommand.Run(); err != nil {
				if args[0] == "pre-push" {
					return fmt.Errorf("Entire pre-push hook failed: %w", err)
				}
				_, _ = fmt.Fprintf(stderr, "[baum] Entire Git hook failed; continuing: %s: %v\n", strings.Join(entireCommand.Args, " "), err)
			}
			return nil
		},
	}
	gitCommand.SetOut(stdout)
	gitCommand.SetErr(stderr)
	cmd.AddCommand(gitCommand)
	cmd.AddCommand(newAgentHooksCommand(stdout, stderr))
	cmd.AddCommand(newBootstrapHooksCommand(stdout, stderr))
	return cmd
}

// newAgentHooksCommand runs a coding agent's hook. Unlike the Git hooks, the
// agent hook protocol belongs to the agent: standard input carries its payload,
// standard output carries the reply it parses, and the exit code decides what
// the agent does next, so all four are passed through untouched.
func newAgentHooksCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:                "agent AGENT EVENT",
		Short:              "Run a baum coding-agent hook",
		Args:               cobra.MinimumNArgs(2),
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] == "--check" {
				if len(args) != 3 {
					return errors.New("usage: baum hooks agent --check AGENT EVENT")
				}
				if err := validateAgentHook(args[1], args[2]); err != nil {
					return err
				}
				return hostedHooksEnabled(cmd.Context(), ".")
			}
			if len(args) != 2 {
				return errors.New("usage: baum hooks agent AGENT EVENT")
			}
			agent, event := args[0], args[1]
			if err := validateAgentHook(agent, event); err != nil {
				return err
			}
			if event == entireAgentSessionStart {
				// Session start precedes any commit in the session, so this is
				// where a fresh clone gets the hooks its first commit needs.
				bootstrapContext, cancel := context.WithTimeout(cmd.Context(), sessionStartBootstrapTimeout)
				defer cancel()
				bootstrapHooks(bootstrapContext, ".", stderr)
			}
			entire, err := resolveEntireBinary()
			if err != nil {
				spec, _ := lookupAgentHookSpec(agent)
				if notice := missingEntireNotice(spec, event); notice != "" {
					_, _ = fmt.Fprintln(stdout, notice)
				}
				return nil
			}
			entireCommand := exec.CommandContext(cmd.Context(), entire, "hooks", agent, event)
			entireCommand.Stdin = cmd.InOrStdin()
			entireCommand.Stdout = stdout
			entireCommand.Stderr = stderr
			err = entireCommand.Run()
			if err == nil {
				return nil
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				_, _ = fmt.Fprintf(stderr, "[baum] Entire agent hook could not run; continuing: %s: %v\n", strings.Join(entireCommand.Args, " "), err)
				return nil
			}
			code := exitErr.ExitCode()
			if code < 0 {
				code = 1
			}
			return &ExitError{Code: code}
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd
}

// errHostedHooksOff is the `--check` answer where the user has not opted in
// to the hosted service. The dispatchers committed to repositories and
// installed as Git hooks run baum only when `--check` succeeds, and otherwise
// run Entire directly, so an open-source user's hooks behave as if baum were
// not installed.
var errHostedHooksOff = errors.New("hosted hooks are off here: the repository's committed agent hooks do not dispatch through baum; run `baum login` or `baum setup` to opt in")

// hostedHooksEnabled reports whether the hooks of the clone containing dir
// should run through baum. They do when the user is logged in (BAUM_TOKEN or
// stored credentials, for any server); when this clone was set up by
// `baum setup` or bootstrapped, which leaves a marker in its Git directory;
// or when the repository is onboarded: an agent settings file committed at
// HEAD carries baum's dispatcher, as the onboarding pull request and
// `baum setup` write it. The last two only let the hooks install Entire and
// the Git hook dispatchers and delegate to Entire; nothing a hook does
// contacts a baum server, and every command that does still needs a login.
// The repository is judged by its committed files, not by a hook having run,
// so a hook configured outside the repository falls through to Entire in a
// repository that was never onboarded. It reads only local state.
func hostedHooksEnabled(ctx context.Context, dir string) error {
	if os.Getenv(tokenEnvVar) != "" {
		return nil
	}
	if creds, ok, err := loadCredentials(); err == nil && ok && creds.Token != "" {
		return nil
	}
	if hostedClone(ctx, dir) || onboardedRepository(ctx, dir) {
		return nil
	}
	return errHostedHooksOff
}

// hostedClone reports whether the clone containing dir carries the marker of
// a `baum setup` or of a hook bootstrap, in the worktree's own Git directory
// or in the common one, so every linked worktree of a set-up clone counts.
func hostedClone(ctx context.Context, dir string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, gitDir := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		for _, name := range []string{setupMarkerName, bootstrapMarkerName} {
			info, err := os.Stat(filepath.Join(gitDir, "baum", name))
			if err == nil && info.Mode().IsRegular() {
				return true
			}
		}
	}
	return false
}

// onboardedRepository reports whether an agent settings file committed at
// HEAD in the repository containing dir carries baum's dispatcher. One git
// process reads every settings file, missing ones included.
func onboardedRepository(ctx context.Context, dir string) bool {
	var request strings.Builder
	for _, spec := range agenthooks.All() {
		request.WriteString("HEAD:" + spec.SettingsFile + "\n")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(request.String())
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	reader := bufio.NewReader(bytes.NewReader(output))
	for {
		header, err := reader.ReadString('\n')
		if err != nil {
			return false
		}
		// <oid> <type> <size>, or <object> missing.
		fields := strings.Fields(header)
		if len(fields) != 3 {
			continue
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 {
			return false
		}
		contents := make([]byte, size+1)
		if _, err := io.ReadFull(reader, contents); err != nil {
			return false
		}
		if fields[1] == "blob" && agenthooks.ContainsDispatcher(contents[:size]) {
			return true
		}
	}
}

func entireGitHookArgs(args []string) ([]string, bool) {
	argumentCount, ok := entireGitHookArgumentCounts[args[0]]
	if !ok {
		return nil, false
	}
	result := []string{"hooks", "git", args[0]}
	for i := 0; i < argumentCount; i++ {
		value := ""
		if i+1 < len(args) {
			value = args[i+1]
		}
		result = append(result, value)
	}
	return result, true
}
