package uiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/doctor"
	"github.com/baum-ai/baum/internal/repos"
)

func diagnosticsServer(t *testing.T, api LocalAPI) *Server {
	t.Helper()
	registry, err := repos.Open(filepath.Join(t.TempDir(), "repos.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	api.Registry = registry
	api.Config = config.Default()
	handler := api.Handler()
	t.Cleanup(handler.Close)
	s, err := New(Options{API: handler})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Listen(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.listener.Close() })
	return s
}

func sampleReport() doctor.Report {
	return doctor.Report{Status: doctor.StatusProblem, Checks: []doctor.Check{
		{ID: "git", Label: "git", Status: doctor.StatusOK, Detail: "found", Path: "/usr/bin/git", Version: "2.43.0"},
		{ID: "gh", Label: "GitHub CLI", Status: doctor.StatusProblem, Detail: "not found", Fix: "Install gh."},
	}}
}

func TestDiagnosticsReturnsReportAndCapabilities(t *testing.T) {
	s := diagnosticsServer(t, LocalAPI{Diagnostics: func(context.Context) doctor.Report { return sampleReport() }})
	w := request(t, s, "GET", "/api/diagnostics", "", "", "", "Bearer "+s.launch, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct {
		Local  bool           `json:"local"`
		Runner bool           `json:"generation_runner"`
		Report *doctor.Report `json:"report"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Local || body.Runner || body.Report == nil || body.Report.Status != doctor.StatusProblem || len(body.Report.Checks) != 2 || body.Report.Checks[1].Fix != "Install gh." {
		t.Fatalf("%s", w.Body.String())
	}
}

func TestDiagnosticsRequiresAuthenticationAndGET(t *testing.T) {
	var runs atomic.Int32
	s := diagnosticsServer(t, LocalAPI{Diagnostics: func(context.Context) doctor.Report { runs.Add(1); return sampleReport() }})
	if w := request(t, s, "GET", "/api/diagnostics", "", "", "", "", ""); w.Code == 200 || runs.Load() != 0 {
		t.Fatalf("unauthenticated request got %d, runs %d", w.Code, runs.Load())
	}
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		w := request(t, s, method, "/api/diagnostics", "", "", "application/json", "Bearer "+s.launch, "{}")
		if w.Code == 200 || runs.Load() != 0 {
			t.Fatalf("%s got %d, runs %d", method, w.Code, runs.Load())
		}
		// Past the server's Origin and content-type checks, the route itself
		// refuses the method.
		w = request(t, s, method, "/api/diagnostics", "", s.Origin(), "application/json", "Bearer "+s.launch, "{}")
		if w.Code != http.StatusMethodNotAllowed || runs.Load() != 0 {
			t.Fatalf("%s with a valid Origin got %d, runs %d", method, w.Code, runs.Load())
		}
	}
}

func TestDiagnosticsWithoutProviderHasNullReport(t *testing.T) {
	s := diagnosticsServer(t, LocalAPI{})
	w := request(t, s, "GET", "/api/diagnostics", "", "", "", "Bearer "+s.launch, "")
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
		t.Fatal(w.Code, err)
	}
	if v, ok := body["report"]; !ok || v != nil || body["local"] != true {
		t.Fatalf("%v", body)
	}
}

func TestDiagnosticsConcurrentRequestsShareOneRun(t *testing.T) {
	var runs atomic.Int32
	release := make(chan struct{})
	s := diagnosticsServer(t, LocalAPI{Diagnostics: func(context.Context) doctor.Report {
		runs.Add(1)
		<-release
		return sampleReport()
	}})
	var wg sync.WaitGroup
	codes := make([]int, 5)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = request(t, s, "GET", "/api/diagnostics", "", "", "", "Bearer "+s.launch, "").Code
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if runs.Load() != 1 {
		t.Fatalf("provider ran %d times", runs.Load())
	}
	for _, c := range codes {
		if c != 200 {
			t.Fatal(codes)
		}
	}
}

func TestDiagnosticsDisconnectDoesNotCancelSharedRun(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	waiting := make(chan struct{}, 2)
	var entries atomic.Int32
	var cancelled atomic.Bool
	api := LocalAPI{Diagnostics: func(ctx context.Context) doctor.Report {
		entries.Add(1)
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			cancelled.Store(true)
		}
		return sampleReport()
	}}
	api.diagnosticsWaiting = func() { waiting <- struct{}{} }
	s := diagnosticsServer(t, api)
	gone, hangUp := context.WithCancel(context.Background())
	first := httptest.NewRequest("GET", s.Origin()+"/api/diagnostics", nil).WithContext(gone)
	first.Header.Set("Authorization", "Bearer "+s.launch)
	firstDone := make(chan struct{})
	go func() { defer close(firstDone); s.Handler().ServeHTTP(httptest.NewRecorder(), first) }()
	<-started
	<-waiting
	second := make(chan int, 1)
	go func() { second <- request(t, s, "GET", "/api/diagnostics", "", "", "", "Bearer "+s.launch, "").Code }()
	<-waiting // the second request has joined the run in flight
	hangUp()
	<-firstDone
	if cancelled.Load() {
		t.Fatal("the first client's disconnect cancelled the shared run")
	}
	close(release)
	if code := <-second; code != 200 || cancelled.Load() || entries.Load() != 1 {
		t.Fatalf("second waiter got %d, run cancelled %v, provider entered %d times", code, cancelled.Load(), entries.Load())
	}
}

func TestDiagnosticsTimeoutIsEnforced(t *testing.T) {
	s := diagnosticsServer(t, LocalAPI{DiagnosticsTimeout: 50 * time.Millisecond, Diagnostics: func(ctx context.Context) doctor.Report {
		<-ctx.Done()
		time.Sleep(time.Second) // a provider that is slow to notice
		return sampleReport()
	}})
	start := time.Now()
	w := request(t, s, "GET", "/api/diagnostics", "", "", "", "Bearer "+s.launch, "")
	if w.Code != http.StatusGatewayTimeout || time.Since(start) > 800*time.Millisecond {
		t.Fatalf("%d after %s: %s", w.Code, time.Since(start), w.Body.String())
	}
}

func TestDiagnosticsResponseHasNoPlantedSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	const token, key = "ghp_PLANTEDFAKETOKEN0000000000000000", "sk-ant-PLANTEDFAKEKEY00000000000"
	home, bin := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n--version) echo 'gh version 2.50.0 (2026-01-01)';;\nauth) echo " + token + ";;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("GH_CONFIG_DIR", t.TempDir())
	t.Setenv("PATH", bin)
	for _, name := range []string{"GH_PATH", "GITHUB_TOKEN", "GH_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		t.Setenv(name, "")
	}
	t.Setenv("ANTHROPIC_API_KEY", key)
	s := diagnosticsServer(t, LocalAPI{Diagnostics: func(ctx context.Context) doctor.Report { return doctor.Run(ctx, doctor.Options{}) }})
	w := request(t, s, "GET", "/api/diagnostics", "", "", "", "Bearer "+s.launch, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct {
		Report doctor.Report `json:"report"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var tokenCheck *doctor.Check
	for i, c := range body.Report.Checks {
		if strings.HasPrefix(c.ID, "github-token") {
			tokenCheck = &body.Report.Checks[i]
		}
	}
	if tokenCheck == nil || tokenCheck.Status != doctor.StatusWarning || tokenCheck.Source != "" {
		t.Fatalf("token check %+v in %s", tokenCheck, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "PLANTED") {
		t.Fatalf("secret in response: %s", w.Body.String())
	}
}
