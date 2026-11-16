package tileframes_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/internal/tileframes"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/tilestest"
)

// definition returns a sealed definition whose HTML is unique to n and padded
// to about pad bytes.
func definition(t testing.TB, n, pad int) tiles.TypeDefinition {
	t.Helper()
	def := tiles.TypeDefinition{
		Name:       fmt.Sprintf("frame_%d", n),
		Title:      "Frame",
		UseWhen:    "Never: a test type.",
		Fill:       "Nothing.",
		DataSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		Kit:        1,
		Sizes:      []tiles.Size{tiles.SizeHalf},
		Source: tiles.TypeSource{Format: "html", HTML: fmt.Sprintf("<script>baum.tile({glance: () => %q, detail: () => %q});</script><!--%s-->",
			fmt.Sprint(n), fmt.Sprint(n), strings.Repeat("x", pad))},
	}
	var sealed tiles.TypeDefinition
	if err := json.Unmarshal(tilestest.SealDefinition(def), &sealed); err != nil {
		t.Fatal(err)
	}
	return sealed
}

func composed(t testing.TB, def tiles.TypeDefinition) []byte {
	t.Helper()
	document, err := kit.Compose(def.Kit, def.Source.HTML)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestAddComposesOnceAndGetServesTheComposition(t *testing.T) {
	r := tileframes.New(tileframes.MaxBytes)
	def := definition(t, 1, 0)
	before := tileframes.Composed.Load()
	for range 3 {
		digest, err := r.Add(def)
		if err != nil || digest != def.Digest {
			t.Fatalf("%q, %v", digest, err)
		}
	}
	if got := tileframes.Composed.Load() - before; got != 1 {
		t.Fatalf("composed %d times", got)
	}
	document, ok := r.Get(def.Digest)
	if !ok || !bytes.Equal(document, composed(t, def)) {
		t.Fatal("the registered document is not the kit's composition")
	}
	for _, digest := range []string{"", strings.TrimPrefix(def.Digest, "sha256:"), strings.ToUpper(def.Digest), def.Digest + " ", definition(t, 2, 0).Digest} {
		if _, ok := r.Get(digest); ok {
			t.Errorf("%q was found", digest)
		}
	}
}

// Only a definition that matches its digest, in html, that the kit composes
// is registered.
func TestAddRefuses(t *testing.T) {
	r := tileframes.New(tileframes.MaxBytes)
	good := definition(t, 3, 0)
	for _, tc := range []struct {
		name   string
		mutate func(*tiles.TypeDefinition)
		want   error
	}{
		{"no digest", func(d *tiles.TypeDefinition) { d.Digest = "" }, tileframes.ErrDigest},
		{"malformed digest", func(d *tiles.TypeDefinition) { d.Digest = strings.ToUpper(d.Digest) }, tileframes.ErrDigest},
		{"another type's digest", func(d *tiles.TypeDefinition) { d.Digest = definition(t, 4, 0).Digest }, tileframes.ErrDigest},
		{"changed HTML", func(d *tiles.TypeDefinition) { d.Source.HTML += "<script>steal()</script>" }, tileframes.ErrDigest},
		{"changed title", func(d *tiles.TypeDefinition) { d.Title = "Other" }, tileframes.ErrDigest},
		{"not html", func(d *tiles.TypeDefinition) { d.Source.Format = "svg" }, tileframes.ErrFormat},
	} {
		def := good
		tc.mutate(&def)
		if _, err := r.Add(def); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
		if _, ok := r.Get(def.Digest); ok {
			t.Errorf("%s: registered", tc.name)
		}
	}
	// What the kit refuses, sealed so the digest matches.
	newer := good
	newer.Kit, newer.Digest = 2, ""
	large := good
	large.Source.HTML, large.Digest = strings.Repeat("界", kit.MaxAuthorHTML/3+1), ""
	for name, def := range map[string]tiles.TypeDefinition{"unsupported kit": newer, "large HTML": large} {
		var sealed tiles.TypeDefinition
		if err := json.Unmarshal(tilestest.SealDefinition(def), &sealed); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Add(sealed); err == nil {
			t.Errorf("%s: registered", name)
		}
		if _, ok := r.Get(sealed.Digest); ok {
			t.Errorf("%s: found", name)
		}
	}
	if documents, _, _ := r.Stats(); documents != 0 {
		t.Fatalf("%d documents after refusals", documents)
	}
}

// The least recently used document goes first once the bytes pass the
// bound; the newest stays whatever its size.
func TestEvictsTheLeastRecentlyUsed(t *testing.T) {
	a, b, c := definition(t, 10, 1000), definition(t, 11, 1000), definition(t, 12, 1000)
	size := len(composed(t, a))
	r := tileframes.New(2 * size)
	for _, def := range []tiles.TypeDefinition{a, b} {
		if _, err := r.Add(def); err != nil {
			t.Fatal(err)
		}
	}
	// Reading a makes b the least recently used.
	if _, ok := r.Get(a.Digest); !ok {
		t.Fatal("a missing")
	}
	if _, err := r.Add(c); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get(b.Digest); ok {
		t.Error("b outlived the bound")
	}
	for _, def := range []tiles.TypeDefinition{a, c} {
		if _, ok := r.Get(def.Digest); !ok {
			t.Errorf("%s was dropped", def.Name)
		}
	}
	if documents, bytes, evicted := r.Stats(); documents != 2 || bytes != 2*size || evicted != 1 {
		t.Fatalf("%d documents, %d bytes, %d evicted", documents, bytes, evicted)
	}
	// Adding b again composes it again.
	before := tileframes.Composed.Load()
	if _, err := r.Add(b); err != nil {
		t.Fatal(err)
	}
	if tileframes.Composed.Load() != before+1 {
		t.Error("an evicted definition was not composed again")
	}

	tiny := tileframes.New(1)
	if _, err := tiny.Add(a); err != nil {
		t.Fatal(err)
	}
	if _, ok := tiny.Get(a.Digest); !ok {
		t.Fatal("the newest document was dropped")
	}
	if _, err := tiny.Add(b); err != nil {
		t.Fatal(err)
	}
	if _, ok := tiny.Get(a.Digest); ok {
		t.Fatal("an older document outlived a newer one")
	}
}

// A hung digest is refused from then on, registered or not.
func TestMarkHung(t *testing.T) {
	r := tileframes.New(tileframes.MaxBytes)
	held, later := definition(t, 20, 0), definition(t, 21, 0)
	if _, err := r.Add(held); err != nil {
		t.Fatal(err)
	}
	r.MarkHung(held.Digest)
	r.MarkHung(later.Digest)
	if _, err := r.Add(later); err != nil {
		t.Fatal(err)
	}
	for _, def := range []tiles.TypeDefinition{held, later} {
		if _, ok := r.Get(def.Digest); ok {
			t.Errorf("%s is served after it hung", def.Name)
		}
	}
	other := definition(t, 22, 0)
	if _, err := r.Add(other); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get(other.Digest); !ok {
		t.Error("marking one digest refused another")
	}
}

// Adds, reads and marks from many goroutines, for the race detector, under a
// bound that keeps evicting.
func TestConcurrentUse(t *testing.T) {
	defs := make([]tiles.TypeDefinition, 12)
	for i := range defs {
		defs[i] = definition(t, 100+i, 2000)
	}
	r := tileframes.New(4 * len(composed(t, defs[0])))
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				def := defs[(g+i)%len(defs)]
				if _, err := r.Add(def); err != nil {
					t.Error(err)
					return
				}
				if document, ok := r.Get(def.Digest); ok && !bytes.HasSuffix(document, []byte("</html>\n")) {
					t.Error("a torn document")
					return
				}
				if i%50 == 0 {
					r.MarkHung(defs[len(defs)-1].Digest)
				}
				r.Stats()
			}
		}()
	}
	wg.Wait()
	if documents, _, _ := r.Stats(); documents > 4 {
		t.Fatalf("%d documents under a bound of 4", documents)
	}
}
