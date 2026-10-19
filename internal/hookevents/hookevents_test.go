//go:build !windows

package hookevents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/uiserver"
)

// testPaths are short private paths: Unix socket paths are limited to about
// 100 bytes, which t.TempDir() can exceed.
func testPaths(t *testing.T) daemon.Paths {
	t.Helper()
	dir, err := os.MkdirTemp("", "he")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return daemon.Paths{State: filepath.Join(dir, "s"), Socket: filepath.Join(dir, "d.sock")}
}

// testDaemon is a daemon running in this process.
type testDaemon struct {
	service *Service
	done    chan struct{}
	cancel  context.CancelFunc
	err     error
}

// runDaemon runs the real daemon in this process, as a spawned one would
// run; with events it serves the hook event service as the command line's
// daemon does. It returns once the application is built or Run failed.
func runDaemon(paths daemon.Paths, version string, events bool) *testDaemon {
	ctx, cancel := context.WithCancel(context.Background())
	d := &testDaemon{done: make(chan struct{}), cancel: cancel}
	ready := make(chan *Service, 1)
	go func() {
		defer close(d.done)
		d.err = daemon.Run(ctx, daemon.Options{Paths: paths, Version: version, Start: func(context.Context, *uiserver.Server) (daemon.App, error) {
			if !events {
				ready <- nil
				return daemon.App{}, nil
			}
			service, err := Open(paths, nil)
			if err != nil {
				return daemon.App{}, err
			}
			ready <- service
			return daemon.App{Methods: service.Methods(), Run: service.Run, Close: func() { _ = service.Close() }}, nil
		}})
	}()
	select {
	case d.service = <-ready:
	case <-d.done:
	}
	return d
}

// startDaemon runs a daemon until the test ends and waits until it serves.
func startDaemon(t *testing.T, paths daemon.Paths, version string, events bool) *testDaemon {
	t.Helper()
	d := runDaemon(paths, version, events)
	t.Cleanup(func() {
		d.stop()
		if d.err != nil {
			t.Error(d.err)
		}
	})
	waitFor(t, func() bool {
		_, err := (&daemon.Client{Paths: paths}).Hello(context.Background())
		return err == nil
	})
	return d
}

