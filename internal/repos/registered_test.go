package repos

import (
	"context"
	"testing"
)

// Registered reports each stored registration, whichever way it was made, and
// nothing a registration refused.
func TestRegisteredReportsStoredClones(t *testing.T) {
	ctx := context.Background()
	r := registry(t)
	var got []Clone
	r.Registered = func(c Clone) { got = append(got, c) }
	p := clone(t, "https://github.com/acme/app.git")

	if _, e := r.AddFor(ctx, "/nonexistent", "acme/app", nil); e == nil || len(got) != 0 {
		t.Fatalf("failed registration reported: %v %+v", e, got)
	}
	if _, e := r.Add(ctx, p, nil); e != nil || len(got) != 1 || got[0].Repository != "acme/app" || got[0].Path == "" {
		t.Fatalf("Add reported %+v (%v)", got, e)
	}
	if _, e := r.AddFor(ctx, p, "acme/app", nil); e != nil || len(got) != 2 {
		t.Fatalf("AddFor reported %+v (%v)", got, e)
	}
	if _, e := r.AttachFound(ctx, p, "acme/app"); e != nil || len(got) != 3 {
		t.Fatalf("AttachFound reported %+v (%v)", got, e)
	}
}
