package baumcli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/baum-ai/baum/hosted/agenthooks"
)

const (
	entireVersion           = agenthooks.EntireVersion
	maxEntireArchiveSize    = 256 << 20
	maxEntireExecutableSize = 256 << 20
)

type entireAgent struct {
	Slug        string
	DisplayName string
	Binaries    []string
	ConfigDirs  []string
}

var entireAgents = []entireAgent{
	{Slug: "claude-code", DisplayName: "Claude Code", Binaries: []string{"claude"}, ConfigDirs: []string{".claude"}},
	{Slug: "codex", DisplayName: "Codex", Binaries: []string{"codex"}, ConfigDirs: []string{".codex"}},
	{Slug: "cursor", DisplayName: "Cursor", Binaries: []string{"cursor"}, ConfigDirs: []string{".cursor"}},
	{Slug: "gemini", DisplayName: "Gemini CLI", Binaries: []string{"gemini"}, ConfigDirs: []string{".gemini"}},
	{Slug: "opencode", DisplayName: "OpenCode", Binaries: []string{"opencode"}, ConfigDirs: []string{".config/opencode"}},
	{Slug: "pi", DisplayName: "Pi", Binaries: []string{"pi"}, ConfigDirs: []string{".pi"}},
	{Slug: "copilot-cli", DisplayName: "Copilot CLI", Binaries: []string{"copilot"}, ConfigDirs: []string{".copilot"}},
	{Slug: "factoryai-droid", DisplayName: "Factory AI Droid", Binaries: []string{"droid"}, ConfigDirs: []string{".factory"}},
}

var (
	lookPath             = exec.LookPath
	entireReleaseBaseURL = "https://github.com/entireio/cli/releases/download/v" + entireVersion
	entireInstallDir     = defaultEntireInstallDir()
	entireHTTPClient     = &http.Client{Timeout: 2 * time.Minute}
	entireRuntimeGOOS    = runtime.GOOS
	entireRuntimeGOARCH  = runtime.GOARCH
	entireReleaseSHA256  = map[string]string{
		"entire_darwin_amd64.tar.gz": "a8b1f89e2eda7987d89bab3d99d8f5c1057cc4e86da80a4c5278c0160d0bdad3",
		"entire_darwin_arm64.tar.gz": "ddbe01c458c22494489dbd04227594b8e4360705c53c51d7ca246dcbc4f49c9e",
		"entire_linux_amd64.tar.gz":  "6bb93b257f5321ea311f0cd8e0ca74b4ea9f8f5bdf3b11e68ded57242f824d26",
		"entire_linux_arm64.tar.gz":  "5311e6fda6856897b74b4ed17f7da99400ecded72840bea8306f4c4193b561bd",
	}
)

type entireStatusReport struct {
	Enabled bool     `json:"enabled"`
	Agents  []string `json:"agents"`
}

type entireEnableOptions struct {
	Agents           []entireAgent
	CheckpointRemote string
}

func defaultEntireInstallDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".local", "bin")
	}
	return filepath.Join(home, ".local", "bin")
}

func setupEntire(ctx context.Context, root string, stdout, stderr io.Writer) (bool, error) {
	bin, err := ensureEntireInstalled(ctx, stdout, stderr)
	if err != nil {
		return false, err
	}
	agents := detectEntireAgents()
	if len(agents) == 0 {
		_, _ = fmt.Fprintln(stdout, "No supported coding agents detected; skipping Entire enable.")
		return false, nil
	}

	status, err := readEntireStatus(ctx, bin, root)
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return false, err
		}
	}
	if err == nil && status.Enabled {
		configured := make(map[string]bool, len(status.Agents))
		for _, name := range status.Agents {
			configured[name] = true
		}
		missing := make([]entireAgent, 0, len(agents))
		for _, agent := range agents {
			// An agent whose hooks already dispatch through baum is set
			// up even though Entire's status omits it; see agentHooksOwned.
			if configured[agent.DisplayName] || agentHooksOwned(root, agent.Slug) {
				continue
			}
			missing = append(missing, agent)
		}
		if len(missing) == 0 {
			if err := ownEntireHooksDuringSetup(ctx, root, stdout); err != nil {
				return false, err
			}
			_, _ = fmt.Fprintln(stdout, "Entire is already enabled for all detected coding agents.")
			return true, nil
		}
		if err := addEntireAgents(ctx, bin, root, missing, stdout, stderr); err != nil {
			return false, err
		}
		if err := ownEntireHooksDuringSetup(ctx, root, stdout); err != nil {
			return false, err
		}
		printEntireSummary(stdout, agents)
		return true, nil
	}

	if err := enableEntire(ctx, bin, root, entireEnableOptions{Agents: agents}, stdout, stderr); err != nil {
		return false, err
	}
	if err := ownEntireHooksDuringSetup(ctx, root, stdout); err != nil {
		return false, err
	}
	printEntireSummary(stdout, agents)
	return true, nil
}

