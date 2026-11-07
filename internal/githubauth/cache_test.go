package githubauth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingStore counts Load calls and can hold them open. Its credential is
// replaced with set, as another process replacing the keyring item would.
type countingStore struct {
	mu      sync.Mutex
	c       Credential
	loads   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (s *countingStore) Load(ctx context.Context) (Credential, error) {
	s.loads.Add(1)
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return Credential{}, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.c.AccessToken == "" {
		return Credential{}, ErrNotConnected
	}
	return s.c, nil
}
func (s *countingStore) Save(_ context.Context, c Credential) error { s.set(c); return nil }
func (s *countingStore) Delete(context.Context) error               { s.set(Credential{}); return nil }
func (s *countingStore) set(c Credential) {
	s.mu.Lock()
	s.c = c
	s.mu.Unlock()
}

func cachedManager(c Credential) (*Manager, *countingStore, *time.Time) {
	store := &countingStore{c: c}
	m := New(store)
	now := time.Unix(10000, 0)
	m.now = func() time.Time { return now }
	return m, store, &now
}

func mustToken(t *testing.T, m *Manager, want string) {
	t.Helper()
	got, err := m.Token(context.Background())
	if err != nil || got != want {
		t.Errorf("Token = %q, %v; want %q", got, err, want)
	}
}

func TestConcurrentTokenCallsLaunchTheStoreOnce(t *testing.T) {
	m, store, _ := cachedManager(Credential{AccessToken: "tok", Login: "alice", ID: 1})
	store.entered = make(chan struct{}, 1)
	store.release = make(chan struct{})
	var wg sync.WaitGroup
	for range 48 {
		wg.Go(func() { mustToken(t, m, "tok") })
	}
	<-store.entered
	time.Sleep(20 * time.Millisecond)
	close(store.release)
	wg.Wait()
	if n := store.loads.Load(); n != 1 {
		t.Fatalf("store loaded %d times for concurrent misses, want 1", n)
	}
	for range 10 {
		mustToken(t, m, "tok")
	}
	if n := store.loads.Load(); n != 1 {
		t.Fatalf("store loaded %d times after warm hits, want 1", n)
	}
	if id := m.AccountForToken("tok"); id != 1 {
		t.Fatalf("hit path lost the issued account: %d", id)
	}
}

func TestStatusDropsTheCacheWhenTheCredentialWasDeletedElsewhere(t *testing.T) {
	m, store, _ := cachedManager(Credential{AccessToken: "tok", Login: "alice", ID: 1})
	mustToken(t, m, "tok")
	store.set(Credential{})
	if st, err := m.Status(context.Background()); err != nil || st.State != "disconnected" {
		t.Fatal(st, err)
	}
	if _, err := m.Token(context.Background()); err == nil {
		t.Fatal("deleted credential still served")
	}
}

func TestFailedRefreshSaveDropsTheCache(t *testing.T) {
	m, s, now, reply, _ := fixture(t)
	s.c = Credential{AccessToken: "old", RefreshToken: "r", RefreshExpiresAt: now.Unix() + 7200, ExpiresAt: now.Unix() + 600, ID: 1, Login: "octocat"}
	mustToken(t, m, "old")
	*now = now.Add(9*time.Minute + 30*time.Second)
	*reply = `{"access_token":"new","token_type":"bearer","expires_in":3600,"refresh_token":"r2","refresh_token_expires_in":7200}`
	m.store = writeFailure{s}
	if _, err := m.Token(context.Background()); err == nil {
		t.Fatal("save failure not reported")
	}
	if _, ok := m.cachedAccess(); ok {
		t.Fatal("cache survived a failed credential save")
	}
}

func TestCacheHitDoesNotWaitForASlowStoreRead(t *testing.T) {
	m, store, _ := cachedManager(Credential{AccessToken: "tok", Login: "alice", ID: 1})
	mustToken(t, m, "tok")
	store.entered = make(chan struct{}, 1)
	store.release = make(chan struct{})
	done := make(chan struct{})
	go func() { _, _ = m.Status(context.Background()); close(done) }() // holds the manager lock in Load
	<-store.entered
	hit := make(chan struct{})
	go func() { mustToken(t, m, "tok"); close(hit) }()
	select {
	case <-hit:
	case <-time.After(2 * time.Second):
		t.Fatal("cache hit queued behind a store read")
	}
	close(store.release)
	<-done
}

func TestTokenIsRereadAfterTTL(t *testing.T) {
	m, store, now := cachedManager(Credential{AccessToken: "first", Login: "alice", ID: 1})
	mustToken(t, m, "first")
	store.set(Credential{AccessToken: "second", Login: "alice", ID: 1})
	*now = now.Add(tokenCacheTTL - time.Second)
	mustToken(t, m, "first")
	*now = now.Add(2 * time.Second)
	mustToken(t, m, "second")
	if n := store.loads.Load(); n != 2 {
		t.Fatalf("loads = %d, want 2", n)
	}
}

func TestCacheStopsBeforeCredentialExpiry(t *testing.T) {
	m, _, now := cachedManager(Credential{AccessToken: "tok", Login: "alice", ID: 1, ExpiresAt: 10000 + 8*60, RefreshToken: "r", RefreshExpiresAt: 99999})
	mustToken(t, m, "tok")
	want := now.Add(8*time.Minute - tokenExpiryMargin)
	if m.cached == nil || !m.cached.validUntil.Equal(want) {
		t.Fatalf("validUntil = %v, want %v", m.cached, want)
	}
}

func TestUnauthorizedResponseDropsTheCacheOnce(t *testing.T) {
	m, store, _ := cachedManager(Credential{AccessToken: "old", Login: "alice", ID: 1})
	mustToken(t, m, "old")
	// A late 401 for some other credential leaves the cache alone.
	m.ObserveResponse("stale", http.StatusUnauthorized, http.Header{})
	mustToken(t, m, "old")
	if n := store.loads.Load(); n != 1 {
		t.Fatalf("loads = %d after unrelated 401, want 1", n)
	}
	store.set(Credential{AccessToken: "new", Login: "alice", ID: 1})
	m.ObserveResponse("old", http.StatusUnauthorized, http.Header{})
	mustToken(t, m, "new")
	mustToken(t, m, "new")
	if n := store.loads.Load(); n != 2 {
		t.Fatalf("loads = %d after 401, want 2 (one re-read)", n)
	}
}

func TestDisconnectDropsTheCachedToken(t *testing.T) {
	m, _, _ := cachedManager(Credential{AccessToken: "tok", Login: "alice", ID: 1})
	mustToken(t, m, "tok")
	if err := m.Disconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if token, err := m.Token(context.Background()); err == nil || token != "" {
		t.Fatalf("token %q survived sign-out (err %v)", token, err)
	}
}

func TestDisconnectDropsTheCacheEvenWhenDeleteFails(t *testing.T) {
	m, store, _ := cachedManager(Credential{AccessToken: "tok", Login: "alice", ID: 1})
	mustToken(t, m, "tok")
	m.store = failingDelete{store}
	if err := m.Disconnect(context.Background()); err == nil {
		t.Fatal("delete failure not reported")
	}
	if store.loads.Load() != 1 {
		t.Fatal("unexpected load")
	}
	mustToken(t, m, "tok") // re-read from the store, which still holds it
	if n := store.loads.Load(); n != 2 {
		t.Fatalf("loads = %d, want a re-read after failed delete", n)
	}
}

type failingDelete struct{ *countingStore }

func (failingDelete) Delete(context.Context) error { return ErrStore }

func TestSignInAsAnotherAccountReplacesTheCachedToken(t *testing.T) {
	m, s, now, reply, _ := fixture(t)
	store := &countingStore{c: Credential{AccessToken: "alice-token", Login: "alice", ID: 7}}
	m.store = store
	mustToken(t, m, "alice-token")
	d, err := m.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(5 * time.Second)
	*reply = `{"access_token":"bob-token","token_type":"bearer"}`
	if st, err := m.Poll(context.Background(), d.ID); err != nil || st.State != "connected" {
		t.Fatal(st, err)
	}
	_ = s
	mustToken(t, m, "bob-token")
	if id := m.AccountForToken("bob-token"); id != 1 {
		t.Fatalf("account = %d", id)
	}
	if id := m.AccountForToken("alice-token"); id != 0 {
		t.Fatal("old token kept an account")
	}
}

func TestStatusNoticesAnAccountChangeAndDropsTheCache(t *testing.T) {
	m, store, _ := cachedManager(Credential{AccessToken: "alice-token", Login: "alice", ID: 1})
	mustToken(t, m, "alice-token")
	store.set(Credential{AccessToken: "bob-token", Login: "bob", ID: 2})
	if st, err := m.Status(context.Background()); err != nil || st.AccountID != 2 {
		t.Fatal(st, err)
	}
	mustToken(t, m, "bob-token")
}

func TestRefreshReplacesTheCachedToken(t *testing.T) {
	m, s, now, reply, calls := fixture(t)
	s.c = Credential{AccessToken: "old", RefreshToken: "r", RefreshExpiresAt: now.Unix() + 7200, ExpiresAt: now.Unix() + 600, ID: 1, Login: "octocat"}
	mustToken(t, m, "old")
	*now = now.Add(9*time.Minute + 30*time.Second) // inside the expiry margin
	*reply = `{"access_token":"new","token_type":"bearer","expires_in":3600,"refresh_token":"r2","refresh_token_expires_in":7200}`
	mustToken(t, m, "new")
	mustToken(t, m, "new")
	if *calls != 1 || s.saves != 1 {
		t.Fatalf("refresh exchanges %d saves %d, want 1 each", *calls, s.saves)
	}
}

func TestReadStartedBeforeADropCannotRepopulateTheCache(t *testing.T) {
	m, _, _ := cachedManager(Credential{})
	gen := m.cacheGeneration()
	m.dropCache()
	m.remember(Credential{AccessToken: "stale", ID: 1}, gen)
	if _, ok := m.cachedAccess(); ok {
		t.Fatal("stale read repopulated the cache")
	}
}
