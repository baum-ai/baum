package review

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

// A staged review is written in parts by separate model calls: the outline
// (the overview and each section's id, title, kind, hunks and assessment),
// each section's details, and the summary's codebase context. The schemas a
// stage replies against are projections of the model schema (ModelSchema),
// so they cannot drift from it, and each part is validated by merging it into
// the outline and running the same rules as a whole document, under the
// staged contract (stagedContract): a section may lack its details, and the
// summary its context.

// The fields each stage writes. The outline writes every summary field the
// model writes except the context, which the context stage writes, and the
// reading time, which the host computes (ReviewMinutes).
var (
	outlineFields        = []string{"schema", "summary", "sections", "relations"}
	outlineSummaryFields = []string{"text", "review_exchange", "change_kind", "implements", "release_note", "breaking_change", "attention", "authorship"}
	outlineSectionFields = []string{"id", "title", "kind", "hunks", "assessment"}
	detailsSectionFields = []string{"id", "summary", "attention", "risks", "evidence", "blocks", "tiles"}
)

// stagedContract is the authored contract of a document assembled from the
// stages: the strict authored contract, except that a section may carry the
// stored details state (pending or failed), with a null summary, a null
// attention and no evidence, blocks or tiles, and that the summary may lack
// its context, which a failed context stage leaves out. The model never
// writes against it: every stage's schema is a projection of the model
// schema, which offers neither.
func stagedContract(contract map[string]any) (map[string]any, error) {
	staged, err := cloneTree(contract)
	if err != nil {
		return nil, err
	}
	var stored map[string]any
	if err := json.Unmarshal(reviewSchema, &stored); err != nil {
		return nil, err
	}
	storedSection := stored["$defs"].(map[string]any)["section"].(map[string]any)
	section := staged["$defs"].(map[string]any)["section"].(map[string]any)
	properties := section["properties"].(map[string]any)
	properties["details"] = storedSection["properties"].(map[string]any)["details"]
	properties["summary"].(map[string]any)["type"] = []any{"object", "null"}
	section["required"] = removeStrings(section["required"], "summary")
	rules, _ := section["allOf"].([]any)
	for _, rule := range storedSection["allOf"].([]any) {
		condition, _ := rule.(map[string]any)["if"].(map[string]any)
		without, _ := condition["not"].(map[string]any)
		if containsString(stringSlice(without["required"]), "details") {
			rules = append(rules, rule)
		}
	}
	section["allOf"] = append(rules, map[string]any{
		"if": map[string]any{"required": []any{"details"}},
		"then": map[string]any{"properties": map[string]any{
			"attention": map[string]any{"type": "null"},
			"evidence":  map[string]any{"maxItems": 0},
			"blocks":    map[string]any{"maxItems": 0},
			"tiles":     map[string]any{"maxItems": 0},
		}},
	})
	summary := staged["properties"].(map[string]any)["summary"].(map[string]any)
	summary["required"] = removeStrings(summary["required"], "context")
	staged["title"] = "Model-authored baum review fields, assembled in stages"
	return staged, nil
}

// OutlineSchema is the schema of the outline stage's reply: the model schema
// without tiles, keeping only the document's schema, summary (without its
// context and reading time), sections (each with only its id, title, kind,
// hunks and assessment) and relations.
func (v *DocumentValidator) OutlineSchema() ([]byte, error) {
	model, err := v.ModelSchema(nil)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(model, &root); err != nil {
		return nil, err
	}
	projectSchema(root, outlineFields...)
	projectSchema(root["properties"].(map[string]any)["summary"].(map[string]any), outlineSummaryFields...)
	projectSchema(root["$defs"].(map[string]any)["section"].(map[string]any), outlineSectionFields...)
	root["title"] = "The outline of a baum review"
	root["description"] = "The overview of a pull request review and its sections' outline: each section's ID, title, kind, hunks and assessment. Section details and the codebase context are written separately."
	delete(root, "$comment")
	dropUnreferencedDefinitions(root)
	return json.Marshal(root)
}

