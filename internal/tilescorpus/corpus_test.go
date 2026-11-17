// Package tilescorpus converts every saved review version of a directory of
// review stores into tile documents and checks the conversion's invariants on
// each. It runs only when BAUM_TILES_CORPUS names a directory, and logs only
// counts: the stores hold private pull request content.
package tilescorpus

import (
	"bytes"
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/tilestest"
)

// Env names the directory to search for *.versions.sqlite stores. They are
// copied and the copies opened read-only; the originals are never opened.
const Env = "BAUM_TILES_CORPUS"

type version struct {
	store       int
	pullRequest string
	number      int
	document    []byte
}

type tally struct {
	versions, converted        int
	failures                   map[string]int
	tilesPerVersion            []int
	landingTiles               map[int]int
	allChangesLanding          int
	renamedVersions            int
	renamedRisks, renamedItems int
	types                      map[string]int
	unavailable                int
	normalize, convert, checks []time.Duration
	pairs                      int
}

func (t *tally) fail(stage string) { t.failures[stage]++ }

func TestCorpus(t *testing.T) {
	dir := os.Getenv(Env)
	if dir == "" {
		t.Skipf("%s is not set", Env)
	}
	stores := findStores(t, dir)
	if len(stores) == 0 {
		t.Fatalf("%s holds no *.versions.sqlite", Env)
	}
	validator := tiles.NewValidator()
	stats := &tally{failures: map[string]int{}, landingTiles: map[int]int{}, types: map[string]int{}}
	for i, store := range stores {
		versions := readStore(t, i, store)
		var previous *tiles.Document
		var previousPR string
		for _, v := range versions {
			stats.versions++
			doc := check(stats, validator, v)
			if doc != nil && previous != nil && previousPR == v.pullRequest {
				stats.pairs++
				for _, failure := range tilestest.CheckStability(previous, doc) {
					stats.fail(invariantOf(failure))
				}
			}
			previous, previousPR = doc, v.pullRequest
		}
	}
	report(t, stats, len(stores))
	if len(stats.failures) > 0 || stats.converted != stats.versions {
		t.Fatalf("%d of %d versions converted; failures by stage: %s", stats.converted, stats.versions, counts(stats.failures))
	}
}

// check converts one version and runs every check on it. It returns the tile
// document, or nil when the conversion failed.
func check(stats *tally, validator *tiles.Validator, v version) *tiles.Document {
	start := time.Now()
	normalized, err := review.NormalizeStored(v.document)
	stats.normalize = append(stats.normalize, time.Since(start))
	if err != nil {
		stats.fail("normalize")
		return nil
	}
	start = time.Now()
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	stats.convert = append(stats.convert, time.Since(start))
	if err != nil {
		stats.fail("convert")
		return nil
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		stats.fail("encode")
		return nil
	}
	again, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		stats.fail("convert again")
	} else if encodedAgain, err := json.Marshal(again); err != nil || !bytes.Equal(encoded, encodedAgain) {
		stats.fail("determinism")
	} else {
		// The tile-level checks a host runs on each document it serves.
		start = time.Now()
		if err := validator.CheckTiles(again); err != nil {
			stats.fail("check tiles")
		}
		stats.checks = append(stats.checks, time.Since(start))
	}
	validated, err := validator.Validate(encoded)
	if err != nil {
		var invalid *tiles.DocumentError
		if errors.As(err, &invalid) {
			stats.fail("validate: document-level")
		} else {
			stats.fail("validate")
		}
		return nil
	}
	ok := true
	for _, tile := range validated.Tiles {
		if tile.State == tiles.StateUnavailable {
			stats.unavailable++
			ok = false
		}
	}
	for _, failure := range tilestest.CheckConversion(normalized, doc) {
		stats.fail(invariantOf(failure))
		ok = false
	}
	if ok {
		stats.converted++
	}
	record(stats, doc)
	return doc
}

// invariantOf returns the anonymous part of a failure: the invariant it
// names, never the identifiers after it.
func invariantOf(failure string) string {
	if head, _, ok := strings.Cut(failure, ":"); ok && strings.HasPrefix(head, "invariant ") {
		return head
	}
	return "invariants"
}

func record(stats *tally, doc *tiles.Document) {
	stats.tilesPerVersion = append(stats.tilesPerVersion, len(doc.Tiles))
	types := map[string]string{}
	for _, tile := range doc.Tiles {
		stats.types[tile.Type]++
		types[tile.ID] = tile.Type
	}
	visible := tiles.Visible(doc.Layout)
	shown, changes, changesShown := 0, 0, 0
	for i, entry := range doc.Layout.Entries {
		if visible[i] {
			shown++
		}
		if types[entry.Tile] == tiles.TypeChange && entry.Group == tiles.GroupChanges {
			changes++
			if visible[i] {
				changesShown++
			}
		}
	}
	stats.landingTiles[shown]++
	if changes == changesShown {
		stats.allChangesLanding++
	}
	renamed := false
	for _, risk := range doc.Risks {
		if risk.Origin != nil {
			stats.renamedRisks++
			renamed = true
		}
	}
	for _, item := range doc.Evidence {
		if item.Origin != nil {
			stats.renamedItems++
			renamed = true
		}
	}
	for _, tile := range doc.Tiles {
		if tile.Type != tiles.TypeDecisions {
			continue
		}
		var data tiles.DecisionsData
		_ = json.Unmarshal(tile.Data, &data)
		for _, item := range data.Items {
			var decision struct {
				Origin tiles.BlockOrigin `json:"origin"`
			}
			if json.Unmarshal(item, &decision) == nil && decision.Origin.ID != "" {
				stats.renamedItems++
				renamed = true
			}
		}
	}
	if renamed {
		stats.renamedVersions++
	}
}

