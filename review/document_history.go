package review

import (
	"encoding/json"
	"errors"
	"fmt"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

// reconcileAuthoredHistory restores published identities for validation and
// completion. The uploaded candidate stays authored-only so completion can
// independently distinguish omitted risks from explicit reactivations.
// attachments are the images this generation was offered.
func reconcileAuthoredHistory(document []byte, previous *bundlepkg.PreviousVersion, attachments []bundlepkg.Attachment, lifecycle bool) ([]byte, error) {
	next, err := decodeObject(document, "generated review")
	if err != nil {
		return nil, err
	}
	generated, err := arrayField(next, "sections")
	if err != nil {
		return nil, err
	}
	byID, order, err := objectsByID(generated, "generated section")
	if err != nil {
		return nil, err
	}
	priorSections, err := previousSections(previous)
	if err != nil {
		return nil, err
	}
	priorVersion := 0
	if previous != nil {
		priorVersion = previous.Version
	}
	sections := make([]any, 0, len(priorSections)+len(generated))
	appendSection := func(prior, current map[string]any) error {
		hunks, err := arrayField(current, "hunks")
		if err != nil {
			return err
		}
		if err := reconcileSectionRisks(prior, current, priorVersion, priorVersion+1, len(hunks) == 0); err != nil {
			return err
		}
		if !lifecycle {
			for _, value := range current["risks"].([]any) {
				risk := value.(map[string]any)
				delete(risk, "state")
				delete(risk, "introduced_version")
				delete(risk, "mitigated_version")
			}
		}
		sections = append(sections, current)
		return nil
	}
	for _, value := range priorSections {
		prior := value.(map[string]any)
		id, err := stringField(prior, "id")
		if err != nil {
			return nil, err
		}
		current, found := byID[id]
		if !found {
			current = cloneObject(prior)
			current["hunks"] = []any{}
			dropUnlistedImages(current, attachments)
		}
		if err := appendSection(prior, current); err != nil {
			return nil, err
		}
		delete(byID, id)
	}
	for _, id := range order {
		if current, ok := byID[id]; ok {
			if err := appendSection(nil, current); err != nil {
				return nil, err
			}
		}
	}
	next["sections"] = sections
	return json.Marshal(next)
}

// RestoreOmittedPreviousSections returns the authored document with each
// previous section it omits restored as completion would restore it: the
// previous content with no hunks, only offered images and no risk lifecycle
// fields, with the previous sections first in their order and the new ones
// after. Sections the document carries are left as they are, so a risk
// omitted from them still reads as omitted. A stale section's risks keep
// their recorded state either way, so a host may restore early to edit
// restored content, and completion keeps the edited section. Unlike a section
// completion restores itself, the restored content is then normalized like
// any authored section: it loses its assessment, as every section without
// hunks does, since none takes part in the risk score. The document is
// returned unchanged when it omits no previous section.
func RestoreOmittedPreviousSections(document []byte, previous *bundlepkg.PreviousVersion, attachments []bundlepkg.Attachment) ([]byte, error) {
	if previous == nil || len(previous.Sections) == 0 {
		return document, nil
	}
	next, err := decodeObject(document, "generated review")
	if err != nil {
		return nil, err
	}
	generated, err := arrayField(next, "sections")
	if err != nil {
		return nil, err
	}
	byID, order, err := objectsByID(generated, "generated section")
	if err != nil {
		return nil, err
	}
	priorSections, err := previousSections(previous)
	if err != nil {
		return nil, err
	}
	sections := make([]any, 0, len(priorSections)+len(generated))
	restored := false
	for _, value := range priorSections {
		prior := value.(map[string]any)
		id, err := stringField(prior, "id")
		if err != nil {
			return nil, err
		}
		current, found := byID[id]
		if !found {
			current = cloneObject(prior)
			current["hunks"] = []any{}
			dropUnlistedImages(current, attachments)
			risks, _ := current["risks"].([]any)
			for _, value := range risks {
				if risk, ok := value.(map[string]any); ok {
					delete(risk, "state")
					delete(risk, "introduced_version")
					delete(risk, "mitigated_version")
				}
			}
			restored = true
		}
		sections = append(sections, current)
		delete(byID, id)
	}
	if !restored {
		return document, nil
	}
	for _, id := range order {
		if current, ok := byID[id]; ok {
			sections = append(sections, current)
		}
	}
	next["sections"] = sections
	return json.Marshal(next)
}

func previousSections(previous *bundlepkg.PreviousVersion) ([]any, error) {
	sections := []any{}
	if previous == nil {
		return sections, nil
	}
	for _, section := range previous.Sections {
		content, err := decodeObject(section.Content, "previous section")
		if err != nil {
			return nil, err
		}
		delete(content, "derived")
		sections = append(sections, content)
	}
	return sections, nil
}

// A stale section (no current hunks) says nothing about whether its risks
// were addressed, so they keep the state they were last recorded with; if the
// hunks return, a continuing risk is active under its original version.
func reconcileSectionRisks(prior, current map[string]any, priorVersion, version int, stale bool) error {
	currentRisks, err := arrayField(current, "risks")
	if err != nil {
		return err
	}
	byID, order, err := objectsByID(currentRisks, "generated risk")
	if err != nil {
		return err
	}
	var priorRisks []any
	if prior != nil {
		priorRisks, err = arrayField(prior, "risks")
		if err != nil {
			return err
		}
	}
	reconciled := make([]any, 0, len(priorRisks)+len(currentRisks))
	for _, value := range priorRisks {
		oldRisk, ok := value.(map[string]any)
		if !ok {
			return errors.New("previous risk must be an object")
		}
		id, err := stringField(oldRisk, "id")
		if err != nil {
			return err
		}
		introduced := optionalPositiveInteger(oldRisk, "introduced_version", priorVersion)
		wasMitigated := oldRisk["state"] == "mitigated"
		nextRisk, found := byID[id]
		delete(byID, id)
		if found && !stale {
			nextRisk["state"] = "active"
			nextRisk["introduced_version"] = introduced
			nextRisk["mitigated_version"] = nil
			reconciled = append(reconciled, nextRisk)
			continue
		}
		if !found {
			nextRisk = cloneObject(oldRisk)
		}
		nextRisk["introduced_version"] = introduced
		switch {
		case wasMitigated:
			nextRisk["state"] = "mitigated"
			nextRisk["mitigated_version"] = optionalPositiveInteger(oldRisk, "mitigated_version", version)
		case stale:
			nextRisk["state"] = "active"
			nextRisk["mitigated_version"] = nil
		default:
			nextRisk["state"] = "mitigated"
			nextRisk["mitigated_version"] = version
		}
		reconciled = append(reconciled, nextRisk)
	}
	for _, id := range order {
		risk, found := byID[id]
		if !found {
			continue
		}
		risk["state"] = "active"
		risk["introduced_version"] = version
		risk["mitigated_version"] = nil
		reconciled = append(reconciled, risk)
	}
	current["risks"] = reconciled
	return nil
}

// PreserveRiskOmissionsForRepair removes prior risks that were absent from the
// provider's first assessment. A validation repair must not turn those
// omissions into explicit reactivations.
//
// It is exported for hosts that run the generate-and-repair loop themselves.
func PreserveRiskOmissionsForRepair(previous *bundlepkg.PreviousVersion, assessed, repaired []byte) []byte {
	if previous == nil {
		return repaired
	}
	priorSections, err := previousSections(previous)
	priorRoot := map[string]any{"sections": priorSections}
	if err != nil {
		return repaired
	}
	if _, err := reconcileAuthoredHistory(assessed, previous, nil, false); err != nil {
		return repaired
	}
	assessedRoot, err := decodeObject(assessed, "generated review")
	if err != nil {
		return repaired
	}
	repairedRoot, err := decodeObject(repaired, "repaired review")
	if err != nil {
		return repaired
	}
	omitted := omittedPriorRisks(priorRoot, assessedRoot)
	sections, err := arrayField(repairedRoot, "sections")
	if err != nil {
		return repaired
	}
	for _, value := range sections {
		section, ok := value.(map[string]any)
		if !ok {
			continue
		}
		sectionID, _ := section["id"].(string)
		missing := omitted[sectionID]
		if len(missing) == 0 {
			continue
		}
		risks, ok := section["risks"].([]any)
		if !ok {
			continue
		}
		kept := risks[:0]
		for _, riskValue := range risks {
			risk, ok := riskValue.(map[string]any)
			id, _ := risk["id"].(string)
			if !ok || !missing[id] {
				kept = append(kept, riskValue)
			}
		}
		section["risks"] = kept
	}
	encoded, err := json.Marshal(repairedRoot)
	if err != nil {
		return repaired
	}
	return encoded
}

func omittedPriorRisks(previous, assessed map[string]any) map[string]map[string]bool {
	present := map[string]map[string]bool{}
	if sections, ok := assessed["sections"].([]any); ok {
		for _, value := range sections {
			section, ok := value.(map[string]any)
			if !ok {
				continue
			}
			sectionID, _ := section["id"].(string)
			present[sectionID] = map[string]bool{}
			if risks, ok := section["risks"].([]any); ok {
				for _, riskValue := range risks {
					risk, _ := riskValue.(map[string]any)
					id, _ := risk["id"].(string)
					present[sectionID][id] = true
				}
			}
		}
	}
	omitted := map[string]map[string]bool{}
	if sections, ok := previous["sections"].([]any); ok {
		for _, value := range sections {
			section, ok := value.(map[string]any)
			if !ok {
				continue
			}
			sectionID, _ := section["id"].(string)
			if risks, ok := section["risks"].([]any); ok {
				for _, riskValue := range risks {
					risk, _ := riskValue.(map[string]any)
					id, _ := risk["id"].(string)
					if !present[sectionID][id] {
						if omitted[sectionID] == nil {
							omitted[sectionID] = map[string]bool{}
						}
						omitted[sectionID][id] = true
					}
				}
			}
		}
	}
	return omitted
}

func decodeObject(data []byte, name string) (map[string]any, error) {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", name, err)
	}
	if value == nil {
		return nil, fmt.Errorf("%s must be an object", name)
	}
	return value, nil
}

