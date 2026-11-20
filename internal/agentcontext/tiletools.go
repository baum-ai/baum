package agentcontext

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
)

// The tile making tools let a chat make a tile type for the pull request
// page the person has open and put it on the page at once, as a tile they
// added (addtools.go): read the guide (tile_guide), show a pending card
// on the section where the tile will stand (start_tile), check the type
// with the render check, with this pull request's data (check_tile), and
// add the tile that passed (add_custom_tile). The tile is personal, shows
// only on this pull request's page and has Remove on its menu; it reads
// "Custom · <title>" wherever the page names it. Saving it as a type for
// later reviews is a proposal (propose_tile_type). Like add_tile they are
// chat-only and bound to the pull request the turn was sent from.

// ChatTiles makes tiles in chat for a pull request; the daemon's is
// uiserver's, over the render checker, the added tiles and the pending
// cards.
// owner is the conversation's consumer ID: each conversation has its own
// pending card.
type ChatTiles interface {
	// Start shows a pending card on the pull request's page. It fails when
	// the pull request has no saved review.
	Start(ctx context.Context, owner, repository string, number int, start TileStart) (TileDraft, error)
	// Check runs the render check on a candidate type, with the pull
	// request's data when given, and returns its result as JSON. A problem
	// with the candidate is a fail result, not an error.
	Check(ctx context.Context, owner, repository string, number int, check TileCheck) (json.RawMessage, error)
	// Add puts the tile a passed check with data checked on the pull
	// request's page.
	Add(ctx context.Context, owner, repository string, number int, add CustomTileAdd) (TileAdded, error)
	// TurnEnded leaves owner's pending card unfinished.
	TurnEnded(owner string)
}

// TileStart is start_tile's arguments: the tile's title and the section it
// will stand on (empty: the host places it).
type TileStart struct {
	Title   string
	Section string
}

// TileDraft is a pending card: its ID (draft_<n>) and phase.
type TileDraft struct {
	Tile  string
	Phase string
}

// TileCheck is check_tile's arguments: the candidate type's definition, its
// samples and the pull request's data, if any.
type TileCheck struct {
	Draft      string
	Definition tiles.TypeDefinition
	Glance     string
	Samples    []json.RawMessage
	Data       json.RawMessage
}

// CustomTileAdd is add_custom_tile's arguments: the check, the pending
// card and the section (empty: the card's).
type CustomTileAdd struct {
	Check   string
	Draft   string
	Section string
}

// tileMakingTools are the tools the pull request page lists for making
// tiles in chat.
var tileMakingTools = []string{"tile_guide", "start_tile", "check_tile", "add_custom_tile"}

var errNoChatTiles = errors.New("making tiles in chat is not available in this baum")

// errNoFrames is a desktop shell that mounts no tile frames (the page says
// so in its focus).
var errNoFrames = errors.New("Custom tiles aren't available in this version of the desktop app yet")

// framesOff reports whether the page says it mounts no tile frames: a
// desktop shell that has not declared them (the Windows app, for now).
func framesOff(c CallContext) bool {
	var focus struct {
		Frames string `json:"frames"`
	}
	return c.HasPage && json.Unmarshal(c.Page.Focus, &focus) == nil && focus.Frames == "off"
}

// tilePull is the pull request a tile making tool works on, as add_tile
// finds it, refusing a page that cannot show custom tiles.
func (s *Server) tilePull(c CallContext) (string, int, error) {
	if s.chatTiles == nil {
		return "", 0, errNoChatTiles
	}
	repository, number, err := tilePage(c)
	if err != nil {
		return "", 0, err
	}
	if framesOff(c) {
		return "", 0, errNoFrames
	}
	return repository, number, nil
}

// maxTileHTML bounds a candidate's component, as the render check does.
const maxTileHTML = 256 << 10

const tileRule = " The tile is the person's own: it shows only on this pull request's page, at once, with Remove on its menu, and reads \"Custom · <title>\" wherever the page names it."

