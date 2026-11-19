package uiserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/addedtiles"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// tiles runs a request to add a tile as the panel's agent would, through
// the add tools of baum's MCP server over HTTP with the turn's token, and
// answers in its words from what the tools said: "add the api changes
// tile" (a library type, filled by baum) and "show every new environment
// variable" (a tile made in chat). ok is false for any other request.
func (a *panelAgent) tiles(ctx context.Context, r agent.Request) (answer string, ok bool) {
	prompt := strings.ToLower(r.Prompt)
	call := func(tool string, args any) (map[string]any, string, error) {
		text, failed, err := mcpCall(ctx, r.MCP, tool, args)
		status := "completed"
		if err != nil || failed {
			status = "failed"
		}
		if r.Transcript != nil {
			r.Transcript(agent.TranscriptEntry{Kind: "tool", ID: "toolu_" + tool, Tool: "mcp__baum__" + tool, Title: tool, Status: status, Text: text})
		}
		if err != nil {
			return nil, "", err
		}
		if failed {
			return nil, text, errors.New(text)
		}
		var out map[string]any
		_ = json.Unmarshal([]byte(text), &out)
		return out, text, nil
	}
	switch {
	case a.mcp == nil || r.MCP == nil:
		return "", false
	case strings.Contains(prompt, "api changes tile"):
		// Without data: baum fills it (fixtureFiller), and the tile is
		// pending until then.
		result, _, err := call("add_tile", map[string]any{"type": "api_change"})
		if err != nil {
			return err.Error(), true
		}
		return fmt.Sprintf("Added the interface changes tile (%v). Remove on its menu takes it off.", result["state"]), true
	case strings.Contains(prompt, "environment variable"):
		return a.makeTile(ctx, call), true
	}
	return "", false
}

// makeTile makes a tile in chat as the panel's agent would for "show every
// new environment variable": a pending card, the starter type as the
// guide gives it, a first check whose data cites a hunk the review does not
// have, a second that cites the review's first hunk, and the add of the
// check that passed. Each step takes panelStep, so the page shows the card
// writing, then checking, then the tile.
func (a *panelAgent) makeTile(ctx context.Context, call func(string, any) (map[string]any, string, error)) string {
	pause := func() {
		select {
		case <-ctx.Done():
		case <-time.After(panelStep):
		}
	}
	if _, _, err := call("start_tile", map[string]any{"title": "New environment variables"}); err != nil {
		return err.Error()
	}
	pause()
	guide, _, err := call("tile_guide", map[string]any{"part": "starter"})
	if err != nil {
		return err.Error()
	}
	files, _ := guide["files"].(map[string]any)
	text := func(name string) string { s, _ := files[name].(string); return s }
	var typ map[string]any
	if err := json.Unmarshal([]byte(text("type.json")), &typ); err != nil {
		return "the starter has no type.json"
	}
	diff, _, err := call("diff", map[string]any{})
	if err != nil {
		return err.Error()
	}
	hunk := ""
	if list, _ := diff["files"].([]any); len(list) > 0 {
		if refs, _ := list[0].(map[string]any)["hunk_ids"].([]any); len(refs) > 0 {
			hunk, _ = refs[0].(map[string]any)["id"].(string)
		}
	}
	check := func(cited string) (map[string]any, error) {
		data := map[string]any{"summary": "Adds **one** environment variable.", "items": []any{map[string]any{
			"title": "RETRY_LIMIT, default 10", "severity": "low", "text": nil,
			"cites": []any{map[string]any{"type": "hunk", "id": cited}}}}}
		result, _, err := call("check_tile", map[string]any{
			"type": map[string]any{"name": "env_vars", "title": "New environment variables", "use_when": typ["use_when"], "glance": typ["glance"],
				"sizes": typ["sizes"], "kit": typ["kit"], "fill": text("fill.md")},
			"data_schema": json.RawMessage(text("schema.json")), "html": text("tile.html"), "data": data})
		return result, err
	}
	if _, err := check("h_000000000000"); err != nil {
		return err.Error()
	}
	pause()
	result, err := check(hunk)
	if err != nil {
		return err.Error()
	}
	if result["verdict"] != "pass" {
		return fmt.Sprintf("The tile did not pass its check: %v", result)
	}
	pause()
	added, _, err := call("add_custom_tile", map[string]any{"check": result["check"]})
	if err != nil {
		return err.Error()
	}
	if section, _ := added["section"].(string); section != "" {
		return "Added New environment variables to " + section + ". Remove on its menu takes it off."
	}
	return "The tile is added."
}

// mcpCall calls a tool of baum's MCP server as an agent CLI does, and
// returns its text without the envelope's fence; failed is the tool's error.
func mcpCall(ctx context.Context, server *agent.MCPServer, tool string, args any) (text string, failed bool, err error) {
	params, err := json.Marshal(map[string]any{"name": tool, "arguments": args})
	if err != nil {
		return "", false, err
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + string(params) + `}`
	request, err := http.NewRequestWithContext(ctx, "POST", server.URL, bytes.NewReader([]byte(body)))
	if err != nil {
		return "", false, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+server.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = response.Body.Close() }()
	var reply struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		return "", false, err
	}
	if reply.Error != nil {
		return "", false, errors.New(reply.Error.Message)
	}
	if len(reply.Result.Content) == 0 {
		return "", false, errors.New("the tool answered nothing")
	}
	return unfence(reply.Result.Content[0].Text), reply.Result.IsError, nil
}

// unfence returns the data between the envelope's BEGIN and END lines.
func unfence(text string) string {
	lines := strings.Split(text, "\n")
	var inside []string
	in := false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "BEGIN UNTRUSTED DATA"):
			in = true
		case strings.HasPrefix(line, "END UNTRUSTED DATA"):
			in = false
		case in:
			inside = append(inside, line)
		}
	}
	if len(inside) == 0 {
		return text
	}
	return strings.Join(inside, "\n")
}

// fixtureFiller fills an added api_change tile as baum's fill run would, a
// moment after it is asked: a consumer cited on the version's first hunk.
type fixtureFiller struct {
	versions *store.Store
	added    *addedtiles.Service
}

func (f fixtureFiller) Fill(r addedtiles.FillRequest) {
	go func() {
		time.Sleep(8 * time.Second)
		ctx := context.Background()
		entry, ok := library.Get(r.Type)
		if !ok {
			return
		}
		version, err := f.versions.Get(ctx, store.Key{Repository: r.Repository, Number: r.Number}, r.Version)
		if err != nil {
			return
		}
		normalized, err := review.NormalizeStored(version.Document)
		if err != nil {
			return
		}
		doc, err := tiles.FromReview(normalized, tiles.Options{})
		if err != nil {
			return
		}
		diff := library.DiffOf(doc)
		if len(diff) == 0 {
			return
		}
		data := fmt.Sprintf(`{"endpoints":[],"docs":null,"consumers":[{"label":"the demo client","updated":true,"cites":[{"type":"hunk","id":%q}]}]}`, diff[0].ID)
		_ = f.added.PutFill(ctx, addedtiles.Key(r.Repository, r.Number), tiles.LibraryTileID(r.Type), r.Version, addedtiles.OriginFill, entry.Encoded, json.RawMessage(data))
	}()
}
