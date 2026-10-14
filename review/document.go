// Package review owns the review document model: the document types, the
// strict, lenient and stored views of the baum.review/1 schema, normalisation of
// model output, validation with located findings, derived fields, version
// history and model-free refresh, plus the tile catalogue.
package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
	reviewschema "github.com/lastpersonlabs/baum/schema"
)

// reviewSchema is the review document schema, owned by the open-source baum
// module in oss/schema.
var reviewSchema = reviewschema.Document

// DocumentValidator holds three views of the review schema: Schema is the
// lenient contract the model writes against, strict is the authored contract
// a normalized document must satisfy before completion, and full is the
// stored document with the host's derived fields.
type DocumentValidator struct {
	Schema   []byte
	Revision string
	strict   map[string]any
	resolved *jsonschema.Resolved
	section  *jsonschema.Resolved
	// rest is the authored contract with the sections' content left
	// unchecked, so document-level violations are reported even when every
	// section has its own.
	rest  *jsonschema.Resolved
	full  *jsonschema.Resolved
	items *sectionItemSchemas
}

// Contract returns a copy of the strict authored schema as a decoded tree, with
// its $defs; changing it does not affect the validator.
//
// It is exported for hosts that inspect the authored contract, such as in
// their tests.
func (v *DocumentValidator) Contract() map[string]any {
	encoded, err := json.Marshal(v.strict)
	if err != nil {
		return nil
	}
	var contract map[string]any
	if json.Unmarshal(encoded, &contract) != nil {
		return nil
	}
	return contract
}

// NewDocumentValidator compiles the embedded review schema once. The result is
// read-only, so it may be shared between goroutines.
func NewDocumentValidator() (*DocumentValidator, error) {
	var fullSchema jsonschema.Schema
	if err := json.Unmarshal(reviewSchema, &fullSchema); err != nil {
		return nil, err
	}
	full, err := fullSchema.Resolve(nil)
	if err != nil {
		return nil, err
	}
	var contract map[string]any
	if err := json.Unmarshal(reviewSchema, &contract); err != nil {
		return nil, err
	}
	props := contract["properties"].(map[string]any)
	review := props["review"].(map[string]any)["properties"].(map[string]any)
	props["relations"] = review["relations"]
	for _, key := range []string{"review", "stats", "index", "agent_reviews", "unassigned_hunks"} {
		delete(props, key)
	}
	contract["required"] = []string{"schema", "summary", "sections", "relations"}
	delete(contract, "$id")
	delete(contract, "$schema")
	contract["title"] = "Model-authored baum review fields"
	summary := props["summary"].(map[string]any)
	omitProperties(summary, "title", "author", "risk_score")
	authorship := summary["properties"].(map[string]any)["authorship"].(map[string]any)
	omitProperties(authorship, "observed_sessions", "observed_vendors", "observed_models")
	exchange := summary["properties"].(map[string]any)["review_exchange"].(map[string]any)
	omitProperties(exchange, "reviews", "back_and_forths", "critical_issues_found", "changes_after_open")
	defs := contract["$defs"].(map[string]any)
	omitProperties(defs["section"].(map[string]any), "derived")
	// The stale-state rule constrains a derived field the model never writes.
	var authoredRules []any
	for _, rule := range defs["section"].(map[string]any)["allOf"].([]any) {
		then, _ := rule.(map[string]any)["then"].(map[string]any)
		if properties, _ := then["properties"].(map[string]any); properties["derived"] == nil {
			authoredRules = append(authoredRules, rule)
		}
	}
	defs["section"].(map[string]any)["allOf"] = authoredRules
	risks := defs["section"].(map[string]any)["properties"].(map[string]any)["risks"].(map[string]any)["items"].(map[string]any)
	omitProperties(risks, "state", "introduced_version", "mitigated_version")
	delete(risks, "allOf")
	encoded, err := json.Marshal(contract)
	if err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return nil, err
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, err
	}
	sectionContract, err := json.Marshal(map[string]any{"$ref": "#/$defs/section", "$defs": defs})
	if err != nil {
		return nil, err
	}
	var sectionSchema jsonschema.Schema
	if err := json.Unmarshal(sectionContract, &sectionSchema); err != nil {
		return nil, err
	}
	section, err := sectionSchema.Resolve(nil)
	if err != nil {
		return nil, err
	}
	var restContract map[string]any
	if err := json.Unmarshal(encoded, &restContract); err != nil {
		return nil, err
	}
	restContract["properties"].(map[string]any)["sections"] = map[string]any{"type": "array"}
	restEncoded, err := json.Marshal(restContract)
	if err != nil {
		return nil, err
	}
	var restSchema jsonschema.Schema
	if err := json.Unmarshal(restEncoded, &restSchema); err != nil {
		return nil, err
	}
	rest, err := restSchema.Resolve(nil)
	if err != nil {
		return nil, err
	}
	lenient, err := lenientContract(contract)
	if err != nil {
		return nil, err
	}
	modelSchema, err := json.Marshal(lenient)
	if err != nil {
		return nil, err
	}
	var check jsonschema.Schema
	if err := json.Unmarshal(modelSchema, &check); err != nil {
		return nil, err
	}
	if _, err := check.Resolve(nil); err != nil {
		return nil, fmt.Errorf("lenient review contract: %w", err)
	}
	items, err := newSectionItemSchemas(defs)
	if err != nil {
		return nil, err
	}
	revision := defs["schemaRevision"].(map[string]any)["const"].(string)
	return &DocumentValidator{Schema: modelSchema, Revision: revision, strict: contract, resolved: resolved, section: section, rest: rest, full: full, items: items}, nil
}

