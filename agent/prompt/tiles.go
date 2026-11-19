package prompt

// TilesVersion is the prompt version of a tile review's prompts: the tile
// analysis (tileanalysis.go). Change it with their texts.
const TilesVersion = "review-tiles-public-v1"

// TilesPrompts implements agent.TileAnalysisPrompts and agent.ViewPrompts
// with this module's public tile review prompts.
type TilesPrompts struct {
	// Overrides replaces built-in texts. Callers must Validate the text
	// first; composition does not.
	Overrides Overrides
}