// DetailsSchema is the schema of the section details stage's reply for the
// offered tile kinds: a list of sections, each with its id and only the
// fields of its details (summary, attention, risks, evidence, blocks and
// tiles), as the model schema (ModelSchema(kinds)) defines them.
func (v *DocumentValidator) DetailsSchema(kinds []string) ([]byte, error) {
	model, err := v.ModelSchema(kinds)
	if err != nil {
		return nil, err
	}
	var source map[string]any
	if err := json.Unmarshal(model, &source); err != nil {
		return nil, err
	}
	defs := source["$defs"].(map[string]any)
	projectSchema(defs["section"].(map[string]any), detailsSectionFields...)
	root := map[string]any{
		"type":        "object",
		"title":       "Section details of a baum review",
		"description": "The details of the sections asked for, each named by the ID the outline gave it.",
		"properties": map[string]any{
			"sections": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"$ref": "#/$defs/section"}},
		},
		"required":             []any{"sections"},
		"additionalProperties": false,
		"$defs":                defs,
	}
	dropUnreferencedDefinitions(root)
	return json.Marshal(root)
}

// ContextSchema is the schema of the context stage's reply: the summary's
// context, as the model schema defines it, under a context key.
func (v *DocumentValidator) ContextSchema() ([]byte, error) {
	model, err := v.ModelSchema(nil)
	if err != nil {
		return nil, err
	}
	var source map[string]any
	if err := json.Unmarshal(model, &source); err != nil {
		return nil, err
	}
	summary := source["properties"].(map[string]any)["summary"].(map[string]any)
	root := map[string]any{
		"type":                 "object",
		"title":                "The codebase context of a baum review",
		"properties":           map[string]any{"context": summary["properties"].(map[string]any)["context"]},
		"required":             []any{"context"},
		"additionalProperties": false,
		"$defs":                source["$defs"],
	}
	dropUnreferencedDefinitions(root)
	return json.Marshal(root)
}

// projectSchema keeps only the named properties of an object schema, the
// required names among them, and the allOf rules that name no other property.
func projectSchema(node map[string]any, keep ...string) {
	properties, _ := node["properties"].(map[string]any)
	for name := range properties {
		if !slices.Contains(keep, name) {
			delete(properties, name)
		}
	}
	required := []any{}
	for _, name := range stringSlice(node["required"]) {
		if _, ok := properties[name]; ok {
			required = append(required, name)
		}
	}
	node["required"] = required
	rules, ok := node["allOf"].([]any)
	if !ok {
		return
	}
	var kept []any
	for _, rule := range rules {
		if slices.ContainsFunc(ruleProperties(rule), func(name string) bool { return !slices.Contains(keep, name) }) {
			continue
		}
		kept = append(kept, rule)
	}
	if len(kept) == 0 {
		delete(node, "allOf")
		return
	}
	node["allOf"] = kept
}

// ruleProperties names the properties a conditional rule reads or constrains.
func ruleProperties(rule any) []string {
	var names []string
	object, _ := rule.(map[string]any)
	for _, part := range []string{"if", "then", "else"} {
		branch, _ := object[part].(map[string]any)
		properties, _ := branch["properties"].(map[string]any)
		for name := range properties {
			names = append(names, name)
		}
		names = append(names, stringSlice(branch["required"])...)
	}
	return names
}

// dropUnreferencedDefinitions removes every definition the schema does not
// reach from its root.
func dropUnreferencedDefinitions(root map[string]any) {
	defs, _ := root["$defs"].(map[string]any)
	reachable := referencedDefinitions(root, defs)
	for name := range defs {
		if !reachable[name] {
			delete(defs, name)
		}
	}
}

// OutlineSectionFields are the fields of a section the outline keeps, as
// ReadOutline and OutlineOf project it.
func OutlineSectionFields() []string { return slices.Clone(outlineSectionFields) }

