package uiserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tilelayout"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// layoutPR is the browser fixture's pull request whose tiles the layout
// test lays out its own way: acme/demo#14, no longer listed, with one saved
// version until the test asks for the next.
const layoutPR = 14

// layoutDropped is the change the next version of layoutPR no longer has.
const layoutDropped = "sec_docs"

// seedLayoutReview saves the first version of layoutPR: the fixture's
// review of acme/demo#7 under its own number.
func seedLayoutReview(t *testing.T, versions *store.Store, from store.Version) {
	t.Helper()
	_ = os.Remove(filepath.Join(os.Getenv("BAUM_APP_STATE_DIR"), "layout-next-version"))
	document, err := layoutDocument(from.Document, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	first := store.Version{Metadata: store.Metadata{Version: 1, Head: from.Metadata.Head, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Backend: "claude", Model: "claude-sonnet-4-5"},
		Document: document, Attachments: from.Attachments}
	if err := versions.Put(context.Background(), store.Key{Repository: "acme/demo", Number: layoutPR}, store.Version{}, first); err != nil {
		t.Fatal(err)
	}
}

// layoutNextVersion saves layoutPR's next version, without layoutDropped,
// whose hunks no change describes any more.
func layoutNextVersion(versions *store.Store) error {
	ctx := context.Background()
	key := store.Key{Repository: "acme/demo", Number: layoutPR}
	latest, err := versions.Latest(ctx, key)
	if err != nil {
		return err
	}
	n := latest.Metadata.Version + 1
	document, err := layoutDocument(latest.Document, n, true)
	if err != nil {
		return err
	}
	next := store.Version{Metadata: store.Metadata{Version: n, Head: latest.Metadata.Head, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Backend: "claude", Model: "claude-sonnet-4-5"},
		Document: document, Attachments: latest.Attachments}
	return versions.Put(ctx, key, latest, next)
}

func layoutDocument(from []byte, version int, drop bool) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(from, &doc); err != nil {
		return nil, err
	}
	review := doc["review"].(map[string]any)
	review["pull_request"], review["version"], review["previous_version"] = layoutPR, version, nil
	if version > 1 {
		review["previous_version"] = version - 1
	}
	doc["summary"].(map[string]any)["text"] = "A review laid out the reader's way"
	if drop {
		var kept []any
		for _, raw := range doc["sections"].([]any) {
			section := raw.(map[string]any)
			if section["id"] == layoutDropped {
				unassigned, _ := doc["unassigned_hunks"].([]any)
				doc["unassigned_hunks"] = append(unassigned, section["hunks"].([]any)...)
				continue
			}
			kept = append(kept, section)
		}
		doc["sections"] = kept
	}
	return json.Marshal(doc)
}

// layout runs a request about the layout of the tiles as the panel's agent
// would, through the layout tools of baum's MCP server over HTTP with the
// turn's token, and answers in its words from what the tools said: "move
// the risks to the top", "make this wider" and "remove this" about the tile
// the page focuses, "undo that" and "reset the layout". ok is false for any
// other request.
func (a *panelAgent) layout(ctx context.Context, r agent.Request) (answer string, ok bool) {
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
	said := func(result map[string]any, err error) string {
		if err != nil {
			return err.Error()
		}
		var lines []string
		for _, key := range []string{"applied", "undone"} {
			if items, ok := result[key].([]any); ok {
				for _, item := range items {
					lines = append(lines, fmt.Sprint(item))
				}
			}
		}
		if len(lines) == 0 {
			return "Done: the tiles are laid out as the review was generated. Undo on the page brings your layout back."
		}
		return strings.Join(lines, ". ") + ". Undo on the page takes it back."
	}
	focused := func() (map[string]any, map[string]any, error) {
		view, _, err := call("tile_layout", nil)
		if err != nil {
			return nil, nil, err
		}
		focus, _ := view["focus"].(map[string]any)
		if focus == nil {
			return view, nil, errors.New("Which tile do you mean? Point at it with Ask about this tile, or name it")
		}
		for _, raw := range view["tiles"].([]any) {
			if tile := raw.(map[string]any); tile["id"] == focus["tile"] {
				return view, tile, nil
			}
		}
		return view, nil, errors.New("The tile you pointed at is not on this review")
	}
	switch {
	case a.mcp == nil || r.MCP == nil:
		return "", false
	case strings.Contains(prompt, "undo"):
		result, _, err := call("undo_tiles", nil)
		return said(result, err), true
	case strings.Contains(prompt, "reset"):
		result, _, err := call("reset_tiles", nil)
		return said(result, err), true
	case strings.Contains(prompt, "risks") && (strings.Contains(prompt, "top") || strings.Contains(prompt, "first")):
		result, _, err := call("edit_tiles", map[string]any{"ops": []any{map[string]any{"op": "move", "tile": "risks", "to": map[string]any{"edge": "first"}}}})
		return said(result, err), true
	case strings.Contains(prompt, "wider"):
		_, tile, err := focused()
		if err != nil {
			return err.Error(), true
		}
		order := []string{string(tiles.SizeThird), string(tiles.SizeHalf), string(tiles.SizeFull)}
		at := slices.Index(order, fmt.Sprint(tile["size"]))
		var sizes []string
		for _, size := range tile["sizes"].([]any) {
			sizes = append(sizes, fmt.Sprint(size))
		}
		for _, wider := range order[at+1:] {
			if slices.Contains(sizes, wider) {
				result, _, err := call("edit_tiles", map[string]any{"ops": []any{map[string]any{"op": "resize", "tile": tile["id"], "size": wider}}})
				return said(result, err), true
			}
		}
		return fmt.Sprintf("%s is already as wide as it can be.", tile["title"]), true
	case strings.Contains(prompt, "api changes tile"):
		// Without data: baum fills it (fixtureFiller), and the card is
		// pending until then.
		result, _, err := call("add_tile", map[string]any{"type": "api_change"})
		return said(result, err), true
	case strings.Contains(prompt, "remove this") || strings.Contains(prompt, "hide this"):
		_, tile, err := focused()
		if err != nil {
			return err.Error(), true
		}
		result, _, err := call("edit_tiles", map[string]any{"ops": []any{map[string]any{"op": "hide", "tile": tile["id"]}}})
		return said(result, err), true
	}
	return "", false
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
	layouts  *tilelayout.Service
}

func (f fixtureFiller) Fill(r tilelayout.FillRequest) {
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
		_ = f.layouts.PutFill(ctx, tilelayout.Key(r.Repository, r.Number), tiles.LibraryTileID(r.Type), r.Version, tilelayout.OriginFill, entry.Encoded, json.RawMessage(data))
	}()
}
