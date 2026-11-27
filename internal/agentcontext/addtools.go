package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/baum-ai/baum/tiles/library"
)

// The add tools add a tile to the review on the pull request page the person
// has open, at once: a library tile type (add_tile) or a tile made in chat
// (add_custom_tile, tiletools.go). They are the one exception to "a chat
// changes baum only through proposals", and a narrow one: they change only
// the person's own review page of a pull request, never the review's
// content, the saved version, the repository or GitHub. An added tile sits
// on the section of the page the person was reading (the page's focus), is
// kept on this computer only, and has Remove on its menu, with Undo. Like
// the propose_* tools they are global, so an action never sees them, and
// refuse any page but a pull request's. They act on the pull request the
// turn was sent from, so a turn keeps working on it while the person reads
// another page (tileTarget).

// AddedTiles adds library tiles to a pull request's review page; the
// daemon's is uiserver's, over internal/addedtiles.
type AddedTiles interface {
	// Add adds a library tile type to the review, on a section, with the
	// data the agent wrote for it (checked against the version's hunks and
	// masked) or, without data, filled in the background.
	Add(ctx context.Context, repository string, number int, add TileAdd) (TileAdded, error)
}

// TileAdd is an add_tile of a built-in or library type: its name, the
// section it goes on (empty: the host places it), the data the agent
// wrote, if any, and a built-in tile's title.
type TileAdd struct {
	Type    string
	Section string
	Data    json.RawMessage
	Title   string
}

// TileAdded is what an add did: the tile's ID, "ready" when it shows its
// data or "filling" while it is filled, and the section it sits on.
type TileAdded struct {
	Tile    string
	State   string
	Section string
}

// addPageTools are the add tools the pull request page lists.
var addPageTools = []string{"tile_type", "add_tile"}

// errNoAddedTiles is a daemon without added tiles.
var errNoAddedTiles = errors.New("adding tiles is not available in this baum")

// tileTarget is the pull request page a tool that changes tiles acts on:
// the page the conversation's current turn was sent from, else the page
// open now. A conversation's page follows the person around the app, so a
// long turn keeps working on the pull request it was asked about while the
// person reads another, and never changes the one they opened meanwhile.
// While the page open now is that same pull request, it is the one read, as
// the person may have moved to another section since.
func tileTarget(c CallContext) (PageContext, bool) {
	switch {
	case c.HasTurn && c.HasPage && samePull(c.Turn, c.Page):
		return c.Page, true
	case c.HasTurn:
		return c.Turn, true
	}
	return c.Page, c.HasPage
}

// samePull reports whether two pages are the same pull request's page.
func samePull(a, b PageContext) bool {
	return a.Page == PagePullRequest && b.Page == PagePullRequest && strings.EqualFold(a.Repository, b.Repository) && a.PullRequest == b.PullRequest
}

// tilePage is the pull request a tool that changes its tiles acts on (see
// tileTarget): only a chat may, and only about a pull request's page.
func tilePage(c CallContext) (string, int, error) {
	p, ok := tileTarget(c)
	if c.Consumer.Kind == Action || !ok || p.Page != PagePullRequest || p.Repository == "" || p.PullRequest < 1 {
		return "", 0, errors.New("tiles are added to a pull request's review page: ask from the pull request's page")
	}
	return p.Repository, p.PullRequest, nil
}

// focusSection is where on the review page the person is reading, from the
// focus of the page tileTarget gives: a section of How it works, "context"
// for Where this fits, or "overview" on the Overview tab; empty elsewhere
// (Code), where the tile goes on the section whose code it cites most.
func focusSection(c CallContext) string {
	p, ok := tileTarget(c)
	if !ok || len(p.Focus) == 0 {
		return ""
	}
	var focus struct {
		Tab     string `json:"tab"`
		Section string `json:"section"`
		Tile    string `json:"tile"`
	}
	if json.Unmarshal(p.Focus, &focus) != nil {
		return ""
	}
	switch {
	case focus.Section != "":
		return focus.Section
	case focus.Tile != "":
		// A tile link shows the section that owns the tile; the host
		// resolves a tile ID to that section.
		return focus.Tile
	case focus.Tab == "overview":
		return "overview"
	}
	return ""
}

