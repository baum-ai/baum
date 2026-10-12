package gitlocal

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrAmbiguous reports an abbreviated commit id that matches more than one
// object.
var ErrAmbiguous = errors.New("ambiguous abbreviated commit id")

var abbreviatedOIDPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// ResolveCommit expands a full or abbreviated commit id (7 to 64 lower-case hex
// digits) to the full id of a commit the clone holds. It never fetches: an id
// the clone lacks is ErrObjectNotFound, one that matches several objects is
// ErrAmbiguous, and anything that is not hex is ErrInvalidInput.
func (r *Repo) ResolveCommit(ctx context.Context, id string) (string, error) {
	if !abbreviatedOIDPattern.MatchString(id) {
		return "", ErrInvalidInput
	}
	done, err := r.begin()
	if err != nil {
		return "", err
	}
	defer done()
	out, err := r.read(ctx, maxOIDOutputBytes, "rev-parse", "--verify", "--end-of-options", id+"^{commit}")
	if err != nil {
		if passThrough(err) {
			return "", err
		}
		var gitErr *Error
		if errors.As(err, &gitErr) && strings.Contains(strings.ToLower(gitErr.Stderr), "ambiguous") {
			return "", fmt.Errorf("%w: %s", ErrAmbiguous, id)
		}
		return "", fmt.Errorf("%w: commit %s is not in this clone", ErrObjectNotFound, id)
	}
	oid := strings.TrimSpace(string(out))
	if !validOID(oid) {
		return "", fmt.Errorf("%w: commit %s", ErrObjectNotFound, id)
	}
	return oid, nil
}
