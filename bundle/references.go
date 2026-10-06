package bundle

import (
	"regexp"
	"sort"
)

// Reference kinds.
const (
	ReferenceIssueKey             = "issue_key"
	ReferenceSpec                 = "spec"
	ReferenceCherryPick           = "cherry_pick"
	ReferenceStackedOn            = "stacked_on"
	ReferenceSupersedes           = "supersedes"
	ReferenceIncludes             = "includes"
	ReferenceFollowUp             = "follow_up"
	ReferenceAuthorshipDisclosure = "authorship_disclosure"
	ReferenceImage                = "image"
	ReferenceVideo                = "video"
)

var (
	// issueKeyPattern matches a Linear/Jira-style issue key: 2+ uppercase
	// letters, a hyphen, digits (e.g. ABC-123). Word-bounded so it does not
	// match inside a longer token.
	issueKeyPattern = regexp.MustCompile(`\b[A-Z][A-Z0-9]{1,9}-\d+\b`)
	specPattern     = regexp.MustCompile(`\bSPEC-\d+(?:-A\d+)?\b`)

	cherryPickPattern = regexp.MustCompile(`(?i)\bcherry[- ]?pick(?:s|ed|ing)?\s+(?:of\s+)?(?:#(\d+)|([0-9a-f]{7,40}))`)
	stackedOnPattern  = regexp.MustCompile(`(?i)\bstacked\s+on\s+#(\d+)`)
	supersedesPattern = regexp.MustCompile(`(?i)\bsupersedes\s+#(\d+)`)
	includesPattern   = regexp.MustCompile(`(?i)\bincludes\s+#(\d+)`)
	followUpPattern   = regexp.MustCompile(`(?i)\bfollow[- ]?up\s+(?:to|for|of)\s+#(\d+)`)

	authorshipDisclosurePattern = regexp.MustCompile(`(?i)\b(?:generated|written|authored|co-authored|reviewed)\s+(?:by|with)\s+(?:an?\s+)?(?:ai|llm|agent|claude|codex|copilot|cursor|devin)\b[^.\n]{0,80}`)

	imageURLPattern = regexp.MustCompile(`(?i)\bhttps?://\S+\.(?:png|jpe?g|gif|webp|svg)\b`)
	videoURLPattern = regexp.MustCompile(`(?i)\bhttps?://\S+\.(?:mp4|mov|webm)\b`)
)

// ExtractReferences finds every recognised reference in text, tagging each
// with source.
func ExtractReferences(text, source string) []Reference {
	if text == "" {
		return nil
	}
	var refs []Reference
	add := func(kind, value string) {
		refs = append(refs, Reference{Kind: kind, Value: value, Source: source})
	}
	for _, m := range issueKeyPattern.FindAllString(text, -1) {
		add(ReferenceIssueKey, m)
	}
	for _, m := range specPattern.FindAllString(text, -1) {
		add(ReferenceSpec, m)
	}
	for _, m := range cherryPickPattern.FindAllStringSubmatch(text, -1) {
		add(ReferenceCherryPick, prNumberOrSHA(m))
	}
	for _, m := range stackedOnPattern.FindAllStringSubmatch(text, -1) {
		add(ReferenceStackedOn, "#"+m[1])
	}
	for _, m := range supersedesPattern.FindAllStringSubmatch(text, -1) {
		add(ReferenceSupersedes, "#"+m[1])
	}
	for _, m := range includesPattern.FindAllStringSubmatch(text, -1) {
		add(ReferenceIncludes, "#"+m[1])
	}
	for _, m := range followUpPattern.FindAllStringSubmatch(text, -1) {
		add(ReferenceFollowUp, "#"+m[1])
	}
	for _, m := range authorshipDisclosurePattern.FindAllString(text, -1) {
		add(ReferenceAuthorshipDisclosure, m)
	}
	for _, m := range imageURLPattern.FindAllString(text, -1) {
		add(ReferenceImage, m)
	}
	for _, m := range videoURLPattern.FindAllString(text, -1) {
		add(ReferenceVideo, m)
	}
	return refs
}

func prNumberOrSHA(match []string) string {
	if match[1] != "" {
		return "#" + match[1]
	}
	return match[2]
}

// CollectReferences extracts the references of a bundle's pull request
// description, head branch, commit subjects and conversation, de-duplicated
// and ordered. It reads untrimmed text, so call it before caps and trimming.
func CollectReferences(b Bundle) []Reference {
	var refs []Reference
	refs = append(refs, ExtractReferences(b.PullRequest.Body, "body")...)
	refs = append(refs, ExtractReferences(b.PullRequest.HeadRef, "branch")...)
	for _, commit := range b.PullRequest.Commits {
		refs = append(refs, ExtractReferences(commit.Subject, "commit_subject")...)
	}
	for _, item := range b.Conversation {
		refs = append(refs, ExtractReferences(item.Body, "comment:"+item.ID)...)
	}
	return dedupeReferences(refs)
}

func dedupeReferences(refs []Reference) []Reference {
	seen := make(map[string]bool, len(refs))
	out := make([]Reference, 0, len(refs))
	for _, ref := range refs {
		key := ref.Kind + "\x00" + ref.Value + "\x00" + ref.Source
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Value != out[j].Value {
			return out[i].Value < out[j].Value
		}
		return out[i].Source < out[j].Source
	})
	return out
}
