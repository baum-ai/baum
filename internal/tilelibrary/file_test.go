package tilelibrary_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/tilelibrary"
	"github.com/baum-ai/baum/schema"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// exported is a saved type's file, written by Export.
func exported(t *testing.T, s *tilelibrary.Store, name string) []byte {
	t.Helper()
	saved, err := s.Get(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tilelibrary.Export(saved, "1.2.3", time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// edit decodes a file, changes it and encodes it again.
func edit(t *testing.T, raw []byte, change func(file map[string]any, def map[string]any)) []byte {
	t.Helper()
	var file map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	change(file, file["definition"].(map[string]any))
	out, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A type exported on one computer is imported on another (a fresh state
// directory) as the same type: the same digest, texts, paths and samples,
// marked imported and not checked yet. The file has no digest and validates
// against the published schema.
func TestExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	from, _ := newStore(t)
	saved := mustCreate(t, from, change(t, "migrations", ""))
	raw := exported(t, from, "migrations")
	if !bytes.HasSuffix(raw, []byte("\n")) || bytes.Contains(raw, []byte(`"digest"`)) {
		t.Errorf("the file carries a digest or no final newline")
	}
	if tilelibrary.FileName("migrations") != "migrations.baumtile.json" {
		t.Error(tilelibrary.FileName("migrations"))
	}
	resolved, err := schema.CompileTileType()
	if err != nil {
		t.Fatal(err)
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(instance); err != nil {
		t.Fatalf("the file does not validate against tile-type-1.json: %v", err)
	}

	to := &tilelibrary.Store{Path: filepath.Join(appdbtest.TempDir(t), "app.sqlite")}
	got, err := to.Import(ctx, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != (tilelibrary.Imported{Name: "migrations"}) {
		t.Errorf("imported: %+v", got)
	}
	imported, err := to.Get(ctx, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	if imported.Entry.Definition.Digest != saved.Entry.Definition.Digest || imported.Origin != tilelibrary.OriginImport ||
		!imported.Unchecked || imported.Entry.Glance != saved.Entry.Glance || imported.Entry.ReviewChecks != saved.Entry.ReviewChecks ||
		len(imported.Paths) != 1 || len(imported.Entry.Samples) != len(saved.Entry.Samples) {
		t.Errorf("imported %+v", imported)
	}
	if problems := library.Check(imported.Entry); len(problems) > 0 {
		t.Errorf("library.Check: %v", problems)
	}
	// Exporting the imported type gives the same file but for when it was written.
	if again := exported(t, to, "migrations"); !bytes.Equal(again, raw) {
		t.Error("export, import and export again changed the file")
	}
	// A render check marks it checked.
	if err := to.MarkChecked(ctx, imported.Entry.Definition.Digest, tilelibrary.VerdictPass); err != nil {
		t.Fatal(err)
	}
	if again, _ := to.Get(ctx, "migrations"); again.Unchecked {
		t.Error("still not checked after a check")
	}
}

// A name already in the library, a deleted type's included, is renamed with
// the first free numeric suffix, and the import says so.
func TestImportRenamesAClashingName(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	mustCreate(t, s, change(t, "migrations", ""))
	raw := exported(t, s, "migrations")
	candidate, err := s.Inspect(ctx, raw)
	if err != nil || candidate.Name != "migrations_2" || candidate.Renamed != "migrations" || candidate.Entry.Definition.Name != "migrations_2" {
		t.Fatalf("inspect: %+v %v", candidate, err)
	}
	got, err := s.Import(ctx, raw, nil)
	if err != nil || got.Name != "migrations_2" || got.RenamedFrom != "migrations" {
		t.Fatalf("import: %+v %v", got, err)
	}
	if err := s.Delete(ctx, "migrations_2"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Import(ctx, raw, nil); err != nil || got.Name != "migrations_3" {
		t.Errorf("past a deleted type: %+v %v", got, err)
	}
	// A long name keeps within 64 characters.
	long := strings.Repeat("a", 64)
	mustCreate(t, s, change(t, long, ""))
	if got, err := s.Import(ctx, exported(t, s, long), nil); err != nil || got.Name != strings.Repeat("a", 62)+"_2" {
		t.Errorf("a long name: %+v %v", got, err)
	}
}

// A render check runs on the samples when a runner is there; without one the
// type is not checked yet; a failed check refuses the file.
func TestImportRunsTheRenderCheck(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	mustCreate(t, s, change(t, "migrations", ""))
	raw := exported(t, s, "migrations")
	if err := s.Delete(ctx, "migrations"); err != nil {
		t.Fatal(err)
	}
	checked := 0
	pass := func(_ context.Context, entry library.Entry) (string, error) {
		checked++
		if len(entry.Samples) == 0 {
			t.Error("checked without samples")
		}
		return tilelibrary.VerdictPass, nil
	}
	if got, err := s.Import(ctx, raw, pass); err != nil || !got.Checked || checked != 1 {
		t.Errorf("with a runner: %+v %v", got, err)
	}
	noRunner := func(context.Context, library.Entry) (string, error) { return "", tilelibrary.ErrNoRunner }
	if got, err := s.Import(ctx, raw, noRunner); err != nil || got.Checked {
		t.Errorf("without a runner: %+v %v", got, err)
	}
	fail := func(context.Context, library.Entry) (string, error) { return "fail", nil }
	if _, err := s.Import(ctx, raw, fail); err == nil {
		t.Error("a failed check imported")
	}
}

// Hostile files are refused before anything is saved, each with a reason
// that names no content.
func TestImportRefusesHostileFiles(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	mustCreate(t, s, change(t, "migrations", ""))
	good := exported(t, s, "migrations")
	if err := s.Delete(ctx, "migrations"); err != nil {
		t.Fatal(err)
	}
	deep := strings.Repeat(`{"a":`, 100) + "1" + strings.Repeat("}", 100)
	many := `{"schema":"baum.tile-type/1","paths":[` + strings.Repeat("0,", 250_000) + `0]}`
	cases := map[string]struct {
		raw  []byte
		want string
	}{
		"oversized":       {append(append([]byte(nil), good...), bytes.Repeat([]byte(" "), tilelibrary.MaxFileBytes)...), "larger than 3 MiB"},
		"deep JSON":       {[]byte(deep), "nested deeper"},
		"many values":     {[]byte(many), "more than"},
		"not JSON":        {[]byte("{nope"), "not valid JSON"},
		"two values":      {append(append([]byte(nil), good...), []byte(" {}")...), "more than one JSON value"},
		"wrong schema":    {edit(t, good, func(f, _ map[string]any) { f["schema"] = "baum.tile-type/2" }), "baum.tile-type/1"},
		"unknown member":  {edit(t, good, func(f, _ map[string]any) { f["builtin"] = true }), "member"},
		"NUL":             {bytes.Replace(good, []byte(`"title": "`), []byte("\"title\": \"\x00"), 1), "NUL"},
		"escaped NUL":     {edit(t, good, func(_, d map[string]any) { d["title"] = "Saved\x00migrations" }), "NUL"},
		"bad UTF-8":       {bytes.Replace(good, []byte(`"title": "Saved`), []byte("\"title\": \"\xff\xfeSaved"), 1), "not"},
		"unsupported kit": {edit(t, good, func(_, d map[string]any) { d["kit"] = 99 }), "kit"},
		"bad dialect": {edit(t, good, func(_, d map[string]any) {
			d["data_schema"] = map[string]any{"type": "object", "$ref": "https://example.com/schema.json"}
		}), "definition"},
		"lint errors": {edit(t, good, func(_, d map[string]any) {
			source := d["source"].(map[string]any)
			source["html"] = source["html"].(string) + `<script>fetch("https://example.com/collect")</script>`
		}), "component"},
		"invalid sample": {edit(t, good, func(f, _ map[string]any) {
			f["samples"].([]any)[0].(map[string]any)["data"] = map[string]any{"nothing": true}
		}), "tile type"},
		"sample cites a hunk it lacks": {edit(t, good, func(f, _ map[string]any) {
			f["samples"].([]any)[0].(map[string]any)["hunks"] = map[string]any{}
		}), "synthetic"},
		"no samples":    {edit(t, good, func(f, _ map[string]any) { f["samples"] = []any{} }), "samples"},
		"reserved name": {edit(t, good, func(_, d map[string]any) { d["name"] = "sec_migrations" }), "cannot start with sec_"},
		"built-in name": {edit(t, good, func(_, d map[string]any) { d["name"] = "risks" }), "built-in tile type"},
		"bad name":      {edit(t, good, func(_, d map[string]any) { d["name"] = "Bad Name" }), "definition"},
	}
	for name, c := range cases {
		_, err := s.Import(ctx, c.raw, nil)
		var refused *tilelibrary.FileError
		if err == nil || !errors.As(err, &refused) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want a refusal saying %q", name, err, c.want)
		}
	}
	if list, err := s.List(ctx, false); err != nil || len(list) != 0 {
		t.Errorf("a refused file saved %d types: %v", len(list), err)
	}
}

// A digest or built-in marking in the file is never read: the digest is the
// content's, and a file that claims to be baum's own is a custom type.
func TestImportComputesDigestAndBuiltin(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	mustCreate(t, s, change(t, "migrations", ""))
	raw := exported(t, s, "migrations")
	if err := s.Delete(ctx, "migrations"); err != nil {
		t.Fatal(err)
	}
	builtin, _ := library.Get("schema_change")
	forged := edit(t, raw, func(_, d map[string]any) {
		d["digest"] = builtin.Definition.Digest
		d["builtin"] = true
	})
	got, err := s.Import(ctx, forged, nil)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := s.Get(ctx, got.Name)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := tiles.DefinitionDigest(imported.Entry.Encoded)
	if err != nil || imported.Entry.Definition.Digest != digest || digest == builtin.Definition.Digest {
		t.Errorf("digest %s, computed %s, built-in %s: %v", imported.Entry.Definition.Digest, digest, builtin.Definition.Digest, err)
	}
	if library.IsBuiltin(imported.Entry.Encoded) || bytes.Contains(imported.Entry.Encoded, []byte(`"builtin"`)) {
		t.Error("a file made itself baum's own")
	}
}
