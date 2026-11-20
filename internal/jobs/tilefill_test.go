package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/addedtiles"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/tiles/library"
)

// stubFiller answers fills with data, or err, after release is closed.
type stubFiller struct {
	mu      sync.Mutex
	calls   []addedtiles.FillRequest
	release chan struct{}
	data    json.RawMessage
	err     error
}

func (f *stubFiller) FillTile(ctx context.Context, r addedtiles.FillRequest) (json.RawMessage, json.RawMessage, *float64, error) {
	f.mu.Lock()
	f.calls = append(f.calls, r)
	release, data, err := f.release, f.data, f.err
	f.mu.Unlock()
	if release != nil {
		<-release
	}
	entry, _ := library.Get(r.Type)
	cost := 0.4
	return entry.Encoded, data, &cost, err
}

func (f *stubFiller) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// A fill of an added tile runs once at a time per version and type, stores
// its data as the version's fill and tells the page; a failed one is not
// tried again for a while.
func TestTileFillsRunOnceAndStoreTheData(t *testing.T) {
	var events []string
	service := &addedtiles.Service{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite"), Changed: func(pr string) { events = append(events, pr) }}
	filler := &stubFiller{release: make(chan struct{}), data: json.RawMessage(`{"endpoints":[],"docs":null,"consumers":[]}`)}
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	fills := &TileFills{Filler: filler, Added: service, Now: func() time.Time { return now }}
	request := addedtiles.FillRequest{Clone: "c1", Repository: "acme/widgets", Number: 42, Version: 3, Type: "api_change"}
	fills.Fill(request)
	fills.Fill(request)
	close(filler.release)
	fills.Wait()
	if filler.count() != 1 {
		t.Fatalf("%d fills for one request", filler.count())
	}
	// Filled once: asking again (an add undone and made again) runs nothing.
	fills.Fill(request)
	fills.Wait()
	if filler.count() != 1 {
		t.Errorf("a filled version's type was filled again: %d calls", filler.count())
	}
	held, err := service.Fills(context.Background(), "acme/widgets#42")
	if err != nil || len(held) != 1 || held[0].Version != 3 || held[0].Tile != "api_change" || len(events) != 1 {
		t.Fatalf("held %+v, events %v: %v", held, events, err)
	}

	filler.err, filler.release = errors.New("the provider failed"), nil
	request.Version = 4
	fills.Fill(request)
	fills.Wait()
	fills.Fill(request)
	fills.Wait()
	if filler.count() != 2 {
		t.Errorf("a failed fill was tried again at once: %d calls", filler.count())
	}
	now = now.Add(tileFillRetry + time.Second)
	fills.Fill(request)
	fills.Wait()
	if filler.count() != 3 {
		t.Errorf("a failed fill was not tried again later: %d calls", filler.count())
	}
}

// A review of a pull request fills the library types the person added.
func TestAlwaysTilesComeFromTheAddedTiles(t *testing.T) {
	ctx := context.Background()
	service := &addedtiles.Service{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
	key := store.Key{Repository: "acme/widgets", Number: 42}
	l := &Local{Added: service}
	if got := l.alwaysTiles(ctx, key); got != nil {
		t.Fatalf("nothing added: %+v", got)
	}
	entry, _ := library.Get("api_change")
	pr := addedtiles.Key(key.Repository, key.Number)
	if err := service.Add(ctx, pr, addedtiles.Added{Tile: "api_change", Kind: addedtiles.KindLibrary, Name: "api_change", Digest: entry.Definition.Digest, By: "chat"}); err != nil {
		t.Fatal(err)
	}
	if got := l.alwaysTiles(ctx, key); !slices.Equal(got, []string{"api_change"}) {
		t.Fatalf("always: %+v", got)
	}
}

// A fill naming a digest is a tile made in chat, whose fill instructions
// the session agent wrote from untrusted content: it is refused before any
// model call.
func TestFillTileRefusesATileMadeInChat(t *testing.T) {
	l := &Local{}
	_, _, _, err := l.FillTile(context.Background(), addedtiles.FillRequest{Clone: "c1", Repository: "acme/widgets", Number: 42, Version: 2, Type: "env_vars", Digest: "d_chat"})
	if err == nil || !strings.Contains(err.Error(), "made in chat") {
		t.Fatalf("a custom digest: %v", err)
	}
}
