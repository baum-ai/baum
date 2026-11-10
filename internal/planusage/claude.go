package planusage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// usageURL is Claude Code's own /usage endpoint. It is undocumented and may
// change; every failure becomes an unavailable plan with a fixed reason.
const usageURL = "https://api.anthropic.com/api/oauth/usage"

var errNoCredential = errors.New("not signed in")

// Credential is the access token Claude Code stored. It never leaves this
// package: it is not logged, stored, serialised or put in an error.
type Credential struct {
	Token     string
	ExpiresAt time.Time // zero when unknown
	// Plan is the plan's name as Anthropic sells it ("Pro", "Max 5x",
	// "Team Premium"), from what Claude Code recorded with the token; empty
	// when it did not say.
	Plan string
}

// Claude asks Anthropic for the usage of the plan Claude Code is signed in to.
type Claude struct {
	// Credential finds Claude Code's token; nil reads its stored credential.
	Credential func(ctx context.Context) (Credential, error)
	// Client makes the request; nil uses a client that refuses redirects.
	Client *http.Client
	// URL overrides the endpoint in tests.
	URL string
	Now func() time.Time

	mu sync.Mutex
	// last is the newest reading that worked, served while the endpoint
	// fails for a reason that should pass.
	last *Plan
	// notBefore is when the endpoint may be asked again after it limited
	// the rate, and limited how many times in a row it has.
	notBefore time.Time
	limited   int
}

const (
	// staleFor is how long the last reading stands in for one that failed.
	// It keeps its own FetchedAt, so its age shows.
	staleFor = 6 * time.Hour
	// The wait after a 429 that names none: doubled each time in a row.
	firstRateLimitWait = 5 * time.Minute
	maxRateLimitWait   = time.Hour
)

type outcome int

const (
	read outcome = iota
	// signedOut needs the person: Anthropic refuses the credential.
	signedOut
	// rateLimited is a 429; the duration with it is its Retry-After, if any.
	rateLimited
	// failed is anything else that may pass by itself.
	failed
)

func NewClaude() *Claude { return &Claude{} }

func (c *Claude) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Claude) unavailable(reason string) Plan {
	return Plan{Provider: "claude", Reason: reason, Windows: []Window{}, FetchedAt: c.now()}
}

// Read never refreshes the token: that would rotate Claude Code's credential.
// A failure that should pass (the endpoint limits the rate, is down or cannot
// be reached) serves the last reading for up to staleFor, and after a 429 the
// endpoint is left alone for a while: asking again only keeps the limit.
func (c *Claude) Read(ctx context.Context) Plan {
	c.mu.Lock()
	defer c.mu.Unlock()
	// The stored credential is checked on every read, also while the endpoint
	// is left alone: signing out must not leave the last reading standing.
	find := c.Credential
	if find == nil {
		find = StoredCredential
	}
	cred, err := find(ctx)
	switch {
	case err != nil || cred.Token == "":
		c.last = nil
		return c.unavailable("Claude Code is not signed in on this computer.")
	case !cred.ExpiresAt.IsZero() && !cred.ExpiresAt.After(c.now()):
		c.last = nil
		return c.unavailable("Open Claude Code to refresh its sign-in.")
	}
	if c.now().Before(c.notBefore) {
		return c.stale(c.unavailable("Claude's usage is unavailable right now."))
	}
	plan, result, wait := c.fetch(ctx, cred)
	if result == rateLimited {
		if wait <= 0 {
			wait = firstRateLimitWait << min(c.limited, 8)
		}
		c.limited++
		c.notBefore = c.now().Add(min(wait, maxRateLimitWait))
	} else {
		c.limited, c.notBefore = 0, time.Time{}
	}
	switch result {
	case read:
		c.last = &plan
	case signedOut:
		c.last = nil
	case rateLimited, failed:
		return c.stale(plan)
	}
	return plan
}

// stale is the last reading while it is recent enough to stand in for a
// failure, without the figure of a window that has reset since; otherwise the
// failure itself.
func (c *Claude) stale(failure Plan) Plan {
	now := c.now()
	if c.last == nil || now.Sub(c.last.FetchedAt) >= staleFor {
		return failure
	}
	plan := *c.last
	plan.Windows = make([]Window, len(c.last.Windows))
	for i, w := range c.last.Windows {
		if w.ResetsAt != nil && !w.ResetsAt.After(now) {
			w.Reset, w.UsedPercent = true, nil
		}
		plan.Windows[i] = w
	}
	return plan
}

