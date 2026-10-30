package discover

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/baum-ai/baum/features/assign"
)

// Schema is the discovery reply's schema: the taxonomy's features.
var Schema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["features"],"properties":{"features":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["title","description","paths","examples"],"properties":{"title":{"type":"string"},"description":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}},"examples":{"type":"array","items":{"type":"integer"}}}}}}}`)

// Reply is a discovery reply.
type Reply struct {
	Features []TaxonomyFeature `json:"features"`
}

// Check is the task's check of a reply the schema accepts: the taxonomy's
// bounds. It returns the findings, empty for a valid reply.
func Check(document []byte) string {
	var reply Reply
	err := json.Unmarshal(document, &reply)
	if err == nil {
		err = ValidateTaxonomyFeatures(reply.Features)
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

// ComposePrompt is a discovery prompt: frame with the definition of a
// feature of the single-item assigner prompt singlePrompt, cut out
// unchanged, in place of {{definition}}, so discovery and assignment group
// at the same level.
func ComposePrompt(frame, singlePrompt string) (string, error) {
	definition, _, err := assign.CutDefinition(singlePrompt)
	if err != nil {
		return "", fmt.Errorf("composing the discovery prompt: %w", err)
	}
	return strings.ReplaceAll(frame, "{{definition}}", definition), nil
}

// PublicVersion is the published discovery prompt's version, composed from
// assign.PublicVersion.
const PublicVersion = "feature-discover-public-v1"

//go:embed prompts/feature-discover-public-v1-frame.txt
var publicFrame string

// PublicPrompt is the published discovery prompt.
var PublicPrompt = func() string {
	single, _ := assign.Public.Prompt(assign.PublicVersion)
	prompt, err := ComposePrompt(publicFrame, single)
	if err != nil {
		panic(err)
	}
	return prompt
}()

// Under reports whether a repository path is under a taxonomy path prefix:
// the prefix itself or in the folder it names, with or without a trailing
// "/".
func Under(path, prefix string) bool {
	prefix = strings.TrimSuffix(prefix, "/")
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// PathScores are the path scores of seeds, each seed's taxonomy path
// prefixes, for an item touching paths: the sum, over the item's distinct
// paths under a seed's prefixes, of one over the number of seeds holding the
// path.
func PathScores(seeds [][]string, paths []string) []float64 {
	scores := make([]float64, len(seeds))
	for _, path := range slices.Compact(slices.Sorted(slices.Values(paths))) {
		var holding []int
		for i, prefixes := range seeds {
			if slices.ContainsFunc(prefixes, func(prefix string) bool { return Under(path, prefix) }) {
				holding = append(holding, i)
			}
		}
		for _, i := range holding {
			scores[i] += 1 / float64(len(holding))
		}
	}
	return scores
}
