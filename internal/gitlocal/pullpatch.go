package gitlocal

import "context"

// PullPatch reads the complete text diff of an already resolved pull request.
// Output is capped by MaxPatchBytes; an oversized diff is an error, never a
// truncated diff that could be mistaken for all of the PR's changes. Unlike
// Compare it does not hash every file's patch or read file statistics.
func (r *Repo) PullPatch(ctx context.Context, pull Pull) ([]byte, error) {
	if !validOID(pull.MergeBaseOID) || !validOID(pull.HeadOID) {
		return nil, ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return nil, err
	}
	defer done()
	return r.read(ctx, r.opts.MaxPatchBytes, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--full-index", "-M", "-l1000", pull.MergeBaseOID, pull.HeadOID, "--")
}
