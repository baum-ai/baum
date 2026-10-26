package clonefind

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/repos"
)

type fixture struct {
	home     string
	registry *repos.Registry
	service  *Service
	events   chan hookevents.Record
	changes  atomic.Int32
	stop     func()
}

// start runs a service over a temporary home and registry until the test ends.
func start(t *testing.T, h string) *fixture {
	t.Helper()
	registry, err := repos.Open(filepath.Join(t.TempDir(), "registry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	f := &fixture{home: h, registry: registry, events: make(chan hookevents.Record, 8)}
	f.service = &Service{Registry: registry, Options: options(h), Changed: func() { f.changes.Add(1) }, Interval: time.Hour}
	return f
}

func (f *fixture) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.service.Run(ctx, f.events, nil)
	}()
	f.stop = func() {
		cancel()
		<-done
	}
	t.Cleanup(f.stop)
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// scan runs one scan and waits for it: the service scans on its own only
// while a picked repository has no clone.
func (f *fixture) scan(t *testing.T) {
	t.Helper()
	eventually(t, "a scan to start", f.service.Refresh)
	eventually(t, "the scan to end", f.scanned)
}

func (f *fixture) scanned() bool {
	s := f.service.Snapshot()
	return s.ScannedAt != nil && !s.Running
}

func (f *fixture) clones(t *testing.T) []repos.Clone {
	t.Helper()
	list, err := f.registry.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestPickedRepositoryGetsTheCloneFoundInACodeFolder(t *testing.T) {
	h := home(t)
	clone := repo(t, filepath.Join(h, "code", "app"), "origin", gh("acme/app"))
	repo(t, filepath.Join(h, "code", "other"), "origin", gh("acme/other")) // not picked
	f := start(t, h)
	if _, err := f.registry.Pick(context.Background(), "acme/app", nil); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	eventually(t, "the clone to be registered", func() bool { return len(f.clones(t)) == 1 })
	eventually(t, "the scan to end", f.scanned)
	got := f.clones(t)
	if got[0].Repository != "acme/app" || got[0].Path != clone || !got[0].Available {
		t.Fatalf("clones = %+v", got)
	}
	if f.changes.Load() == 0 {
		t.Fatal("the app was not told")
	}
	snapshot := f.service.Snapshot()
	if len(snapshot.Attached) != 1 || snapshot.Attached[0].Repository != "acme/app" || snapshot.Attached[0].Path != clone {
		t.Fatalf("attached = %+v", snapshot.Attached)
	}
	// The unpicked repository is listed as found but was not picked.
	if len(snapshot.Repositories["acme/other"]) != 1 {
		t.Fatalf("found = %+v", snapshot.Repositories)
	}
	picked, err := f.registry.Repositories(context.Background())
	if err != nil || len(picked) != 1 {
		t.Fatalf("picked = %+v, %v", picked, err)
	}
}

func TestNothingIsSearchedOrAttachedForUnpickedRepositories(t *testing.T) {
	h := home(t)
	repo(t, filepath.Join(h, "code", "app"), "origin", gh("acme/app"))
	f := start(t, h)
	f.run(t)
	f.service.Trigger()
	time.Sleep(200 * time.Millisecond)
	if s := f.service.Snapshot(); s.ScannedAt != nil || s.Running || len(s.Repositories) != 0 {
		t.Fatalf("searched with nothing picked: %+v", s)
	}
	f.scan(t)
	f.service.Trigger()
	if n := len(f.clones(t)); n != 0 || f.changes.Load() != 0 {
		t.Fatalf("clones = %d, changes = %d", n, f.changes.Load())
	}
}

func TestPickingARepositoryStartsTheSearch(t *testing.T) {
	h := home(t)
	clone := repo(t, filepath.Join(h, "code", "app"), "origin", gh("acme/app"))
	f := start(t, h)
	f.run(t)
	eventually(t, "the service to start", func() bool { return f.service.spawn(func(context.Context) {}) })
	if f.service.Snapshot().ScannedAt != nil {
		t.Fatal("searched before anything was picked")
	}
	if _, err := f.registry.Pick(context.Background(), "acme/app", nil); err != nil {
		t.Fatal(err)
	}
	f.service.Trigger()
	// The pick reads as being looked for from the moment it is made.
	if !f.service.Snapshot().Running {
		t.Fatal("a just-picked repository was not reported as being looked for")
	}
	eventually(t, "the clone to be registered", func() bool { return len(f.clones(t)) == 1 })
	eventually(t, "the search to end", f.scanned)
	if got := f.clones(t)[0]; got.Path != clone {
		t.Fatalf("clone = %+v", got)
	}
}

func TestForkCloneIsAttachedToThePickedUpstream(t *testing.T) {
	h := home(t)
	repo(t, filepath.Join(h, "code", "fork"), "origin", gh("me/app"), "upstream", gh("acme/app"))
	f := start(t, h)
	if _, err := f.registry.Pick(context.Background(), "acme/app", nil); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	eventually(t, "the clone to be registered", func() bool { return len(f.clones(t)) == 1 })
	got := f.clones(t)[0]
	if got.Repository != "acme/app" || !got.Available {
		t.Fatalf("clone = %+v", got)
	}
	picked, err := f.registry.Repositories(context.Background())
	if err != nil || len(picked) != 1 || picked[0].Repository != "acme/app" {
		t.Fatalf("picked = %+v, %v (the fork must not be picked)", picked, err)
	}
}

func TestAgentEventAttachesANewClone(t *testing.T) {
	h := home(t)
	f := start(t, h)
	if _, err := f.registry.Pick(context.Background(), "acme/app", nil); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	f.scan(t)
	if len(f.clones(t)) != 0 {
		t.Fatal("a clone appeared from nowhere")
	}
	clone := repo(t, filepath.Join(h, "elsewhere", "app"), "origin", gh("acme/app"))
	if err := os.MkdirAll(filepath.Join(clone, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.events <- agentEvent(filepath.Join(clone, "cmd"), time.Now())
	eventually(t, "the clone to be registered", func() bool { return len(f.clones(t)) == 1 })
	eventually(t, "the app to be told", func() bool { return f.changes.Load() > 0 })
	if got := f.clones(t)[0]; got.Path != clone || got.Repository != "acme/app" {
		t.Fatalf("clone = %+v", got)
	}
	if found := f.service.Snapshot().Repositories["acme/app"]; len(found) != 1 || found[0].Source != SourceAgent {
		t.Fatalf("found = %+v", found)
	}
}

func TestAPIServesAndRefreshes(t *testing.T) {
	h := home(t)
	clone := repo(t, filepath.Join(h, "code", "app"), "origin", gh("acme/app"))
	f := start(t, h)
	f.run(t)
	f.scan(t)
	handler := f.service.Handler()

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest("GET", "/api/clones/found", nil))
	var body struct {
		ScannedAt    *time.Time `json:"scanned_at"`
		Running      bool       `json:"running"`
		Repositories map[string][]struct {
			Path         string     `json:"path"`
			CommonDir    string     `json:"common_dir"`
			LastActivity *time.Time `json:"last_activity"`
			Source       string     `json:"source"`
		} `json:"repositories"`
	}
	if get.Code != 200 || json.Unmarshal(get.Body.Bytes(), &body) != nil {
		t.Fatalf("GET = %d %s", get.Code, get.Body)
	}
	got := body.Repositories["acme/app"]
	if body.ScannedAt == nil || body.Running || len(got) != 1 || got[0].Path != clone ||
		got[0].CommonDir != filepath.Join(clone, ".git") || got[0].LastActivity != nil || got[0].Source != SourceScan {
		t.Fatalf("body = %s", get.Body)
	}
	if !strings.Contains(get.Body.String(), `"last_activity":null`) {
		t.Fatalf("body = %s", get.Body)
	}

	first := *body.ScannedAt
	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest("POST", "/api/clones/found/refresh", strings.NewReader("{}")))
	if post.Code != http.StatusAccepted || !strings.Contains(post.Body.String(), `"started":true`) {
		t.Fatalf("POST = %d %s", post.Code, post.Body)
	}
	eventually(t, "a new scan", func() bool {
		s := f.service.Snapshot()
		return s.ScannedAt != nil && s.ScannedAt.After(first) && !s.Running
	})

	for _, bad := range []string{`{"x":1}`, ``, `[]`, `{} {}`} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/clones/found/refresh", strings.NewReader(bad)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %q = %d", bad, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/clones/found/refresh", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET refresh = %d", rec.Code)
	}
}

