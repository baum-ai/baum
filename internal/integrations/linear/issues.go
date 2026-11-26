package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/integrations"
)

var links = regexp.MustCompile(`(?i)https://linear\.app/([a-z0-9_-]+)/issue/([a-z][a-z0-9_]*-[0-9]+)\b`)

type cachedIssue struct {
	issue integrations.Issue
	at    time.Time
}

func (p *Provider) query(ctx context.Context, token, query string, vars any, data any) error {
	b, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return errRequest
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.linear.app/graphql", bytes.NewReader(b))
	if err != nil {
		return errRequest
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return errRequest
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 {
		return errReconnect
	}
	var out struct {
		Data   json.RawMessage
		Errors []json.RawMessage
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out) != nil || len(out.Errors) > 0 || json.Unmarshal(out.Data, data) != nil {
		return errRequest
	}
	return nil
}
func (p *Provider) Issues(ctx context.Context, text string) []integrations.Issue {
	matches := links.FindAllStringSubmatch(text, 64)
	if len(matches) == 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clientID == "" || p.reconnect || p.load(ctx) != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	seen := map[string]bool{}
	var issues []integrations.Issue
	for _, m := range matches {
		key := strings.ToUpper(m[2])
		if !strings.EqualFold(m[1], p.credential.Workspace) || seen[key] {
			continue
		}
		if len(seen) >= 12 {
			break
		}
		seen[key] = true
		if item, ok := p.cache[key]; ok && time.Since(item.at) < 5*time.Minute {
			issues = append(issues, item.issue)
			continue
		}
		token, err := p.token(ctx)
		var data struct {
			Issue struct {
				Identifier, Title, URL string
				State                  struct{ Name string }
			}
		}
		if err == nil {
			err = p.query(ctx, token, `query($id:String!){issue(id:$id){identifier title url state{name}}}`, map[string]string{"id": key}, &data)
		}
		if err == errReconnect {
			p.reconnect = true
			p.cache = nil
			return nil
		}
		if err != nil {
			continue
		}
		i := data.Issue
		u, err := url.Parse(i.URL)
		if err != nil || u.Scheme != "https" || u.Host != "linear.app" || u.User != nil || i.Identifier == "" {
			continue
		}
		issue := integrations.Issue{Key: i.Identifier, Title: i.Title, URL: i.URL, State: i.State.Name}
		if p.cache == nil || len(p.cache) >= 512 {
			p.cache = map[string]cachedIssue{}
		}
		p.cache[key] = cachedIssue{issue, time.Now()}
		issues = append(issues, issue)
	}
	return issues
}