// schemaFindings locates a schema violation. The library reports only the
// first violation it meets, so each section is checked on its own and the rest
// of the document separately; a repair then sees every failing section and
// any document-level problem at once instead of one per pass.
func (v *DocumentValidator) schemaFindings(document []byte, err error) []string {
	var outline struct {
		Sections []json.RawMessage `json:"sections"`
	}
	var value any
	if json.Unmarshal(document, &outline) != nil || json.Unmarshal(document, &value) != nil {
		return []string{err.Error()}
	}
	var findings []string
	for i, raw := range outline.Sections {
		var section any
		if json.Unmarshal(raw, &section) != nil {
			return []string{err.Error()}
		}
		if sectionErr := v.section.Validate(section); sectionErr != nil {
			var named struct {
				ID any `json:"id"`
			}
			_ = json.Unmarshal(raw, &named)
			id, _ := named.ID.(string)
			if id == "" {
				id = "unnamed"
			}
			location := fmt.Sprintf("section %d (%s)", i+1, id)
			object, _ := section.(map[string]any)
			if located := v.items.findings(location, object, v.section); len(located) > 0 {
				findings = append(findings, located...)
			} else {
				findings = append(findings, location+": "+conciseSchemaError(sectionErr))
			}
		}
	}
	if restErr := v.rest.Validate(value); restErr != nil {
		findings = append(findings, conciseSchemaError(restErr))
	}
	if len(findings) == 0 {
		return []string{err.Error()}
	}
	return findings
}

type sectionOutline struct {
	ID    string   `json:"id"`
	Kind  string   `json:"kind"`
	Hunks []string `json:"hunks"`
}

// sectionOutlineFindings checks the rules that need only section IDs, kinds and
// hunk lists, so they also hold for a document the schema rejected.
func sectionOutlineFindings(sections []sectionOutline, bundle *bundlepkg.Bundle) []string {
	hunks := map[string]bool{}
	for _, h := range bundle.Diff {
		hunks[h.ID] = true
	}
	previous := map[string]bool{}
	if bundle.Previous != nil {
		for _, section := range bundle.Previous.Sections {
			previous[section.ID] = true
		}
	}
	ids := map[string]bool{}
	var findings []string
	mechanical := 0
	for i, section := range sections {
		if ids[section.ID] {
			findings = append(findings, "duplicate section ID: "+section.ID)
		}
		ids[section.ID] = true
		if len(section.Hunks) == 0 && !previous[section.ID] {
			findings = append(findings, "section "+section.ID+" has no hunks; only a section of the previous review may be kept without hunks")
		}
		if bundle.Previous != nil && i < len(bundle.Previous.Sections) && section.ID != bundle.Previous.Sections[i].ID {
			findings = append(findings, "previous sections must remain first in their original order with the same IDs")
		}
		// A hunk may belong to several sections or to none: completion
		// lists the uncovered ones in unassigned_hunks.
		for _, id := range section.Hunks {
			if !hunks[id] {
				findings = append(findings, "unknown assigned hunk: "+id)
			}
		}
		if section.Kind == "mechanical" {
			mechanical++
			if mechanical > 1 {
				findings = append(findings, "more than one mechanical section")
			}
		}
	}
	if bundle.Previous != nil && len(sections) < len(bundle.Previous.Sections) {
		findings = append(findings, "previous sections cannot be removed")
	}
	return findings
}

func omitProperties(schema map[string]any, keys ...string) {
	props := schema["properties"].(map[string]any)
	for _, key := range keys {
		delete(props, key)
	}
	var required []any
	for _, key := range schema["required"].([]any) {
		if _, ok := props[key.(string)]; ok {
			required = append(required, key)
		}
	}
	schema["required"] = required
}

// Validate checks a complete stored document against the schema and the
// document's semantic rules, and returns every finding in one pass, or "" when
// it is valid.
func (v *DocumentValidator) Validate(document []byte, bundle *bundlepkg.Bundle) string {
	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		return "output is not a JSON document"
	}
	if err := v.resolved.Validate(value); err != nil {
		return err.Error()
	}
	var doc struct {
		Stack    *documentStack    `json:"stack"`
		Sections []authoredSection `json:"sections"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		return err.Error()
	}
	attachments := map[string]bool{}
	if bundle != nil {
		for _, attachment := range bundle.Attachments {
			attachments[attachment.ID] = true
		}
	}
	findings, err := ruleFindings(value, doc.Stack, doc.Sections, bundle, attachments)
	if err != nil {
		return err.Error()
	}
	return strings.Join(findings, "\n")
}

// ValidateStored checks a stored baum.review/1 document against the full
// schema and the document's semantic rules, and returns every finding, or nil
// when it is valid. Images are checked against the document's own index. The
// rules that need the generation bundle run only when bundle is non-nil.
func (v *DocumentValidator) ValidateStored(document []byte, bundle *bundlepkg.Bundle) []string {
	value, err := decodeObject(document, "document")
	if err != nil {
		return []string{err.Error()}
	}
	labelBlankSectionTitles(value)
	if err := v.full.Validate(value); err != nil {
		return []string{conciseSchemaError(err)}
	}
	var doc struct {
		Stack    *documentStack    `json:"stack"`
		Sections []authoredSection `json:"sections"`
		Index    struct {
			Attachments []struct {
				ID string `json:"id"`
			} `json:"attachments"`
		} `json:"index"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		return []string{err.Error()}
	}
	listed := map[string]bool{}
	for _, attachment := range doc.Index.Attachments {
		listed[attachment.ID] = true
	}
	findings, err := ruleFindings(value, doc.Stack, doc.Sections, bundle, listed)
	if err != nil {
		return []string{err.Error()}
	}
	return findings
}