func ensureEntireInstalled(ctx context.Context, stdout, stderr io.Writer) (string, error) {
	bin, err := resolveEntireBinary()
	if err != nil {
		_, _ = fmt.Fprintln(stdout, "Entire is not installed; installing it now.")
		bin, err = installEntire(ctx, stdout, stderr)
		if err != nil {
			return "", err
		}
	}
	cmd := exec.CommandContext(ctx, bin, "version")
	output, err := cmd.Output()
	if err != nil {
		return "", commandError(cmd, err)
	}
	_, _ = fmt.Fprintf(stdout, "Entire: %s\n", strings.TrimSpace(string(output)))
	return bin, nil
}

func installEntire(ctx context.Context, stdout, _ io.Writer) (string, error) {
	if entireRuntimeGOOS != "linux" && entireRuntimeGOOS != "darwin" {
		return "", fmt.Errorf("automatic Entire installation is unsupported on %s", entireRuntimeGOOS)
	}
	if entireRuntimeGOARCH != "amd64" && entireRuntimeGOARCH != "arm64" {
		return "", fmt.Errorf("automatic Entire installation is unsupported on %s/%s", entireRuntimeGOOS, entireRuntimeGOARCH)
	}
	asset := fmt.Sprintf("entire_%s_%s.tar.gz", entireRuntimeGOOS, entireRuntimeGOARCH)
	wantSHA256, ok := entireReleaseSHA256[asset]
	if !ok {
		return "", fmt.Errorf("no trusted checksum for Entire %s", asset)
	}
	url := strings.TrimRight(entireReleaseBaseURL, "/") + "/" + asset
	_, _ = fmt.Fprintf(stdout, "Downloading %s\n", url)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create Entire download request: %w", err)
	}
	resp, err := entireHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download Entire: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download Entire: %s", resp.Status)
	}
	archive, err := io.ReadAll(io.LimitReader(resp.Body, maxEntireArchiveSize+1))
	if err != nil {
		return "", fmt.Errorf("read Entire archive: %w", err)
	}
	if len(archive) > maxEntireArchiveSize {
		return "", fmt.Errorf("Entire archive exceeds %d bytes", maxEntireArchiveSize)
	}
	gotSHA256 := fmt.Sprintf("%x", sha256.Sum256(archive))
	if gotSHA256 != wantSHA256 {
		return "", fmt.Errorf("verify Entire archive %s: checksum mismatch (got %s)", asset, gotSHA256)
	}
	bin, err := extractEntire(bytes.NewReader(archive), entireInstallDir)
	if err != nil {
		return "", err
	}
	_, _ = fmt.Fprintf(stdout, "Installed Entire at %s. Add %s to PATH if needed.\n", bin, entireInstallDir)
	return bin, nil
}

func extractEntire(r io.Reader, installDir string) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("open Entire archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read Entire archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "entire" {
			continue
		}
		if header.Size < 0 || header.Size > maxEntireExecutableSize {
			return "", fmt.Errorf("Entire binary has invalid size %d", header.Size)
		}
		if err := os.MkdirAll(installDir, 0o755); err != nil {
			return "", fmt.Errorf("create Entire install directory: %w", err)
		}
		path := filepath.Join(installDir, "entire")
		file, err := os.CreateTemp(installDir, ".entire-*")
		if err != nil {
			return "", fmt.Errorf("create Entire binary: %w", err)
		}
		temporaryPath := file.Name()
		removeTemporary := true
		defer func() {
			if removeTemporary {
				_ = os.Remove(temporaryPath)
			}
		}()
		_, copyErr := io.CopyN(file, tr, header.Size)
		closeErr := file.Close()
		if copyErr != nil {
			return "", fmt.Errorf("extract Entire binary: %w", copyErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close Entire binary: %w", closeErr)
		}
		if err := os.Chmod(temporaryPath, 0o755); err != nil {
			return "", fmt.Errorf("make Entire binary executable: %w", err)
		}
		if err := os.Rename(temporaryPath, path); err != nil {
			return "", fmt.Errorf("install Entire binary: %w", err)
		}
		removeTemporary = false
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve Entire binary path: %w", err)
		}
		return absolute, nil
	}
	return "", errors.New("Entire archive does not contain an entire binary")
}

