package baumcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const (
	baumHookMarker   = "# Managed by baum. Re-run `baum setup` to repair."
	entireHookMarker = "# Entire CLI hooks"
)

// errCustomGitHooksPath reports that a repository configures core.hooksPath.
// Entire installs and runs its own hooks there, so this is a reason to leave
// them alone, not a setup failure.
var errCustomGitHooksPath = errors.New("refusing to manage hooks from core.hooksPath; remove the custom hooks path or use Entire's hooks directly")

func ownEntireGitHooks(ctx context.Context, root string) error {
	if err := refuseCustomGitHooksPath(ctx, root); err != nil {
		return err
	}
	dir, err := gitHooksDir(ctx, root)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read Git hooks: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".sample") || strings.HasSuffix(entry.Name(), ".pre-entire") {
			continue
		}
		if _, supported := entireGitHookArgumentCounts[entry.Name()]; !supported {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect Git hook %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read Git hook %s: %w", entry.Name(), err)
		}
		managed := strings.Contains(string(contents), baumHookMarker)
		if !managed && !isEntireHook(entry.Name(), contents) {
			continue
		}
		if err := os.WriteFile(path, []byte(gitHookDispatcher(entry.Name())), 0o755); err != nil {
			return fmt.Errorf("install Git hook dispatcher %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func refuseCustomGitHooksPath(ctx context.Context, root string) error {
	configured := exec.CommandContext(ctx, "git", "-C", root, "config", "--get", "core.hooksPath")
	output, err := configured.Output()
	if err == nil {
		if strings.TrimSpace(string(output)) != "" {
			return errCustomGitHooksPath
		}
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		return commandError(configured, err)
	}
	return nil
}

// installEntireGitHooks puts a dispatcher in place for every supported hook,
// whatever the clone currently has. Unlike ownEntireGitHooks, which only takes
// over hooks Entire already installed, this is the path a fresh clone needs: it
// creates missing hooks and preserves an unrelated hook as `<hook>.pre-entire`,
// which the dispatcher then chains, the way Entire's own installer does. It
// reports the hooks it left untouched because backing them up was impossible.
func installEntireGitHooks(ctx context.Context, root string) ([]string, error) {
	if err := refuseCustomGitHooksPath(ctx, root); err != nil {
		return nil, err
	}
	dir, err := gitHooksDir(ctx, root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create Git hooks directory: %w", err)
	}
	var unclaimed []string
	for _, name := range entireGitHookNames() {
		path := filepath.Join(dir, name)
		dispatcher := gitHookDispatcher(name)
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return nil, fmt.Errorf("inspect Git hook %s: %w", name, err)
		// A symlinked hook belongs to whatever put it there; the dispatcher
		// would be written through it into another file. Unless it already is
		// the dispatcher, it is reported so the clone is not marked done.
		case !info.Mode().IsRegular():
			// Only a regular target is read: a FIFO or device would block
			// forever and ignore the context.
			if !isDispatcherFile(path, dispatcher) {
				unclaimed = append(unclaimed, name)
			}
			continue
		default:
			contents, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read Git hook %s: %w", name, err)
			}
			if string(contents) == dispatcher {
				continue
			}
			if !strings.Contains(string(contents), baumHookMarker) && !isEntireHook(name, contents) {
				backedUp, err := backUpForeignGitHook(path)
				if err != nil {
					return nil, err
				}
				if !backedUp {
					unclaimed = append(unclaimed, name)
					continue
				}
			}
		}
		if err := os.WriteFile(path, []byte(dispatcher), 0o755); err != nil {
			return nil, fmt.Errorf("install Git hook dispatcher %s: %w", name, err)
		}
		if err := os.Chmod(path, 0o755); err != nil {
			return nil, fmt.Errorf("make Git hook %s executable: %w", name, err)
		}
	}
	return unclaimed, nil
}

// backUpForeignGitHook moves an unrelated hook aside so the dispatcher can
// chain it, and reports whether it did. A taken backup slot means this hook
// cannot be preserved: the dispatcher chains one `.pre-entire` file, so the
// caller leaves the hook alone rather than overwriting a file nothing would
// ever run again.
func backUpForeignGitHook(path string) (bool, error) {
	backup := path + ".pre-entire"
	_, err := os.Lstat(backup)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect Git hook backup %s: %w", filepath.Base(backup), err)
	}
	if err := os.Rename(path, backup); err != nil {
		return false, fmt.Errorf("back up Git hook %s: %w", filepath.Base(path), err)
	}
	return true, nil
}

