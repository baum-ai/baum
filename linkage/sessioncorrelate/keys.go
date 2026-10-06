package sessioncorrelate

// attributeKeys are the attribute-log spellings one vendor uses for the facts
// the matcher needs. The session model keeps each vendor's own key unrenamed
// (see ARCHITECTURE.md), so reading the log at all means knowing those
// spellings; this table is the per-vendor hook, and the only place in the
// package a vendor is named.
type attributeKeys struct {
	workingDir     map[string]bool
	branch         map[string]bool
	startingCommit map[string]bool
}

var vendorKeys = map[string]attributeKeys{
	// Claude Code repeats cwd and gitBranch on every envelope line; the mapper
	// logs each change.
	"claude_code": {
		workingDir: keySet("cwd"),
		branch:     keySet("gitBranch"),
	},
	// Codex logs its turn-context snapshot (cwd among it) and the git block of
	// session_meta, which is where the commit the session started on lives.
	"codex": {
		workingDir:     keySet("cwd"),
		branch:         keySet("git.branch"),
		startingCommit: keySet("git.commit_hash"),
	},
	// Cursor records neither a working directory nor a branch of its own; a
	// Cursor session relativizes through the anchor rule.
	"cursor": {},
}

// keysFor falls back to the union of every vendor's spellings for a slug the
// table does not name. Losing a tree's cwd history degrades matching silently,
// while a key no mapper writes matches nothing, so the union is the safe
// default rather than the empty set.
func keysFor(vendor string) attributeKeys {
	if keys, known := vendorKeys[vendor]; known {
		return keys
	}
	return everyVendorKey
}

var everyVendorKey = unionKeys()

func unionKeys() attributeKeys {
	union := attributeKeys{
		workingDir:     map[string]bool{},
		branch:         map[string]bool{},
		startingCommit: map[string]bool{},
	}
	for _, keys := range vendorKeys {
		mergeKeys(union.workingDir, keys.workingDir)
		mergeKeys(union.branch, keys.branch)
		mergeKeys(union.startingCommit, keys.startingCommit)
	}
	return union
}

func mergeKeys(into, from map[string]bool) {
	for key := range from {
		into[key] = true
	}
}

func keySet(keys ...string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}
