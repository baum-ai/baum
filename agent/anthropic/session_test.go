package anthropic

import (
	"context"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

const testSession = "01a18588-ef8c-7962-b6af-d86e6966dacd"

func TestSessionOptionsAreRefused(t *testing.T) {
	for _, r := range []agent.Request{{PersistSession: true}, {ResumeSession: testSession}, {ResumeSession: testSession, ForkSession: true}} {
		r.MaxCalls, r.MaxTokens = 2, 100
		if _, err := (&Backend{}).Run(context.Background(), r); err == nil || !strings.Contains(err.Error(), "session options are not supported") {
			t.Errorf("%+v: error %v", r, err)
		}
	}
}
