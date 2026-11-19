package replay

import (
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
	"sync"

	_ "modernc.org/sqlite" // the review stores' driver

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/view"
)

// The sources the view generator reads its reviews from (savedReviews).
const (
	// ComposeFromStore presents the dataset's saved reviews
	// (reviews/<machine>/<repository>.versions.sqlite): for each case, the
	// version generated from the case's bundle, else the latest version at
	// the case's head, else the pull request's latest version.
	ComposeFromStore = "store"
	// ComposeFromRun presents the review documents an earlier run generated
	// (<run>/cases/<id>/document.json).
	ComposeFromRun = "run"
)

// savedReviews reads the reviews that already exist, which a generator
// presents with no analysis of its own: the saved reviews of the dataset,
// or the documents of an earlier run.
type savedReviews struct {
	// Source is ComposeFromStore or ComposeFromRun; Dataset is the dataset
	// directory for the first, Run the earlier run's directory for the
	// second.
	Source, Dataset, Run string

	once     sync.Once
	stores   *reviewStores
	redactor *bundle.Redactor
	err      error
}

// Close closes the copied review stores and removes their directory. The
// driver calls it when a run ends; a generator never opened is a no-op.
func (g *savedReviews) Close() error {
	if g.stores == nil {
		return nil
	}
	err := g.stores.Close()
	g.stores = nil
	return err
}

func (g *savedReviews) init() error {
	g.once.Do(func() {
		if g.redactor, g.err = bundle.NewRedactor(); g.err != nil {
			return
		}
		if g.Source == ComposeFromStore {
			g.stores, g.err = openReviewStores(filepath.Join(g.Dataset, "reviews"))
		}
	})
	return g.err
}

// ComposedReview identifies the composed review.
type ComposedReview struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Version    int    `json:"version,omitempty"`
	Match      string `json:"match"`
}

// review returns the review document a case composes.
func (g *savedReviews) review(c Case) ([]byte, ComposedReview, error) {
	if err := g.init(); err != nil {
		return nil, ComposedReview{}, err
	}
	which := ComposedReview{Repository: c.Bundle.Repository, Number: c.Bundle.PullRequest.Number}
	if g.Source == ComposeFromRun {
		raw, err := os.ReadFile(filepath.Join(g.Run, "cases", c.ID, "document.json"))
		if err != nil {
			return nil, which, fmt.Errorf("case %s: the run has no review document: %w", c.ID, errors.Unwrap(err))
		}
		which.Match = "run"
		return raw, which, nil
	}
	return g.stores.find(c)
}

// converted normalises a review and converts it.
func converted(document []byte) ([]byte, *tiles.Document, error) {
	normalized, err := review.NormalizeStored(document)
	if err != nil {
		return nil, nil, fmt.Errorf("normalising the review: %w", err)
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		return nil, nil, fmt.Errorf("converting the review: %w", err)
	}
	return normalized, doc, nil
}

// analysis reads the tile analysis an earlier tiles run kept beside a case
// (fills/<type>.json, exhibits.json), so presenting it again places the same
// library and authored tiles; none for another source or run.
func (g *savedReviews) analysis(c Case, doc *tiles.Document) ([]view.Fill, []view.Authored) {
	if g.Source != ComposeFromRun {
		return nil, nil
	}
	dir := filepath.Join(g.Run, "cases", c.ID)
	t := &agent.TilesOutcome{}
	names, _ := filepath.Glob(filepath.Join(dir, "fills", "*.json"))
	slices.Sort(names)
	for _, name := range names {
		var f agent.FillResult
		if raw, err := os.ReadFile(name); err == nil && json.Unmarshal(raw, &f) == nil {
			t.Fills = append(t.Fills, f)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "exhibits.json")); err == nil {
		var x agent.ExhibitsResult
		if json.Unmarshal(raw, &x) == nil {
			t.Exhibits = &x
		}
	}
	if len(t.Fills) == 0 && t.Exhibits == nil {
		return nil, nil
	}
	return generate.TileExtras(doc, t)
}

// reviewStores indexes the dataset's saved review versions by pull request.
// The stores are copied, with their write-ahead logs, and the copies opened
// read-only; the dataset is never opened for writing.
type reviewStores struct {
	dir string
	dbs []*sql.DB
	// names are the stores' paths relative to the reviews directory, by
	// index in dbs.
	names    []string
	versions map[string][]storedVersion
}