// keepKeys removes every key of an object but the named ones.
func keepKeys(object map[string]any, keep ...string) {
	for key := range object {
		if !slices.Contains(keep, key) {
			delete(object, key)
		}
	}
}

// withoutSectionDetails strips a section to its outline with the given
// details state, as the staged contract requires of one.
func withoutSectionDetails(section map[string]any, state string) {
	keepKeys(section, outlineSectionFields...)
	section["details"] = state
	section["summary"] = nil
	section["attention"] = nil
	section["risks"], section["evidence"], section["blocks"] = []any{}, []any{}, []any{}
}

var sectionIdentifier = identifierRule{prefix: "sec"}

// PrepareOutline normalizes and validates the outline stage's reply. It
// returns the outline as a staged document whose sections all have pending
// details, and the findings, empty when it is valid. Only the outline fields
// are kept; the rules are those of a whole document (the schema, hunk and
// section IDs, the previous sections first in their order, at most one
// mechanical section), and every logical section with hunks must have an
// assessment.
func (v *DocumentValidator) PrepareOutline(reply []byte, b *bundlepkg.Bundle, directory string) ([]byte, string) {
	root, err := decodeObject(reply, "outline")
	if err != nil {
		return reply, "output is not a JSON document"
	}
	keepKeys(root, outlineFields...)
	if summary, ok := root["summary"].(map[string]any); ok {
		keepKeys(summary, outlineSummaryFields...)
	}
	sections, _ := root["sections"].([]any)
	for _, value := range sections {
		if section, ok := value.(map[string]any); ok {
			withoutSectionDetails(section, sectionDetailsPending)
		}
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return reply, err.Error()
	}
	prepared, findings := v.prepare(v.staged, encoded, b, directory)
	prepared = previousSectionsFirst(prepared, b.Previous)
	var outline struct {
		Sections []struct {
			ID         string          `json:"id"`
			Kind       string          `json:"kind"`
			Hunks      []string        `json:"hunks"`
			Assessment json.RawMessage `json:"assessment"`
		} `json:"sections"`
	}
	if json.Unmarshal(prepared, &outline) == nil {
		var unassessed []string
		for i, section := range outline.Sections {
			if section.Kind == "logical" && len(section.Hunks) > 0 && len(section.Assessment) == 0 {
				unassessed = append(unassessed, fmt.Sprintf("section %d (%s): a logical section with hunks needs an assessment", i+1, section.ID))
			}
		}
		findings = strings.Trim(findings+"\n"+strings.Join(unassessed, "\n"), "\n")
	}
	return prepared, findings
}

// previousSectionsFirst puts the previous version's sections a document
// holds first, in their previous order, and the others after them in theirs,
// as completion orders them, so the stages and the drafts see the sections
// where the published version will have them. A previous section the
// document leaves out stays out: completion carries it. A document that
// cannot be read is returned as it is.
func previousSectionsFirst(document []byte, previous *bundlepkg.PreviousVersion) []byte {
	if previous == nil {
		return document
	}
	root, err := decodeObject(document, "outline")
	if err != nil {
		return document
	}
	sections, ok := root["sections"].([]any)
	if !ok {
		return document
	}
	rank := map[string]int{}
	for i, section := range previous.Sections {
		rank[section.ID] = i
	}
	position := func(value any) int {
		section, _ := value.(map[string]any)
		id, _ := section["id"].(string)
		if i, ok := rank[id]; ok {
			return i
		}
		return len(rank)
	}
	if slices.IsSortedFunc(sections, func(a, b any) int { return position(a) - position(b) }) {
		return document
	}
	slices.SortStableFunc(sections, func(a, b any) int { return position(a) - position(b) })
	encoded, err := json.Marshal(root)
	if err != nil {
		return document
	}
	return encoded
}

