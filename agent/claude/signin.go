package claude

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"time"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/proc"
)

// SignedIn asks Claude Code whether it is signed in, with the environment a
// run in the User profile gets: a claude.ai or Console sign-in, an API key
// helper or a key in its settings counts, a provider key in baum's own
// environment does not, since a run never sees it. known is false when the
// answer could not be read, as from a Claude Code without `auth status`.
func SignedIn(ctx context.Context, binary string) (signedIn, known bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "auth", "status", "--json")
	cmd.Env = append(agent.LocalEnvironment(os.Environ(), "CLAUDE_CONFIG_DIR"), fixedEnvironment(true)...)
	proc.Configure(cmd)
	cmd.WaitDelay = 5 * time.Second
	// It exits 1 when signed out, so the output decides, not the exit status.
	out, _ := cmd.Output()
	var status struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if json.Unmarshal(out, &status) != nil || status.LoggedIn == nil {
		return false, false
	}
	return *status.LoggedIn, true
}
