package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/workstats"
)

func newDaemonProfileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Profile the running daemon: CPU, memory and the work it did",
		Long: `Profile watches the running daemon without restarting it. By default it
records a CPU profile for --duration, takes heap and goroutine profiles at the
end, and saves them with the daemon's work counters (transcripts decoded,
cache hits, git processes, refresh passes) from the start and the end into
--out. It then prints the CPU time the daemon used, as a share of one core,
and what the counters say it did. Read the CPU profile with
"go tool pprof -top <dir>/cpu.pprof" or "go tool pprof -http=: <dir>/cpu.pprof".

With --watch it records no profile. It prints one line every --interval with
the CPU share and memory over that interval and the counters that moved,
until --duration has passed (forever when 0) or it is interrupted.`,
		Args: cobra.NoArgs,
		RunE: runDaemonProfile,
	}
	cmd.Flags().Duration("duration", time.Minute, "how long to profile; with --watch, 0 watches until interrupted")
	cmd.Flags().String("out", "", "directory for the profiles (default: baum-profile-<time> in the current directory)")
	cmd.Flags().Bool("watch", false, "print counters every --interval instead of recording profiles")
	cmd.Flags().Duration("interval", 10*time.Second, "how often --watch prints")
	cmd.Flags().Bool("trace", false, "also record an execution trace for --duration (large)")
	return cmd
}

// profileClient reads the running daemon's debug endpoints with its launch
// credential.
type profileClient struct {
	origin, token string
	http          *http.Client
}

func newProfileClient() (profileClient, error) {
	paths, err := daemonPaths()
	if err != nil {
		return profileClient{}, err
	}
	credential, err := daemon.ReadCredential(paths)
	if errors.Is(err, daemon.ErrNoCredential) {
		return profileClient{}, errors.New("no baum daemon is running; start it with \"baum daemon ensure\"")
	}
	if err != nil {
		return profileClient{}, err
	}
	return profileClient{origin: credential.Origin(), token: credential.Token(), http: &http.Client{}}, nil
}

func (c profileClient) get(ctx context.Context, path string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.origin+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("reaching the daemon: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if resp.StatusCode == http.StatusNotFound {
			return errors.New("the running daemon does not serve profiles; update baum and restart it with \"baum daemon stop\" and \"baum daemon ensure\"")
		}
		return fmt.Errorf("%s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (c profileClient) stats(ctx context.Context) (workstats.Snapshot, []byte, error) {
	var body strings.Builder
	if err := c.get(ctx, "/api/debug/stats", &body); err != nil {
		return workstats.Snapshot{}, nil, err
	}
	var s workstats.Snapshot
	if err := json.Unmarshal([]byte(body.String()), &s); err != nil {
		return workstats.Snapshot{}, nil, fmt.Errorf("reading the daemon's counters: %w", err)
	}
	return s, []byte(body.String()), nil
}

func runDaemonProfile(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()
	client, err := newProfileClient()
	if err != nil {
		return err
	}
	duration, _ := cmd.Flags().GetDuration("duration")
	if watch, _ := cmd.Flags().GetBool("watch"); watch {
		interval, _ := cmd.Flags().GetDuration("interval")
		return watchDaemon(ctx, cmd.OutOrStdout(), client, interval, duration)
	}
	if duration < time.Second {
		return errors.New("--duration must be at least 1s")
	}
	out, _ := cmd.Flags().GetString("out")
	if out == "" {
		out = "baum-profile-" + time.Now().Format("20060102-150405")
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	start, startJSON, err := client.stats(ctx)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "stats-start.json"), startJSON, 0o600); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "profiling baum daemon for %s into %s ...\n", duration, out)

	seconds := int(duration.Round(time.Second) / time.Second)
	record := func(name, path string) chan error {
		done := make(chan error, 1)
		go func() {
			f, err := os.OpenFile(filepath.Join(out, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				done <- err
				return
			}
			err = client.get(ctx, path, f)
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
			done <- err
		}()
		return done
	}
	cpu := record("cpu.pprof", fmt.Sprintf("/api/debug/pprof/profile?seconds=%d", seconds))
	var trace chan error
	if withTrace, _ := cmd.Flags().GetBool("trace"); withTrace {
		trace = record("trace.out", fmt.Sprintf("/api/debug/pprof/trace?seconds=%d", seconds))
	}
	if err := <-cpu; err != nil {
		return fmt.Errorf("recording the CPU profile: %w", err)
	}
	if trace != nil {
		if err := <-trace; err != nil {
			return fmt.Errorf("recording the trace: %w", err)
		}
	}
	end, endJSON, err := client.stats(ctx)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "stats-end.json"), endJSON, 0o600); err != nil {
		return err
	}
	for name, path := range map[string]string{
		"heap.pprof":    "/api/debug/pprof/heap",
		"goroutine.txt": "/api/debug/pprof/goroutine?debug=1",
	} {
		if err := <-record(name, path); err != nil {
			return fmt.Errorf("recording %s: %w", name, err)
		}
	}
	summary := profileSummary(start, end)
	if err := os.WriteFile(filepath.Join(out, "summary.txt"), []byte(summary), 0o600); err != nil {
		return err
	}
	_, _ = io.WriteString(w, summary)
	_, err = fmt.Fprintf(w, "\nprofiles: %s\nread the CPU profile with: go tool pprof -top %s\n", out, filepath.Join(out, "cpu.pprof"))
	return err
}

