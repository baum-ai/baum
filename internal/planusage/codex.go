package planusage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// codexMaxFiles bounds how many session files are searched, newest first.
	codexMaxFiles = 8
	// codexTailBytes is how much of the end of a file is searched.
	codexTailBytes = 1 << 20
)

// Codex asks Codex's usage endpoint with the ChatGPT sign-in Codex stored,
// and otherwise (an API key sign-in, an expired token, no network) reads the
// newest rate-limit snapshot Codex wrote to its session files.
type Codex struct {
	// Home is CODEX_HOME; empty means $CODEX_HOME, else ~/.codex.
	Home string
	// Credential finds Codex's sign-in; nil reads Home's auth.json.
	Credential func(ctx context.Context) (CodexCredential, error)
	// Client makes the request; nil uses a client that refuses redirects.
	Client *http.Client
	// URL overrides the endpoint in tests.
	URL string
	Now func() time.Time
}

func NewCodex() *Codex { return &Codex{} }

func (c *Codex) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Codex) home() string {
	if c.Home != "" {
		return c.Home
	}
	if h := os.Getenv("CODEX_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".codex")
	}
	return ""
}

type codexLine struct {
	Timestamp string `json:"timestamp"`
	Payload   struct {
		Type       string `json:"type"`
		RateLimits *struct {
			Primary   *codexWindow `json:"primary"`
			Secondary *codexWindow `json:"secondary"`
			PlanType  string       `json:"plan_type"`
		} `json:"rate_limits"`
	} `json:"payload"`
}

// codexWindow carries the reset as unix seconds (newer Codex) or as seconds
// from the snapshot (older Codex).
type codexWindow struct {
	UsedPercent     *float64 `json:"used_percent"`
	WindowMinutes   *float64 `json:"window_minutes"`
	ResetsAt        *float64 `json:"resets_at"`
	ResetsInSeconds *float64 `json:"resets_in_seconds"`
}

func (c *Codex) Read(ctx context.Context) Plan {
	now := c.now()
	out := Plan{Provider: "codex", Windows: []Window{}, FetchedAt: now}
	home := c.home()
	if home == "" {
		out.Reason = "Codex's home folder could not be found."
		return out
	}
	if plan, ok := c.readAPI(ctx, home); ok {
		return plan
	}
	files := newestRollouts(filepath.Join(home, "sessions"), codexMaxFiles)
	if len(files) == 0 {
		out.Reason = "No Codex sessions found yet. Run Codex once to see its usage."
		return out
	}
	var latest Plan
	for _, name := range files {
		if ctx.Err() != nil {
			break
		}
		if plan, ok := codexSnapshot(name, now); ok && (!latest.Available || plan.FetchedAt.After(latest.FetchedAt)) {
			latest = plan
		}
	}
	if latest.Available {
		return latest
	}
	out.Reason = "Codex has not reported its usage limits yet."
	return out
}

// newestRollouts lists up to limit rollout files, newest first, walking the
// YYYY/MM/DD directories from the latest so older ones are never listed.
func newestRollouts(root string, limit int) []string {
	var found []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if len(found) >= limit {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		var names []string
		for _, e := range entries {
			isRollout := !e.IsDir() && strings.HasPrefix(e.Name(), "rollout-") && strings.HasSuffix(e.Name(), ".jsonl")
			if depth < 3 && e.IsDir() || depth == 3 && isRollout {
				names = append(names, e.Name())
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		for _, n := range names {
			if depth == 3 {
				if len(found) < limit {
					found = append(found, filepath.Join(dir, n))
				}
			} else {
				walk(filepath.Join(dir, n), depth+1)
			}
		}
	}
	walk(root, 0)
	return found
}

func codexSnapshot(name string, now time.Time) (Plan, bool) {
	f, err := os.Open(name)
	if err != nil {
		return Plan{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Plan{}, false
	}
	start := max(info.Size()-codexTailBytes, 0)
	data := make([]byte, info.Size()-start)
	if _, err := io.ReadFull(io.NewSectionReader(f, start, int64(len(data))), data); err != nil {
		return Plan{}, false
	}
	if start > 0 { // the first line is probably cut
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if !bytes.Contains(lines[i], []byte("rate_limits")) {
			continue
		}
		var l codexLine
		if json.Unmarshal(lines[i], &l) != nil || l.Payload.Type != "token_count" || l.Payload.RateLimits == nil {
			continue
		}
		taken, err := time.Parse(time.RFC3339Nano, l.Timestamp)
		if err != nil {
			taken = info.ModTime()
		}
		rl := l.Payload.RateLimits
		var windows []Window
		for _, w := range []*codexWindow{rl.Primary, rl.Secondary} {
			if w != nil && w.UsedPercent != nil {
				windows = append(windows, codexWindowOf(w, taken, now))
			}
		}
		if len(windows) == 0 {
			continue
		}
		return Plan{Provider: "codex", Available: true, Plan: codexPlanName(rl.PlanType), Windows: windows, FetchedAt: taken}, true
	}
	return Plan{}, false
}

// codexPlanNames are the names Codex itself shows for its plan_type values
// (its status line reads "Pro 100" for "prolite"). The Pro tiers are named by
// their monthly price: the original Pro is the $200 one.
var codexPlanNames = map[string]string{
	"free":                      "Free",
	"go":                        "Go",
	"plus":                      "Plus",
	"prolite":                   "Pro 100",
	"pro":                       "Pro 200",
	"promax":                    "Pro 500",
	"team":                      "Team",
	"business":                  "Business",
	"enterprise":                "Enterprise",
	"enterprise_cbp_automation": "Enterprise (Automation)",
	"edu":                       "Edu",
	"edu_pro":                   "Edu Pro",
	"education":                 "Education",
}

// codexPlanName is the name Codex shows for a plan_type; an unknown one is
// passed on as it came, so a new plan still says something.
func codexPlanName(planType string) string {
	if name, ok := codexPlanNames[planType]; ok {
		return name
	}
	return planType
}

func codexWindowOf(w *codexWindow, taken, now time.Time) Window {
	out := Window{Name: windowName(w.WindowMinutes)}
	var resets time.Time
	switch {
	case w.ResetsAt != nil:
		resets = time.Unix(int64(*w.ResetsAt), 0).UTC()
	case w.ResetsInSeconds != nil:
		resets = taken.Add(time.Duration(*w.ResetsInSeconds * float64(time.Second))).UTC()
	}
	if !resets.IsZero() {
		out.ResetsAt = &resets
		if !resets.After(now) {
			out.Reset = true
			return out
		}
	}
	out.UsedPercent = w.UsedPercent
	return out
}

// windowName names Codex's usual window lengths and describes others.
func windowName(minutes *float64) string {
	if minutes == nil || *minutes <= 0 {
		return "window"
	}
	m := math.Round(*minutes)
	switch {
	case m == 300:
		return "5-hour"
	case m == 10080:
		return "weekly"
	case m >= 1440 && math.Mod(m, 1440) == 0:
		return fmt.Sprintf("%d-day", int(m/1440))
	case m >= 60 && math.Mod(m, 60) == 0:
		return fmt.Sprintf("%d-hour", int(m/60))
	}
	return fmt.Sprintf("%d-minute", int(m))
}
