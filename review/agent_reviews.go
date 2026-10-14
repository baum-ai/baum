package review

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	bundlepkg "github.com/baumai/baum/bundle"
)

// threadIndex lists the bundle's review threads as the document indexes them.
func threadIndex(b *bundlepkg.Bundle) []documentThread {
	threads := []documentThread{}
	for _, t := range b.Threads {
		item := documentThread{ID: t.ID, Line: t.Line, Resolved: t.Resolved, Outdated: t.Outdated}
		if t.Path != "" {
			item.Path = new(t.Path)
		}
		if t.URL != "" {
			item.URL = new(t.URL)
		}
		threads = append(threads, item)
	}
	return threads
}

var (
	agentToolPattern     = regexp.MustCompile(`[^a-z0-9_]+`)
	findingMustFix       = regexp.MustCompile(`(?i)\bP[01]\b|\b(?:critical|high)[ -](?:severity|priority)\b`)
	findingShouldFix     = regexp.MustCompile(`(?i)\bP2\b|\bmedium[ -](?:severity|priority)\b`)
	findingMarkdownImage = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	findingHTML          = regexp.MustCompile(`<[^>]*>`)
)

const maxFindingText = 300

// agentReviews derives one entry per recognised agent review tool and
// assessed head from the tool's submitted reviews. Its findings are the
// threads the review opened; a finding's state is its thread's resolution.
func agentReviews(b *bundlepkg.Bundle, sections []documentSection, head string) []documentAgentReview {
	type key struct{ tool, head string }
	entries := []documentAgentReview{}
	at := map[key]int{}
	reviewEntry := map[string]int{}
	for _, c := range b.Conversation {
		if c.Kind != "review" || c.BotReport || c.BotTool == "" || !validAssessedHead(c.Commit) {
			continue
		}
		tool := strings.Trim(agentToolPattern.ReplaceAllString(strings.ToLower(c.BotTool), "_"), "_")
		if tool == "" {
			continue
		}
		k := key{tool, c.Commit}
		i, ok := at[k]
		if !ok {
			i = len(entries)
			at[k] = i
			id := "ar_" + tool + "_" + c.Commit[:7]
			for n := 2; slices.ContainsFunc(entries, func(e documentAgentReview) bool { return e.ID == id }); n++ {
				id = "ar_" + tool + "_" + c.Commit[:7] + "_" + strconv.Itoa(n)
			}
			entries = append(entries, documentAgentReview{
				ID: id, Tool: tool, AssessedHead: c.Commit,
				Stale: c.Commit != head, Anchors: []documentAnchor{}, Findings: []documentFinding{},
			})
		}
		entries[i].Verdict = agentVerdict(c.State)
		// The index lists only comments with a time, so only those anchor.
		if c.CreatedAt != nil {
			entries[i].Anchors = append(entries[i].Anchors, documentAnchor{Type: "pr_comment", ID: jsonString(c.ID)})
		}
		reviewEntry[c.ID] = i
	}
	// A thread's line is placed on the pull request's current head, so it
	// selects a hunk only while the review describes that head.
	lineUsable := b.PullRequest.HeadSHA == head
	for _, c := range b.Conversation {
		i, ok := reviewEntry[c.Review]
		if c.Kind != "review_comment" || !ok || c.Thread != "" && !c.ThreadRoot {
			continue
		}
		entry := &entries[i]
		finding := documentFinding{
			ID: "fnd_" + entry.Tool + "_" + agentToolPattern.ReplaceAllString(strings.ToLower(c.ID), "_"), Severity: findingSeverity(c.Body),
			Text: findingText(c.Body), Anchors: []documentAnchor{}, State: "open",
		}
		if c.Resolved != nil && *c.Resolved {
			finding.State = "resolved"
		}
		var line *int
		if c.Thread != "" {
			finding.Anchors = append(finding.Anchors, documentAnchor{Type: "review_thread", ID: jsonString(c.Thread)})
			for _, t := range b.Threads {
				if t.ID == c.Thread && lineUsable {
					line = t.Line
				}
			}
		}
		if section, hunk := findingLocation(b, sections, c.Path, line); section != "" {
			finding.Section = new(section)
			finding.Anchors = append(finding.Anchors, documentAnchor{Type: "hunk", ID: jsonString(hunk)})
		}
		switch finding.Severity {
		case "must_fix":
			entry.Counts.MustFix++
		case "should_fix":
			entry.Counts.ShouldFix++
		default:
			entry.Counts.Consider++
		}
		entry.Findings = append(entry.Findings, finding)
	}
	return entries
}

