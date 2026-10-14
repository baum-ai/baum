package tilecatalogue

import "strings"

// A host's tile allow-list decides which catalogue kinds a run offers the
// model; the prompt version does not. With no kinds the model is shown no tiles
// and every returned tile is dropped. Stored tiles are shown however they were
// generated.

// guidance precedes the offered catalogue. Prompt bodies stay immutable,
// so the tile rules travel with the catalogue rather than in a prompt.
const guidance = `Tiles (trusted guidance): sections and individual risks may include optional ordered tiles arrays, using only the kinds in the tile catalogue below and the schema; tiles of any other kind are discarded. A tile replaces prose; do not add one that retells it or duplicate its explanation in generic blocks. Choose the smallest useful tile set; an offered kind may repeat. Place a risk-specific tile on the risk, not on both the risk and its section. Omit a tile when it would repeat narrative content or its essential facts are unavailable; never invent facts to fill one.
A tile's id uses the tile_ prefix, is unique within its section or risk, and is preserved when the subject persists. Give source anchors when something narrower than the section's hunks is true; the platform uses those hunks otherwise. Supply code, which may be empty; optional fields and code excerpts may be omitted. No HTML, layout, executable content or UI-preview.
Code sources use exact repository-relative paths and name their revision as "head" or "merge_base" (merge_base for deleted files); the platform resolves the SHA and sets review_version. An excerpt names only the hunk headers (@@ -old,count +new,count @@) of the supplied comparison it wants shown, one header per patch hunk; the platform fills in the lines, so do not write line text yourself. Preserve inherited sources from the previous review unchanged, including their lines and review_version. If a source cannot be verified from the supplied input, omit it rather than guessing.`

// Instructions is what a run adds to the prompt body for the offered kinds:
// nothing when none are offered, otherwise the tile guidance and the catalogue
// entries of those kinds.
func Instructions(kinds []string) string {
	entries := Entries(kinds)
	if len(entries) == 0 {
		return ""
	}
	raw := make([]string, len(entries))
	for i, entry := range entries {
		raw[i] = string(entry.Raw)
	}
	return "\n\n" + guidance + "\n\nTile catalogue (trusted selection guidance):\n[\n  " + strings.Join(raw, ",\n  ") + "\n]"
}
