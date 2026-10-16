package pricing

import (
	"context"
	"errors"
	"testing"
	"time"
)

// resetSource restores the embedded list and an empty refresh record.
func resetSource(t *testing.T) {
	t.Helper()
	previous := Active()
	t.Cleanup(func() {
		Install(previous)
		refreshState.mu.Lock()
		refreshState.list, refreshState.fetchedAt, refreshState.err = nil, time.Time{}, ""
		refreshState.mu.Unlock()
	})
	Install(Embedded())
	refreshState.mu.Lock()
	refreshState.list, refreshState.fetchedAt, refreshState.err = nil, time.Time{}, ""
	refreshState.mu.Unlock()
}

func TestCurrentSourceReportsTheEmbeddedList(t *testing.T) {
	resetSource(t)
	got := CurrentSource()
	if got.Origin != OriginEmbedded || got.Version != Embedded().Version() || !got.FetchedAt.IsZero() || got.RefreshError != "" {
		t.Errorf("source = %+v", got)
	}
}

func TestCurrentSourceReportsAFetchedListAndTheLastFailure(t *testing.T) {
	resetSource(t)
	fetched := &List{version: "2999-01-01", full: true, entries: Embedded().entries}
	Install(fetched)
	noteRefresh(fetched, nil)
	got := CurrentSource()
	if got.Origin != OriginFetched || got.Version != "2999-01-01" || got.FetchedAt.IsZero() || got.RefreshError != "" {
		t.Errorf("fetched source = %+v", got)
	}

	noteRefresh(nil, errors.New("pricing download unavailable"))
	got = CurrentSource()
	if got.Origin != OriginFetched || got.RefreshError != "pricing download unavailable" || got.FetchedAt.IsZero() {
		t.Errorf("after a failed refresh = %+v", got)
	}

	noteRefresh(nil, nil)
	if got = CurrentSource(); got.RefreshError != "" {
		t.Errorf("a successful refresh must clear the error: %+v", got)
	}
}

func TestRefreshRecordsItsFailure(t *testing.T) {
	resetSource(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Refresh(ctx); err == nil {
		t.Fatal("Refresh with a cancelled context succeeded")
	}
	got := CurrentSource()
	if got.RefreshError == "" || got.Origin != OriginEmbedded {
		t.Errorf("source = %+v", got)
	}
}