func (s *Server) tileMakingTools() []Tool {
	draft := `"draft":{"type":"string","pattern":"^draft_[0-9]{1,9}$","description":"The pending card start_tile gave; without it, this conversation's card, if any."}`
	return []Tool{{
		Name: "tile_guide",
		Description: "How to write a tile type: the guide (part guide, the default: the files, the rules, the two faces, the data schema dialect, citations, the kit API, samples and the render check), the starter type to copy (part starter: its files), or the names the kit offers (part manifest: its API, view, elements, classes and tokens). " +
			"Read the guide and the starter before writing a tile with check_tile. " + preferBuiltIn + " add_tile lists the built-in and library types, and tile_type reads one in full.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"part":{"type":"string","enum":["guide","starter","manifest"]}},"additionalProperties":false}`),
		Call: func(_ context.Context, c CallContext, args json.RawMessage) (string, error) {
			if c.Consumer.Kind == Action {
				return "", errors.New("the tile guide is for a chat in the baum app")
			}
			var a struct {
				Part string `json:"part"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			return tileGuide(a.Part)
		},
	}, {
		Name: "start_tile",
		Description: "Starts making a tile for the review on the pull request page the person has open: the page shows a pending card on the section where the tile will stand (\"Being made in the session panel\"), which says how far you got as you check it. " + preferBuiltIn + " Call it first, when the person asks for a tile no built-in or library type gives (add_tile lists them); then tile_guide, write the type in your own scratch directory (never in the repository's tree), check it with check_tile and add it with add_custom_tile. " +
			"If you stop before adding it, the card says \"Not finished\" for a few minutes, with Remove. The result is the card's id (draft_<n>)." + tileRule,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":80,"description":"The tile's title, as the card shows it."},` + sectionArg + `},"required":["title"],"additionalProperties":false}`),
		Edits:       true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			repository, number, err := s.tilePull(c)
			if err != nil {
				return "", err
			}
			var a struct {
				Title   string `json:"title"`
				Section string `json:"section"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			if strings.TrimSpace(a.Title) == "" {
				return "", errors.New("a tile needs a title")
			}
			section := strings.TrimSpace(a.Section)
			if section == "" {
				section = focusSection(c)
			}
			draft, err := s.chatTiles.Start(ctx, c.Consumer.ID, repository, number, TileStart{Title: a.Title, Section: section})
			if err != nil {
				return "", err
			}
			return compactJSON(map[string]string{"tile": draft.Tile, "phase": draft.Phase,
				"note": "The page shows the pending card now. Write the type, check it with check_tile (with data for this pull request) and add it with add_custom_tile."})
		},
	}, {
		Name: "check_tile",
		Description: "Runs the render check on a tile type you wrote, as the guide's section \"The render check\" describes, and waits for its result (up to 30 seconds): the definition (type, data_schema, html), up to 3 samples of your own and data, the tile's data for this pull request. " +
			"Without drawing, it checks the definition and schema, lints the HTML, checks the samples and the data against the schema, and checks that every citation in data resolves to this pull request's review as the page shows it, and that data cites at least one hunk, file, prompt or session event of it. Then the app draws the data, your samples and two samples made from your schema at each size, in light and dark. " +
			"The result's verdict is pass, fail (each problem with its code, message, sample, face, size and a fixed hint), hang (an endless loop: change the code) or unavailable (no window of the app could draw it: tell the person to open the baum app). A problem with what you sent is a fail, never an error: fix it and check again; each check is a round the pending card counts. " +
			"Samples are synthetic: data with its own hunks and items, written for a made-up repository (acme/widgets), never this pull request's code. data is this pull request's: cite hunks by the ids the diff tool gives. It is masked for secrets before anything is kept. add_custom_tile adds a tile whose check passed with data.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + draft + `,` +
			`"type":{"type":"object","description":"The type, as type.json and fill.md hold it.","properties":{` +
			`"name":{"type":"string","pattern":"^[a-z][a-z0-9_]{0,63}$"},"title":{"type":"string","minLength":1,"maxLength":160},` +
			`"use_when":{"type":"string","maxLength":200},"glance":{"type":"string","maxLength":200},` +
			`"sizes":{"type":"array","minItems":1,"maxItems":3,"items":{"type":"string","enum":["full","half","third"]}},` +
			`"kit":{"type":"integer","minimum":1},"fill":{"type":"string","maxLength":8000,"description":"Instructions for the model that fills the data on later versions of the pull request; without them the tile keeps the data you give it."}},` +
			`"required":["name","title","sizes","kit"],"additionalProperties":false},` +
			`"data_schema":{"type":"object","description":"The data schema, in the dialect of the guide's section 4."},` +
			`"html":{"type":"string","minLength":1,"maxLength":262144,"description":"The component, tile.html: <style> and <script> only."},` +
			`"samples":{"type":"array","maxItems":3,"items":{"type":"object","properties":{"name":{"type":"string","maxLength":64},"data":{},"hunks":{"type":"object"},"items":{"type":"object"}},"required":["data"],"additionalProperties":false}},` +
			`"data":{"type":"object","description":"The tile's data for this pull request, matching data_schema."}},` +
			`"required":["type","data_schema","html"],"additionalProperties":false}`),
		Edits: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			repository, number, err := s.tilePull(c)
			if err != nil {
				return "", err
			}
			var a struct {
				Draft string `json:"draft"`
				Type  struct {
					Name    string       `json:"name"`
					Title   string       `json:"title"`
					UseWhen string       `json:"use_when"`
					Glance  string       `json:"glance"`
					Sizes   []tiles.Size `json:"sizes"`
					Kit     int          `json:"kit"`
					Fill    string       `json:"fill"`
				} `json:"type"`
				DataSchema json.RawMessage   `json:"data_schema"`
				HTML       string            `json:"html"`
				Samples    []json.RawMessage `json:"samples"`
				Data       json.RawMessage   `json:"data"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			if len(a.HTML) > maxTileHTML {
				return "", errors.New("the component is at most 256 KiB")
			}
			def := tiles.TypeDefinition{Name: a.Type.Name, Title: a.Type.Title, UseWhen: a.Type.UseWhen, Fill: a.Type.Fill, DataSchema: a.DataSchema,
				Kit: a.Type.Kit, Sizes: a.Type.Sizes, Source: tiles.TypeSource{Format: "html", HTML: a.HTML}}
			return stringOf(s.chatTiles.Check(ctx, c.Consumer.ID, repository, number, TileCheck{Draft: a.Draft, Definition: def, Glance: a.Type.Glance, Samples: a.Samples, Data: a.Data}))
		},
	}, {
		Name: "add_custom_tile",
		Description: "Adds the tile a check_tile check passed with data for this pull request to the review on the page the person has open, at once: the pending card becomes the tile, on its section. " +
			"check is the passed check's id (chk_…), made in the last hour. The section defaults to the pending card's. Tell the person it is added, that Remove on its menu takes it off, and that \"Save to library…\" on its menu (or asking you) saves it as a type for later reviews." + tileRule,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"check":{"type":"string","pattern":"^chk_[A-Za-z0-9_]{1,64}$"},` + draft + `,` + sectionArg + `},"required":["check"],"additionalProperties":false}`),
		Edits:       true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			repository, number, err := s.tilePull(c)
			if err != nil {
				return "", err
			}
			var a struct {
				Check   string `json:"check"`
				Draft   string `json:"draft"`
				Section string `json:"section"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			added, err := s.chatTiles.Add(ctx, c.Consumer.ID, repository, number, CustomTileAdd{Check: a.Check, Draft: a.Draft, Section: strings.TrimSpace(a.Section)})
			if err != nil {
				return "", err
			}
			return addResult(added)
		},
	}}
}

