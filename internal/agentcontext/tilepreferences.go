package agentcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/baum-ai/baum/tiles/compose"
)

// The tile preference tools read and propose a repository's standing
// preferences for the tile pages of its reviews: an instruction the composer
// of every later review follows, and tiles pinned to the front of its pages.
// Unlike the layout edits, which change only the page the person has open, a
// preference outlives the pull request, so changing it is a proposal the
// person saves. Both are the pull request page's, bound to it as the layout
// tools are (layoutPage).

// TargetTilePreferences ends a pull request page's target for the page's
// repository (RepositoryTarget).
const TargetTilePreferences = "tile_preferences"

// maxTileText and maxTilePins are the limits of a repository's tile
// preferences, as the registry enforces them when the person saves.
const (
	maxTileText = 1000
	maxTilePins = 8
)

// tilePreferencePageTools are the tile preference tools the pull request page
// lists besides its propose_* tool (proposePageTools).
var tilePreferencePageTools = []string{"tile_preferences"}

// tilePreferenceTools are the tools that read and propose a repository's tile
// preferences. Both are global, so an action never sees them.
func (s *Server) tilePreferenceTools() []Tool {
	repository := `"repository":{"type":"string","maxLength":200,"description":"The repository as owner/name, only to check it is the open page's."}`
	return []Tool{{
		Name: "tile_preferences",
		Description: "The standing tile preferences of the repository of the pull request page the person has open: text, the instruction the composer of every new review in the repository follows, and pins, the tiles that lead its pages. Both are empty when there are none. " +
			"They apply to the next review generated in the repository, with tile reviews on; they do not change the review the person has open.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` + repository + `},"additionalProperties":false}`),
		Reads:       []string{"/api/repositories"},
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Repository string `json:"repository"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			page, _, err := layoutPage(c, false)
			if err != nil {
				return "", err
			}
			if err := samePageRepository(page, a.Repository); err != nil {
				return "", err
			}
			row, ok, err := registered(ctx, c, page)
			if err != nil {
				return "", err
			}
			if !ok {
				return "", fmt.Errorf("%s is not a repository baum watches", page)
			}
			out := struct {
				Repository string   `json:"repository"`
				Text       string   `json:"text"`
				Pins       []string `json:"pins"`
			}{Repository: row.Repository, Pins: []string{}}
			if t := row.Settings.Tiles; t != nil {
				out.Text = t.Text
				if len(t.Pins) > 0 {
					out.Pins = t.Pins
				}
			}
			return compactJSON(out)
		},
	}, {
		Name: "propose_tile_preferences",
		Description: "Proposes a change to the standing tile preferences of the repository of the pull request page the person has open: text, an instruction the composer of every later review in the repository follows (null clears it), and pins, the tiles that lead its pages whenever they apply (the whole list; an empty list clears it). " +
			"A pin is a single tile type (overview, risks, evidence, intent, decisions, context, media, everything_else) or a library tile type's name such as schema_change; tile_layout lists the library types under addable. Only the fields given change. " +
			"The preference is a proposal because it outlives this pull request and instructs every later review: nothing changes until the person saves it, and it applies to the next review generated, never to this page. " +
			"To change the page the person has open too, also call edit_tiles: layout edits apply at once.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"text":{"type":["string","null"],"maxLength":` + fmt.Sprint(maxTileText) + `,"description":"The instruction, such as: lead with what changed in the data model. Null clears it."},` +
			`"pins":{"type":"array","maxItems":` + fmt.Sprint(maxTilePins) + `,"items":{"type":"string","minLength":1,"maxLength":128},"description":"Tile types that lead the pages, replacing the list."},` +
			repository + `},"minProperties":1,"additionalProperties":false}`),
		Proposes: true,
		Call: func(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
			var a struct {
				Text       *string  `json:"text"`
				Pins       []string `json:"pins"`
				Repository string   `json:"repository"`
			}
			if err := strictArgs(args, &a); err != nil {
				return "", err
			}
			page, _, err := layoutPage(c, true)
			if err != nil {
				return "", err
			}
			if err := samePageRepository(page, a.Repository); err != nil {
				return "", err
			}
			keys := present(args)
			if !keys["text"] && !keys["pins"] {
				return "", fmt.Errorf("give text, pins or both")
			}
			if a.Text != nil && utf8.RuneCountInString(strings.TrimSpace(*a.Text)) > maxTileText {
				return "", fmt.Errorf("the instruction is too long: the most is %d characters", maxTileText)
			}
			for _, pin := range a.Pins {
				if err := compose.CheckPin(pin); err != nil {
					return "", err
				}
			}
			after := map[string]any{}
			if keys["text"] {
				after["text"] = a.Text
			}
			if keys["pins"] {
				pins := a.Pins
				if pins == nil {
					pins = []string{}
				}
				after["pins"] = pins
			}
			return propose(ctx, c, PagePullRequest, RepositoryTarget(page, TargetTilePreferences), after)
		},
	}}
}

// samePageRepository refuses a repository other than the page's.
func samePageRepository(page, named string) error {
	if named = strings.TrimSpace(named); named != "" && !strings.EqualFold(named, page) {
		return fmt.Errorf("the person has %s's pull request open: tile preferences are for that repository", page)
	}
	return nil
}
