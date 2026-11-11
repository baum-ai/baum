package sessions

import (
	"context"
	"reflect"
	"testing"
)

func TestLoadSessionIDsMapsOnlyListedSessions(t *testing.T) {
	fx := newFixture(t)
	useCache(t, 0)
	all, _, err := Load(context.Background(), fx.repo, fx.options())
	if err != nil {
		t.Fatal(err)
	}
	const codexID = "019f0000-0000-7000-8000-000000000001"
	var want *Session
	for i := range all {
		if all[i].ID == codexID {
			want = &all[i]
		}
	}
	if want == nil || len(all) < 2 {
		t.Fatalf("fixture sessions: %+v", all)
	}

	decodes := 0
	decodeObserver = func(int64) func() { decodes++; return func() {} }
	t.Cleanup(func() { decodeObserver = nil })
	opts := fx.options()
	opts.SessionIDs = []string{codexID}
	got, _, err := Load(context.Background(), fx.repo, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != codexID || got[0].Key != want.Key || got[0].Path != want.Path || !reflect.DeepEqual(got[0].Graph, want.Graph) {
		t.Errorf("filtered load = %+v, want only %s", got, codexID)
	}
	if decodes != 1 {
		t.Errorf("decoded %d sessions, want 1", decodes)
	}

	opts.SessionIDs = []string{"no-such-session"}
	if got, _, err := Load(context.Background(), fx.repo, opts); err != nil || len(got) != 0 {
		t.Errorf("unknown id: %+v, %v", got, err)
	}
}