func stringOf(raw json.RawMessage, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// tileGuide is tile_guide's answer for a part.
func tileGuide(part string) (string, error) {
	switch part {
	case "", "guide":
		return kit.Guide(), nil
	case "starter":
		files := map[string]string{}
		starter := kit.Starter()
		err := fs.WalkDir(starter, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := fs.ReadFile(starter, path)
			if err != nil {
				return err
			}
			files[path] = string(data)
			return nil
		})
		if err != nil {
			return "", err
		}
		return compactJSON(map[string]any{"files": files,
			"note": "Copy the starter into your own scratch directory and change it; check_tile takes type.json's fields with fill.md as type.fill, schema.json as data_schema, tile.html as html and samples/*.json as samples."})
	case "manifest":
		manifest, err := kit.LoadManifest(tiles.KitLatest)
		if err != nil {
			return "", err
		}
		names := func(entries []kit.Entry, signatures bool) []string {
			out := make([]string, 0, len(entries))
			for _, e := range entries {
				if signatures && e.Signature != "" {
					out = append(out, e.Signature)
					continue
				}
				out = append(out, e.Name)
			}
			return out
		}
		return compactJSON(map[string]any{"kit": manifest.Kit, "api": names(manifest.API, true), "view": names(manifest.View, true),
			"elements": names(manifest.Elements, false), "classes": names(manifest.Classes, false), "tokens": names(manifest.Tokens, false)})
	}
	return "", errors.New("part is guide, starter or manifest")
}