// ruleFindings applies the semantic rules shared by the authored and stored
// views of a document. The rules that need the generation bundle (hunk line
// ranges, the section outline, evidence against checks, restricted sessions)
// run only when bundle is non-nil; attachments lists the image IDs a document
// may name.
func ruleFindings(value any, stack *documentStack, sections []authoredSection, bundle *bundlepkg.Bundle, attachments map[string]bool) ([]string, error) {
	findings := append(validateTileReferences(sections), stackFindings(stack, sections)...)
	hunks := map[string]bundlepkg.Hunk{}
	checks := map[string]bundlepkg.Check{}
	restricted := map[string]bool{}
	if bundle != nil {
		for _, h := range bundle.Diff {
			hunks[h.ID] = h
		}
		for _, check := range bundle.Checks {
			checks[check.ID] = check
		}
		for _, session := range bundle.Sessions {
			restricted[session.RootSessionID] = session.Restricted
		}
	}
	walkJSON(value, func(candidate map[string]any) {
		if bundle == nil || candidate["type"] != "hunk" || candidate["lines"] == nil {
			return
		}
		encoded, err := json.Marshal(candidate)
		if err != nil {
			return
		}
		var anchor documentAnchor
		if json.Unmarshal(encoded, &anchor) != nil {
			return
		}
		if hunk, exists := hunks[anchor.identifier()]; exists {
			if finding := hunkLineRangeFinding(anchor.Lines, hunk); finding != "" {
				findings = append(findings, finding)
			}
		}
	})
	outlines := make([]sectionOutline, 0, len(sections))
	for _, section := range sections {
		outlines = append(outlines, sectionOutline{ID: section.ID, Kind: section.Kind, Hunks: section.Hunks})
	}
	if bundle != nil {
		findings = append(findings, sectionOutlineFindings(outlines, bundle)...)
	}
	for sectionIndex, section := range sections {
		items := map[string]bool{}
		addID := func(id string) bool {
			if items[id] {
				return false
			}
			items[id] = true
			return true
		}
		for _, risk := range section.Risks {
			if !addID(risk.ID) {
				findings = append(findings, "duplicate risk ID: "+risk.ID)
			}
			if section.Kind == "mechanical" && risk.Tier != "low" && risk.Tier != "mechanical" {
				findings = append(findings, "mechanical section carries a risk above low")
			}
		}
		for _, evidence := range section.Evidence {
			if !addID(evidence.ID) {
				findings = append(findings, "duplicate evidence ID: "+evidence.ID)
			}
			if bundle != nil && evidence.Status == "verified" {
				success, test := false, false
				for _, anchor := range evidence.Anchors {
					id := anchor.identifier()
					if anchor.Type == "check" && checks[id].Conclusion == "success" {
						success = true
					}
					if anchor.Type == "check" {
						if check, exists := checks[id]; exists && check.Conclusion != "success" {
							findings = append(findings, "verified evidence cites a check that did not succeed")
						}
					}
					if anchor.Type == "hunk" && hunks[id].Role == "test" {
						test = true
					}
					if anchor.Type == "bot_report" {
						for _, report := range bundle.Conversation {
							if report.ID == id && report.BotReport {
								test = true
							}
						}
					}
				}
				if success && evidence.Kind == "test" && !test {
					findings = append(findings, "verified test evidence requires a test hunk or recognized report")
				}
			}
		}
		dataKinds := map[string]bool{}
		var blocks []map[string]any
		if err := json.Unmarshal(section.Blocks, &blocks); err != nil {
			return nil, err
		}
		for _, block := range blocks {
			kind, _ := block["kind"].(string)
			if !strings.Contains("|diagram|code|media|images|repro|table|", "|"+kind+"|") {
				if dataKinds[kind] {
					findings = append(findings, "duplicate data block: "+kind)
				}
				dataKinds[kind] = true
			}
			if finding := unlistedImageFinding(block, attachments); finding != "" {
				findings = append(findings, fmt.Sprintf("section %d (%s) %s", sectionIndex+1, section.ID, finding))
			}
			if kind == "decisions" {
				if decisions, ok := block["items"].([]any); ok {
					for _, item := range decisions {
						id, _ := item.(map[string]any)["id"].(string)
						if !addID(id) {
							findings = append(findings, "duplicate decision ID: "+id)
						}
					}
				}
			}
			// Item identifiers are scoped to their data block within the section.
			blockIDs := map[string]bool{}
			var duplicate string
			walkJSON(block, func(m map[string]any) {
				if id, ok := m["id"].(string); ok && m["type"] == nil {
					if blockIDs[id] {
						duplicate = id
					}
					blockIDs[id] = true
				}
			})
			if duplicate != "" {
				findings = append(findings, "duplicate block item ID: "+duplicate)
			}
		}
		var attention *struct {
			Refs []string `json:"refs"`
		}
		if err := json.Unmarshal(section.Attention, &attention); err != nil {
			return nil, err
		}
		if attention != nil {
			for _, id := range attention.Refs {
				if !items[id] {
					location := fmt.Sprintf("section %d (%s) attention ref %q", sectionIndex+1, section.ID, id)
					if strings.HasPrefix(id, "h_") {
						findings = append(findings, location+": hunk IDs cannot be attention references; create and reference an evidence (ev_*), risk (rsk_*), or decision (dec_*) item in this section, or set attention to null")
					} else {
						findings = append(findings, location+": does not resolve to evidence, risk, or decision in this section; create the item and reference its ev_*, rsk_*, or dec_* ID, or set attention to null")
					}
				}
			}
		}
	}

	var invalid string
	walkJSON(value, func(anchor map[string]any) {
		if anchor["type"] == "prompt" || anchor["type"] == "session_event" {
			session, _ := anchor["session"].(string)
			if restricted[session] {
				invalid = "anchor cites a restricted session"
			}
		}
	})
	if invalid != "" {
		findings = append(findings, invalid)
	}
	return findings, nil
}

