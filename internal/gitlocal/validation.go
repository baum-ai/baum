package gitlocal

import (
	"context"
	"strings"

	"github.com/baum-ai/baum/validation/commitlabel"
)

// ValidationCommit reads a unique commit and its tree without fetching. The
// object-prefix lookup avoids Git's ref-name DWIM rules for hex-looking refs.
func (r *Repo) ValidationCommit(ctx context.Context, id string) (commitlabel.Commit, error) {
	if r == nil {
		return commitlabel.Commit{}, ErrClosed
	}
	if !abbreviatedOIDPattern.MatchString(id) {
		return commitlabel.Commit{}, ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return commitlabel.Commit{}, err
	}
	defer done()
	out, err := r.read(ctx, maxOIDOutputBytes, "rev-parse", "--disambiguate="+id)
	if err != nil {
		return commitlabel.Commit{}, err
	}
	ids := strings.Fields(string(out))
	if len(ids) != 1 || !validOID(ids[0]) {
		return commitlabel.Commit{}, ErrObjectNotFound
	}
	out, err = r.read(ctx, maxOIDOutputBytes, "show", "-s", "--format=%H%x00%T", "--end-of-options", ids[0]+"^{commit}")
	if err != nil {
		return commitlabel.Commit{}, rangeError(err)
	}
	sha, tree, ok := strings.Cut(strings.TrimSpace(string(out)), "\x00")
	if !ok || !validOID(sha) || !validOID(tree) {
		return commitlabel.Commit{}, ErrObjectNotFound
	}
	return commitlabel.Commit{SHA: sha, TreeSHA: tree}, nil
}