// sectionArg is the section argument the add tools take.
const sectionArg = `"section":{"type":"string","maxLength":128,"description":"Where on the review page the tile goes: overview for the Overview tab, context for Where this fits, or a section id. Without it, where the person is reading (overview on the Overview tab), else the section whose code the tile cites most, else overview."}`

const addRule = " It changes only the person's own review page of this pull request, at once; never the review's content. The tile has Remove on its menu, with Undo."

// preferBuiltIn is the rule every tile tool states: a built-in or library
// type whenever one shows it, a custom tile only when none does.
const preferBuiltIn = "Use a built-in or library type whenever one can show what the person asks for, including screenshots and other images (images); make a custom tile (start_tile, tile_guide, check_tile, add_custom_tile) only when none of them makes sense."

// addTools are the tools that add a built-in or library tile.
func (s *Server) addTools() []Tool {
	return []Tool{{
		Name: "add_tile",
		Description: "Adds a tile to the review on the pull request page this turn was sent from, at once, on a section of the page; it stays that pull request's if the person opens another page meanwhile. " + preferBuiltIn + " " +
			"The built-in types, drawn by the page itself: " + builtinNames() + ". The library types: " + libraryNames() + ". " +
			"First read tile_type and write the data yourself from the diff and checkout you have, then pass it as data: the tile shows at once. The host checks the data against the type's schema and this version's hunks and says where it does not fit, never changing anything then; fix it and call again. " +
			"A built-in type always needs data, and keeps it for later versions of the pull request. For images, pass each image file's absolute path on this computer (take a screenshot, or download an image, to a file first; if the repository's agent instructions (CLAUDE.md, AGENTS.md, their guides) describe how to screenshot its app, use that): the host stores the images itself, so never put image bytes in data. " +
			"A library type without data shows as being filled and baum fills it in the background, which costs a separate model run; it fills it again for later versions of the pull request." + addRule,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"type":{"type":"string","minLength":1,"maxLength":128,"description":"A built-in or library type's name."},` +
			sectionArg + `,"title":{"type":"string","maxLength":80,"description":"A built-in tile's title; without it, its diagram's title or the type's."},` +
			`"data":{"type":"object","description":"The tile's data, matching tile_type's fill_schema."}},"required":["type"],"additionalProperties":false}`),
		Edits: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			if s.added == nil {
				return "", errNoAddedTiles
			}
			repository, number, err := tilePage(c)
			if err != nil {
				return "", err
			}
			var a struct {
				Type    string          `json:"type"`
				Section string          `json:"section"`
				Title   string          `json:"title"`
				Data    json.RawMessage `json:"data"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			name := strings.TrimSpace(a.Type)
			if _, ok := builtinType(name); !ok {
				e, ok := library.Find(name)
				if !ok {
					return "", unknownType(name)
				}
				name = e.Definition.Name
			}
			section := strings.TrimSpace(a.Section)
			if section == "" {
				section = focusSection(c)
			}
			added, err := s.added.Add(ctx, repository, number, TileAdd{Type: name, Section: section, Data: a.Data, Title: strings.TrimSpace(a.Title)})
			if err != nil {
				return "", err
			}
			return addResult(repository, number, added)
		},
	}}
}

// libraryNames lists the library types, each with its title.
func libraryNames() string {
	names := make([]string, 0, len(library.All()))
	for _, e := range library.All() {
		names = append(names, e.Definition.Name+" ("+e.Definition.Title+")")
	}
	return strings.Join(names, ", ")
}

// unknownType refuses a type that is neither built in nor a library type,
// naming them.
func unknownType(name string) error {
	return fmt.Errorf("No tile type %s: the built-in types are %s; the library types are %s", name, builtinNames(), libraryNames())
}

// addResult answers an add: the tile, whether it shows its data or is being
// filled, and the section it sits on.
func addResult(repository string, number int, added TileAdded) (string, error) {
	out := map[string]string{"tile": added.Tile, "state": added.State, "section": added.Section, "pull_request": fmt.Sprintf("%s#%d", repository, number),
		"note": "Tell the person the tile is on this pull request's review page (name the pull request if they have another page open now), and that Remove on its menu takes it off."}
	if added.State == "filling" {
		out["note"] = "The tile shows as being filled until baum's fill lands, usually within a minute; tell the person."
	}
	return compactJSON(out)
}