type storedVersion struct {
	store        int
	version      int
	bundleDigest string
	head         string
}

func openReviewStores(dir string) (*reviewStores, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".versions.sqlite") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("the dataset's saved reviews: %w", err)
	}
	if len(paths) == 0 {
		return nil, errors.New("the dataset has no saved reviews (reviews/*/*.versions.sqlite)")
	}
	slices.Sort(paths)
	tmp, err := os.MkdirTemp("", "baum-replay-reviews-")
	if err != nil {
		return nil, err
	}
	s := &reviewStores{dir: tmp, versions: map[string][]storedVersion{}}
	opened := false
	defer func() {
		if !opened {
			_ = s.Close()
		}
	}()
	for n, path := range paths {
		copied := filepath.Join(tmp, fmt.Sprintf("%d.sqlite", n))
		for _, suffix := range []string{"", "-wal", "-shm"} {
			raw, err := os.ReadFile(path + suffix)
			if errors.Is(err, fs.ErrNotExist) && suffix != "" {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("reading saved review store %d: %w", n, errors.Unwrap(err))
			}
			if err := os.WriteFile(copied+suffix, raw, 0o600); err != nil {
				return nil, err
			}
		}
		db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.ToSlash(copied)}).String()+"?mode=ro")
		if err != nil {
			return nil, err
		}
		s.dbs = append(s.dbs, db)
		name, _ := filepath.Rel(dir, path)
		s.names = append(s.names, filepath.ToSlash(name))
		rows, err := db.Query(`SELECT repository, pr, version, metadata FROM versions ORDER BY repository, pr, version`)
		if err != nil {
			return nil, fmt.Errorf("reading saved review store %d: %w", n, err)
		}
		for rows.Next() {
			var repository string
			var pr, version int
			var metadata []byte
			if err := rows.Scan(&repository, &pr, &version, &metadata); err != nil {
				_ = rows.Close()
				return nil, err
			}
			var meta struct {
				BundleDigest string `json:"bundle_digest"`
				Head         string `json:"head"`
			}
			_ = json.Unmarshal(metadata, &meta)
			key := fmt.Sprintf("%s#%d", repository, pr)
			s.versions[key] = append(s.versions[key], storedVersion{store: n, version: version, bundleDigest: meta.BundleDigest, head: meta.Head})
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	opened = true
	return s, nil
}

// Close closes the stores' copies and removes them.
func (s *reviewStores) Close() error {
	var errs []error
	for _, db := range s.dbs {
		errs = append(errs, db.Close())
	}
	s.dbs = nil
	errs = append(errs, os.RemoveAll(s.dir))
	return errors.Join(errs...)
}

// find returns the saved review a case composes.
func (s *reviewStores) find(c Case) ([]byte, ComposedReview, error) {
	which := ComposedReview{Repository: c.Bundle.Repository, Number: c.Bundle.PullRequest.Number}
	versions := s.versions[fmt.Sprintf("%s#%d", which.Repository, which.Number)]
	if len(versions) == 0 {
		return nil, which, fmt.Errorf("case %s: the dataset has no saved review of its pull request", c.ID)
	}
	var chosen *storedVersion
	for _, rule := range []struct {
		match string
		ok    func(storedVersion) bool
	}{
		{"bundle", func(v storedVersion) bool { return v.bundleDigest != "" && v.bundleDigest == c.Digest }},
		{"head", func(v storedVersion) bool { return v.head != "" && v.head == c.Bundle.PullRequest.HeadSHA }},
		{"latest", func(storedVersion) bool { return true }},
	} {
		for i := range versions {
			if rule.ok(versions[i]) && (chosen == nil || versions[i].version > chosen.version) {
				chosen = &versions[i]
			}
		}
		if chosen != nil {
			which.Match = rule.match
			break
		}
	}
	which.Version = chosen.version
	var document []byte
	err := s.dbs[chosen.store].QueryRow(`SELECT document FROM versions WHERE repository = ? AND pr = ? AND version = ?`,
		which.Repository, which.Number, chosen.version).Scan(&document)
	if err != nil {
		return nil, which, fmt.Errorf("case %s: reading its saved review: %w", c.ID, err)
	}
	return document, which, nil
}
