package uiserver

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/baum-ai/baum/internal/doctor"
)

func TestDiagnosticsCache(t *testing.T) {
	now := time.Unix(1000, 0)
	c := newDiagnosticsCache()
	c.now = func() time.Time { return now }
	var group singleflight.Group
	runs := 0
	var fail error
	get := func(refresh bool) (any, error) {
		res := <-c.doChan(&group, refresh, func() (any, error) {
			runs++
			if fail != nil {
				return nil, fail
			}
			return runs, nil
		})
		return res.Val, res.Err
	}
	check := func(refresh bool, wantVal, wantRuns int) {
		t.Helper()
		v, err := get(refresh)
		if err != nil || v != wantVal || runs != wantRuns {
			t.Fatalf("got %v, %v after %d runs; want %d after %d", v, err, runs, wantVal, wantRuns)
		}
	}

	check(false, 1, 1) // miss
	check(false, 1, 1) // hit
	now = now.Add(diagnosticsTTL - time.Second)
	check(false, 1, 1) // still fresh
	now = now.Add(time.Second)
	check(false, 2, 2) // expired
	check(true, 3, 3)  // forced
	check(false, 3, 3) // the forced result is cached

	fail = errors.New("boom")
	if _, err := get(true); err == nil {
		t.Fatal("expected an error")
	}
	if v, err := get(false); err != nil || v != 3 {
		t.Fatalf("a failed refresh keeps the old report: %v, %v", v, err)
	}
	now = now.Add(diagnosticsTTL)
	if _, err := get(false); err == nil {
		t.Fatal("expected an error after expiry")
	}
	fail = nil
	check(false, 6, 6) // the error was not cached
}

func TestDiagnosticsEndpointCachesUntilRefreshed(t *testing.T) {
	var runs atomic.Int32
	s := diagnosticsServer(t, LocalAPI{Diagnostics: func(context.Context) doctor.Report {
		runs.Add(1)
		return sampleReport()
	}})
	get := func(path string) {
		t.Helper()
		if w := request(t, s, "GET", path, "", "", "", "Bearer "+s.launch, ""); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	get("/api/diagnostics")
	get("/api/diagnostics")
	if runs.Load() != 1 {
		t.Fatalf("ran %d times, want 1", runs.Load())
	}
	get("/api/diagnostics?refresh=1")
	get("/api/diagnostics")
	if runs.Load() != 2 {
		t.Fatalf("ran %d times, want 2", runs.Load())
	}
}
