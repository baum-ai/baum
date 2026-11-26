//go:build darwin

package livesessions

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// sameProcess reports whether p's pid is still the process that registered
// the session, by its start time as Claude Code reads it there (`ps -o lstart=`
// in the C locale and UTC). A start time that differs, possibly written
// otherwise, is an error rather than ErrAgentGone: the session is left alone
// rather than recorded ended.
func sameProcess(p ClaudeProcess) error {
	if p.ProcStart == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(p.PID))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("reading the start of process %d: %w", p.PID, err)
	}
	if strings.Join(strings.Fields(string(out)), " ") != strings.Join(strings.Fields(p.ProcStart), " ") {
		return fmt.Errorf("process %d did not start when claude code session %s says", p.PID, p.SessionID)
	}
	return nil
}
