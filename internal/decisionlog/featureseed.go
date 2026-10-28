package decisionlog

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/features/assign"
	"github.com/baum-ai/baum/features/discover"
)

// Seeding (README.md, "First build"): a new version of a first build starts
// with the features of a taxonomy as provisional features with no members,
// which its pull requests may join; those none joined are deleted before
// the switch.

// buildSeed is a seeded feature no pull request has joined, as a candidate
// with its taxonomy paths.
type buildSeed struct {
	candidate assign.Candidate
}

// unseeded reports whether the version is new to seeding: it has no seeds,
// no taxonomy and no features.
func (r *featureRun) unseeded(ctx context.Context) (bool, error) {
	var taxonomy sql.NullInt64
	var features int
	err := r.w.Store.db.QueryRowContext(ctx, `SELECT taxonomy_id, (SELECT COUNT(*) FROM feature WHERE version_id=?1)
	+ (SELECT COUNT(*) FROM feature_seed WHERE version_id=?1) FROM feature_version WHERE id=?1`, r.version).Scan(&taxonomy, &features)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return err == nil && !taxonomy.Valid && features == 0, err
}

// versionCreated is when the run's version was created; now for a version
// not yet made.
func (r *featureRun) versionCreated(ctx context.Context) (time.Time, error) {
	if r.version == 0 {
		return r.now, nil
	}
	var created int64
	err := r.w.Store.db.QueryRowContext(ctx, `SELECT created_at FROM feature_version WHERE id=?`, r.version).Scan(&created)
	return time.UnixMilli(created), err
}

// willDiscover reports whether a build with the taxonomy choice would run
// discovery: an automatic choice for a version new to seeding with no
// taxonomy made after it, and a backend discovery can run on.
func (r *featureRun) willDiscover(ctx context.Context, choice string) (bool, error) {
	if choice != "" && choice != TaxonomyAuto {
		return false, nil
	}
	if fresh, err := r.unseeded(ctx); err != nil || !fresh {
		return false, err
	}
	created, err := r.versionCreated(ctx)
	if err != nil {
		return false, err
	}
	if id, _, err := r.w.Store.taxonomyAfter(ctx, r.repository, created); err != nil || id != 0 {
		return false, err
	}
	return r.planDiscovery(ctx).skip == "", nil
}

// seed seeds a version new to seeding from the build's taxonomy choice,
// running discovery when it calls for one, and loads the version's seeds no
// pull request joined. A discovery that could not run or failed leaves the
// version unseeded, with the reason in the report.
func (b *buildRun) seed(ctx context.Context) error {
	fresh, err := b.unseeded(ctx)
	if err != nil {
		return err
	}
	if fresh && b.taxonomy != TaxonomyNone {
		id, taxonomy, err := b.chooseTaxonomy(ctx)
		if err != nil {
			return err
		}
		if id != 0 {
			if err := b.w.Store.seedVersion(context.WithoutCancel(ctx), b.version, b.repository, id, taxonomy, b.now); err != nil {
				return err
			}
			b.report.Seeds = len(taxonomy.Features)
		}
	}
	if err := b.w.Store.db.QueryRowContext(ctx, `SELECT COALESCE(taxonomy_id, 0) FROM feature_version WHERE id=?`, b.version).
		Scan(&b.report.Taxonomy); err != nil {
		return err
	}
	b.seeds, err = b.w.Store.openSeeds(ctx, b.version)
	b.exclude = map[string]bool{}
	for _, seed := range b.seeds {
		b.exclude[seed.candidate.ID] = true
	}
	return err
}

// chooseTaxonomy is the taxonomy the build's choice names: a stored one, or
// for the automatic choice the newest made after the version, else a new
// discovery's. Its id is 0 when there is none, with the reason in the
// report.
func (b *buildRun) chooseTaxonomy(ctx context.Context) (int64, discover.FeatureTaxonomy, error) {
	if b.taxonomy != "" && b.taxonomy != TaxonomyAuto {
		return b.w.Store.Taxonomy(ctx, b.repository, b.taxonomy)
	}
	created, err := b.versionCreated(ctx)
	if err != nil {
		return 0, discover.FeatureTaxonomy{}, err
	}
	id, taxonomy, err := b.w.Store.taxonomyAfter(ctx, b.repository, created)
	if err != nil || id != 0 {
		return id, taxonomy, err
	}
	b.notify(BuildDiscovering)
	report, taxonomy, err := b.discover(ctx)
	b.report.Discovery = &report
	if report.Calls > 0 || report.SpentUSD != nil {
		b.calls.build.add(context.WithoutCancel(ctx), b.w.Store, b.w.logger(), report.SpentUSD)
	}
	if err != nil {
		return 0, taxonomy, err
	}
	b.report.Unseeded = report.Stopped
	return report.TaxonomyID, taxonomy, nil
}