// stackFindings applies the rules JSON Schema cannot express for a stack
// review: members are unique, a section names only members of the stack, and
// a document without a stack has no section pull requests.
func stackFindings(stack *documentStack, sections []authoredSection) []string {
	var findings []string
	members := map[int]bool{}
	if stack != nil {
		for _, member := range stack.Members {
			if members[member.Number] {
				findings = append(findings, fmt.Sprintf("duplicate stack member: #%d", member.Number))
			}
			members[member.Number] = true
		}
	}
	for index, section := range sections {
		for _, number := range section.PullRequests {
			switch {
			case stack == nil:
				findings = append(findings, fmt.Sprintf("section %d (%s) names pull request #%d but the document has no stack", index+1, section.ID, number))
			case !members[number]:
				findings = append(findings, fmt.Sprintf("section %d (%s) names pull request #%d, which is not a stack member", index+1, section.ID, number))
			}
		}
	}
	return findings
}

// unlistedImageFinding reports an images block item naming an attachment the
// list does not hold: the bundle's at generation, a stored version's own index
// afterwards.
func unlistedImageFinding(block map[string]any, listed map[string]bool) string {
	if block["kind"] != "images" {
		return ""
	}
	for _, item := range jsonObjects(block["items"]) {
		if id, _ := item["attachment"].(string); !listed[id] {
			return fmt.Sprintf("images block names attachment %q, which the attachments part does not list; use only listed attachment IDs", id)
		}
	}
	return ""
}

func walkJSON(value any, visit func(map[string]any)) {
	switch value := value.(type) {
	case map[string]any:
		visit(value)
		for _, child := range value {
			walkJSON(child, visit)
		}
	case []any:
		for _, child := range value {
			walkJSON(child, visit)
		}
	}
}

// Unresolved evidence remains visible as a model assertion; it cannot claim verification.
func (v *DocumentValidator) Prepare(document []byte, bundle *bundlepkg.Bundle, directory string) ([]byte, string) {
	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		return document, "output is not a JSON document"
	}
	// The model wrote against the lenient contract; everything the host
	// can decide for it is decided here, before the strict contract applies.
	value, derived := normalizeAuthored(value, v.strict, bundle)
	if normalized, err := json.Marshal(value); err == nil {
		document = normalized
	}
	if err := v.resolved.Validate(value); err != nil {
		// The schema-dependent checks assume a well-formed document. The
		// partition rules do not, and they are the findings most often left
		// for a further repair, so they are reported alongside the schema ones.
		findings := v.schemaFindings(document, err)
		var outline struct {
			Sections []sectionOutline `json:"sections"`
		}
		if json.Unmarshal(document, &outline) == nil {
			findings = append(findings, sectionOutlineFindings(outline.Sections, bundle)...)
		}
		return document, strings.Join(append(findings, derived...), "\n")
	}
	var doc authoredDocument
	if err := json.Unmarshal(document, &doc); err != nil {
		return document, err.Error()
	}
	for i := range doc.Sections {
		normalizeEvidence(doc.Sections[i].Evidence, bundle, directory)
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return document, err.Error()
	}
	reconciled, err := reconcileAuthoredHistory(encoded, bundle.Previous, bundle.Attachments, bundle.Diff, false)
	if err != nil {
		return encoded, err.Error()
	}
	var restored authoredDocument
	if err := json.Unmarshal(reconciled, &restored); err != nil {
		return encoded, err.Error()
	}
	for i := range restored.Sections {
		normalizeEvidence(restored.Sections[i].Evidence, bundle, directory)
	}
	reconciled, err = json.Marshal(restored)
	if err != nil {
		return encoded, err.Error()
	}
	findings := derived
	if semantic := v.Validate(reconciled, bundle); semantic != "" {
		findings = append(findings, semantic)
	}
	findings = append(findings, validateTileSources(restored.Sections, bundle, directory)...)
	return encoded, strings.Join(findings, "\n")
}