func (d *testDaemon) stop() {
	d.cancel()
	<-d.done
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 10s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func testEvent(name string) Event {
	e := New(KindAgent, "claude-code", name, "/work/demo", "v0.2.0")
	e.Session = &Session{ID: "session-1", TranscriptPath: "/home/user/.claude/projects/demo/session-1.jsonl", Cwd: "/work/demo"}
	return e
}

func spooledEvents(t *testing.T, paths daemon.Paths) []Event {
	t.Helper()
	spool := &Spool{Dir: SpoolDir(paths)}
	var events []Event
	for _, entry := range spool.entries() {
		data, err := os.ReadFile(filepath.Join(spool.Dir, entry.name))
		if err != nil {
			t.Fatal(err)
		}
		var e Event
		if err = json.Unmarshal(data, &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	return events
}

func recorded(t *testing.T, service *Service) []Record {
	t.Helper()
	records, err := service.Store.Recent(0)
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func noSpawn(daemon.Paths, daemon.StartOptions) (<-chan struct{}, error) {
	return nil, errors.New("test: no daemon")
}

func TestSenderDeliversToTheRunningDaemon(t *testing.T) {
	paths := testPaths(t)
	d := startDaemon(t, paths, "v0.2.0", true)
	sender := &Sender{Paths: paths, Version: "v0.2.0", Spawn: noSpawn}
	e := testEvent("stop")
	if outcome := sender.Send(context.Background(), e); outcome != Delivered {
		t.Fatalf("outcome = %v, want Delivered", outcome)
	}
	// The same event again (a late answer and the spool can both deliver
	// one) is recorded once.
	if outcome := sender.Send(context.Background(), e); outcome != Delivered {
		t.Fatalf("second outcome = %v", outcome)
	}
	records := recorded(t, d.service)
	if len(records) != 1 || records[0].Event.ID != e.ID || records[0].Via != ViaSocket || records[0].Event.Session.ID != "session-1" {
		t.Fatalf("records = %+v", records)
	}
	if spooled := spooledEvents(t, paths); len(spooled) != 0 {
		t.Errorf("spooled %d events", len(spooled))
	}
}

// A hook of a later release may add fields; this daemon records what it
// knows and ignores the rest.
func TestDaemonIgnoresUnknownEventFields(t *testing.T) {
	paths := testPaths(t)
	d := startDaemon(t, paths, "v0.2.0", true)
	params := map[string]any{"v": 1, "id": NewID(), "time": time.Now(), "kind": "agent", "agent": "codex", "name": "stop", "mood": "cheerful", "session": map[string]any{"id": "s", "colour": "blue"}}
	response, err := (&daemon.Client{Paths: paths}).Call(context.Background(), Method, params)
	if err != nil || !response.OK {
		t.Fatalf("response = %+v, %v", response, err)
	}
	if records := recorded(t, d.service); len(records) != 1 || records[0].Event.Session.ID != "s" {
		t.Fatalf("records = %+v", records)
	}
}

// An event of a newer format is refused with its own code, and the hook
// spools it for a daemon that reads it.
func TestNewerEventVersionIsSpooledNotLost(t *testing.T) {
	paths := testPaths(t)
	d := startDaemon(t, paths, "v0.2.0", true)
	e := testEvent("stop")
	e.V = Version + 1
	response, err := (&daemon.Client{Paths: paths}).Call(context.Background(), Method, e)
	if err != nil || response.OK || response.Error == nil || response.Error.Code != CodeEventVersion {
		t.Fatalf("response = %+v, %v", response, err)
	}
	sender := &Sender{Paths: paths, Version: "v0.2.0", Spawn: noSpawn}
	if outcome := sender.Send(context.Background(), e); outcome != Spooled {
		t.Fatalf("outcome = %v, want Spooled", outcome)
	}
	// This daemon leaves it in the spool for a newer one.
	d.service.Drain()
	if spooled := spooledEvents(t, paths); len(spooled) != 1 || spooled[0].ID != e.ID {
		t.Fatalf("spool = %+v", spooled)
	}
	if records := recorded(t, d.service); len(records) != 0 {
		t.Fatalf("records = %+v", records)
	}
}

// With no daemon running, the hook spools the event and starts one without
// waiting for it; the new daemon drains the spool when it starts.
func TestSenderStartsTheDaemonWhichDrainsTheSpool(t *testing.T) {
	paths := testPaths(t)
	var started *testDaemon
	var mu sync.Mutex
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if started != nil {
			started.stop()
		}
	})
	spawn := func(p daemon.Paths, _ daemon.StartOptions) (<-chan struct{}, error) {
		// Started in the background, as a detached process would be.
		go func() {
			d := runDaemon(p, "v0.2.0", true)
			mu.Lock()
			started = d
			mu.Unlock()
		}()
		return nil, nil
	}
	sender := &Sender{Paths: paths, Version: "v0.2.0", Spawn: spawn}
	e := testEvent("session-start")
	if outcome := sender.Send(context.Background(), e); outcome != Spooled {
		t.Fatalf("outcome = %v, want Spooled", outcome)
	}
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		if started == nil {
			return false
		}
		records, _ := started.service.Store.Recent(0)
		return len(records) == 1 && records[0].Event.ID == e.ID && records[0].Via == ViaSpool
	})
	if spooled := spooledEvents(t, paths); len(spooled) != 0 {
		t.Errorf("the spool still holds %d events", len(spooled))
	}
	// The next event goes straight to it.
	if outcome := sender.Send(context.Background(), testEvent("stop")); outcome != Delivered {
		t.Fatalf("outcome = %v, want Delivered", outcome)
	}
}

