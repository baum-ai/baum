package agentdirs

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// SignedIn asks whether the agent is signed in with the directory dir; known
// is false when it cannot tell (not installed, or too old to be asked).
// claude.SignedInWithConfigDir and codex.SignedInWithHome build the ones that
// ask the agent a run would start.
type SignedIn func(ctx context.Context, dir string) (signedIn, known bool)

// Choose checks a directory of the agent the user chose in Settings for
// reviews, fixes or data extraction, and returns it as it is stored: absolute
// and clean, or empty for the environment's. It must work before it is
// accepted: a directory that exists, since the agent would start a new one at
// a mistyped path, and one the agent says it is signed in with, since every
// run with another would fail. An answer signedIn cannot give, or a nil
// signedIn, does not refuse it. The directory is then remembered, so the
// sessions of the runs it gets are read and removed.
func (a Agent) Choose(ctx context.Context, given string, signedIn SignedIn) (string, error) {
	if strings.TrimSpace(given) == "" {
		return "", nil
	}
	dir, err := Normalize(given)
	if err != nil {
		return "", fmt.Errorf("%s: %w", a.What, err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s: %s is not a directory on this computer", a.What, dir)
	}
	if signedIn == nil {
		signedIn = func(context.Context, string) (bool, bool) { return false, false }
	}
	if ok, known := signedIn(ctx, dir); known && !ok {
		return "", fmt.Errorf("%s is not signed in with %s. %s, then choose it again", a.Product, dir, a.signIn(dir))
	}
	if _, err := a.Remember(dir); err != nil {
		return "", fmt.Errorf("%s: remembering %s: %w", a.What, dir, err)
	}
	return dir, nil
}
