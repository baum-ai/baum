package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/baum-ai/baum/internal/githubauth"
)

type fakeConnection struct {
	status githubauth.Status
	err    error
}

func (f fakeConnection) Status(context.Context) (githubauth.Status, error) { return f.status, f.err }

func TestWorkflowAccessFollowsTheGitHubConnection(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name       string
		connection connectionStatus
		want       bool
	}{
		{"explicit token, scopes unknown", nil, true},
		{"connected with workflow access", fakeConnection{status: githubauth.Status{State: "connected", Workflow: true}}, true},
		{"connected without workflow access", fakeConnection{status: githubauth.Status{State: "connected"}}, false},
		{"status unreadable", fakeConnection{err: errors.New("keyring locked")}, false},
		{"not connected", fakeConnection{status: githubauth.Status{State: "disconnected"}}, true},
	} {
		if got := workflowAccess(ctx, test.connection); got != test.want {
			t.Errorf("%s: %v, want %v", test.name, got, test.want)
		}
	}
}
