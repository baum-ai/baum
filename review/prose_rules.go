package review

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

// The prose rules catch noise that recurs in model-written reviews and that
// the schema cannot express: wording that states when something was captured,
// talk about the model's own inputs, internal identifiers in sentences, and a
// section that carries too many or repeated points. They apply to newly
// authored documents only (proseFindings is called by validation and
// preparation during generation, never by ValidateStored or refresh), so a
// stored review that has such wording is never rejected or rewritten. The
// findings are given back to the model verbatim, so each names where the
// problem is, quotes what matched and says how to repair it.

const (
	// maxMissingEvidence bounds the missing items a section may list; more
	// buries the point a reviewer should act on. Risks are not capped: a cap
	// makes the model drop real risks, which a later version then records as
	// mitigated.
	maxMissingEvidence = 1

	// nearDuplicateThreshold is the containment at or above which two texts of
	// one section state the same point. Containment is the share of the
	// shorter text's distinct words (stop words aside) found in the longer
	// one: Jaccard similarity misses a short restatement inside a long
	// paragraph, which is the usual repeat. A text under nearDuplicateMinWords
	// distinct words is too short to compare.
	nearDuplicateThreshold = 0.8
	nearDuplicateMinWords  = 6
)

// proseKeys are the human-readable text fields of a review. Identifiers,
// enums, paths, code, anchors and references are not prose and are skipped.
var proseKeys = map[string]bool{
	"text": true, "claim": true, "detail": true, "expects": true, "title": true,
	"before": true, "after": true, "question": true, "rule": true, "caption": true,
	"summary": true, "rationale": true, "note": true, "trigger": true,
	"release_note": true, "breaking_change": true,
}

// proseSkipKeys are subtrees that never hold prose a reader takes as the
// review's own words.
var proseSkipKeys = map[string]bool{
	"id": true, "anchors": true, "refs": true, "tiles": true, "hunks": true, "source": true,
	"assessment": true, "derived": true, "authorship": true, "implements": true,
	"author": true, "risk_score": true, "index": true,
}

// proseBlockKinds are the data blocks whose fields are prose; the others
// (code, diagram, media, images, repro, table) hold code, commands or data.
var proseBlockKinds = map[string]bool{
	"intent": true, "behavior": true, "entities": true, "groups": true,
	"decisions": true, "dependency": true, "included_prs": true,
}

type proseRule struct {
	pattern *regexp.Regexp
	// follower, when set, decides on the word after the match (empty at the
	// end of the text or before punctuation): the match counts only when it
	// returns true. RE2 has no lookahead, so look-alikes such as "the checkout
	// page" are told apart here.
	follower func(next string) bool
	advice   string
}

