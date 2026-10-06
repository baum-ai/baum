package review

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// sectionItemSchemas validates a section's list items one at a time. The
// schema library reports the first failing schema path of a whole section,
// which names neither the item nor, for a oneOf, the variant the model meant;
// a repair given that message tends to return the document unchanged.
type sectionItemSchemas struct {
	evidence, risk, attention *jsonschema.Resolved
	blocks, tiles             map[string]*jsonschema.Resolved
}

func newSectionItemSchemas(defs map[string]any) (*sectionItemSchemas, error) {
	properties := defs["section"].(map[string]any)["properties"].(map[string]any)
	evidence, err := resolveWithDefs(properties["evidence"].(map[string]any)["items"], defs)
	if err != nil {
		return nil, err
	}
	risk, err := resolveWithDefs(properties["risks"].(map[string]any)["items"], defs)
	if err != nil {
		return nil, err
	}
	attention := properties["attention"].(map[string]any)["oneOf"].([]any)[1]
	resolvedAttention, err := resolveWithDefs(attention, defs)
	if err != nil {
		return nil, err
	}
	blocks, err := kindVariants(defs, "block")
	if err != nil {
		return nil, err
	}
	tiles, err := kindVariants(defs, "tile")
	if err != nil {
		return nil, err
	}
	return &sectionItemSchemas{evidence: evidence, risk: risk, attention: resolvedAttention, blocks: blocks, tiles: tiles}, nil
}

// kindVariants resolves each oneOf variant of a definition discriminated by
// a constant kind.
func kindVariants(defs map[string]any, name string) (map[string]*jsonschema.Resolved, error) {
	variants := map[string]*jsonschema.Resolved{}
	for _, variant := range defs[name].(map[string]any)["oneOf"].([]any) {
		kind, _ := variant.(map[string]any)["properties"].(map[string]any)["kind"].(map[string]any)["const"].(string)
		resolved, err := resolveWithDefs(variant, defs)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", name, kind, err)
		}
		variants[kind] = resolved
	}
	return variants, nil
}

func resolveWithDefs(node any, defs map[string]any) (*jsonschema.Resolved, error) {
	object := maps.Clone(node.(map[string]any))
	object["$defs"] = defs
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return nil, err
	}
	return schema.Resolve(nil)
}

// findings names each failing item of a section the schema rejected. The
// rest of the section is checked with the failing lists emptied, so a
// section-level problem is still reported alongside them.
func (s *sectionItemSchemas) findings(location string, section map[string]any, sectionSchema *jsonschema.Resolved) []string {
	var findings []string
	remainder := maps.Clone(section)
	if attention := section["attention"]; attention != nil {
		if err := s.attention.Validate(attention); err != nil {
			findings = append(findings, attentionFindings(location, attention, err)...)
			// A malformed optional attention must not hide unrelated section
			// violations behind the section schema's oneOf error.
			remainder["attention"] = nil
		}
	}
	evidence := Objects(section["evidence"])
	for _, item := range evidence {
		if err := s.evidence.Validate(item); err != nil {
			findings = append(findings, fmt.Sprintf("%s evidence %v: %s", location, item["id"], evidenceFinding(item, err)))
			remainder["evidence"] = []any{}
		}
	}
	for _, risk := range Objects(section["risks"]) {
		where := fmt.Sprintf("%s risk %v", location, risk["id"])
		tiles := s.tileFindings(where, risk["tiles"])
		withoutTiles := maps.Clone(risk)
		delete(withoutTiles, "tiles")
		if err := s.risk.Validate(withoutTiles); err != nil {
			tiles = append(tiles, where+": "+conciseSchemaError(err))
		}
		if len(tiles) > 0 {
			findings = append(findings, tiles...)
			remainder["risks"] = []any{}
		}
	}
	for i, block := range Objects(section["blocks"]) {
		where := fmt.Sprintf("%s block %d", location, i+1)
		variant, known := s.blocks[fmt.Sprint(block["kind"])]
		if !known {
			findings = append(findings, fmt.Sprintf("%s: kind %q is not a schema-defined block kind; use one of %s", where, fmt.Sprint(block["kind"]), strings.Join(slices.Sorted(maps.Keys(s.blocks)), ", ")))
			remainder["blocks"] = []any{}
			continue
		}
		if err := variant.Validate(block); err != nil {
			findings = append(findings, fmt.Sprintf("%s (%v): %s", where, block["kind"], conciseSchemaError(err)))
			remainder["blocks"] = []any{}
		}
	}
	if tiles := s.tileFindings(location, section["tiles"]); len(tiles) > 0 {
		findings = append(findings, tiles...)
		remainder["tiles"] = []any{}
	}
	if len(findings) == 0 {
		return nil
	}
	if err := sectionSchema.Validate(remainder); err != nil {
		findings = append(findings, location+": "+conciseSchemaError(err))
	}
	return findings
}

