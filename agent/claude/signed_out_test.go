//go:build unix

package claude

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baum-ai/baum/agent"
)

// signedOutScript is what Claude Code 2.1.312 prints with no sign-in: a
// synthetic assistant message classed authentication_failed, then a result
// with is_error set, no api_error_status and subtype success, exit 0.
const signedOutScript = "cat >/dev/null\ncat <<'EVENTS'\n" +
	`{"type":"system","subtype":"init","session_id":"9c7f0c5e-2005-4be1-91fb-53fe122de749"}` + "\n" +
	`{"type":"assistant","message":{"id":"6cb14e58-aeae-4ae8-95f0-1f930b3869ef","model":"<synthetic>","role":"assistant","stop_reason":"stop_sequence","usage":{"input_tokens":0,"output_tokens":0},"content":[{"type":"text","text":"Not logged in · Please run /login"}]},"session_id":"9c7f0c5e-2005-4be1-91fb-53fe122de749","error":"authentication_failed","is_api_error_message":true}` + "\n" +
	`{"type":"result","subtype":"success","is_error":true,"num_turns":1,"api_error_status":null,"result":"Not logged in · Please run /login","session_id":"9c7f0c5e-2005-4be1-91fb-53fe122de749","usage":{"input_tokens":0,"output_tokens":0}}` + "\n" +
	"EVENTS\n"

func TestSignedOutIsARefusalTheLoopDoesNotRetry(t *testing.T) {
	script := fakeClaude(t, "2.1.312", signedOutScript)
	r := request(t.TempDir(), nil)
	_, err := (&Backend{Binary: script}).Run(context.Background(), r)
	var rejected *agent.RejectedError
	if !errors.As(err, &rejected) || rejected.Status != "authentication_error" || !agent.SignInRejected(err) {
		t.Fatalf("err = %v", err)
	}
	calls := 0
	g := agent.Generator{Backend: countingBackend{&Backend{Binary: script}, &calls}, RetryDelay: func(int) time.Duration { return 0 }}
	var outcome agent.Outcome
	if _, err := g.Continue(context.Background(), agent.Budgets{MaxCalls: 2, MaxTokens: 100, MaxProviderAttempts: 3}, r, &outcome); !agent.SignInRejected(err) || calls != 1 {
		t.Errorf("Continue err %v after %d calls", err, calls)
	}
}