// seedVersion creates a provisional feature with no members for each of
// the taxonomy's features, records them as the version's seeds and the
// taxonomy as the version's, in one transaction.
func (s *Store) seedVersion(ctx context.Context, version int64, repository string, taxonomyID int64, taxonomy discover.FeatureTaxonomy, now time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for i, feature := range taxonomy.Features {
			id, err := target(ctx, tx, version, repository, placeTarget{
				Title: strings.TrimSpace(feature.Title), Description: strings.TrimSpace(feature.Description)}, now)
			if err != nil {
				return err
			}
			paths, err := json.Marshal(feature.Paths)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO feature_seed(version_id, feature_id, entry, paths_json) VALUES (?,?,?,?)`,
				version, id, i, string(paths)); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE feature_version SET taxonomy_id=? WHERE id=?`, taxonomyID, version)
		return err
	})
}

// openSeeds are the version's seeded features with no members, in the
// taxonomy's order.
func (s *Store) openSeeds(ctx context.Context, version int64) ([]buildSeed, error) {
	return collect(func(rows *sql.Rows) (buildSeed, error) {
		var seed buildSeed
		var paths string
		if err := rows.Scan(&seed.candidate.ID, &seed.candidate.Title, &seed.candidate.Description, &paths); err != nil {
			return seed, err
		}
		return seed, json.Unmarshal([]byte(paths), &seed.candidate.Paths)
	})(s.db.QueryContext(ctx, `SELECT f.id, f.title, f.description, fs.paths_json FROM feature_seed fs
JOIN feature f ON f.id=fs.feature_id AND f.version_id=fs.version_id
WHERE fs.version_id=?1 AND f.merged_into IS NULL AND NOT EXISTS (SELECT 1 FROM feature_pull fp WHERE fp.version_id=?1 AND fp.feature_id=f.id)
ORDER BY fs.entry`, version))
}

// joined takes a feature a pull request joined out of the open seeds: from
// then on it is ranked like any feature.
func (b *buildRun) joined(featureID string) {
	if i := slices.IndexFunc(b.seeds, func(seed buildSeed) bool { return seed.candidate.ID == featureID }); i >= 0 {
		b.seeds = slices.Delete(b.seeds, i, i+1)
		delete(b.exclude, featureID)
		b.report.SeedsJoined++
	}
}

// deleteEmptySeeds deletes the version's seeded features no pull request
// joined, before the switch.
func (b *buildRun) deleteEmptySeeds(ctx context.Context) error {
	result, err := b.w.Store.db.ExecContext(ctx, `DELETE FROM feature WHERE version_id=?1
AND id IN (SELECT feature_id FROM feature_seed WHERE version_id=?1)
AND NOT EXISTS (SELECT 1 FROM feature_pull fp WHERE fp.version_id=?1 AND fp.feature_id=feature.id)
AND NOT EXISTS (SELECT 1 FROM feature_session fs WHERE fs.version_id=?1 AND fs.feature_id=feature.id)`, b.version)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	b.report.SeedsDeleted += int(deleted)
	b.seeds = nil
	return err
}

// withSeeds puts the seeds whose paths hold some of the item's paths among
// the item's ranked candidates, as the hosted rebuild does: each seed goes,
// best path score (discover.PathScores) first, before the first ranked
// candidate not linked to the item whose path score is lower. A seed has no
// pull requests, so the call offering one uses the seeded batch version.
func withSeeds(ranked []assign.Candidate, scores []float64, seeds []buildSeed, paths []string) []assign.Candidate {
	if len(seeds) == 0 {
		return ranked
	}
	prefixes := make([][]string, len(seeds))
	for i, seed := range seeds {
		prefixes[i] = seed.candidate.Paths
	}
	seedScores := discover.PathScores(prefixes, paths)
	type offer struct {
		candidate assign.Candidate
		score     float64
	}
	var offered []offer
	for i, seed := range seeds {
		if seedScores[i] > 0 {
			offered = append(offered, offer{seed.candidate, seedScores[i]})
		}
	}
	slices.SortStableFunc(offered, func(a, b offer) int { return cmp.Compare(b.score, a.score) })
	merged := make([]assign.Candidate, 0, len(ranked)+len(offered))
	for i, candidate := range ranked {
		links := candidate.Links
		for len(offered) > 0 && len(links.Stacked)+len(links.SharedSessions)+len(links.Mentioned) == 0 && offered[0].score > scores[i] {
			merged, offered = append(merged, offered[0].candidate), offered[1:]
		}
		merged = append(merged, candidate)
	}
	for _, o := range offered {
		merged = append(merged, o.candidate)
	}
	return merged
}
