package uiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/workstats"
)

func debugServer(t *testing.T) *Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET /api/debug/stats", debugStats())
	mux.Handle("GET /api/debug/pprof/", debugProfiles())
	s, err := New(Options{API: mux})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Listen(0); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		stop()
		<-done
	})
	return s
}

func TestDebugRoutesNeedTheCredential(t *testing.T) {
	s := debugServer(t)
	for _, path := range []string{"/api/debug/stats", "/api/debug/pprof/", "/api/debug/pprof/heap"} {
		if w := request(t, s, "GET", path, "", "", "", "", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without a credential: %d", path, w.Code)
		}
		if w := request(t, s, "GET", path, "", "", "", "Bearer wrong", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s with a wrong token: %d", path, w.Code)
		}
	}
}

func TestDebugStats(t *testing.T) {
	s := debugServer(t)
	workstats.Inc("test.debug_stats")
	w := request(t, s, "GET", "/api/debug/stats", "", "", "", "Bearer "+s.launch, "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var snapshot workstats.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Counters["test.debug_stats"] < 1 || snapshot.Process.Goroutines == 0 {
		t.Errorf("snapshot = %+v", snapshot)
	}
}

func TestDebugProfiles(t *testing.T) {
	s := debugServer(t)
	auth := "Bearer " + s.launch
	if w := request(t, s, "GET", "/api/debug/pprof/", "", "", "", auth, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "goroutine") {
		t.Errorf("index: %d %.200s", w.Code, w.Body.String())
	}
	if w := request(t, s, "GET", "/api/debug/pprof/goroutine?debug=1", "", "", "", auth, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "goroutine profile:") {
		t.Errorf("goroutine: %d %.200s", w.Code, w.Body.String())
	}
	if w := request(t, s, "GET", "/api/debug/pprof/heap", "", "", "", auth, ""); w.Code != http.StatusOK || w.Body.Len() == 0 {
		t.Errorf("heap: %d", w.Code)
	}
	if w := request(t, s, "GET", "/api/debug/pprof/profile?seconds=1", "", "", "", auth, ""); w.Code != http.StatusOK || w.Body.Len() == 0 {
		t.Errorf("cpu profile: %d %.200s", w.Code, w.Body.String())
	}
	// The command line can carry paths and tokens; it is not served.
	if w := request(t, s, "GET", "/api/debug/pprof/cmdline", "", "", "", auth, ""); w.Code != http.StatusNotFound {
		t.Errorf("cmdline: %d", w.Code)
	}
}
