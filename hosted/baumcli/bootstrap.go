package baumcli

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/sessions/collector"
)

// sessionStartBootstrapTimeout bounds the bootstrap an agent session start
// triggers. A first-run Entire download must not consume the agent's own hook
// timeout, which is 30 seconds for Codex; an interrupted bootstrap simply runs
// again on the next session.
const sessionStartBootstrapTimeout = 15 * time.Second

// bootstrapMarkerName is the file in the clone's .git/baum directory recording
// which dispatchers and Entire release the last bootstrap installed.
const bootstrapMarkerName = "bootstrap-ok"

func newBootstrapHooksCommand(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Install the Entire binary and Git hooks this clone needs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			bootstrapHooks(cmd.Context(), ".", stdout)
			return nil
		},
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd
}

// bootstrapHooks installs what a fresh clone needs before its first commit: the
// pinned Entire binary and this clone's Git hook dispatchers. It never fails the
// caller, because agent hooks run it on every session start and must not
// disturb the session.
func bootstrapHooks(ctx context.Context, dir string, log io.Writer) {
	if err := runHooksBootstrap(ctx, dir, log); err != nil {
		_, _ = fmt.Fprintf(log, "[baum] hook bootstrap skipped: %v\n", err)
	}
}

func runHooksBootstrap(ctx context.Context, dir string, log io.Writer) error {
	root, err := collector.RepoRoot(dir)
	if err != nil {
		return err
	}
	marker, err := hooksBootstrapMarker(ctx, root)
	if err != nil {
		return err
	}
	fingerprint := hooksBootstrapFingerprint()
	if hooksBootstrapCurrent(marker, fingerprint) {
		return nil
	}
	if err := ensureEntireForBootstrap(ctx, log); err != nil {
		return err
	}
	unclaimed, err := installEntireGitHooks(ctx, root)
	if err != nil {
		if errors.Is(err, errCustomGitHooksPath) {
			_, _ = fmt.Fprintln(log, "[baum] core.hooksPath is set; leaving Entire's own Git hooks in place.")
			return nil
		}
		return err
	}
	if len(unclaimed) > 0 {
		// No marker: this clone is not bootstrapped, and the next session
		// start should say so again until someone clears the hook.
		_, _ = fmt.Fprintf(log, "[baum] Git %s replaced by another tool and a .pre-entire backup already exists; leaving it in place. Move it aside to let baum install its hook.\n", gitHookNoun(unclaimed))
		return nil
	}
	return writeHooksBootstrapMarker(marker, fingerprint)
}

// ensureEntireForBootstrap installs the pinned Entire release when this machine
// has none. Unlike setup, it says nothing about an Entire that is already there:
// this runs inside an agent session, where every line is noise.
func ensureEntireForBootstrap(ctx context.Context, log io.Writer) error {
	if _, err := resolveEntireBinary(); err == nil {
		return nil
	}
	_, _ = fmt.Fprintln(log, "[baum] Entire is not installed; installing it now.")
	_, err := installEntire(ctx, log, log)
	return err
}

// hooksBootstrapCurrent reports whether this clone was already bootstrapped by
// a binary that installs the same dispatchers and pins the same Entire release.
// It is the fast path taken on every healthy session start.
func hooksBootstrapCurrent(marker, fingerprint string) bool {
	contents, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(contents)) != fingerprint {
		return false
	}
	_, err = resolveEntireBinary()
	return err == nil
}

func gitHookNoun(names []string) string {
	if len(names) == 1 {
		return "hook " + names[0] + " was"
	}
	return "hooks " + strings.Join(names, ", ") + " were"
}

func writeHooksBootstrapMarker(marker, fingerprint string) error {
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return fmt.Errorf("create hook bootstrap state directory: %w", err)
	}
	if err := os.WriteFile(marker, []byte(fingerprint+"\n"), 0o644); err != nil {
		return fmt.Errorf("record hook bootstrap: %w", err)
	}
	return nil
}

func hooksBootstrapMarker(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--path-format=absolute", "--git-dir")
	output, err := cmd.Output()
	if err != nil {
		return "", commandError(cmd, err)
	}
	return filepath.Join(filepath.Clean(strings.TrimSpace(string(output))), "baum", bootstrapMarkerName), nil
}

func hooksBootstrapFingerprint() string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, entireVersion)
	for _, name := range entireGitHookNames() {
		_, _ = io.WriteString(hash, gitHookDispatcher(name))
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

// resolveEntireBinary finds Entire on PATH, then where this CLI installs it.
// The bootstrap that installs Entire cannot change its own caller's PATH, so
// the session that triggered the install still has to reach the new binary.
func resolveEntireBinary() (string, error) {
	if bin, err := lookPath("entire"); err == nil {
		return bin, nil
	}
	installed := filepath.Join(entireInstallDir, "entire")
	if info, err := os.Stat(installed); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
		return installed, nil
	}
	return "", exec.ErrNotFound
}