var attentionReferencePattern = regexp.MustCompile(`^(ev|rsk|dec)_[a-z0-9_]+$`)

func attentionFindings(location string, value any, validationErr error) []string {
	attention, ok := value.(map[string]any)
	if !ok {
		return []string{location + " attention: " + conciseSchemaError(validationErr)}
	}
	refs, ok := attention["refs"].([]any)
	if !ok || len(refs) == 0 {
		return []string{location + " attention: refs must contain at least one evidence (ev_*), risk (rsk_*), or decision (dec_*) ID from this section; otherwise set attention to null"}
	}
	var findings []string
	for i, raw := range refs {
		ref, ok := raw.(string)
		if ok && attentionReferencePattern.MatchString(ref) {
			continue
		}
		where := fmt.Sprintf("%s attention ref %d (%q)", location, i+1, fmt.Sprint(raw))
		if ok && strings.HasPrefix(ref, "h_") {
			findings = append(findings, where+": hunk IDs cannot be attention references; use an evidence (ev_*), risk (rsk_*), or decision (dec_*) ID from this section, or set attention to null")
		} else {
			findings = append(findings, where+": attention references must be evidence (ev_*), risk (rsk_*), or decision (dec_*) IDs from this section, or attention must be null")
		}
	}
	if len(findings) == 0 {
		return []string{location + " attention: " + conciseSchemaError(validationErr)}
	}
	return findings
}

func (s *sectionItemSchemas) tileFindings(location string, value any) []string {
	var findings []string
	for _, tile := range Objects(value) {
		where := fmt.Sprintf("%s tile %v", location, tile["id"])
		variant, known := s.tiles[fmt.Sprint(tile["kind"])]
		if !known {
			findings = append(findings, fmt.Sprintf("%s: kind %q is not a catalogue tile kind; use one of %s", where, fmt.Sprint(tile["kind"]), strings.Join(slices.Sorted(maps.Keys(s.tiles)), ", ")))
			continue
		}
		if err := variant.Validate(tile); err != nil {
			findings = append(findings, fmt.Sprintf("%s (%v): %s", where, tile["kind"], conciseSchemaError(err)))
		}
	}
	return findings
}

// evidenceStatusRules state the conditional rules of the review schema per
// evidence status, which a schema error only names by their allOf position.
var evidenceStatusRules = map[string]string{
	"verified": "verified evidence needs a check anchor and a granularity of job or test, and verified test evidence also needs a hunk, file or bot_report anchor; cite them or use status asserted",
	"observed": "observed evidence needs a session_event anchor; cite one or use status asserted",
	"missing":  "missing evidence needs expects, saying what evidence would settle the claim",
	"reported": "reported evidence needs at least one anchor and a source of author_human, author_agent, agent_review or human_review; otherwise use status asserted",
}

func evidenceFinding(item map[string]any, err error) string {
	message := conciseSchemaError(err)
	if rule, ok := evidenceStatusRules[fmt.Sprint(item["status"])]; ok && strings.Contains(err.Error(), "/allOf/") {
		return rule + " (" + message + ")"
	}
	return message
}

// conciseSchemaError drops the chain of enclosing schema locations the
// library prefixes to a violation, keeping the innermost location and the
// violation itself.
func conciseSchemaError(err error) string {
	message := err.Error()
	location := ""
	for {
		rest, found := strings.CutPrefix(message, "validating ")
		if !found {
			break
		}
		path, violation, found := strings.Cut(rest, ": ")
		if !found {
			break
		}
		location, message = path, violation
	}
	if location == "" || location == "root" {
		return message
	}
	return message + " (at " + location + ")"
}