// OutlineOf returns a staged document's outline in the shape the outline
// stage writes it: the summary without its context and reading time, and
// each section's outline fields only. A document that cannot be read is
// returned as it is.
func OutlineOf(document []byte) []byte {
	outline, err := ReadOutline(document)
	if err != nil {
		return document
	}
	return outline
}

// ReadOutline is OutlineOf for a caller that must never be handed the
// document itself: it returns the outline, or an error when the document
// cannot be read.
func ReadOutline(document []byte) ([]byte, error) {
	root, err := decodeObject(document, "outline")
	if err != nil {
		return nil, err
	}
	keepKeys(root, outlineFields...)
	if summary, ok := root["summary"].(map[string]any); ok {
		keepKeys(summary, outlineSummaryFields...)
	}
	for _, section := range jsonObjects(root["sections"]) {
		keepKeys(section, outlineSectionFields...)
	}
	return json.Marshal(root)
}

// DetailsOf returns a section's details in the shape the details stage
// writes them: its id, summary, attention, risks, evidence, blocks and tiles.
// A section that cannot be read is returned as it is.
func DetailsOf(section []byte) []byte {
	object, err := decodeObject(section, "section")
	if err != nil {
		return section
	}
	keepKeys(object, detailsSectionFields...)
	encoded, err := json.Marshal(object)
	if err != nil {
		return section
	}
	return encoded
}

// SpliceSections returns latest, the stored version as it is now (refreshed
// since, perhaps), with the sections ids replaced by those of completed, the
// same version completed again after their details were written
// (CompleteStaged), so that the details land without losing what a refresh
// brought in the meantime. What follows from the sections is taken from
// completed, which derived it from the same sections: the risk score and
// its limitations, the reading time and the risk line counts; the open
// agent findings per section are counted again from latest's agent reviews.
// The two must be the same generation (repository, pull request, version,
// head, base, merge base and generation time), and every id a section of
// both.
func (v *DocumentValidator) SpliceSections(latest, completed []byte, ids []string) ([]byte, error) {
	now, err := v.decodeFullDocument(latest)
	if err != nil {
		return nil, &StoredDocumentError{Err: err}
	}
	done, err := v.decodeFullDocument(completed)
	if err != nil {
		return nil, &StoredDocumentError{Err: err}
	}
	a, b := now.Review, done.Review
	if a.Repository != b.Repository || a.PullRequest != b.PullRequest || a.Version != b.Version || a.Head != b.Head || a.Base != b.Base || a.MergeBase != b.MergeBase || a.GeneratedAt != b.GeneratedAt {
		return nil, fmt.Errorf("the completed document is not the generation of the version")
	}
	written := map[string]documentSection{}
	for _, section := range done.Sections {
		written[section.ID] = section
	}
	checks := map[string]string{}
	for _, check := range now.Index.Checks {
		if check.Head == now.Review.Head {
			checks[check.ID] = check.Conclusion
		}
	}
	testHunks := map[string]bool{}
	for _, hunk := range now.Index.Hunks {
		testHunks[hunk.ID] = hunk.Role == "test"
	}
	sources := refreshEvidence{currentHead: now.Review.ChecksHead == now.Review.Head, checks: checks, testHunks: testHunks}
	for _, id := range ids {
		index := slices.IndexFunc(now.Sections, func(section documentSection) bool { return section.ID == id })
		section, ok := written[id]
		if index < 0 || !ok {
			return nil, fmt.Errorf("section %s is not in both documents", id)
		}
		// A refresh may have changed checks or other evidence while details were
		// being written. Keep its assessment for evidence already in the saved
		// section; newly written evidence cannot claim a verified result from
		// the older snapshot.
		current := now.Sections[index]
		statuses := make(map[string]documentEvidence, len(current.Evidence))
		for _, evidence := range current.Evidence {
			statuses[evidence.ID] = evidence
		}
		counts := map[string]int{}
		for i := range section.Evidence {
			evidence := &section.Evidence[i]
			if saved, ok := statuses[evidence.ID]; ok && saved.Kind == evidence.Kind && saved.Claim == evidence.Claim && sameAnchors(saved.Anchors, evidence.Anchors) {
				evidence.Status, evidence.Granularity = saved.Status, saved.Granularity
			} else {
				checkAnchored, onlyChecksAndHunks := false, true
				for _, anchor := range evidence.Anchors {
					checkAnchored = checkAnchored || anchor.Type == "check"
					onlyChecksAndHunks = onlyChecksAndHunks && (anchor.Type == "check" || anchor.Type == "hunk")
				}
				if checkAnchored && onlyChecksAndHunks {
					if status := sources.status(evidence.Kind, evidence.Anchors, evidence.Status); status != "" {
						evidence.Status = status
					}
				} else if evidence.Status == "verified" || evidence.Status == "observed" {
					evidence.Status = "asserted"
				}
				if evidence.Status != "verified" {
					evidence.Granularity = nil
				}
			}
			if section.Derived.State != "stale" {
				counts[evidence.Status]++
			}
		}
		section.Derived.EvidenceSummary = counts
		section.Derived.Reviewers = current.Derived.Reviewers
		now.Sections[index] = section
	}
	now.Summary.RiskScore, now.Summary.ReviewMinutes, now.Stats.RiskLOC = done.Summary.RiskScore, done.Summary.ReviewMinutes, done.Stats.RiskLOC
	countFindings(now)
	return v.encodeFullDocument(now)
}