func TestPackageMakesNoNetworkRequests(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal(files, err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"http.Client", "http.Get", "http.Post", "http.DefaultClient", "http.NewRequest", "net.Dial", "\"net\"", "ssh\"", "RepositoryFromRemote", "githubapi"} {
			if strings.Contains(string(data), banned) {
				t.Errorf("%s uses %s", name, banned)
			}
		}
	}
}

func TestRefreshCanIncludeDocumentsOnce(t *testing.T) {
	h := home(t)
	repo(t, filepath.Join(h, "Documents", "app"), "origin", gh("acme/app"))
	f := start(t, h)
	f.service.Options.GOOS = "darwin"
	f.run(t)
	f.scan(t)
	if len(f.service.Snapshot().Repositories) != 0 {
		t.Fatal("Documents walked without opt-in")
	}
	post := func(body string) {
		rec := httptest.NewRecorder()
		f.service.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/clones/found/refresh", strings.NewReader(body)))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("POST %s = %d", body, rec.Code)
		}
	}
	scannedAfter := func(at time.Time) func() bool {
		return func() bool {
			s := f.service.Snapshot()
			return s.ScannedAt != nil && s.ScannedAt.After(at) && !s.Running
		}
	}
	first := *f.service.Snapshot().ScannedAt
	post(`{"include_documents": true}`)
	eventually(t, "the scan with Documents", scannedAfter(first))
	if len(f.service.Snapshot().Repositories["acme/app"]) != 1 {
		t.Fatal("opt-in did not walk Documents")
	}
	// The opt-in is not kept: a plain refresh leaves Documents out again.
	second := *f.service.Snapshot().ScannedAt
	post(`{}`)
	eventually(t, "the plain scan", scannedAfter(second))
	if len(f.service.Snapshot().Repositories) != 0 {
		t.Fatal("opt-in persisted")
	}
}
