//go:build unix

package codex

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

// What codex-cli 0.165.0 prints for exec resume of a thread it does not
// have: nothing on stdout and one line on stderr, here after a warning.
func TestMissingThreadIsReportedAsSuch(t *testing.T) {
	missing := "00000000-0000-4000-8000-000000000000"
	run := "cat >/dev/null\necho 'WARNING: proceeding, even though we could not update PATH' >&2\necho 'Error: thread/resume: thread/resume failed: no rollout found for thread id " + missing + " (code -32600)' >&2\nexit 1\n"
	for _, fork := range []bool{false, true} {
		r := schemaRequest(t.TempDir(), nil)
		r.ResumeSession, r.ForkSession = missing, fork
		_, err := (&Backend{Binary: fakeCodex(t, "codex-cli 0.165.0", run)}).Run(context.Background(), r)
		if !errors.Is(err, agent.ErrSessionNotFound) || !strings.Contains(err.Error(), "no rollout found") {
			t.Errorf("fork %v: err = %v", fork, err)
		}
	}
	r := schemaRequest(t.TempDir(), nil)
	r.PersistSession = true
	if _, err := (&Backend{Binary: fakeCodex(t, "codex-cli 0.165.0", run)}).Run(context.Background(), r); err == nil || errors.Is(err, agent.ErrSessionNotFound) {
		t.Errorf("a fresh thread: %v", err)
	}
}