func validAssessedHead(sha string) bool {
	if len(sha) < 7 || len(sha) > 64 {
		return false
	}
	for _, r := range sha {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// agentVerdict maps a GitHub review state onto the document's verdicts. A
// dismissed review states no verdict.
func agentVerdict(state string) string {
	switch state {
	case "approved":
		return "approve"
	case "changes_requested":
		return "request_changes"
	case "commented":
		return "comment"
	default:
		return "none"
	}
}

// findingSeverity reads the priority a tool labels its finding with: P0/P1 or
// critical/high severity is must_fix, P2 or medium is should_fix, and any
// other finding, labelled lower or not at all, is consider.
func findingSeverity(body string) string {
	switch {
	case findingMustFix.MatchString(body):
		return "must_fix"
	case findingShouldFix.MatchString(body):
		return "should_fix"
	default:
		return "consider"
	}
}

// findingText is the finding's first line of prose, without badges, markup
// or emphasis.
func findingText(body string) string {
	body = findingHTML.ReplaceAllString(findingMarkdownImage.ReplaceAllString(body, ""), "")
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "#*_` "))
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > maxFindingText {
			line = string([]rune(line)[:maxFindingText-1]) + "…"
		}
		return line
	}
	return "Finding"
}

// findingLocation is the first current section holding a hunk of the
// commented path that contains the thread's line, or any hunk of that path
// when the line is unknown.
func findingLocation(b *bundlepkg.Bundle, sections []documentSection, path string, line *int) (string, string) {
	if path == "" {
		return "", ""
	}
	hunks := map[string]bundlepkg.Hunk{}
	for _, h := range b.Diff {
		hunks[h.ID] = h
	}
	for _, s := range sections {
		for _, id := range s.Hunks {
			h, ok := hunks[id]
			if !ok || h.Path != path {
				continue
			}
			if line == nil || *line >= h.StartLine && *line < h.StartLine+max(h.LineCount, 1) {
				return s.ID, id
			}
		}
	}
	return "", ""
}

const (
	threadsUnavailableNote       = "Review threads are unavailable in this input, so agent-review findings carry no thread state."
	legacyThreadsUnavailableNote = "Review threads and structured external agent reviews are unavailable in this input."
)

// threadNotes states the missing threads exactly while they are missing, so
// a refresh that brings them in also clears the note.
func threadNotes(notes []string, b *bundlepkg.Bundle) []string {
	unavailable := !b.Sources["review_threads"]
	kept, stated := []string{}, false
	for _, note := range notes {
		switch {
		case note == threadsUnavailableNote && unavailable:
			kept, stated = append(kept, note), true
		case note != threadsUnavailableNote && note != legacyThreadsUnavailableNote:
			kept = append(kept, note)
		}
	}
	if unavailable && !stated {
		kept = append(kept, threadsUnavailableNote)
	}
	return kept
}

// countFindings derives the document's finding totals by state and each
// current section's open findings from its agent reviews.
func countFindings(doc *reviewDocument) {
	totals := documentStatsAgentFindings{}
	open := map[string]int{}
	for _, review := range doc.AgentReviews {
		for _, finding := range review.Findings {
			switch finding.State {
			case "resolved":
				totals.Resolved++
			case "dismissed":
				totals.Dismissed++
			default:
				totals.Open++
				if finding.Section != nil {
					open[*finding.Section]++
				}
			}
		}
	}
	doc.Stats.AgentFindings = totals
	for i := range doc.Sections {
		section := &doc.Sections[i]
		section.Derived.OpenFindings = 0
		if section.Derived.State != "stale" {
			section.Derived.OpenFindings = open[section.ID]
		}
	}
}