func detectEntireAgents() []entireAgent {
	home, _ := os.UserHomeDir()
	detected := make([]entireAgent, 0, len(entireAgents))
	for _, agent := range entireAgents {
		found := false
		for _, binary := range agent.Binaries {
			if _, err := lookPath(binary); err == nil {
				found = true
				break
			}
		}
		if !found && home != "" {
			for _, dir := range agent.ConfigDirs {
				if info, err := os.Stat(filepath.Join(home, filepath.FromSlash(dir))); err == nil && info.IsDir() {
					found = true
					break
				}
			}
		}
		if found {
			detected = append(detected, agent)
		}
	}
	return detected
}

func readEntireStatus(ctx context.Context, bin, root string) (entireStatusReport, error) {
	cmd := exec.CommandContext(ctx, bin, "status", "--json")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		return entireStatusReport{}, commandError(cmd, err)
	}
	var report entireStatusReport
	if err := json.Unmarshal(output, &report); err != nil {
		return entireStatusReport{}, fmt.Errorf("decode `entire status --json`: %w", err)
	}
	return report, nil
}

func enableEntire(ctx context.Context, bin, root string, opts entireEnableOptions, stdout, stderr io.Writer) error {
	if len(opts.Agents) == 0 {
		return errors.New("cannot enable Entire without a detected coding agent")
	}
	args := []string{"enable", "--agent", opts.Agents[0].Slug, "--checkpoint-backend", "branch", "--no-init-repo", "--yes"}
	if opts.CheckpointRemote != "" {
		args = append(args, "--checkpoint-remote", opts.CheckpointRemote)
	}
	if err := runEntireCommand(ctx, bin, root, args, stdout, stderr); err != nil {
		return err
	}
	return addEntireAgents(ctx, bin, root, opts.Agents[1:], stdout, stderr)
}

func addEntireAgents(ctx context.Context, bin, root string, agents []entireAgent, stdout, stderr io.Writer) error {
	for _, agent := range agents {
		if err := runEntireCommand(ctx, bin, root, []string{"agent", "add", agent.Slug}, stdout, stderr); err != nil {
			return err
		}
	}
	return nil
}

func runEntireCommand(ctx context.Context, bin, root string, args []string, stdout, stderr io.Writer) error {
	_, _ = fmt.Fprintf(stdout, "Running: entire %s\n", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, bin, args...)
	var commandStdout, commandStderr bytes.Buffer
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, &commandStdout, &commandStderr
	if err := cmd.Run(); err != nil {
		_, _ = io.Copy(stdout, &commandStdout)
		_, _ = io.Copy(stderr, &commandStderr)
		return commandError(cmd, err)
	}
	_, _ = io.Copy(stdout, &commandStdout)
	_, _ = io.WriteString(stderr, filterEntireSuccessStderr(commandStderr.String()))
	return nil
}

func filterEntireSuccessStderr(output string) string {
	var filtered strings.Builder
	for line := range strings.SplitSeq(output, "\n") {
		if isExpectedEntireHookReplacementWarning(line) {
			continue
		}
		if filtered.Len() > 0 {
			filtered.WriteByte('\n')
		}
		filtered.WriteString(line)
	}
	return filtered.String()
}

func isExpectedEntireHookReplacementWarning(line string) bool {
	const prefix = "[entire] Warning: replacing "
	remainder, ok := strings.CutPrefix(line, prefix)
	if !ok {
		return false
	}
	hook, backup, ok := strings.Cut(remainder, " (backup ")
	return ok && hook != "" && backup == hook+".pre-entire already exists from a previous install)"
}

func commandError(cmd *exec.Cmd, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) != 0 {
		return fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	return fmt.Errorf("%s: %w", strings.Join(cmd.Args, " "), err)
}

func printEntireSummary(w io.Writer, agents []entireAgent) {
	names := make([]string, 0, len(agents))
	for _, agent := range agents {
		names = append(names, agent.DisplayName)
	}
	_, _ = fmt.Fprintf(w, "Entire enabled for %s. Checkpoints are recorded on entire/checkpoints/v1 and uploaded on git push.\n", strings.Join(names, ", "))
}
