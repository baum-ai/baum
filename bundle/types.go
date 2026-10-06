package bundle

import "errors"

var (
	// ErrOutputTooLarge reports git output over the configured limit. A
	// patch fetcher returns it to make that file textless.
	ErrOutputTooLarge = errors.New("git output exceeds configured limit")
	// ErrPatchUnreadable reports a file git could not diff. A patch fetcher
	// returns it to make that file textless.
	ErrPatchUnreadable = errors.New("git could not diff the file")
)

// BudgetExceededReason names which checkout budget a failure exceeded:
// exceeding it fails the run naming the budget.
func BudgetExceededReason(name string) string {
	return "checkout budget exceeded: " + name
}

// FileChange is one file of a Comparison.
type FileChange struct {
	OldPath   string `json:"old_path,omitempty"`
	NewPath   string `json:"new_path,omitempty"`
	Status    string `json:"status"`
	OldMode   string `json:"old_mode,omitempty"`
	NewMode   string `json:"new_mode,omitempty"`
	OldOID    string `json:"old_oid,omitempty"`
	NewOID    string `json:"new_oid,omitempty"`
	PatchHash string `json:"patch_hash,omitempty"`
	Additions *int64 `json:"additions,omitempty"`
	Deletions *int64 `json:"deletions,omitempty"`
}

// Comparison is the list of files that differ between two commits.
type Comparison struct {
	BaseOID      string       `json:"base_oid"`
	HeadOID      string       `json:"head_oid"`
	MergeBaseOID string       `json:"merge_base_oid,omitempty"`
	Files        []FileChange `json:"files"`
}

// ChangePaths returns the pathspecs that let a single-file diff detect the
// change as a rename: the old path and, when it differs, the new path.
func ChangePaths(change FileChange) []string {
	paths := make([]string, 0, 2)
	if change.OldPath != "" {
		paths = append(paths, change.OldPath)
	}
	if change.NewPath != "" && change.NewPath != change.OldPath {
		paths = append(paths, change.NewPath)
	}
	return paths
}

// ErrNotFound reports that a source has no such commit, path or repository.
// A blob or export source returns it, or an error wrapping it, so the
// REVIEW.md loader and the checkout export can treat the absence as an
// ordinary outcome.
var ErrNotFound = errors.New("not found")

// ErrInvalidArchive reports a checkout or attachments archive that does not
// have the shape this package writes: an unexpected entry, a path outside
// the tree, a missing file or a corrupt stream.
var ErrInvalidArchive = errors.New("invalid archive")