// fetch asks the endpoint with a credential Read found usable.
func (c *Claude) fetch(ctx context.Context, cred Credential) (Plan, outcome, time.Duration) {
	client := c.Client
	if client == nil {
		client = &http.Client{
			Timeout:       15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	url := c.URL
	if url == "" {
		url = usageURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return c.unavailable("Claude's usage could not be requested."), failed, 0
	}
	req.Header.Set("Authorization", "Bearer "+cred.Token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// The error is dropped: nothing in it is safe or useful to show.
		return c.unavailable("Claude's usage could not be reached."), failed, 0
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return c.unavailable("Open Claude Code to refresh its sign-in."), signedOut, 0
	case resp.StatusCode == http.StatusTooManyRequests:
		// Retry-After in seconds; absent, zero or a date leaves the wait to Read.
		seconds, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return c.unavailable("Claude's usage is unavailable right now."), rateLimited, time.Duration(seconds) * time.Second
	case resp.StatusCode != http.StatusOK:
		return c.unavailable("Claude's usage is unavailable right now."), failed, 0
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return c.unavailable("Claude's usage could not be read."), failed, 0
	}
	windows, ok := parseClaudeUsage(body)
	if !ok {
		return c.unavailable("Claude's usage came back in a form baum does not understand."), failed, 0
	}
	return Plan{Provider: "claude", Available: true, Plan: cred.Plan, Windows: windows, FetchedAt: c.now()}, read, 0
}

type claudeWindow struct {
	Utilization *float64   `json:"utilization"`
	ResetsAt    *time.Time `json:"resets_at"`
}

// claudeWindows lists the response keys baum shows, in display order.
var claudeWindows = []struct{ key, name string }{
	{"five_hour", "5-hour"},
	{"seven_day", "weekly"},
	{"seven_day_opus", "weekly Opus"},
	{"seven_day_sonnet", "weekly Sonnet"},
}

// parseClaudeUsage ignores unknown keys and null or malformed windows.
func parseClaudeUsage(body []byte) ([]Window, bool) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return nil, false
	}
	windows := []Window{}
	for _, k := range claudeWindows {
		var w claudeWindow
		if json.Unmarshal(raw[k.key], &w) != nil || w.Utilization == nil {
			continue
		}
		windows = append(windows, Window{Name: k.name, UsedPercent: w.Utilization, ResetsAt: w.ResetsAt})
	}
	return windows, len(windows) > 0
}

type storedCredential struct {
	ClaudeAIOAuth struct {
		AccessToken      string `json:"accessToken"`
		ExpiresAt        int64  `json:"expiresAt"`
		SubscriptionType string `json:"subscriptionType"`
		RateLimitTier    string `json:"rateLimitTier"`
	} `json:"claudeAiOauth"`
}

func parseCredential(data []byte) (Credential, error) {
	var s storedCredential
	if json.Unmarshal(data, &s) != nil || s.ClaudeAIOAuth.AccessToken == "" {
		return Credential{}, errNoCredential
	}
	cred := Credential{
		Token: s.ClaudeAIOAuth.AccessToken,
		Plan:  claudePlanName(s.ClaudeAIOAuth.SubscriptionType, s.ClaudeAIOAuth.RateLimitTier),
	}
	if s.ClaudeAIOAuth.ExpiresAt > 0 {
		cred.ExpiresAt = time.UnixMilli(s.ClaudeAIOAuth.ExpiresAt)
	}
	return cred, nil
}

// claudePlanName names the plan as Anthropic sells it, from Claude Code's
// subscriptionType and rateLimitTier ("default_claude_max_5x"): Pro, Max 5x
// and Max 20x for a person; a Team seat is Standard or Premium (the 5x tier);
// Enterprise is billed at API prices and has no tier. Anything else is passed
// on as it came, so a new plan still says something.
func claudePlanName(subscription, tier string) string {
	switch subscription {
	case "pro":
		return "Pro"
	case "max":
		switch {
		case strings.HasSuffix(tier, "max_20x"):
			return "Max 20x"
		case strings.HasSuffix(tier, "max_5x"):
			return "Max 5x"
		}
		return "Max"
	case "team":
		switch {
		case strings.HasSuffix(tier, "max_5x"):
			return "Team Premium"
		case tier != "":
			return "Team Standard"
		}
		return "Team"
	case "enterprise":
		return "Enterprise"
	}
	return subscription
}

// StoredCredential reads Claude Code's credential: on macOS the Keychain item
// first, then $CLAUDE_CONFIG_DIR/.credentials.json (default
// ~/.claude/.credentials.json).
func StoredCredential(ctx context.Context) (Credential, error) {
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
		if err == nil {
			if cred, err := parseCredential([]byte(strings.TrimSpace(string(out)))); err == nil {
				return cred, nil
			}
		}
	}
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Credential{}, errNoCredential
		}
		dir = filepath.Join(home, ".claude")
	}
	data, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		return Credential{}, errNoCredential
	}
	return parseCredential(data)
}