func normalizeEvidence(items []documentEvidence, bundle *bundlepkg.Bundle, directory string) {
	for i := range items {
		evidence := &items[i]
		for _, anchor := range evidence.Anchors {
			if !resolveDocumentAnchor(anchor, bundle, directory) {
				evidence.Status, evidence.Granularity = "asserted", nil
				break
			}
		}
	}
}

func resolveDocumentAnchor(a documentAnchor, b *bundlepkg.Bundle, directory string) bool {
	id := a.identifier()
	switch a.Type {
	case "hunk":
		for _, h := range b.Diff {
			if h.ID == id {
				return hunkLineRangeFinding(a.Lines, h) == ""
			}
		}
	case "file":
		path := valueOrEmpty(a.Path)
		if !filepath.IsLocal(path) {
			return false
		}
		for _, root := range []string{"head", "merge-base"} {
			if info, err := os.Stat(filepath.Join(directory, root, path)); err == nil && info.Mode().IsRegular() {
				return true
			}
		}
	case "check":
		for _, c := range b.Checks {
			if c.ID == id {
				return true
			}
		}
	case "commit":
		for _, c := range b.PullRequest.Commits {
			if c.SHA == valueOrEmpty(a.SHA) {
				return true
			}
		}
	case "pr_body":
		return b.PullRequest.Body != ""
	case "review_thread":
		for _, t := range b.Threads {
			if t.ID == id {
				return true
			}
		}
	case "pr_comment", "bot_report":
		for _, c := range b.Conversation {
			if c.ID == id && (a.Type != "bot_report" || c.BotReport) {
				return true
			}
		}
	case "prompt", "session_event":
		for _, s := range b.Sessions {
			if s.RootSessionID != valueOrEmpty(a.Session) || s.Restricted {
				continue
			}
			if a.Type == "prompt" {
				for _, p := range s.Prompts {
					if a.Index != nil && p.Index == *a.Index {
						return true
					}
				}
			} else {
				for _, e := range s.CommandEvents {
					if e.ID == valueOrEmpty(a.Event) {
						return true
					}
				}
			}
		}
	case "external_url":
		return true
	case "attachment":
		if len(a.ID) == 0 {
			return true
		}
		for _, attachment := range b.Attachments {
			if attachment.ID == id {
				return true
			}
		}
	case "issue", "spec", "pr":
		for _, reference := range b.References {
			if valueOrEmpty(a.Key) == reference.Value || valueOrEmpty(a.Page) == reference.Value || a.Number != nil && fmt.Sprint(*a.Number) == reference.Value {
				return true
			}
		}
	}
	return false
}

func hunkLineRangeFinding(lines []int, hunk bundlepkg.Hunk) string {
	if len(lines) == 0 {
		return ""
	}
	if len(lines) != 2 || lines[0] > lines[1] {
		return fmt.Sprintf("hunk anchor %s has an invalid right-side line range", hunk.ID)
	}
	if hunk.LineCount == 0 {
		return fmt.Sprintf("hunk anchor %s cannot have a line range because its right side is empty", hunk.ID)
	}
	start, end := hunk.StartLine, hunk.StartLine+hunk.LineCount-1
	if lines[1] < start || lines[0] > end {
		return fmt.Sprintf("hunk anchor %s line range %d-%d does not overlap its right side %d-%d", hunk.ID, lines[0], lines[1], start, end)
	}
	return ""
}

// ValidHunkIDs reports whether every hunk ID in the bundle has the form the
// schema requires.
func (v *DocumentValidator) ValidHunkIDs(bundle *bundlepkg.Bundle) bool {
	var contract struct {
		Defs map[string]struct {
			Pattern string `json:"pattern"`
		} `json:"$defs"`
	}
	if json.Unmarshal(reviewSchema, &contract) != nil {
		return false
	}
	pattern := regexp.MustCompile(contract.Defs["hunkId"].Pattern)
	for _, h := range bundle.Diff {
		if !pattern.MatchString(h.ID) {
			return false
		}
	}
	return true
}

// Complete derives host-owned fields, then validates the full document.
// It never calls the model and never accepts derived fields from its output;
// model names the generator recorded in the document.
func (v *DocumentValidator) Complete(document []byte, b *bundlepkg.Bundle, directory string, model string, now time.Time) ([]byte, string) {
	return v.CompleteWithPrompt(document, b, directory, model, "", now)
}

