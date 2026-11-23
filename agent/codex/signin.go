package codex

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/baum-ai/baum/agent"
)

// SignedIn asks Codex whether it is signed in, with the environment a Local
// run gets: a ChatGPT sign-in or an API key saved with `codex login` counts.
// known is false when the answer could not be read.
func SignedIn(ctx context.Context, binary string) (signedIn, known bool) {
	return SignedInWith(ctx, binary, nil)
}

// SignedInWithHome asks the codex at binary ("" is the one on PATH), as a
// run would start it, whether it is signed in with the Codex home dir, which
// it gets as CODEX_HOME.
func SignedInWithHome(binary string) func(ctx context.Context, dir string) (signedIn, known bool) {
	if binary == "" {
		binary = "codex"
	}
	return func(ctx context.Context, dir string) (bool, bool) {
		return SignedInWith(ctx, binary, []string{"CODEX_HOME=" + dir})
	}
}

// SignedInWith is SignedIn with the user's own agent variables env
// ("NAME=value") set over the environment, as a run's Backend.Env is: with
// CODEX_HOME it asks about the sign-in of that home.
func SignedInWith(ctx context.Context, binary string, env []string) (signedIn, known bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := localCommand(ctx, binary, "login", "status")
	cmd.Env = agent.WithUserEnv(cmd.Env, env)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, true
	case errors.As(err, &exit) && strings.Contains(string(out), "Not logged in"):
		return false, true
	}
	return false, false
}