func arrayField(value map[string]any, field string) ([]any, error) {
	result, ok := value[field].([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", field)
	}
	return result, nil
}

func stringField(value map[string]any, field string) (string, error) {
	result, ok := value[field].(string)
	if !ok || result == "" {
		return "", fmt.Errorf("%s must be a non-empty string", field)
	}
	return result, nil
}

func optionalPositiveInteger(value map[string]any, field string, fallback int) int {
	if number, ok := value[field].(float64); ok && number >= 1 && number == float64(int(number)) {
		return int(number)
	}
	return fallback
}

func objectsByID(values []any, name string) (map[string]map[string]any, []string, error) {
	result := make(map[string]map[string]any, len(values))
	order := make([]string, 0, len(values))
	for _, value := range values {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("%s must be an object", name)
		}
		id, err := stringField(object, "id")
		if err != nil {
			return nil, nil, err
		}
		if _, exists := result[id]; exists {
			return nil, nil, fmt.Errorf("duplicate %s id %q", name, id)
		}
		result[id] = object
		order = append(order, id)
	}
	return result, order, nil
}

// dropUnlistedImages removes, from a section the host carries over for a
// model that left it out, images the description no longer offers, and an
// images block left empty. The model cannot repair a section it did not write.
func dropUnlistedImages(section map[string]any, attachments []bundlepkg.Attachment) {
	listed := map[string]bool{}
	for _, attachment := range attachments {
		listed[attachment.ID] = true
	}
	blocks, ok := section["blocks"].([]any)
	if !ok {
		return
	}
	kept := make([]any, 0, len(blocks))
	for _, value := range blocks {
		block, _ := value.(map[string]any)
		if block == nil || block["kind"] != "images" {
			kept = append(kept, value)
			continue
		}
		items := []any{}
		for _, item := range jsonObjects(block["items"]) {
			if id, _ := item["attachment"].(string); listed[id] {
				items = append(items, item)
			}
		}
		if len(items) > 0 {
			block["items"] = items
			kept = append(kept, block)
		}
	}
	section["blocks"] = kept
}

func cloneObject(value map[string]any) map[string]any {
	encoded, _ := json.Marshal(value)
	var clone map[string]any
	_ = json.Unmarshal(encoded, &clone)
	return clone
}
