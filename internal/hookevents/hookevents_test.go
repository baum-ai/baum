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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/uiserver"
)

// TestMain clears what keeps a hook from starting a daemon, which CI sets;
// the tests that need it set it themselves.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("CI")
	_ = os.Unsetenv(NoDaemonEnv)
	os.Exit(m.Run())
}

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
	return runDaemonWith(paths, version, events, nil)
}

// runDaemonWith is runDaemon with a Respawn, which a restart as a newer
// version calls.
func runDaemonWith(paths daemon.Paths, version string, events bool, respawn func(string, daemon.Status) error) *testDaemon {
	ctx, cancel := context.WithCancel(context.Background())
	d := &testDaemon{done: make(chan struct{}), cancel: cancel}
	ready := make(chan *Service, 1)
	go func() {
		defer close(d.done)
		d.err = daemon.Run(ctx, daemon.Options{Paths: paths, Version: version, Respawn: respawn, Start: func(context.Context, *uiserver.Server) (daemon.App, error) {
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
	return startDaemonWith(t, paths, version, events, nil)
}

func startDaemonWith(t *testing.T, paths daemon.Paths, version string, events bool, respawn func(string, daemon.Status) error) *testDaemon {
	t.Helper()
	d := runDaemonWith(paths, version, events, respawn)
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
// the event waits in the spool, and the daemon, older than the hook's baum,
// restarts as it although idle, so the newer daemon starts without anyone
// waiting for the old one to exit.
func TestOlderDaemonRestartsAsTheHooksVersionAndTheEventSpooled(t *testing.T) {
	paths := testPaths(t)
	respawned := make(chan string, 1)
	d := startDaemonWith(t, paths, "v0.1.0", false, func(executable string, _ daemon.Status) error {
		respawned <- executable
		return nil
	})
	sender := &Sender{Paths: paths, Version: "v0.2.0", Spawn: noSpawn, Executable: "/usr/local/bin/baum"}
	if outcome := sender.Send(context.Background(), testEvent("stop")); outcome != Spooled {
		t.Fatalf("outcome = %v, want Spooled", outcome)
	}
	select {
	case executable := <-respawned:
		if executable != "/usr/local/bin/baum" {
			t.Errorf("respawned %q", executable)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the idle older daemon did not restart as the hook's version")
	}
	select {
	case <-d.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the older daemon did not stop after starting its successor")
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

func TestEventBoundsKeepInvalidBytesAndEscapedCharactersWithinTheLine(t *testing.T) {
	e := testEvent("stop")
	// Invalid bytes become six-byte escapes and <, > and & are escaped too:
	// the encoding, not the byte length, has to fit.
	e.Dir = "\xff" + strings.Repeat("<>&\x01", 600)
	e.Session.TranscriptPath = strings.Repeat("\xfe", 2000)
	e.Session.Cwd = "abc\xffdéf"
	e.Bound()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxEventSize {
		t.Errorf("a bounded event encodes to %d bytes", len(data))
	}
	if e.Session.Cwd != "abc\xffdéf" {
		t.Errorf("a short field with an invalid byte was changed: %q", e.Session.Cwd)
	}
	if !strings.HasPrefix(e.Dir, "\xff<>&") {
		t.Errorf("the field was cut at its first invalid byte: %q", e.Dir[:min(10, len(e.Dir))])
	}
	if got := cut("aé", 2); got != "a" {
		t.Errorf("cut inside a character = %q", got)
	}
	if got := cut("a\xffb", 2); got != "a\xff" {
		t.Errorf("cut after an invalid byte = %q", got)
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

// The spool can pass its bound while hooks write at once; the next Put
// restores it.
func TestConcurrentSpoolWritersAreBoundedByTheNextPut(t *testing.T) {
	paths := testPaths(t)
	spool := &Spool{Dir: SpoolDir(paths), MaxEvents: 5}
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			if err := (&Spool{Dir: spool.Dir, MaxEvents: 5}).Put(testEvent("stop")); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := spool.Put(testEvent("stop")); err != nil {
		t.Fatal(err)
	}
	if n := len(spool.entries()); n > 5 {
		t.Errorf("the spool holds %d events after a Put, over its bound of 5", n)
	}
}

func TestDrainForgetsNewerEventsNoLongerSpooled(t *testing.T) {
	paths := testPaths(t)
	spool := &Spool{Dir: SpoolDir(paths)}
	newer := testEvent("stop")
	newer.V = Version + 1
	if err := spool.Put(newer); err != nil {
		t.Fatal(err)
	}
	record := func(Event) error { return nil }
	if err := spool.Drain(record); err != nil || len(spool.newer) != 1 {
		t.Fatalf("drain: %v, newer %v", err, spool.newer)
	}
	for _, entry := range spool.entries() {
		_ = os.Remove(filepath.Join(spool.Dir, entry.name))
	}
	if err := spool.Drain(record); err != nil || len(spool.newer) != 0 {
		t.Fatalf("drain: %v, newer %v", err, spool.newer)
	}
}

// A damaged record loses only its damaged lines: the daemon still opens it.
func TestStoreSkipsDamagedRecordLines(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "events")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	good, _ := json.Marshal(Record{Received: time.Now(), Via: ViaSocket, Event: testEvent("stop")})
	content := "not json\n" + strings.Repeat("x", 3*maxRecordLine) + "\n" + string(good) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	records, err := store.Recent(0)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %+v, %v", records, err)
	}
}

// Whatever the file system or a daemon start does, Send returns within its
// budget.
func TestSendReturnsWithinBudgetWhenSpoolingOrStartingStalls(t *testing.T) {
	t.Run("slow spool", func(t *testing.T) {
		paths := testPaths(t)
		sender := &Sender{Paths: paths, Version: "v0.2.0", Spawn: noSpawn, beforeSpool: func() { time.Sleep(2 * Budget) }}
		start := time.Now()
		outcome := sender.Send(context.Background(), testEvent("stop"))
		if elapsed := time.Since(start); elapsed > Budget+50*time.Millisecond {
			t.Errorf("Send took %s", elapsed)
		}
		if outcome != Unfinished {
			t.Errorf("outcome = %v, want Unfinished", outcome)
		}
	})
	t.Run("start that never serves", func(t *testing.T) {
		paths := testPaths(t)
		spawned := make(chan struct{}, 1)
		sender := &Sender{Paths: paths, Version: "v0.2.0", Spawn: func(daemon.Paths, daemon.StartOptions) (<-chan struct{}, error) {
			spawned <- struct{}{}
			// A process that never takes the lock, and a slow exec.
			time.Sleep(2 * Budget)
			return nil, nil
		}}
		start := time.Now()
		sender.Send(context.Background(), testEvent("stop"))
		if elapsed := time.Since(start); elapsed > Budget+50*time.Millisecond {
			t.Errorf("Send took %s", elapsed)
		}
		select {
		case <-spawned:
		default:
			t.Error("no daemon was started")
		}
		if len(spooledEvents(t, paths)) != 1 {
			t.Error("the event was not spooled")
		}
	})
}

// A daemon that was started but has not served is not started again by every
// hook; once one served, the next hook may start one at once.
func TestHooksBackOffFromADaemonThatDoesNotStart(t *testing.T) {
	paths := testPaths(t)
	var spawns atomic.Int32
	var log bytes.Buffer
	sender := &Sender{Paths: paths, Version: "v0.2.0", Log: slog.New(slog.NewTextHandler(&log, nil)), Spawn: func(daemon.Paths, daemon.StartOptions) (<-chan struct{}, error) {
		spawns.Add(1)
		return nil, nil
	}}
	for range 3 {
		sender.Send(context.Background(), testEvent("stop"))
	}
	if n := spawns.Load(); n != 1 {
		t.Fatalf("started %d daemons, want 1", n)
	}
	if !strings.Contains(log.String(), "started moments ago") {
		t.Errorf("log = %s", log.String())
	}
	// A daemon serves, then stops.
	d := startDaemon(t, paths, "v0.2.0", true)
	d.stop()
	sender.Send(context.Background(), testEvent("stop"))
	if n := spawns.Load(); n != 2 {
		t.Fatalf("started %d daemons after one served, want 2", n)
	}
}

// Hooks do not start a daemon the user stopped, nor where the environment
// opts out; the events still wait in the spool.
func TestHooksDoNotStartADaemonTheUserOrEnvironmentRulesOut(t *testing.T) {
	cases := map[string]func(t *testing.T, paths daemon.Paths){
		"stopped by the user": func(t *testing.T, paths daemon.Paths) {
			if err := daemon.MarkStopped(paths); err != nil {
				t.Fatal(err)
			}
		},
		NoDaemonEnv: func(t *testing.T, _ daemon.Paths) { t.Setenv(NoDaemonEnv, "1") },
		"CI":        func(t *testing.T, _ daemon.Paths) { t.Setenv("CI", "true") },
	}
	for name, setUp := range cases {
		t.Run(name, func(t *testing.T) {
			paths := testPaths(t)
			if err := os.MkdirAll(paths.State, 0o700); err != nil {
				t.Fatal(err)
			}
			setUp(t, paths)
			var spawns atomic.Int32
			sender := &Sender{Paths: paths, Version: "v0.2.0", Spawn: func(daemon.Paths, daemon.StartOptions) (<-chan struct{}, error) {
				spawns.Add(1)
				return nil, nil
			}}
			if outcome := sender.Send(context.Background(), testEvent("stop")); outcome != Spooled {
				t.Fatalf("outcome = %v", outcome)
			}
			if spawns.Load() != 0 {
				t.Error("a daemon was started")
			}
		})
	}
}

// A hook starts the daemon without its TMPDIR, which an agent sandbox points
// at a directory that lasts one session.
func TestHooksStartTheDaemonWithoutTheirTMPDIR(t *testing.T) {
	paths := testPaths(t)
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("BAUM_HOOK_TEST_MARK", "kept")
	var env []string
	sender := &Sender{Paths: paths, Version: "v0.2.0", Spawn: func(_ daemon.Paths, start daemon.StartOptions) (<-chan struct{}, error) {
		env = start.Env
		return nil, nil
	}}
	sender.Send(context.Background(), testEvent("stop"))
	if !slices.Contains(env, "BAUM_HOOK_TEST_MARK=kept") {
		t.Fatalf("the daemon's environment lacks the hook's: %d entries", len(env))
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, "TMPDIR=") {
			t.Errorf("the daemon inherits %s", entry)
		}
	}
}
