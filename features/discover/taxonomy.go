// Package discover holds feature discovery's pure parts (features/README.md,
// "Discovery"): the taxonomy a discovery task replies with and its bounds,
// the pull request history digest the task reads, the task's prompt and
// reply schema, and the path score a seeded feature is offered by. It runs
// no task and reads no database.
package discover

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// TaxonomySchemaVersion is the schema version of FeatureTaxonomy and
// FeatureSeeds.
const TaxonomySchemaVersion = 1

// Bounds of a taxonomy. A feature's text has the bounds of a feature the
// assigner starts.
const (
	MaxTaxonomyFeatures         = 300
	MaxTaxonomyTitleRunes       = 120
	MaxTaxonomyDescriptionRunes = 1000
)

// FeatureTaxonomy is a taxonomy document: a repository's features as
// discovered from its code, which a build or rebuild seeds its new version
// with.
type FeatureTaxonomy struct {
	SchemaVersion int    `json:"schema_version"`
	RepositoryID  string `json:"repository_id"`
	// Revision is the commit the checkout was at.
	Revision  string            `json:"revision"`
	CreatedAt time.Time         `json:"created_at"`
	Features  []TaxonomyFeature `json:"features"`
}

// TaxonomyFeature is one discovered feature: its text, the path prefixes
// its code lives under (Under), and pull requests that are examples of its
// work.
type TaxonomyFeature struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Paths       []string `json:"paths"`
	Examples    []int32  `json:"examples"`
}

// ErrInvalidTaxonomy is a taxonomy that does not parse, belongs to another
// repository or is over its bounds.
var ErrInvalidTaxonomy = errors.New("invalid feature taxonomy")

// ParseFeatureTaxonomy decodes a taxonomy of repositoryID and validates it.
func ParseFeatureTaxonomy(data []byte, repositoryID string) (FeatureTaxonomy, error) {
	var taxonomy FeatureTaxonomy
	if err := json.Unmarshal(data, &taxonomy); err != nil {
		return FeatureTaxonomy{}, fmt.Errorf("%w: %v", ErrInvalidTaxonomy, err)
	}
	if err := taxonomy.Validate(repositoryID); err != nil {
		return FeatureTaxonomy{}, err
	}
	return taxonomy, nil
}

// Validate checks that the taxonomy is of repositoryID and within its
// bounds.
func (t FeatureTaxonomy) Validate(repositoryID string) error {
	switch {
	case t.SchemaVersion != TaxonomySchemaVersion:
		return invalid("schema version %d, want %d", t.SchemaVersion, TaxonomySchemaVersion)
	case t.RepositoryID != repositoryID:
		return invalid("it is of repository %q, not %q", t.RepositoryID, repositoryID)
	}
	return ValidateTaxonomyFeatures(t.Features)
}

// ValidateTaxonomyFeatures checks a taxonomy's features against its bounds.
func ValidateTaxonomyFeatures(features []TaxonomyFeature) error {
	if len(features) == 0 || len(features) > MaxTaxonomyFeatures {
		return invalid("it has %d features, not 1 to %d", len(features), MaxTaxonomyFeatures)
	}
	for i, feature := range features {
		title, description := strings.TrimSpace(feature.Title), strings.TrimSpace(feature.Description)
		switch {
		case title == "":
			return invalid("feature %d has no title", i+1)
		case strings.ContainsRune(title+description, 0):
			return invalid("feature %d's text holds a NUL", i+1)
		case utf8.RuneCountInString(title) > MaxTaxonomyTitleRunes || utf8.RuneCountInString(description) > MaxTaxonomyDescriptionRunes:
			return invalid("feature %d's title or description is longer than %d or %d characters", i+1, MaxTaxonomyTitleRunes, MaxTaxonomyDescriptionRunes)
		}
	}
	return nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidTaxonomy, fmt.Sprintf(format, args...))
}

// FeatureSeeds is the seed mapping of a version: the features a build
// seeded it with from a taxonomy. A seeded feature's paths are kept with the
// mapping only, never with the feature.
type FeatureSeeds struct {
	SchemaVersion int           `json:"schema_version"`
	TaxonomyID    string        `json:"taxonomy_id"`
	Seeds         []FeatureSeed `json:"seeds"`
}

// FeatureSeed is a feature seeded from the taxonomy's feature at Entry (its
// index in the taxonomy's features), with that feature's paths.
type FeatureSeed struct {
	FeatureID string   `json:"feature_id"`
	Entry     int      `json:"entry"`
	Paths     []string `json:"paths"`
}
