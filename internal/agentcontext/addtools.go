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
// refuse any page but the pull request's.

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

// errOtherTurnPage is an edit from a turn begun on another page.
var errOtherTurnPage = errors.New("This conversation was started about another page; ask from the pull request you want to change")

// tilePage is the pull request page the person has open, for a tool that
// changes its tiles: only a chat on the pull request's page may, and only
// from a turn sent from that same pull request: a conversation's page
// follows the person around the app, so a long turn begun about one pull
// request must not change another they opened meanwhile.
func tilePage(c CallContext) (string, int, error) {
	if c.Consumer.Kind == Action || !c.HasPage || c.Page.Page != PagePullRequest || c.Page.Repository == "" || c.Page.PullRequest < 1 {
		return "", 0, errors.New("tiles are added to the pull request page the person has open: open a pull request's page first")
	}
	if c.HasTurn && (c.Turn.Page != PagePullRequest || !strings.EqualFold(c.Turn.Repository, c.Page.Repository) || c.Turn.PullRequest != c.Page.PullRequest) {
		return "", 0, errOtherTurnPage
	}
	return c.Page.Repository, c.Page.PullRequest, nil
}

// focusSection is the section of the review page the person is reading,
// from the page's focus: a section of How it works, or "context" for Where
// this fits; empty on another tab.
func focusSection(c CallContext) string {
	if !c.HasPage || len(c.Page.Focus) == 0 {
		return ""
	}
	var focus struct {
		Section string `json:"section"`
		Tile    string `json:"tile"`
	}
	if json.Unmarshal(c.Page.Focus, &focus) != nil {
		return ""
	}
	// A tile link shows the section that owns the tile; the host resolves
	// a tile ID to that section.
	if focus.Section == "" {
		return focus.Tile
	}
	return focus.Section
}

// sectionArg is the section argument the add tools take.
const sectionArg = `"section":{"type":"string","maxLength":128,"description":"The section of the review page the tile goes on: a section id, or context for Where this fits. Without it, the section the person is reading, else the one whose code the tile cites most."}`

const addRule = " It changes only the person's own review page of this pull request, at once; never the review's content. The tile has Remove on its menu, with Undo."

// preferBuiltIn is the rule every tile tool states: a built-in or library
// type whenever one shows it, a custom tile only when none does.
const preferBuiltIn = "Use a built-in or library type whenever one can show what the person asks for, including screenshots and other images (images); make a custom tile (start_tile, tile_guide, check_tile, add_custom_tile) only when none of them makes sense."

// addTools are the tools that add a built-in or library tile.
func (s *Server) addTools() []Tool {
	return []Tool{{
		Name: "add_tile",
		Description: "Adds a tile to the review on the pull request page the person has open, at once, on a section of the page. " + preferBuiltIn + " " +
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
			return addResult(added)
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
func addResult(added TileAdded) (string, error) {
	out := map[string]string{"tile": added.Tile, "state": added.State, "section": added.Section,
		"note": "Tell the person the tile is on their page, and that Remove on its menu takes it off."}
	if added.State == "filling" {
		out["note"] = "The tile shows as being filled until baum's fill lands, usually within a minute; tell the person."
	}
	return compactJSON(out)
}