func sameAnchors(a, b []documentAnchor) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		left, _ := json.Marshal(a[i])
		right, _ := json.Marshal(b[i])
		if !bytes.Equal(left, right) {
			return false
		}
	}
	return true
}

// StoredOutline returns the outline and the context of a stored document
// written in stages, for a host that writes again the details of its failed
// sections (agent's StagedRun.Outline): the summary and the sections the
// version wrote, not the stale ones it carried from an earlier version, with
// its relations, in the shape the outline stage writes, and the summary's
// context, nil when it has none. PrepareOutline drops the host's fields left
// in them.
func StoredOutline(document []byte) (outline, context []byte, err error) {
	root, _, err := decodeStored(document, "stored review")
	if err != nil {
		return nil, nil, err
	}
	sections := []any{}
	for _, section := range jsonObjects(root["sections"]) {
		if derived, _ := section["derived"].(map[string]any); derived["state"] == "stale" {
			continue
		}
		sections = append(sections, section)
	}
	root["sections"] = sections
	root["relations"] = []any{}
	if meta, ok := root["review"].(map[string]any); ok && meta["relations"] != nil {
		root["relations"] = meta["relations"]
	}
	if summary, ok := root["summary"].(map[string]any); ok && summary["context"] != nil {
		if context, err = json.Marshal(summary["context"]); err != nil {
			return nil, nil, err
		}
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, nil, err
	}
	return OutlineOf(encoded), context, nil
}

// SectionID returns the ID a section's details name, spelled as the outline
// spells section IDs, or "" when they name none.
func SectionID(details []byte) string {
	var named struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(details, &named) != nil || named.ID == "" {
		return ""
	}
	return sectionIdentifier.normalize(named.ID)
}

// PrepareSectionDetails merges one section's details, as the details stage
// wrote them, into the outline (PrepareOutline's document) and validates the
// section with the rules of a whole document, every other section left
// without its details. It returns the merged section, normalized, and the
// findings, empty when it is valid; every finding is the section's own, since
// the outline is valid and the other sections hold nothing to check.
func (v *DocumentValidator) PrepareSectionDetails(outline, details []byte, b *bundlepkg.Bundle, directory string) ([]byte, string) {
	root, err := decodeObject(outline, "outline")
	if err != nil {
		return nil, err.Error()
	}
	part, err := decodeObject(details, "section details")
	if err != nil {
		return nil, "the section's details are not a JSON object"
	}
	id := SectionID(details)
	sections, _ := root["sections"].([]any)
	index := slices.IndexFunc(sections, func(value any) bool {
		section, _ := value.(map[string]any)
		return section["id"] == id
	})
	if index < 0 {
		return nil, fmt.Sprintf("the outline has no section %q", id)
	}
	section := sections[index].(map[string]any)
	keepKeys(section, outlineSectionFields...)
	for _, key := range detailsSectionFields[1:] {
		if value, present := part[key]; present {
			section[key] = value
		}
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, err.Error()
	}
	prepared, findings := v.prepare(v.staged, encoded, b, directory)
	var document struct {
		Sections []json.RawMessage `json:"sections"`
	}
	if json.Unmarshal(prepared, &document) != nil || index >= len(document.Sections) {
		return nil, strings.Trim(findings+"\noutput is not a JSON document", "\n")
	}
	return document.Sections[index], findings
}