func report(t *testing.T, stats *tally, stores int) {
	t.Logf("stores: %d, versions: %d, converted with every check passing: %d", stores, stats.versions, stats.converted)
	t.Logf("consecutive version pairs checked for stability: %d", stats.pairs)
	t.Logf("failures by stage: %s", counts(stats.failures))
	t.Logf("unavailable tiles: %d", stats.unavailable)
	t.Logf("tiles per version: p50 %d, p90 %d, max %d", percentile(stats.tilesPerVersion, 50), percentile(stats.tilesPerVersion, 90), percentile(stats.tilesPerVersion, 100))
	landing := map[string]int{}
	for n, versions := range stats.landingTiles {
		landing[fmt.Sprintf("%d tiles", n)] = versions
	}
	t.Logf("tiles shown on the landing page (versions): %s", counts(landing))
	t.Logf("versions with every current logical change on the landing page: %d", stats.allChangesLanding)
	t.Logf("versions with renamed IDs: %d (risks %d, evidence and decisions %d)", stats.renamedVersions, stats.renamedRisks, stats.renamedItems)
	t.Logf("tiles by type: %s", counts(stats.types))
	total := func(durations []time.Duration) time.Duration {
		var sum time.Duration
		for _, d := range durations {
			sum += d
		}
		return sum
	}
	t.Logf("NormalizeStored per version: p50 %v, p99 %v, max %v, total %v", percentile(stats.normalize, 50), percentile(stats.normalize, 99), percentile(stats.normalize, 100), total(stats.normalize))
	t.Logf("FromReview per version: p50 %v, p99 %v, max %v, total %v", percentile(stats.convert, 50), percentile(stats.convert, 99), percentile(stats.convert, 100), total(stats.convert))
	t.Logf("CheckTiles per version: p50 %v, p99 %v, max %v, total %v", percentile(stats.checks, 50), percentile(stats.checks, 99), percentile(stats.checks, 100), total(stats.checks))
}

func counts(values map[string]int) string {
	if len(values) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int { return cmp.Or(values[b]-values[a], strings.Compare(a, b)) })
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = fmt.Sprintf("%s %d", key, values[key])
	}
	return strings.Join(parts, ", ")
}

func percentile[T cmp.Ordered](values []T, p int) T {
	var zero T
	if len(values) == 0 {
		return zero
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	index := (len(sorted) - 1) * p / 100
	return sorted[index]
}

func findStores(t *testing.T, dir string) []string {
	t.Helper()
	var stores []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".versions.sqlite") {
			stores = append(stores, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("searching the %s directory failed", Env)
	}
	slices.Sort(stores)
	return stores
}

// readStore copies a store, with its write-ahead log and shared memory
// files, into a temporary directory and reads every version from the copy,
// ordered by pull request and version.
func readStore(t *testing.T, n int, path string) []version {
	t.Helper()
	dir := t.TempDir()
	copied := filepath.Join(dir, "versions.sqlite")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		raw, err := os.ReadFile(path + suffix)
		if errors.Is(err, fs.ErrNotExist) && suffix != "" {
			continue
		}
		if err != nil {
			t.Fatalf("store %d: %s", n, anonymous(err, path, dir))
		}
		if err := os.WriteFile(copied+suffix, raw, 0o600); err != nil {
			t.Fatalf("store %d: %s", n, anonymous(err, path, dir))
		}
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.ToSlash(copied)}).String()+"?mode=ro")
	if err != nil {
		t.Fatalf("store %d: %s", n, anonymous(err, path, dir))
	}
	defer db.Close()
	rows, err := db.Query(`SELECT repository, pr, version, document FROM versions ORDER BY repository, pr, version`)
	if err != nil {
		t.Fatalf("store %d: %s", n, anonymous(err, path, dir))
	}
	defer rows.Close()
	var versions []version
	for rows.Next() {
		var repository string
		var pr int
		v := version{store: n}
		if err := rows.Scan(&repository, &pr, &v.number, &v.document); err != nil {
			t.Fatalf("store %d: %s", n, anonymous(err, path, dir))
		}
		v.pullRequest = fmt.Sprintf("%s#%d", repository, pr)
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("store %d: %s", n, anonymous(err, path, dir))
	}
	return versions
}

// anonymous returns an error's text without the store's path or its copy's,
// since a store's file name can carry a repository's name.
func anonymous(err error, paths ...string) string {
	text := err.Error()
	for _, path := range paths {
		text = strings.ReplaceAll(text, path, "<store>")
	}
	return text
}