// When the daemon cannot be started, the event stays in the spool, and the
// next daemon that starts records it.
func TestSpooledEventsAreDrainedOnTheNextStart(t *testing.T) {
	paths := testPaths(t)
	sender := &Sender{Paths: paths, Version: "v0.2.0", Spawn: noSpawn}
	first, second := testEvent("session-start"), testEvent("stop")
	for _, e := range []Event{first, second} {
		if outcome := sender.Send(context.Background(), e); outcome != Spooled {
			t.Fatalf("outcome = %v, want Spooled", outcome)
		}
	}
	log, err := os.ReadFile(HookLogFile(paths))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "hook event spooled") || !strings.Contains(string(log), "baum daemon not started") {
		t.Errorf("hook log = %s", log)
	}
	d := startDaemon(t, paths, "v0.2.0", true)
	waitFor(t, func() bool { return len(recorded(t, d.service)) == 2 })
	records := recorded(t, d.service)
	if records[0].Event.ID != first.ID || records[1].Event.ID != second.ID || records[0].Via != ViaSpool {
		t.Fatalf("records = %+v", records)
	}
	if spooled := spooledEvents(t, paths); len(spooled) != 0 {
		t.Errorf("the spool still holds %d events", len(spooled))
	}
}

// A daemon that accepts the connection but never answers costs the hook at
// most the budget; the event is spooled and no second daemon is started.
func TestSenderStaysWithinBudgetWhenTheDaemonHangs(t *testing.T) {
	paths := testPaths(t)
	if err := os.MkdirAll(paths.State, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Read the request and never answer.
			go func() { _, _ = conn.Read(make([]byte, 1<<16)) }()
		}
	}()
	var spawned atomic.Int32
	sender := &Sender{Paths: paths, Version: "v0.2.0", Spawn: func(daemon.Paths, daemon.StartOptions) (<-chan struct{}, error) {
		spawned.Add(1)
		return nil, errors.New("test: no daemon")
	}}
	start := time.Now()
	outcome := sender.Send(context.Background(), testEvent("stop"))
	if elapsed := time.Since(start); elapsed > Budget {
		t.Errorf("Send took %s, over the %s budget", elapsed, Budget)
	}
	if outcome != Spooled || len(spooledEvents(t, paths)) != 1 {
		t.Fatalf("outcome = %v, spool = %d", outcome, len(spooledEvents(t, paths)))
	}
	if spawned.Load() != 0 {
		t.Error("a daemon was started beside one that holds the socket")
	}
}

// A daemon from before hook events answers that it does not know the method:
// the event waits in the spool, and the daemon, older than the hook's baum, is
// asked to restart as it once idle.
func TestOlderDaemonIsAskedToRestartAndTheEventSpooled(t *testing.T) {
	paths := testPaths(t)
	d := startDaemon(t, paths, "v0.1.0", false)
	sender := &Sender{Paths: paths, Version: "v0.2.0", Spawn: noSpawn, Executable: "/usr/local/bin/baum"}
	if outcome := sender.Send(context.Background(), testEvent("stop")); outcome != Spooled {
		t.Fatalf("outcome = %v, want Spooled", outcome)
	}
	select {
	case <-d.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the idle older daemon did not stop to be replaced")
	}
	if len(spooledEvents(t, paths)) != 1 {
		t.Fatal("the event was not spooled")
	}
}

