package refresh

import (
	"context"
	"testing"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/schema"
)

// frozenSources records which way session evidence was read.
type frozenSources struct {
	*sources
	evidenceCalls, frozenCalls int
}

func (f *frozenSources) Evidence(ctx context.Context, commits []gitlocal.Commit, maxPrompts, maxCommandEvents int) ([]bundle.Session, error) {
	f.evidenceCalls++
	return f.sources.Evidence(ctx, commits, maxPrompts, maxCommandEvents)
}

func (f *frozenSources) FrozenEvidence(ctx context.Context, commits []gitlocal.Commit, maxPrompts, maxCommandEvents int) ([]bundle.Session, error) {
	f.frozenCalls++
	return f.sources.Evidence(ctx, commits, maxPrompts, maxCommandEvents)
}

func TestRefreshKeepsTheSessionsOfAClosedPullRequest(t *testing.T) {
	doc := object(t, schema.Example)
	header := doc["review"].(map[string]any)
	saved := store.Version{Document: schema.Example, Metadata: store.Metadata{Version: 3, Head: header["head"].(string), Base: header["base"].(string), MergeBase: header["merge_base"].(string)}}
	for _, tc := range []struct {
		state  string
		frozen bool
	}{{"open", false}, {"closed", true}, {"merged", true}} {
		source := &frozenSources{sources: &sources{evidence: github.Evidence{Repository: "example-org/taskline", PullRequest: bundle.PullRequest{Number: 212, State: tc.state, HeadSHA: saved.Metadata.Head, BaseSHA: saved.Metadata.Base}, Sources: bundle.Sources{"review_threads": true}}}}
		if _, err := (Refresher{GitHub: source, Git: source, Sessions: source}).Refresh(context.Background(), github.Repository{Host: "github.com", Owner: "example-org", Name: "taskline"}, 212, saved, assemble.DefaultLimits().Caps, time.Now()); err != nil {
			t.Fatalf("%s: %v", tc.state, err)
		}
		if frozen := source.frozenCalls == 1 && source.evidenceCalls == 0; frozen != tc.frozen {
			t.Errorf("%s: evidence %d, frozen %d", tc.state, source.evidenceCalls, source.frozenCalls)
		}
	}
}
