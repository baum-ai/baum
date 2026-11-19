package replay_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/replay"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/tiles/kit"
	"github.com/baum-ai/baum/tiles/library"
)

// libraryDir is a directory holding the kit's starter exported as a saved
// type's file, under name.
func libraryDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		entry, err := library.Load(kit.Starter())
		if err != nil {
			t.Fatal(err)
		}
		def := entry.Definition
		def.Name = name
		if entry.Encoded, err = library.Seal(&def); err != nil {
			t.Fatal(err)
		}
		entry.Definition = def
		var samples []library.Sample
		for _, s := range entry.Samples {
			if tilelibrary.IsSynthetic(s) {
				samples = append(samples, s)
			}
		}
		entry.Samples = samples
		raw, err := tilelibrary.Export(tilelibrary.Type{Name: name, Entry: entry}, "test", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, tilelibrary.FileName(name)), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// --library-dir loads exported tile types as the person's saved types: the
// tiles generator offers them to each case's outline, beside the built-in
// types, and records them in its description.
func TestTilesGeneratorOffersTheLibraryDir(t *testing.T) {
	saved, err := replay.LoadLibrary(libraryDir(t, "env_reads"))
	if err != nil || len(saved) != 1 || saved[0].Name != "env_reads" || saved[0].Unchecked {
		t.Fatalf("%v %+v", err, saved)
	}
	d := newDataset(t, assembleSource(t))
	d.addCase(t, "c1", nil)
	ds, _ := replay.OpenDataset(d.dir)
	backend := &completing{Backend: &generatetest.Backend{Version: "1.0.0", RunFunc: pricedReplies(nil)}}
	settings := tilesSettings(t, backend, replay.GeneratorConfig{LibraryDir: libraryDir(t, "env_reads")})
	summary, _, err := execute(t, replay.Plan{Dataset: ds, OutDir: t.TempDir(), Settings: settings})
	if err != nil || summary.Totals.OK != 1 {
		t.Fatalf("%v %+v", err, summary.Totals)
	}
	offered := false
	for _, r := range backend.Requests {
		offered = offered || strings.Contains(r.Prompt, "- `env_reads` (Findings; from the person's tile library): ")
	}
	if !offered {
		t.Error("the outline was not offered the saved type")
	}
}

func TestLibraryDirIsRefusedWhereItDoesNotApply(t *testing.T) {
	if _, err := replay.LoadLibrary(t.TempDir()); err == nil {
		t.Error("an empty library directory was loaded")
	}
	for _, name := range []string{"review", "compose"} {
		if _, err := replay.NewGenerator(name, replay.GeneratorConfig{Staged: true, LibraryDir: libraryDir(t, "env_reads")}); err == nil {
			t.Errorf("the %s generator took --library-dir", name)
		}
	}
	g, err := replay.NewGenerator("tiles", replay.GeneratorConfig{Staged: true, LibraryDir: libraryDir(t, "env_reads")})
	if err != nil {
		t.Fatal(err)
	}
	described, err := g.Describe(ctx(), "claude")
	if saved, _ := described["saved_tile_types"].(map[string]string); err != nil || saved["env_reads"] == "" {
		t.Errorf("%v %v", err, described)
	}
}
