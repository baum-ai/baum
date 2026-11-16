package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/tilelayout"
	"github.com/baum-ai/baum/internal/tileview/tileviewtest"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// stubFiller answers fills with data, or err, after release is closed.
type stubFiller struct {
	mu      sync.Mutex
	calls   []tilelayout.FillRequest
	release chan struct{}
	data    json.RawMessage
	err     error
}

func (f *stubFiller) FillTile(ctx context.Context, r tilelayout.FillRequest) (json.RawMessage, json.RawMessage, *float64, error) {
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
	service := &tilelayout.Service{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite"), Changed: func(pr string, revision int64) { events = append(events, pr) }}
	filler := &stubFiller{release: make(chan struct{}), data: json.RawMessage(`{"endpoints":[],"docs":null,"consumers":[]}`)}
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	fills := &TileFills{Filler: filler, Layouts: service, Now: func() time.Time { return now }}
	request := tilelayout.FillRequest{Clone: "c1", Repository: "acme/widgets", Number: 42, Version: 3, Type: "api_change"}
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

// A review of a pull request fills the library types the person added,
// each with the last data held for it and the hunks of the version that
// data was written for.
func TestAlwaysTilesComeFromTheLayout(t *testing.T) {
	ctx := context.Background()
	service := &tilelayout.Service{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
	versions, err := store.Open("", t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: "acme/widgets", Number: 42}
	base := tileviewtest.Base(t)
	base["review"].(map[string]any)["version"] = 1
	delete(base["review"].(map[string]any), "previous_version")
	document := tileviewtest.Encode(t, base)
	if err := versions.Put(ctx, key, store.Version{}, store.Version{Metadata: store.Metadata{Version: 1}, Document: document}); err != nil {
		t.Fatal(err)
	}
	l := &Local{Layouts: service}
	if got := l.alwaysTiles(ctx, versions, key); got != nil {
		t.Fatalf("no layout: %+v", got)
	}
	normalized, err := review.NormalizeStored(document)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := library.Get("api_change")
	pr := tilelayout.Key(key.Repository, key.Number)
	if _, err := service.Edit(ctx, pr, "chat", doc, 0, []tiles.Op{{Op: tiles.OpAdd, Add: &tiles.Added{Kind: "library", Name: "api_change", Digest: entry.Definition.Digest}}}); err != nil {
		t.Fatal(err)
	}
	diff := library.DiffOf(doc)
	data := json.RawMessage(fmt.Sprintf(`{"endpoints":[],"docs":null,"consumers":[{"label":"x","updated":true,"cites":[{"type":"hunk","id":%q}]}]}`, diff[0].ID))
	if err := service.PutFill(ctx, pr, "api_change", 1, tilelayout.OriginFill, entry.Encoded, data); err != nil {
		t.Fatal(err)
	}
	// Data the agent wrote is the person's: a review never reuses it.
	chatData := json.RawMessage(strings.Replace(string(data), `"label":"x"`, `"label":"the agent's"`, 1))
	if err := service.PutFill(ctx, pr, "api_change", 2, tilelayout.OriginChat, entry.Encoded, chatData); err != nil {
		t.Fatal(err)
	}
	got := l.alwaysTiles(ctx, versions, key)
	if len(got) != 1 || got[0].Name != "api_change" || string(got[0].Last) != string(data) || len(got[0].LastHunks) != len(diff) || !slices.Contains(got[0].LastHunks, diff[0].ID) {
		t.Fatalf("always: %+v", got)
	}
}

// A fill naming a digest is a tile made in chat, whose fill instructions
// the session agent wrote from untrusted content: it is refused before any
// model call.
func TestFillTileRefusesATileMadeInChat(t *testing.T) {
	l := &Local{}
	_, _, _, err := l.FillTile(context.Background(), tilelayout.FillRequest{Clone: "c1", Repository: "acme/widgets", Number: 42, Version: 2, Type: "env_vars", Digest: "d_chat"})
	if err == nil || !strings.Contains(err.Error(), "made in chat") {
		t.Fatalf("a custom digest: %v", err)
	}
}