var (
	// Wording that states when a check, bot review or thread was captured.
	// The state changes after generation, and the evidence status is refreshed
	// without the prose, so the page would show "Verified" beside "still running".
	runStateAlways = regexp.MustCompile(`(?i)\b(?:still[ -](?:running|in[ -]progress|pending|queued)|had not (?:yet )?(?:finished|completed)|has not (?:yet )?(?:finished|completed)|not yet (?:finished|completed)|at (?:the )?(?:snapshot|capture)|when the bundle was (?:taken|captured))\b`)
	// These forms are run state only when the sentence is about a check, CI, a
	// workflow, suite, pipeline or bot; "in-progress sessions", "a feature in
	// progress" and "no job was queued or running" are product text. Job, run,
	// thread and review are not subjects: "runs" is as often the verb.
	runStateWithSubject = regexp.MustCompile(`(?i)\b(?:(?:was|were|is|are|remains?|remained) (?:currently )?(?:in[ -]progress|queued|pending)|(?:was|were) (?:currently )?running)\b`)
	runStateSubject     = regexp.MustCompile(`(?i)\b(?:checks?|ci|workflows?|suites?|pipelines?|bots?)\b`)
	sentenceEnd         = regexp.MustCompile(`[.!?;]\s`)

	// Talk about the model's inputs and its own process. The review is the
	// reader's page, not a log of how it was written. The patterns name the
	// phrasings about the review's input: "the excerpts" or "the snapshot" alone
	// are common words in the code under review.
	metaRules = []proseRule{
		{pattern: regexp.MustCompile(`(?i)\b(?:(?:in|from|of|to|at) the bundle|the bundle (?:was|were|is|shows?|showed|lists?|listed|contains?|contained|provided|includes?|included|has|had|did|does|omits?|omitted|only|given|supplied)|(?:when|while) the bundle (?:was|were|is|had|has)|bundle time)\b`)},
		{pattern: regexp.MustCompile(`(?i)\b(?:(?:in|from|outside) the excerpts?|the excerpts (?:show|do not|omit))\b`)},
		{pattern: regexp.MustCompile(`(?i)\b(?:(?:at|in) the (?:(?:supplied|captured) )?snapshot|the (?:supplied|captured|check) snapshot)\b`)},
		{pattern: regexp.MustCompile(`(?i)\b(?:section details|outline) stage\b`)},
		{pattern: regexp.MustCompile(`(?i)\b(?:(?:in|from) the head checkout|in the checkout (?:read|shown))\b`)},
		{pattern: regexp.MustCompile(`\bI (?:read|opened|did not|didn't|could not|couldn't|checked|ran)\b|(?i:\bmy reading\b)`)},
	}

	// Identifiers in prose must resolve: the viewer turns them into links,
	// so one that names nothing is a dead chip. A full 40-hex SHA is flagged
	// whatever it names; the short SHA is what a reader uses.
	hunkIDPattern    = regexp.MustCompile(`\bh_[0-9a-f]{8,}\b`)
	itemIDPattern    = regexp.MustCompile(`\b(?:rsk|ev|dec|sec)_[a-z0-9_]+\b`)
	checkIDPattern   = regexp.MustCompile(`\b(?:check_run|status):\d+\b`)
	threadIDPattern  = regexp.MustCompile(`\bPRRT_[A-Za-z0-9_-]+`)
	commentIDPattern = regexp.MustCompile(`\bcomment (\d{6,})\b|` + "`(\\d{6,})`")
	sessionIDPattern = regexp.MustCompile(`\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	fullSHAPattern   = regexp.MustCompile(`\b[0-9a-f]{40}\b`)

	ticketTitle = regexp.MustCompile(`^\s*[A-Z][A-Z0-9]+-\d+(?:[:\s]|$)|\([A-Z][A-Z0-9]+-\d+\)`)

	// An absence claim is about tests, checks, runs or evidence ("No test
	// covers X"); "No caller of X remains" is a code fact and is not.
	absenceStart    = regexp.MustCompile(`(?i)^\s*(?:no|none of the|nothing)\s+(?:(?:passing|completed|finished|new|automated|unit|integration)\s+)*(?:tests?|checks?|runs?|ci|coverage|evidence|results?)\b`)
	absenceContains = regexp.MustCompile(`(?i)\bno (?:passing|completed|finished) (?:check|run|result)`)
)

// absenceStatuses are the evidence statuses that can say something is absent.
var absenceStatuses = []string{"missing", "unavailable", "failed", "skipped", "pending"}

// proseScope says which prose rules apply to the document being prepared.
type proseScope int

const (
	// proseFull is a document written in one pass: every rule, every
	// identifier resolved against the document and the bundle.
	proseFull proseScope = iota
	// proseOutline is a staged outline: no risk, evidence or decision exists
	// yet, so prose and the recommendation may cite section IDs only.
	proseOutline
	// proseDetails is one section's details merged into the outline: its own
	// items resolve, and so does any section ID; the recommendation was
	// checked with the outline.
	proseDetails
	// proseReused is details a previous version wrote: the wording rules, but
	// no identifier is resolved against the bundle (a check or hunk of an
	// earlier head does not exist now).
	proseReused
	// proseOff applies no prose rule: a stored outline being continued, the
	// context stage and the final assembly of stages already checked one by
	// one.
	proseOff
)

// proseFindings applies the prose rules to an authored document's decoded
// value; the bundle, when there is one, tells which identifiers exist.
func proseFindings(value any, bundle *bundlepkg.Bundle, scope proseScope) []string {
	if scope == proseOff {
		return nil
	}
	if scope == proseReused {
		bundle = nil
	}
	root, _ := value.(map[string]any)
	known := newKnownIDs(root, bundle, scope)
	var findings []string
	// A section's details are checked on their own: the outline around them was
	// accepted by its stage, or is a stored one that is continued.
	detailsOnly := scope == proseDetails || scope == proseReused
	if summary, ok := root["summary"].(map[string]any); ok && !detailsOnly {
		walkProse(summary, "summary", func(where, key, text string) {
			findings = append(findings, textFindings(known, where, key, text)...)
		})
		findings = append(findings, summaryJudgmentFindings(summary)...)
	}
	sections := jsonObjects(root["sections"])
	for i, section := range sections {
		id, _ := section["id"].(string)
		if detailsOnly && section["details"] != nil {
			continue
		}
		prefix := fmt.Sprintf("section %d (%s)", i+1, id)
		visit := func(where, key, text string) {
			findings = append(findings, textFindings(known, prefix+" "+where, key, text)...)
		}
		if title, ok := section["title"].(string); ok {
			visit("title", "title", title)
		}
		for _, field := range []string{"summary", "attention"} {
			if object, ok := section[field].(map[string]any); ok {
				walkProse(object, field, visit)
			}
		}
		for _, risk := range jsonObjects(section["risks"]) {
			riskID, _ := risk["id"].(string)
			walkProse(risk, "risk "+riskID, visit)
		}
		for _, evidence := range jsonObjects(section["evidence"]) {
			evidenceID, _ := evidence["id"].(string)
			walkProse(evidence, "evidence "+evidenceID, visit)
		}
		for _, block := range jsonObjects(section["blocks"]) {
			kind, _ := block["kind"].(string)
			if proseBlockKinds[kind] {
				walkProse(block, "block "+kind, visit)
			}
		}
		findings = append(findings, sectionJudgmentFindings(prefix, section)...)
		findings = append(findings, sectionShapeFindings(prefix, section)...)
	}
	switch scope {
	case proseFull:
		findings = append(findings, recommendationFindings(root, sections, false)...)
	case proseOutline:
		findings = append(findings, recommendationFindings(root, sections, true)...)
	}
	return findings
}

// The run-state and meta rules apply only to the fields that carry the
// review's own judgement: evidence claims, details and expectations,
// attention, risk text, the overview, the review exchange, assessment reasons
// and the recommendation. Behaviour steps, context, intent, titles and the
// other blocks describe the code under review, which may itself run jobs, take
// snapshots and bundle things.
func summaryJudgmentFindings(summary map[string]any) []string {
	var findings []string
	judge := func(where, text string) {
		findings = append(findings, judgmentFindings(where, text)...)
	}
	if text, ok := summary["text"].(string); ok {
		judge("summary text", text)
	}
	if exchange, ok := summary["review_exchange"].(map[string]any); ok {
		if text, ok := exchange["summary"].(string); ok {
			judge("summary review_exchange summary", text)
		}
	}
	if attention, ok := summary["attention"].(map[string]any); ok {
		if text, ok := attention["text"].(string); ok {
			judge("summary attention text", text)
		}
	}
	if recommendation, ok := summary["recommendation"].(map[string]any); ok {
		if text, ok := recommendation["text"].(string); ok {
			judge("summary recommendation text", text)
		}
	}
	return findings
}

func sectionJudgmentFindings(prefix string, section map[string]any) []string {
	var findings []string
	judge := func(where, text string) {
		findings = append(findings, judgmentFindings(prefix+" "+where, text)...)
	}
	if attention, ok := section["attention"].(map[string]any); ok {
		if text, ok := attention["text"].(string); ok {
			judge("attention text", text)
		}
	}
	for _, risk := range jsonObjects(section["risks"]) {
		id, _ := risk["id"].(string)
		if text, ok := risk["text"].(string); ok {
			judge("risk "+id+" text", text)
		}
	}
	for _, evidence := range jsonObjects(section["evidence"]) {
		id, _ := evidence["id"].(string)
		for _, key := range []string{"claim", "detail", "expects"} {
			if text, ok := evidence[key].(string); ok {
				judge("evidence "+id+" "+key, text)
			}
		}
	}
	if assessment, ok := section["assessment"].(map[string]any); ok {
		for i, reason := range jsonObjects(assessment["reasons"]) {
			if text, ok := reason["text"].(string); ok {
				judge(fmt.Sprintf("assessment reason %d text", i+1), text)
			}
		}
	}
	return findings
}

// walkProse visits every prose string under node, with where naming its path.
func walkProse(node any, where string, visit func(where, key, text string)) {
	switch node := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(node))
		for key := range node {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if proseSkipKeys[key] {
				continue
			}
			switch child := node[key].(type) {
			case string:
				if proseKeys[key] {
					visit(where+" "+key, key, child)
				}
			default:
				walkProse(child, where+" "+key, visit)
			}
		}
	case []any:
		for i, child := range node {
			label := fmt.Sprintf("item %d", i+1)
			if object, ok := child.(map[string]any); ok {
				if id, _ := object["id"].(string); id != "" {
					label = id
				}
			}
			if text, ok := child.(string); ok {
				// A list of strings under a prose key, such as a decision's
				// rejected alternatives, is read as prose.
				if strings.HasSuffix(where, " rejected") {
					visit(where+" "+label, "rejected", text)
				}
				continue
			}
			walkProse(child, where+" "+label, visit)
		}
	}
}

// judgmentFindings applies the run-state and meta rules to one text.
func judgmentFindings(where, text string) []string {
	var findings []string
	finding := func(format string, args ...any) {
		findings = append(findings, fmt.Sprintf("%s: "+format, append([]any{where}, args...)...))
	}
	if match := runStateAlways.FindString(text); match != "" {
		finding("%q states when a check, bot review or thread was captured; that state changes after the review is written and the page shows its current status, so describe what the check covers or the finding itself, not whether it had finished", match)
	} else if match := runStateWithSubjectIn(text); match != "" {
		finding("%q states the run state of a check; that state changes after the review is written and the page shows its current status, so describe what it covers, not whether it had finished", match)
	}
	for _, rule := range metaRules {
		if match := matchRule(rule, text); match != "" {
			finding("%q talks about the model's inputs or process, not the change; state the fact about the code or the pull request directly, without the bundle, excerpts, snapshot, stages, checkout or first person", match)
			break
		}
	}
	return findings
}

// textFindings applies the rules that look at any prose string: identifiers
// must resolve, and a title carries no ticket key.
func textFindings(known knownIDs, where, key, text string) []string {
	findings := known.unresolved(where, text)
	if key == "title" {
		if match := ticketKey(text); match != "" {
			findings = append(findings, fmt.Sprintf("%s: title carries the ticket key %q; the key is shown elsewhere, so write the title without it", where, strings.TrimSpace(match)))
		}
	}
	return findings
}

// standardPrefixes are standards and algorithms whose names look like a
// ticket key ("UTF-8", "SHA-256") and are not one.
var standardPrefixes = []string{"UTF", "SHA", "MD", "ISO", "RFC", "TLS", "AES", "RSA", "ES", "CVE", "GPT", "HTTP", "BLAKE", "SSE", "EC", "HS", "RS", "ECMA"}

// ticketKey returns the ticket key text starts with, or sits in parentheses in
// it, or "" when it has none.
func ticketKey(text string) string {
	match := ticketTitle.FindString(text)
	if match == "" {
		return ""
	}
	key := strings.TrimLeft(match, " \t(")
	prefix, _, _ := strings.Cut(key, "-")
	if slices.Contains(standardPrefixes, prefix) {
		return ""
	}
	return match
}

// runStateWithSubjectIn returns the run-state phrase of the first sentence of
// text that has one and is about a check, CI, a workflow, suite, pipeline or bot.
func runStateWithSubjectIn(text string) string {
	start := 0
	for _, end := range append(sentenceEnd.FindAllStringIndex(text, -1), []int{len(text), len(text)}) {
		sentence := text[start:end[0]]
		if match := runStateWithSubject.FindString(sentence); match != "" && runStateSubject.MatchString(sentence) {
			return match
		}
		start = end[1]
	}
	return ""
}

// matchRule returns the text a rule matches, or "" when it does not match or
// its follower rejects the match.
func matchRule(rule proseRule, text string) string {
	for _, found := range rule.pattern.FindAllStringSubmatch(text, -1) {
		if rule.follower != nil {
			next := ""
			if len(found) > 1 {
				next = strings.ToLower(found[1])
			}
			if !rule.follower(next) {
				continue
			}
		}
		return strings.TrimSpace(found[0])
	}
	return ""
}

// sectionShapeFindings applies the rules about a section's items: ci
// evidence, absence claimed as a finding, too many missing items, and a
// point stated twice.
func sectionShapeFindings(prefix string, section map[string]any) []string {
	var findings []string
	missing := 0
	for _, evidence := range jsonObjects(section["evidence"]) {
		id, _ := evidence["id"].(string)
		kind, _ := evidence["kind"].(string)
		claim, _ := evidence["claim"].(string)
		status, _ := evidence["status"].(string)
		if status == "missing" {
			missing++
		}
		if kind == "ci" {
			findings = append(findings, fmt.Sprintf("%s evidence %s has kind \"ci\": the review page already shows the checks, so drop the item, or make it a \"test\" item whose claim names what the tests cover, anchored to the check", prefix, id))
		}
		if !slices.Contains(absenceStatuses, status) {
			match := absenceStart.FindString(claim)
			if match == "" {
				match = absenceContains.FindString(claim)
			}
			if match != "" {
				findings = append(findings, fmt.Sprintf("%s evidence %s claim: %q asserts an absence with status %q; use status \"missing\" with an \"expects\" saying what would cover it, or drop the item", prefix, id, strings.TrimSpace(match), status))
			}
		}
	}
	if missing > maxMissingEvidence {
		findings = append(findings, fmt.Sprintf("%s has %d evidence items with status \"missing\"; keep at most %d, the one that matters most, and drop or merge the rest", prefix, missing, maxMissingEvidence))
	}
	risks := jsonObjects(section["risks"])
	type point struct{ label, text string }
	var points []point
	if attention, ok := section["attention"].(map[string]any); ok {
		if text, _ := attention["text"].(string); text != "" {
			points = append(points, point{prefix + " attention", text})
		}
	}
	for _, risk := range risks {
		id, _ := risk["id"].(string)
		if text, _ := risk["text"].(string); text != "" {
			points = append(points, point{prefix + " risk " + id, text})
		}
	}
	for i := range points {
		for j := i + 1; j < len(points); j++ {
			if textContainment(points[i].text, points[j].text) >= nearDuplicateThreshold {
				findings = append(findings, fmt.Sprintf("%s states the same point as %s (near-duplicate text); say each point once: drop or rewrite the repeat, and keep attention for something the risks do not already say, or set it to null", points[j].label, points[i].label))
			}
		}
	}
	return findings
}

var (
	wordPattern = regexp.MustCompile(`[a-z0-9]+`)
	stopWords   = map[string]bool{
		"a": true, "an": true, "the": true, "and": true, "or": true, "of": true, "to": true, "in": true,
		"on": true, "for": true, "is": true, "are": true, "was": true, "were": true, "be": true, "it": true,
		"this": true, "that": true, "with": true, "as": true, "by": true, "at": true, "not": true,
	}
)

// textContainment returns the share of the shorter text's distinct words that
// the longer text also has, ignoring case and stop words; it is 0 when the
// shorter text has fewer than nearDuplicateMinWords distinct words.
func textContainment(a, b string) float64 {
	left, right := wordSet(a), wordSet(b)
	if len(left) > len(right) {
		left, right = right, left
	}
	if len(left) < nearDuplicateMinWords {
		return 0
	}
	shared := 0
	for word := range left {
		if right[word] {
			shared++
		}
	}
	return float64(shared) / float64(len(left))
}

func wordSet(text string) map[string]bool {
	words := map[string]bool{}
	for _, word := range wordPattern.FindAllString(strings.ToLower(text), -1) {
		if !stopWords[word] {
			words[word] = true
		}
	}
	return words
}

// recommendationFindings checks summary.recommendation.refs: each names a
// section, or a risk, evidence item or decision of some section, and a
// decision to look first or request changes names at least one. The outline
// stage writes the recommendation before any risk exists, so there
// (sectionsOnly) a ref may name a section only.
func recommendationFindings(root map[string]any, sections []map[string]any, sectionsOnly bool) []string {
	summary, _ := root["summary"].(map[string]any)
	recommendation, _ := summary["recommendation"].(map[string]any)
	if recommendation == nil {
		return nil
	}
	known := newKnownIDs(root, nil, proseFull)
	refs := stringSlice(recommendation["refs"])
	var findings []string
	decision, _ := recommendation["decision"].(string)
	if (decision == "look_first" || decision == "request_changes") && len(refs) == 0 {
		findings = append(findings, fmt.Sprintf("summary recommendation: decision %q needs refs naming the sections, risks or evidence to look at", decision))
	}
	sectionIDs := map[string]bool{}
	for _, section := range sections {
		if id, _ := section["id"].(string); id != "" {
			sectionIDs[id] = true
		}
	}
	for _, ref := range refs {
		switch {
		case sectionIDs[ref]:
		case sectionsOnly:
			findings = append(findings, fmt.Sprintf("summary recommendation ref %q: the outline can cite only section IDs; cite the section", ref))
		case !known.items[ref]:
			findings = append(findings, fmt.Sprintf("summary recommendation ref %q: does not resolve to a section, or a risk, evidence or decision of a section; name an existing ID or drop the ref", ref))
		}
	}
	return findings
}

// knownIDs are the identifiers prose may name: the document's own items, and
// the bundle's hunks, checks, review threads, comments and sessions.
type knownIDs struct {
	// items holds the section, risk, evidence and decision IDs; sectionsOnly is
	// set for a staged outline, which can cite sections only.
	items        map[string]bool
	sectionsOnly bool
	// withBundle is false without a bundle, which leaves hunk, check, thread,
	// comment and session IDs unchecked.
	withBundle                                 bool
	hunks, checks, threads, comments, sessions map[string]bool
}

func newKnownIDs(root map[string]any, bundle *bundlepkg.Bundle, scope proseScope) knownIDs {
	known := knownIDs{items: map[string]bool{}, sectionsOnly: scope == proseOutline}
	for _, section := range jsonObjects(root["sections"]) {
		if id, _ := section["id"].(string); id != "" {
			known.items[id] = true
		}
		for _, field := range []string{"risks", "evidence"} {
			for _, item := range jsonObjects(section[field]) {
				if id, _ := item["id"].(string); id != "" {
					known.items[id] = true
				}
			}
		}
		for _, block := range jsonObjects(section["blocks"]) {
			if block["kind"] != "decisions" {
				continue
			}
			for _, item := range jsonObjects(block["items"]) {
				if id, _ := item["id"].(string); id != "" {
					known.items[id] = true
				}
			}
		}
	}
	if bundle == nil {
		return known
	}
	known.withBundle = true
	known.hunks, known.checks, known.threads, known.comments, known.sessions = map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, hunk := range bundle.Diff {
		known.hunks[hunk.ID] = true
	}
	for _, check := range bundle.Checks {
		known.checks[check.ID] = true
	}
	for _, thread := range bundle.Threads {
		known.threads[thread.ID] = true
	}
	for _, item := range bundle.Conversation {
		known.comments[item.ID] = true
	}
	for _, session := range bundle.Sessions {
		if !session.Restricted {
			known.sessions[session.RootSessionID] = true
		}
	}
	return known
}

// unresolved returns a finding for each identifier in text that names nothing
// the review can link to, and for a full SHA.
func (k knownIDs) unresolved(where, text string) []string {
	var findings []string
	report := func(match, kind string) {
		findings = append(findings, fmt.Sprintf("%s: %q is not a known %s; cite an existing one or drop it", where, match, kind))
	}
	for _, match := range itemIDPattern.FindAllString(text, -1) {
		switch {
		case k.sectionsOnly && !strings.HasPrefix(match, "sec_"):
			findings = append(findings, fmt.Sprintf("%s: %q: the outline can cite only section IDs; cite the section", where, match))
		case !k.items[match]:
			report(match, "section, risk, evidence or decision ID of this review")
		}
	}
	if k.withBundle {
		for _, match := range hunkIDPattern.FindAllString(text, -1) {
			if !k.hunks[match] {
				report(match, "hunk ID of this pull request's diff")
			}
		}
		for _, match := range checkIDPattern.FindAllString(text, -1) {
			if !k.checks[match] {
				report(match, "check ID of this pull request")
			}
		}
		for _, match := range threadIDPattern.FindAllString(text, -1) {
			if !k.threads[match] {
				report(match, "review thread ID of this pull request")
			}
		}
		for _, match := range commentIDPattern.FindAllStringSubmatch(text, -1) {
			if id := match[1] + match[2]; !k.comments[id] {
				report(strings.Trim(match[0], "`"), "comment ID of this pull request")
			}
		}
		for _, match := range sessionIDPattern.FindAllString(text, -1) {
			if !k.sessions[match] {
				report(match, "session ID of this review (restricted sessions cannot be cited)")
			}
		}
	}
	for _, match := range fullSHAPattern.FindAllString(text, -1) {
		findings = append(findings, fmt.Sprintf("%s: %q is a full SHA; write the 7-character short SHA", where, match))
	}
	return findings
}
