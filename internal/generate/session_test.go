package generate_test

import (
	"context"
	"testing"

	"github.com/baum-ai/baum/internal/generate"
)

// A review keeps no agent session: no call persists one, the stored version
// records none and the run directory goes, for a single pull request, a
// staged review and a group alike.
func TestReviewsKeepNoSession(t *testing.T) {
	for _, test := range []struct {
		name    string
		options func(*testing.T, *fixture) generate.Options
	}{
		{"single", func(t *testing.T, f *fixture) generate.Options { return f.options(t) }},
		{"staged", func(t *testing.T, f *fixture) generate.Options {
			o := f.options(t)
			o.Staged, o.Budgets.MaxCalls = true, 16
			return o
		}},
		{"group", func(t *testing.T, f *fixture) generate.Options { return f.groupOptions(t) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			result, err := generate.Run(context.Background(), test.options(t, f))
			if err != nil {
				t.Fatalf("%v\n%s", err, f.progress.String())
			}
			for _, request := range f.backend.Requests {
				if request.PersistSession || request.ResumeSession != "" {
					t.Errorf("a call kept a session: persist %v resume %q", request.PersistSession, request.ResumeSession)
				}
			}
			if result.Metadata.Session != nil || result.RunDir != "" {
				t.Errorf("session %+v dir %q", result.Metadata.Session, result.RunDir)
			}
			f.requireClean(t)
		})
	}
}