func TestSpoolDropsTheOldestEventsBeyondItsBounds(t *testing.T) {
	paths := testPaths(t)
	var log bytes.Buffer
	spool := &Spool{Dir: SpoolDir(paths), MaxEvents: 3, Log: slog.New(slog.NewTextHandler(&log, nil))}
	var ids []string
	for range 5 {
		e := testEvent("stop")
		ids = append(ids, e.ID)
		if err := spool.Put(e); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	var kept []string
	for _, e := range spooledEvents(t, paths) {
		kept = append(kept, e.ID)
	}
	if strings.Join(kept, ",") != strings.Join(ids[2:], ",") {
		t.Errorf("kept %v, want the newest three %v", kept, ids[2:])
	}
	if n := strings.Count(log.String(), "hook event spool full; dropped the oldest event"); n != 2 {
		t.Errorf("logged %d drops, want 2:\n%s", n, log.String())
	}
	if strings.Contains(log.String(), "session-1") {
		t.Error("the log names event contents")
	}

	// The byte bound too: room for two events.
	data, _ := json.Marshal(testEvent("stop"))
	bySize := &Spool{Dir: filepath.Join(paths.State, "by-size"), MaxBytes: int64(2*len(data) + 10)}
	for range 4 {
		if err := bySize.Put(testEvent("stop")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if n := len(bySize.entries()); n != 2 {
		t.Errorf("the byte-bounded spool holds %d events, want 2", n)
	}
}

func TestDrainDiscardsWhatIsNotAPrivateEvent(t *testing.T) {
	paths := testPaths(t)
	var log bytes.Buffer
	spool := &Spool{Dir: SpoolDir(paths), Log: slog.New(slog.NewTextHandler(&log, nil))}
	good := testEvent("stop")
	if err := spool.Put(good); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(spool.Dir, "00000000000000000001-"+NewID()+".json")
	if err := os.WriteFile(junk, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(spool.Dir, "00000000000000000002-"+NewID()+".json")
	data, _ := json.Marshal(testEvent("stop"))
	if err := os.WriteFile(shared, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var drained []string
	if err := spool.Drain(func(e Event) error { drained = append(drained, e.ID); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(drained) != 1 || drained[0] != good.ID {
		t.Errorf("drained %v", drained)
	}
	for _, path := range []string{junk, shared} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s was left in the spool", filepath.Base(path))
		}
	}
	if !strings.Contains(log.String(), "not a hook event") || !strings.Contains(log.String(), "not a small private file") {
		t.Errorf("log = %s", log.String())
	}
}

func TestSpoolRecordAndHookLogArePrivate(t *testing.T) {
	paths := testPaths(t)
	sender := &Sender{Paths: paths, Version: "v0.2.0", Spawn: noSpawn}
	if outcome := sender.Send(context.Background(), testEvent("stop")); outcome != Spooled {
		t.Fatalf("outcome = %v", outcome)
	}
	service, err := Open(paths, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	if _, err = service.Store.Add(testEvent("stop"), ViaSocket); err != nil {
		t.Fatal(err)
	}
	spool := SpoolDir(paths)
	entries := (&Spool{Dir: spool}).entries()
	if len(entries) != 1 {
		t.Fatalf("spool holds %d events", len(entries))
	}
	for path, want := range map[string]os.FileMode{
		spool:                                 0o700,
		filepath.Join(spool, entries[0].name): 0o600,
		RecordDir(paths):                      0o700,
		filepath.Join(RecordDir(paths), "events.jsonl"): 0o600,
		HookLogFile(paths): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s: mode %o, want %o", path, got, want)
		}
	}
}

func TestStoreRecordsOnceAcrossRestartsAndNotifiesSubscribers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "events")
	store, err := OpenStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	ends, unsubscribe := store.Subscribe(4, func(e Event) bool { return e.Name == "session-end" })
	stop, end := testEvent("stop"), testEvent("session-end")
	for _, e := range []Event{stop, end, end} {
		if _, err = store.Add(e, ViaSocket); err != nil {
			t.Fatal(err)
		}
	}
	unsubscribe()
	var got []string
	for record := range ends {
		got = append(got, record.Event.ID)
	}
	if len(got) != 1 || got[0] != end.ID {
		t.Errorf("subscriber got %v, want only the session end", got)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if added, err := store.Add(stop, ViaSpool); err != nil || added {
		t.Errorf("a recorded event was recorded again after a restart: %v, %v", added, err)
	}
	records, err := store.Recent(1)
	if err != nil || len(records) != 1 || records[0].Event.ID != end.ID {
		t.Fatalf("Recent(1) = %+v, %v", records, err)
	}
}

func TestEventBoundsKeepItWithinOneControlLine(t *testing.T) {
	e := testEvent("pre-push")
	long := strings.Repeat("é", 10_000)
	e.Dir, e.Session.TranscriptPath = long, long
	e.Git = &Git{Args: []string{long, long, long, long, long, long}}
	for range 1000 {
		e.Git.Lines = append(e.Git.Lines, long)
	}
	e.Bound()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxEventSize {
		t.Errorf("a bounded event is %d bytes", len(data))
	}
	if !json.Valid(data) || !strings.HasPrefix(e.Dir, "é") || len(e.Dir) > maxField {
		t.Errorf("bounding broke a field: %d bytes", len(e.Dir))
	}
}
