package codex

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// SignedIn asks Codex whether it is signed in, with the environment a Local
// run gets: a ChatGPT sign-in or an API key saved with `codex login` counts.
// known is false when the answer could not be read.
func SignedIn(ctx context.Context, binary string) (signedIn, known bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := localCommand(ctx, binary, "login", "status").CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, true
	case errors.As(err, &exit) && strings.Contains(string(out), "Not logged in"):
		return false, true
	}
	return false, false
}
