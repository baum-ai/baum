package planusage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// codexUsageURL is the usage endpoint the Codex CLI asks for its status line.
// It is undocumented and may change; any failure falls back to the session
// files.
const codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// CodexCredential is the ChatGPT sign-in Codex stored. Like Claude's token it
// never leaves this package: it is not logged, stored, serialised or put in
// an error.
type CodexCredential struct {
	Token     string
	AccountID string
	ExpiresAt time.Time // zero when unknown
}

type storedCodexAuth struct {
	Tokens *struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

// StoredCodexCredential reads $CODEX_HOME/auth.json. A sign-in with an API key
// has no ChatGPT token, and so no plan to read.
func StoredCodexCredential(home string) (CodexCredential, error) {
	data, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		return CodexCredential{}, errNoCredential
	}
	var s storedCodexAuth
	if json.Unmarshal(data, &s) != nil || s.Tokens == nil || s.Tokens.AccessToken == "" || s.Tokens.AccountID == "" {
		return CodexCredential{}, errNoCredential
	}
	return CodexCredential{
		Token:     s.Tokens.AccessToken,
		AccountID: s.Tokens.AccountID,
		ExpiresAt: jwtExpiry(s.Tokens.AccessToken),
	}, nil
}

// jwtExpiry reads the exp claim of a JWT without checking its signature: it
// only decides whether the token is worth sending. Zero when unreadable.
func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(claims.Exp), 0)
}

type codexUsage struct {
	PlanType  string `json:"plan_type"`
	RateLimit *struct {
		Primary   *codexUsageWindow `json:"primary_window"`
		Secondary *codexUsageWindow `json:"secondary_window"`
	} `json:"rate_limit"`
}

type codexUsageWindow struct {
	UsedPercent       *float64 `json:"used_percent"`
	LimitWindowSecs   *float64 `json:"limit_window_seconds"`
	ResetAt           *float64 `json:"reset_at"`
	ResetAfterSeconds *float64 `json:"reset_after_seconds"`
}

// readAPI asks Codex's usage endpoint, as the Codex CLI's status line does.
// It never refreshes the token: that would rotate Codex's sign-in. Any
// failure answers false, and Read falls back to the session files.
func (c *Codex) readAPI(ctx context.Context, home string) (Plan, bool) {
	find := c.Credential
	if find == nil {
		find = func(context.Context) (CodexCredential, error) { return StoredCodexCredential(home) }
	}
	cred, err := find(ctx)
	now := c.now()
	if err != nil || cred.Token == "" || cred.AccountID == "" ||
		!cred.ExpiresAt.IsZero() && !cred.ExpiresAt.After(now) {
		return Plan{}, false
	}
	client := c.Client
	if client == nil {
		client = &http.Client{
			Timeout:       15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	url := c.URL
	if url == "" {
		url = codexUsageURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Plan{}, false
	}
	req.Header.Set("Authorization", "Bearer "+cred.Token)
	req.Header.Set("ChatGPT-Account-Id", cred.AccountID)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "baum")
	resp, err := client.Do(req)
	if err != nil {
		// The error is dropped: nothing in it is safe or useful to show.
		return Plan{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Plan{}, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Plan{}, false
	}
	var usage codexUsage
	if json.Unmarshal(body, &usage) != nil || usage.RateLimit == nil {
		return Plan{}, false
	}
	var windows []Window
	for _, w := range []*codexUsageWindow{usage.RateLimit.Primary, usage.RateLimit.Secondary} {
		if w == nil || w.UsedPercent == nil {
			continue
		}
		var minutes *float64
		if w.LimitWindowSecs != nil {
			m := *w.LimitWindowSecs / 60
			minutes = &m
		}
		out := Window{Name: windowName(minutes), UsedPercent: w.UsedPercent}
		switch {
		case w.ResetAt != nil:
			at := time.Unix(int64(*w.ResetAt), 0).UTC()
			out.ResetsAt = &at
		case w.ResetAfterSeconds != nil:
			at := now.Add(time.Duration(*w.ResetAfterSeconds * float64(time.Second))).UTC()
			out.ResetsAt = &at
		}
		windows = append(windows, out)
	}
	if len(windows) == 0 {
		return Plan{}, false
	}
	return Plan{
		Provider:  "codex",
		Available: true,
		Plan:      codexPlanName(usage.PlanType),
		Windows:   windows,
		FetchedAt: now,
	}, true
}