// CompleteWithPrompt is Complete for a host that versions its instruction
// prompt: promptVersion is recorded as generator.prompt_version, and an empty
// one records nothing. The model's output never sets it.
func (v *DocumentValidator) CompleteWithPrompt(document []byte, b *bundlepkg.Bundle, directory string, model, promptVersion string, now time.Time) ([]byte, string) {
	document, findings := v.Prepare(document, b, directory)
	if findings != "" {
		return nil, findings
	}
	document, err := reconcileAuthoredHistory(document, b.Previous, b.Attachments, b.Diff, true)
	if err != nil {
		return nil, err.Error()
	}
	var doc struct {
		reviewDocument
		Relations json.RawMessage `json:"relations"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		return nil, err.Error()
	}
	for i := range doc.Sections {
		normalizeEvidence(doc.Sections[i].Evidence, b, directory)
	}
	index := documentIndex{Hunks: []documentHunk{}, Checks: []documentCheck{}, Reports: []documentReport{}, Comments: []documentComment{}, Threads: threadIndex(b), Sessions: []documentSession{}, PRs: json.RawMessage("[]")}
	hunks := map[string]bundlepkg.Hunk{}
	hunksByPath := map[string][]string{}
	files := map[string]bool{}
	additions, deletions := 0, 0
	for _, h := range b.Diff {
		hunks[h.ID] = h
		hunksByPath[h.Path] = append(hunksByPath[h.Path], h.ID)
		files[h.Path] = true
		additions += h.Additions
		deletions += h.Deletions
		item := documentHunk{ID: h.ID, Path: h.Path, Change: h.Change, Role: h.Role, NewRange: []int{h.StartLine, h.LineCount}, Additions: h.Additions, Deletions: h.Deletions, Diff: indexedHunkDiff(h)}
		if h.DiffURL != "" {
			item.GitHubURL = &h.DiffURL
		}
		index.Hunks = append(index.Hunks, item)
	}
	checks, checkCounts := checkIndex(b, b.PullRequest.HeadSHA)
	index.Checks = checks
	for _, a := range b.Attachments {
		index.Attachments = append(index.Attachments, documentAttachment{ID: a.ID, Alt: a.Alt, Heading: a.Heading, Position: a.Position, ContentType: a.ContentType, Width: a.Width, Height: a.Height, SourceURL: a.SourceURL})
	}
	for _, c := range b.Conversation {
		if c.CreatedAt == nil {
			continue
		}
		kind := c.Kind
		if kind == "comment" {
			kind = "issue"
		}
		index.Comments = append(index.Comments, documentComment{ID: jsonString(c.ID), Kind: kind, Author: c.AuthorLogin, Bot: c.Bot, CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339Nano)})
		if c.BotReport {
			index.Reports = append(index.Reports, documentReport{ID: c.ID, Tool: c.BotTool, Marker: "baum", Comment: jsonString(c.ID), Summary: c.Body})
		}
	}
	sessions, vendors, models := sessionIndex(b)
	index.Sessions = sessions
	summary := &doc.Summary
	summary.Title = b.PullRequest.Title
	association := b.PullRequest.AuthorAssociation
	if association == "" {
		association = "NONE"
	}
	summary.Author = documentAuthor{Login: b.PullRequest.AuthorLogin, Association: association, Bot: b.PullRequest.AuthorBot}
	authorship := &summary.Authorship
	authorship.ObservedSessions, authorship.ObservedVendors, authorship.ObservedModels = len(b.Sessions), sortedKeys(vendors), sortedKeys(models)
	notes := append([]string{}, b.Truncated...)
	notes = threadNotes(notes, b)
	previous := 0
	prior := map[string]bundlepkg.PreviousSection{}
	if b.Previous != nil {
		previous = b.Previous.Version
		for _, s := range b.Previous.Sections {
			prior[s.ID] = s
		}
	}
	var previousVersion *int
	if previous > 0 {
		previousVersion = &previous
	}
	state := b.PullRequest.State
	if b.PullRequest.MergedSHA != "" {
		state = "merged"
	} else if b.PullRequest.Draft {
		state = "draft"
	}
	var merged, reviewMD *string
	reviewMDChanged := b.ReviewConfig != nil && b.ReviewConfig.ChangedInPullRequest
	if b.PullRequest.MergedSHA != "" {
		merged = &b.PullRequest.MergedSHA
	}
	if b.ReviewConfig != nil && b.ReviewConfig.Present {
		reviewMD = &b.ReviewConfig.Digest
	}
	specLinks := false
	for _, r := range b.References {
		specLinks = specLinks || r.Kind == "spec" || r.Kind == "spec_assertion"
	}
	doc.Review = documentReview{
		Repository: b.Repository, PullRequest: b.PullRequest.Number, PRState: state,
		Head: b.PullRequest.HeadSHA, Base: b.PullRequest.BaseSHA, MergeBase: b.PullRequest.MergeBaseSHA,
		MergedCommit: merged, Version: previous + 1, PreviousVersion: previousVersion,
		GenerationState: "current", GeneratedAt: now.UTC().Format(time.RFC3339Nano), EvidenceRefreshedAt: now.UTC().Format(time.RFC3339Nano),
		Relations: doc.Relations,
		Generator: documentReviewGenerator{Model: model, ReviewMD: reviewMD, ReviewMDChanged: reviewMDChanged, Truncated: notes, SchemaRevision: v.Revision, PromptVersion: promptVersion},
		Sources:   documentReviewSources{Diff: b.Sources["diff"], Sessions: len(b.Sessions), Checks: len(b.Checks), ReviewThreads: b.Sources["review_threads"], SpecLinks: specLinks, ReviewMD: reviewMD != nil},
	}
	tiers := []string{"mechanical", "unrated", "low", "medium", "high", "critical"}
	rank := func(tier string) int { return slices.Index(tiers, tier) }
	riskLOC := map[string]int{}
	for _, tier := range tiers {
		riskLOC[tier] = 0
	}
	roleLOC := map[string]int{}
	hunkTiers := map[string]string{}
	// A hunk may belong to several sections or to none, so the document's
	// line counts are taken once per diff hunk rather than per section.
	const (
		uncovered = iota
		coveredMechanical
		coveredLogical
	)
	covered := map[string]int{}
	for i := range doc.Sections {
		s := &doc.Sections[i]
		if len(s.Hunks) == 0 {
			continue
		}
		for _, risk := range s.Risks {
			if risk.State == "mitigated" {
				continue
			}
			for _, anchor := range risk.Anchors {
				ids := []string{}
				if anchor.Type == "hunk" {
					ids = append(ids, anchor.identifier())
				}
				if anchor.Type == "file" {
					ids = hunksByPath[valueOrEmpty(anchor.Path)]
				}
				for _, id := range ids {
					if tier := risk.Tier; rank(tier) > rank(hunkTiers[id]) {
						hunkTiers[id] = tier
					}
				}
			}
		}
	}
	for i := range doc.Sections {
		s := &doc.Sections[i]
		unresolved := false
		for _, risk := range s.Risks {
			for _, anchor := range risk.Anchors {
				unresolved = unresolved || !resolveDocumentAnchor(anchor, b, directory)
			}
		}
		var blocks []struct {
			Kind  string          `json:"kind"`
			Steps json.RawMessage `json:"steps"`
		}
		if err := json.Unmarshal(s.Blocks, &blocks); err != nil {
			return nil, err.Error()
		}
		for _, block := range blocks {
			if block.Kind == "behavior" {
				var steps []struct {
					Anchors []documentAnchor `json:"anchors"`
				}
				if err := json.Unmarshal(block.Steps, &steps); err != nil {
					return nil, err.Error()
				}
				for _, step := range steps {
					for _, anchor := range step.Anchors {
						unresolved = unresolved || !resolveDocumentAnchor(anchor, b, directory)
					}
				}
			}
		}
		if unresolved {
			notes = append(notes, "Section "+s.ID+" contains risk or behavior anchors unavailable in this input.")
		}
		ids := s.Hunks
		sectionState := "new"
		if old, ok := prior[s.ID]; ok {
			sectionState = "changed"
			oldIDs, newIDs := slices.Clone(old.HunkContentHashes), slices.Clone(ids)
			sort.Strings(oldIDs)
			sort.Strings(newIDs)
			if slices.Equal(oldIDs, newIDs) {
				sectionState = "unchanged"
			}
		}
		if len(ids) == 0 {
			sectionState = "stale"
		}
		sectionTier := "unrated"
		if s.Kind == "mechanical" {
			sectionTier = "mechanical"
		}
		for _, risk := range s.Risks {
			if risk.State == "mitigated" || sectionState == "stale" {
				continue
			}
			tier := risk.Tier
			if rank(tier) > rank(sectionTier) {
				sectionTier = tier
			}
		}

		sectionAdd, sectionDel := 0, 0
		sectionRoles := map[string]int{}
		for _, id := range ids {
			h := hunks[id]
			lines := h.Additions + h.Deletions
			sectionAdd += h.Additions
			sectionDel += h.Deletions
			sectionRoles[h.Role] += lines
			if s.Kind == "mechanical" {
				covered[id] = max(covered[id], coveredMechanical)
			} else {
				covered[id] = coveredLogical
			}
		}
		evidenceCounts := map[string]int{}
		if sectionState != "stale" {
			for _, evidence := range s.Evidence {
				evidenceCounts[evidence.Status]++
			}
		}
		s.Derived = documentDerived{State: sectionState, RiskTier: sectionTier, Additions: sectionAdd, Deletions: sectionDel, RoleLOC: sectionRoles, Reviewers: requestedReviewers(b.PullRequest.RequestedReviewers), EvidenceSummary: evidenceCounts}
		if score, reason := sectionReviewScore(s); score > 0 {
			s.Derived.ReviewScore, s.Derived.ReviewReason = &score, reason
		}
	}
	unassigned := []string{}
	for _, h := range b.Diff {
		lines := h.Additions + h.Deletions
		roleLOC[h.Role] += lines
		tier := hunkTiers[h.ID]
		switch {
		case covered[h.ID] == uncovered:
			unassigned = append(unassigned, h.ID)
			tier = "unrated"
		case covered[h.ID] == coveredMechanical:
			tier = "mechanical"
		case tier == "":
			tier = "unrated"
		}
		riskLOC[tier] += lines
	}
	doc.Stats = documentStats{Sections: len(doc.Sections), Files: len(files), Hunks: len(b.Diff), Additions: additions, Deletions: deletions, RoleLOC: roleLOC, RiskLOC: riskLOC, Checks: checkCounts}
	summary.RiskScore = riskScore(doc.Sections, b.Diff)
	doc.Index, doc.AgentReviews, doc.UnassignedHunks = index, agentReviews(b, doc.Sections, doc.Review.Head), unassigned
	doc.Review.Sources.AgentReviews = len(doc.AgentReviews)
	countFindings(&doc.reviewDocument)
	doc.Review.Generator.Truncated = notes
	setReviewExchange(&doc.reviewDocument, b)
	encoded, err := v.encodeFullDocument(&doc.reviewDocument)
	if err != nil {
		return nil, err.Error()
	}
	return encoded, ""
}

// indexedHunkDiff turns the authoritative, redacted bundle patch into the
// structured line shape the UI already uses for model-selected tile excerpts.
// A missing or over-trimmed patch stays absent so readers get a source link,
// never a partial excerpt presented as complete.
func indexedHunkDiff(hunk bundlepkg.Hunk) *tileHunk {
	if hunk.Patch == "" {
		return nil
	}
	header, _, _ := strings.Cut(hunk.Patch, "\n")
	oldLine, oldCount, newLine, newCount, ok := tileHunkRange(header)
	if !ok {
		return nil
	}
	var lines []tileLine
	actualOld, actualNew := 0, 0
	body := strings.Split(hunk.Patch, "\n")[1:]
	canMarkNoNewline := false
	for index, line := range body {
		if line == "" && index == len(body)-1 {
			continue
		}
		if line == `\ No newline at end of file` && canMarkNoNewline {
			canMarkNoNewline = false
			continue
		}
		if line == "" {
			return nil
		}
		item := tileLine{Text: line[1:]}
		switch line[0] {
		case ' ':
			item.Kind, item.Old, item.New = "context", oldLine, newLine
			oldLine++
			newLine++
		case '+':
			item.Kind, item.New = "add", newLine
			newLine++
		case '-':
			item.Kind, item.Old = "remove", oldLine
			oldLine++
		default:
			return nil
		}
		lines = append(lines, item)
		canMarkNoNewline = true
		if item.Kind != "add" {
			actualOld++
		}
		if item.Kind != "remove" {
			actualNew++
		}
	}
	if len(lines) == 0 || actualOld != oldCount || actualNew != newCount {
		return nil
	}
	return &tileHunk{Header: header, Lines: lines}
}

// checkIndex lists the bundle's checks as the document indexes them. head is
// the revision those checks ran on — the reviewed head, which is not always
// the pull request's current one once it has been pushed again.
func checkIndex(b *bundlepkg.Bundle, head string) ([]documentCheck, documentStatsChecks) {
	checks := []documentCheck{}
	checkCounts := documentStatsChecks{}
	for _, c := range b.Checks {
		conclusion := c.Conclusion
		if c.Status != "completed" && c.Kind == "check_run" || conclusion == "" {
			conclusion = "pending"
		}
		if conclusion == "timed_out" || conclusion == "action_required" || conclusion == "error" {
			conclusion = "failure"
		}
		item := documentCheck{ID: c.ID, Kind: c.Kind, Name: c.Name, Conclusion: conclusion, Required: c.Required, Head: head, StartedAt: documentTime(c.StartedAt), CompletedAt: documentTime(c.CompletedAt)}
		if c.URL != "" {
			item.URL = &c.URL
		}
		checks = append(checks, item)
		switch conclusion {
		case "pending":
			checkCounts.Pending++
		case "skipped", "neutral":
			checkCounts.Skipped++
		case "failure", "cancelled":
			if c.Required != nil && *c.Required {
				checkCounts.RequiredFailed++
			} else if c.Required != nil {
				checkCounts.OptionalFailed++
			}
		}
	}
	return checks, checkCounts
}

func sessionIndex(b *bundlepkg.Bundle) ([]documentSession, map[string]bool, map[string]bool) {
	sessions := []documentSession{}
	vendors, models := map[string]bool{}, map[string]bool{}
	for _, s := range b.Sessions {
		vendors[s.Vendor] = true
		item := documentSession{ID: s.RootSessionID, Vendor: s.Vendor, Restricted: s.Restricted}
		if !s.Restricted {
			prompts, events := []documentPrompt{}, []documentEvent{}
			for _, p := range s.Prompts {
				if p.At != nil {
					prompts = append(prompts, documentPrompt{Index: p.Index, ID: p.ID, At: p.At.UTC().Format(time.RFC3339Nano), Text: p.Text})
				}
			}
			for _, e := range s.CommandEvents {
				if e.Time == nil {
					continue
				}
				commands := []string{}
				for _, c := range e.Commands {
					commands = append(commands, strings.TrimSpace(c.Program+" "+c.Category+" "+c.Target))
				}
				events = append(events, documentEvent{ID: e.ID, At: e.Time.UTC().Format(time.RFC3339Nano), Verb: "execute", Summary: strings.Join(commands, "; ")})
			}
			item.Outcome = presentJSON(&s.Outcome)
			item.Models, item.Title, item.GitBranch, item.StartedAt, item.EndedAt = s.Models, presentJSON(&s.Title), presentJSON(&s.Branch), documentTime(s.StartedAt), documentTime(s.EndedAt)
			if s.Models == nil {
				item.Models = []string{}
			}
			item.Prompts, item.Events, item.PromptCount, item.ToolCalls = prompts, events, new(len(prompts)), new(len(events))
			for _, model := range s.Models {
				models[model] = true
			}
		}
		sessions = append(sessions, item)
	}
	return sessions, vendors, models
}

func sortedKeys(values map[string]bool) []string {
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func requestedReviewers(logins []string) []documentReviewer {
	reviewers := []documentReviewer{}
	for _, login := range logins {
		reviewers = append(reviewers, documentReviewer{Login: &login, Source: "requested"})
	}
	if len(reviewers) == 0 {
		reviewers = append(reviewers, documentReviewer{Source: "none"})
	}
	return reviewers
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