func entireGitHookNames() []string {
	names := make([]string, 0, len(entireGitHookArgumentCounts))
	for name := range entireGitHookArgumentCounts {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// ownEntireHooksDuringSetup takes over every hook Entire installed for this
// repository, Git and agent alike. A custom core.hooksPath is a skip rather
// than a failure: Entire has already installed working hooks in that directory.
func ownEntireHooksDuringSetup(ctx context.Context, root string, stdout io.Writer) error {
	err := ownEntireGitHooks(ctx, root)
	if errors.Is(err, errCustomGitHooksPath) {
		_, _ = fmt.Fprintln(stdout, "core.hooksPath is set; leaving Entire's own Git hooks in place.")
	} else if err != nil {
		return err
	}
	changed, err := ownEntireAgentHooks(root)
	if err != nil {
		return err
	}
	if len(changed) > 0 {
		_, _ = fmt.Fprintf(stdout, "Rewrote agent hook commands in %s; commit the change.\n", strings.Join(changed, ", "))
	}
	return nil
}

func gitHooksDir(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "config", "--path", "--get", "core.hooksPath")
	output, err := cmd.Output()
	if err == nil {
		path := strings.TrimSpace(string(output))
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		return filepath.Clean(path), nil
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		return "", commandError(cmd, err)
	}
	cmd = exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	output, err = cmd.Output()
	if err != nil {
		return "", commandError(cmd, err)
	}
	return filepath.Clean(strings.TrimSpace(string(output))), nil
}

func isEntireHook(name string, contents []byte) bool {
	text := string(contents)
	return strings.Contains(text, entireHookMarker) && strings.Contains(text, "entire hooks git "+name)
}

// gitHookDispatcher renders the hook baum installs in place of Entire's.
// Hooks Git feeds on standard input buffer it so the chained `.pre-entire` hook
// still receives it after Entire has read it, and a failing pre-push keeps
// Entire's blocking exit status so the push stops.
func gitHookDispatcher(name string) string {
	bufferStdin := name == "post-rewrite" || name == "pre-push"
	onFailure := "|| true"
	if name == "pre-push" {
		onFailure = "|| exit $?"
	}
	redirect := ""
	buffer := ""
	if bufferStdin {
		// Without a temp file the chained hook would read an already-drained
		// stream; without this fallback a missing mktemp would fail the hook,
		// which for pre-push means refusing the push.
		redirect = ` < "$hook_stdin"`
		buffer = fmt.Sprintf(`hook_stdin=$(mktemp "${TMPDIR:-/tmp}/baum-%s.XXXXXX") || hook_stdin=
if [ -n "$hook_stdin" ]; then
  trap 'rm -f "$hook_stdin"' EXIT
  cat > "$hook_stdin"
else
  hook_stdin=/dev/stdin
fi
`, name)
	}
	return fmt.Sprintf(`#!/bin/sh
%s
%s
%sif command -v baum >/dev/null 2>&1 && baum hooks git --check %s >/dev/null 2>&1; then
  baum hooks git %s "$@"%s %s
elif command -v entire >/dev/null 2>&1; then
  %s%s %s
else
  printf '%%s\n' '[baum] baum and Entire are not installed or not on PATH. Skipping Git hook integrations; continuing.' >&2 || :
fi
hook_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
backup="$hook_dir/%s.pre-entire"
if [ -x "$backup" ]; then
  "$backup" "$@"%s
fi
`, baumHookMarker, entireHookMarker, buffer, name, name, redirect, onFailure,
		entireGitHookFallback(name), redirect, onFailure, name, redirect)
}

func entireGitHookFallback(name string) string {
	switch name {
	case "post-commit":
		return "entire hooks git post-commit"
	case "prepare-commit-msg":
		return `entire hooks git prepare-commit-msg "$1" "$2"`
	default:
		return fmt.Sprintf(`entire hooks git %s "$1"`, name)
	}
}

// isDispatcherFile reports whether path, following symlinks, is a regular file
// holding exactly the dispatcher. Anything else is never opened.
func isDispatcherFile(path, dispatcher string) bool {
	target, err := os.Stat(path)
	if err != nil || !target.Mode().IsRegular() {
		return false
	}
	contents, err := os.ReadFile(path)
	return err == nil && string(contents) == dispatcher
}