// profileSummary describes what the daemon did between two snapshots.
func profileSummary(start, end workstats.Snapshot) string {
	var b strings.Builder
	elapsed := end.At.Sub(start.At).Seconds()
	cpu := end.Process.CPUSeconds() - start.Process.CPUSeconds()
	fmt.Fprintf(&b, "window:      %.0fs (daemon up %s)\n", elapsed, time.Duration(end.Uptime*float64(time.Second)).Round(time.Second))
	if elapsed > 0 {
		fmt.Fprintf(&b, "cpu:         %.2fs (%.1f%% of one core; user %.2fs, system %.2fs)\n", cpu, 100*cpu/elapsed,
			end.Process.UserSeconds-start.Process.UserSeconds, end.Process.SystemSeconds-start.Process.SystemSeconds)
	}
	fmt.Fprintf(&b, "memory:      heap %s, %d goroutines, %d GC cycles in window\n", megabytes(end.Process.HeapBytes), end.Process.Goroutines, end.Process.GCCycles-start.Process.GCCycles)
	moved := end.Since(start)
	if len(moved) == 0 {
		b.WriteString("work:        none counted\n")
	} else {
		b.WriteString("work:\n")
		for _, d := range moved {
			fmt.Fprintf(&b, "  %-40s %d\n", d.Name, d.Count)
		}
	}
	if len(end.Gauges) > 0 {
		b.WriteString("gauges:\n")
		names := make([]string, 0, len(end.Gauges))
		for name := range end.Gauges {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(&b, "  %-40s %d\n", name, end.Gauges[name])
		}
	}
	return b.String()
}

func megabytes(n uint64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }

// watchDaemon prints a line per interval until duration passes (never when
// 0) or ctx ends.
func watchDaemon(ctx context.Context, w io.Writer, client profileClient, interval, duration time.Duration) error {
	if interval < time.Second {
		return errors.New("--interval must be at least 1s")
	}
	if duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, duration)
		defer cancel()
	}
	previous, _, err := client.stats(ctx)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		current, _, err := client.stats(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		_, _ = io.WriteString(w, watchLine(previous, current))
		previous = current
	}
}

func watchLine(previous, current workstats.Snapshot) string {
	elapsed := current.At.Sub(previous.At).Seconds()
	cpu := current.Process.CPUSeconds() - previous.Process.CPUSeconds()
	share := 0.0
	if elapsed > 0 {
		share = 100 * cpu / elapsed
	}
	var parts []string
	for _, d := range current.Since(previous) {
		parts = append(parts, fmt.Sprintf("%s=%d", d.Name, d.Count))
	}
	work := "idle"
	if len(parts) > 0 {
		work = strings.Join(parts, " ")
	}
	return fmt.Sprintf("%s cpu %5.1f%% heap %s  %s\n", current.At.Local().Format("15:04:05"), share, megabytes(current.Process.HeapBytes), work)
}