// PrepareContext merges the context stage's reply ({"context": ...}) into the
// outline (PrepareOutline's document) and validates it with the rules of a
// whole document. It returns the summary's context, normalized, and the
// findings, empty when it is valid.
func (v *DocumentValidator) PrepareContext(outline, reply []byte, b *bundlepkg.Bundle, directory string) ([]byte, string) {
	root, err := decodeObject(outline, "outline")
	if err != nil {
		return nil, err.Error()
	}
	var written struct {
		Context json.RawMessage `json:"context"`
	}
	if json.Unmarshal(reply, &written) != nil {
		return nil, "output is not a JSON document"
	}
	summary, _ := root["summary"].(map[string]any)
	if summary == nil {
		return nil, "the outline has no summary"
	}
	var context any
	if len(written.Context) > 0 && json.Unmarshal(written.Context, &context) != nil {
		return nil, "output is not a JSON document"
	}
	summary["context"] = context
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, err.Error()
	}
	prepared, findings := v.prepare(v.staged, encoded, b, directory)
	var document struct {
		Summary struct {
			Context json.RawMessage `json:"context"`
		} `json:"summary"`
	}
	if json.Unmarshal(prepared, &document) != nil {
		return nil, strings.Trim(findings+"\noutput is not a JSON document", "\n")
	}
	if len(document.Summary.Context) == 0 || string(document.Summary.Context) == "null" {
		return nil, strings.Trim(findings+"\ncontext is required: an object with at least an area", "\n")
	}
	return document.Summary.Context, findings
}

// ReusableSectionDetails returns, by section ID, the details the previous
// version holds for each outline section (PrepareOutline's document) whose
// hunks are those of the previous version's section with the same ID, when
// that section's details were complete (it has no details state): its
// summary, attention, active risks without their lifecycle fields, evidence,
// blocks and tiles, in the shape the details stage writes. A mitigated risk
// is left out, so completion keeps it mitigated. A host validates what it
// reuses (PrepareSectionDetails) and asks the model for any it rejects.
//
// Nothing is reusable when the bundle offers an image (offered) the previous
// version was not offered: only the details stage opens images and places
// them, and any section may be the one an image shows. Images often arrive
// after the code, as a session is linked later, so a version whose hunks are
// unchanged would otherwise never look at them.
func ReusableSectionDetails(previous *bundlepkg.PreviousVersion, outline []byte, offered []bundlepkg.Attachment) map[string][]byte {
	if previous == nil || NewImages(previous, offered) {
		return nil
	}
	var document struct {
		Sections []struct {
			ID    string   `json:"id"`
			Hunks []string `json:"hunks"`
		} `json:"sections"`
	}
	if json.Unmarshal(outline, &document) != nil {
		return nil
	}
	prior := map[string]bundlepkg.PreviousSection{}
	for _, section := range previous.Sections {
		prior[section.ID] = section
	}
	reusable := map[string][]byte{}
	for _, section := range document.Sections {
		old, ok := prior[section.ID]
		if !ok || !sameHunks(old.HunkContentHashes, section.Hunks) {
			continue
		}
		content, err := decodeObject(old.Content, "previous section")
		if err != nil || content["details"] != nil {
			continue
		}
		part := map[string]any{"id": section.ID}
		for _, key := range detailsSectionFields[1:] {
			if value, present := content[key]; present {
				part[key] = value
			}
		}
		risks := []any{}
		for _, risk := range jsonObjects(content["risks"]) {
			if risk["state"] == "mitigated" {
				continue
			}
			delete(risk, "state")
			delete(risk, "introduced_version")
			delete(risk, "mitigated_version")
			risks = append(risks, risk)
		}
		part["risks"] = risks
		if encoded, err := json.Marshal(part); err == nil {
			reusable[section.ID] = encoded
		}
	}
	return reusable
}

// NewImages reports whether offered holds an image the previous version was
// not offered. Image IDs are stable across versions: a description image's
// comes from its URL and a session image's from its bytes.
func NewImages(previous *bundlepkg.PreviousVersion, offered []bundlepkg.Attachment) bool {
	if previous == nil {
		return false
	}
	for _, attachment := range offered {
		if !slices.Contains(previous.Attachments, attachment.ID) {
			return true
		}
	}
	return false
}

func sameHunks(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// PreserveSectionRiskOmissions is PreserveRiskOmissionsForRepair for one
// section's details: it removes from a repair (repaired) the previous risks
// the first details of the section (assessed) left out, so a repair cannot
// reactivate them. Details that cannot be read are returned as they are.
func PreserveSectionRiskOmissions(previous *bundlepkg.PreviousVersion, assessed, repaired []byte) []byte {
	if previous == nil {
		return repaired
	}
	wrap := func(details []byte) ([]byte, error) {
		section, err := decodeObject(details, "section details")
		if err != nil {
			return nil, err
		}
		section["id"] = SectionID(details)
		if _, ok := section["risks"].([]any); !ok {
			section["risks"] = []any{}
		}
		section["hunks"] = []any{}
		return json.Marshal(map[string]any{"sections": []any{section}})
	}
	first, err := wrap(assessed)
	if err != nil {
		return repaired
	}
	repair, err := wrap(repaired)
	if err != nil {
		return repaired
	}
	preserved, err := decodeObject(PreserveRiskOmissionsForRepair(previous, first, repair), "repaired details")
	if err != nil {
		return repaired
	}
	sections := jsonObjects(preserved["sections"])
	if len(sections) != 1 {
		return repaired
	}
	original, err := decodeObject(repaired, "repaired details")
	if err != nil {
		return repaired
	}
	original["risks"] = sections[0]["risks"]
	encoded, err := json.Marshal(original)
	if err != nil {
		return repaired
	}
	return encoded
}

// StagedParts are the parts of a staged review written so far: the outline
// (PrepareOutline's document), the complete sections by ID
// (PrepareSectionDetails), the sections whose details failed, and the
// summary's context (PrepareContext), nil while it is not written.
type StagedParts struct {
	Outline  []byte
	Sections map[string][]byte
	Failed   map[string]bool
	Context  []byte
}

// AssembleStaged builds the staged document of the parts: the outline with
// each complete section in place, every other section without its details
// (failed when it is in Failed, else pending) and the context when written.
// A final document has no pending section, every section not complete being
// failed, and its reading time set (ReviewMinutes); a draft has neither.
func AssembleStaged(parts StagedParts, final bool) ([]byte, error) {
	root, err := decodeObject(parts.Outline, "outline")
	if err != nil {
		return nil, err
	}
	sections, _ := root["sections"].([]any)
	for i, value := range sections {
		section, ok := value.(map[string]any)
		if !ok {
			continue
		}
		id, _ := section["id"].(string)
		if complete, ok := parts.Sections[id]; ok {
			var merged map[string]any
			if err := json.Unmarshal(complete, &merged); err != nil {
				return nil, fmt.Errorf("section %s: %w", id, err)
			}
			sections[i] = merged
			continue
		}
		state := sectionDetailsPending
		if final || parts.Failed[id] {
			state = sectionDetailsFailed
		}
		withoutSectionDetails(section, state)
	}
	summary, _ := root["summary"].(map[string]any)
	if summary == nil {
		return nil, fmt.Errorf("the outline has no summary")
	}
	delete(summary, "context")
	delete(summary, "review_minutes")
	if len(parts.Context) > 0 {
		var context any
		if err := json.Unmarshal(parts.Context, &context); err != nil {
			return nil, fmt.Errorf("context: %w", err)
		}
		summary["context"] = context
	}
	if final {
		summary["review_minutes"] = reviewMinutesOf(root)
	}
	return json.Marshal(root)
}

// PrepareStaged is Prepare for a document a staged generator assembled
// (AssembleStaged): complete sections meet the authored rules, sections
// without their details the stored ones, and the summary may lack its
// context.
func (v *DocumentValidator) PrepareStaged(document []byte, b *bundlepkg.Bundle, directory string) ([]byte, string) {
	return v.prepare(v.staged, document, b, directory)
}

// CompleteStaged is CompleteWithPrompt for a document a staged generator
// assembled: it validates it as PrepareStaged does, then derives the
// host-owned fields exactly as for a document written in one pass. A host
// completes a draft with pending sections the same way to show it, and
// publishes or saves only a document without them (FailPendingDetails).
func (v *DocumentValidator) CompleteStaged(document []byte, b *bundlepkg.Bundle, directory string, model, promptVersion string, now time.Time) ([]byte, string) {
	return v.complete(v.staged, document, b, directory, model, promptVersion, now)
}

const readingWordsPerMinute = 400

// ReviewMinutes estimates the whole minutes a reader needs to read an
// authored review document once: the words of the overview (the summary's
// text, attention, review exchange, release note and breaking change), the
// context, and each section's title, summary, attention, behavior steps
// (title, text, before, after and question), risks, evidence (claim, detail
// and expects) and decisions (title, text, rationale, trigger, rejected
// alternative and evidence), at 400 words a minute. Code excerpts add no time.
// The estimate is rounded up and kept within 1 to 240, the schema's range.
// A document that cannot be read takes 1 minute.
func ReviewMinutes(document []byte) int {
	root, err := decodeObject(document, "review")
	if err != nil {
		return minReviewMinutes
	}
	return reviewMinutesOf(root)
}

func reviewMinutesOf(root map[string]any) int {
	words := 0
	count := func(object map[string]any, keys ...string) {
		for _, key := range keys {
			if text, ok := object[key].(string); ok {
				words += len(strings.Fields(text))
			}
		}
	}
	object := func(value any) map[string]any {
		result, _ := value.(map[string]any)
		return result
	}
	summary := object(root["summary"])
	count(summary, "text", "release_note", "breaking_change")
	count(object(summary["attention"]), "text")
	count(object(summary["review_exchange"]), "summary")
	for _, part := range object(summary["context"]) {
		count(object(part), "text")
	}
	for _, section := range jsonObjects(root["sections"]) {
		count(section, "title")
		count(object(section["summary"]), "text")
		count(object(section["attention"]), "text")
		for _, risk := range jsonObjects(section["risks"]) {
			count(risk, "text")
		}
		for _, evidence := range jsonObjects(section["evidence"]) {
			count(evidence, "claim", "detail", "expects")
		}
		for _, block := range jsonObjects(section["blocks"]) {
			switch block["kind"] {
			case "behavior":
				for _, step := range jsonObjects(block["steps"]) {
					count(step, "title", "text", "before", "after", "question")
				}
			case "decisions":
				for _, decision := range jsonObjects(block["items"]) {
					count(decision, "title", "text", "rationale", "trigger", "rejected", "evidence")
				}
			}
		}
	}
	minutes := float64(words) / readingWordsPerMinute
	return int(min(max(math.Ceil(minutes), minReviewMinutes), maxReviewMinutes))
}
