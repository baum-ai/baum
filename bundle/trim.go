package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Caps are the configured count and byte ceilings applied to a bundle.
// ApplyCaps, CapPatches, CapAttachments and Fit read the comment, session,
// patch, image and byte limits. MaxPromptsPerSession and
// MaxCommandEventsPerSession are applied by the source that collects
// sessions, and DiffLimits gives the limits diff assembly reads.
type Caps struct {
	MaxSessions                int
	MaxPromptsPerSession       int
	MaxCommandEventsPerSession int
	MaxComments                int
	MaxHunkPatchLines          int
	// MaxBytes bounds the encoded bundle apart from the diff index's hunk
	// entries; patch text counts here. MaxDiffIndexBytes bounds those entries.
	MaxBytes          int
	MaxDiffIndexBytes int
	// MaxDiffFiles and MaxDiffPatchBytes bound what assembly reads for the
	// diff index.
	MaxDiffFiles      int
	MaxDiffPatchBytes int64
	// MaxImageAltBytes bounds each description image's alt text and heading.
	MaxImageAltBytes int
}

// PromptScanChars is the most characters of a prompt's text, and of its
// reply, a session source reads. It is generous enough that the cut never
// fires in practice: byte trimming (Fit) is what bounds prompt text, in the
// documented trim order. It keeps one oversized prompt from being read whole.
const PromptScanChars = 1 << 20

// DiffLimits returns the limits BuildDiffIndexWithin reads.
func (c Caps) DiffLimits() DiffLimits {
	return DiffLimits{MaxFiles: c.MaxDiffFiles, MaxPatchBytes: c.MaxDiffPatchBytes, MaxHunkLines: c.MaxHunkPatchLines}
}

// CapAttachments bounds each attachment's alt text and heading to
// MaxImageAltBytes and notes how many attachments it shortened. It runs after
// redaction, so a cut cannot leave part of a secret unmasked.
func CapAttachments(bundle *Bundle, caps Caps) {
	limit := caps.MaxImageAltBytes
	if limit <= 0 {
		return
	}
	cut := 0
	for i := range bundle.Attachments {
		shortened := false
		for _, text := range []*string{&bundle.Attachments[i].Alt, &bundle.Attachments[i].Heading} {
			if len(*text) > limit {
				*text, shortened = strings.ToValidUTF8((*text)[:limit], ""), true
			}
		}
		if shortened {
			cut++
		}
	}
	if cut > 0 {
		bundle.Truncated = append(bundle.Truncated, fmt.Sprintf("alt text or heading capped to %d bytes for %d description image(s)", limit, cut))
	}
}

// ApplyCaps enforces the count ceilings, after sorting, so the same rows in
// any order produce the same selection: sessions by confidence then earliest
// start; comments to the newest N plus every review verdict. It does not cap
// prompts or command events: the source that collects sessions already
// selected them (MaxPromptsPerSession, MaxCommandEventsPerSession). Nor does
// it cap patch lines: CapPatches does, after redaction.
func ApplyCaps(bundle *Bundle, caps Caps) {
	var notes []string

	sortSessionsForCaps(bundle.Sessions)
	primaryCount := 0
	for _, s := range bundle.Sessions {
		if !s.Restricted {
			primaryCount++
		}
	}
	if limit := max(caps.MaxSessions, 0); primaryCount > limit {
		cut := primaryCount - limit
		notes = append(notes, fmt.Sprintf("%d lower-confidence session(s) omitted beyond the %d-session limit", cut, limit))
		bundle.Sessions = cutLowestConfidenceSessions(bundle.Sessions, limit)
	}
	sortSessionsForPresentation(bundle.Sessions)

	if limit := max(caps.MaxComments, 0); len(bundle.Conversation) > limit {
		before := len(bundle.Conversation)
		bundle.Conversation = capConversation(bundle.Conversation, limit)
		notes = append(notes, fmt.Sprintf("%d older comment(s) omitted beyond the %d-comment limit; every review verdict is kept", before-len(bundle.Conversation), limit))
	}

	bundle.Truncated = append(bundle.Truncated, notes...)
}

// CapPatches bounds each hunk's patch to its leading MaxHunkPatchLines lines
// and notes how many hunks it shortened. It runs after redaction, which reads
// the margin diff parsing keeps past the cap (DiffLimits.MaxHunkLines), so a
// secret the cut runs through is masked whole.
func CapPatches(bundle *Bundle, caps Caps) {
	limit := caps.MaxHunkPatchLines
	if limit <= 0 {
		return
	}
	cutHunks := 0
	for i := range bundle.Diff {
		if cutLines, cut := capPatchLines(bundle.Diff[i].Patch, limit); cut {
			bundle.Diff[i].Patch = cutLines
			cutHunks++
		}
	}
	if cutHunks > 0 {
		bundle.Truncated = append(bundle.Truncated, fmt.Sprintf("patch text capped to the leading %d line(s) for %d hunk(s)", limit, cutHunks))
	}
}

// cutLowestConfidenceSessions drops the lowest-confidence primary sessions
// beyond limit, keeping every restricted session (restricted sessions carry
// no content and are not subject to the primary-session cap).
func cutLowestConfidenceSessions(sessions []Session, limit int) []Session {
	kept := make([]Session, 0, len(sessions))
	primaryKept := 0
	for _, s := range sessions {
		if s.Restricted {
			kept = append(kept, s)
			continue
		}
		if primaryKept < limit {
			kept = append(kept, s)
			primaryKept++
		}
	}
	return kept
}

// capConversation keeps the newest limit comments plus every review verdict
// (kind "review").
func capConversation(items []ConversationItem, limit int) []ConversationItem {
	var reviews, others []ConversationItem
	for _, item := range items {
		if item.Kind == "review" {
			reviews = append(reviews, item)
		} else {
			others = append(others, item)
		}
	}
	if len(others) > limit {
		others = others[len(others)-limit:]
	}
	kept := append(append([]ConversationItem{}, others...), reviews...)
	SortConversation(kept)
	return kept
}

func capPatchLines(patch string, limit int) (string, bool) {
	if patch == "" {
		return patch, false
	}
	lines := splitLines(patch)
	if len(lines) <= limit {
		return patch, false
	}
	return joinLines(lines[:limit]), true
}

func splitLines(s string) []string { return strings.Split(s, "\n") }

func joinLines(lines []string) string { return strings.Join(lines, "\n") }

// EvidenceBudgetExceededReason prefixes the error Fit returns when the
// bundle cannot be made to fit its byte limits.
const EvidenceBudgetExceededReason = "evidence byte budget exceeded"

// Fit trims the encoded bundle in the documented order until its
// content is within MaxBytes, then checks the diff index against its own
// MaxDiffIndexBytes. The diff index
// is every hunk's entry without its patch text; patch text is content. So
// trimming can empty every patch but never drops a hunk, and the hunk count
// alone never exceeds the content limit. The documented order is assistant
// replies, older prompts, command paraphrases, comment bodies oldest first
// (keeping review verdicts), generated and vendored patch text, superseded
// session decisions, ordinary patch text, the text of cited prompts, then whole
// sessions from lowest confidence. A session's current decisions go only with
// the session:
// they are a short, reviewed account of what its prompts decided, so they
// outlast every cut of raw prompt text. The prompts at the turns they cite
// are kept with them: the older-prompts stage spares them, and before any
// session is dropped their text is shortened, longest first, to a leading
// CitedPromptHeadBytes and then, if still needed, emptied, keeping each
// prompt's entry and index so a surviving decision's prompt anchors still
// resolve. REVIEW.md content is cut first,
// ahead of every documented stage, because it is supplementary guidance
// rather than PR evidence, and because a fixed content cap would otherwise
// make a MaxBytes configured below it permanently unsatisfiable. The attachments part counts as content but no
// stage cuts it: the files it lists reach the agent whole (the description-image rules).
func Fit(bundle *Bundle, caps Caps) error {
	if err := fitContent(bundle, caps); err != nil {
		return err
	}
	if limit := caps.MaxDiffIndexBytes; limit > 0 && diffIndexBytes(bundle) > limit {
		return fmt.Errorf("%s: diff index limit %d bytes", EvidenceBudgetExceededReason, limit)
	}
	return nil
}

func fitContent(bundle *Bundle, caps Caps) error {
	limit := caps.MaxBytes
	if limit <= 0 || fits(bundle, limit) {
		return nil
	}
	olderPromptsNote := "older prompts omitted to fit the size limit; the first and latest prompts of each session are kept"
	if citesTurns(bundle) {
		olderPromptsNote = "older prompts omitted to fit the size limit; the first and latest prompts of each session and the prompts their current decisions cite are kept"
	}
	stages := []struct {
		cut  func(*Bundle) bool
		note string
	}{
		{cutReviewConfigContent, "REVIEW.md content omitted to fit the size limit"},
		{cutAssistantReplies, "assistant replies omitted to fit the size limit"},
		{cutOlderPrompts, olderPromptsNote},
		{cutCommandEvents, "command paraphrases omitted to fit the size limit"},
		{cutOldestComments, "older comments omitted to fit the size limit; review verdicts are kept"},
		{cutGeneratedOrVendoredPatches, "generated and vendored patch text omitted to fit the size limit"},
		{cutSupersededDecisions, "superseded session decisions omitted to fit the size limit; current decisions are kept"},
	}
	for _, stage := range stages {
		for stage.cut(bundle) {
			if stage.note != "" {
				bundle.Truncated = append(bundle.Truncated, stage.note)
				stage.note = ""
			}
			if fits(bundle, limit) {
				return nil
			}
		}
	}

	// The checkout carries the source too. Prefer shorter inline patches to
	// dropping the sessions whose decisions explain the change.
	if slices.ContainsFunc(bundle.Diff, func(h Hunk) bool { return h.Patch != "" }) {
		bundle.Truncated = append(bundle.Truncated, "code patch text shortened to fit the size limit; hunk entries are kept")
	}
	for lines := max(caps.MaxHunkPatchLines/2, 1); ; lines /= 2 {
		for i := range bundle.Diff {
			hunk := &bundle.Diff[i]
			if lines > 0 {
				hunk.Patch, _ = capPatchLines(hunk.Patch, lines)
			} else if hunk.Patch != "" {
				hunk.Patch = ""
				hunk.Textless = true
				hunk.TextlessReason = "trimmed_oversized"
			}
		}
		if fits(bundle, limit) {
			return nil
		}
		if lines == 0 {
			break
		}
	}
	shortenedPrompts := false
	for shortenCitedPrompts(bundle) {
		if !shortenedPrompts {
			bundle.Truncated = append(bundle.Truncated, "text of prompts that current decisions cite shortened to fit the size limit, longest first; the prompts and their decisions are kept")
			shortenedPrompts = true
		}
		if fits(bundle, limit) {
			return nil
		}
	}
	for cutLowestConfidenceSession(bundle) {
		if fits(bundle, limit) {
			return nil
		}
	}

	// Every documented stage is exhausted (all removable sessions are gone) and
	// the content still does not fit: a long PR body or retained review-verdict
	// bodies exceed MaxBytes. This is the
	// backstop for when it does not, so the encoded bundle a provider
	// receives never silently exceeds its configured byte ceiling.
	bundle.Truncated = append(bundle.Truncated, "additional content shortened to fit the size limit after every documented stage")
	bundle.PullRequest.Body = ""
	if fits(bundle, limit) {
		return nil
	}
	for i := range bundle.Conversation {
		bundle.Conversation[i].Body = ""
		if fits(bundle, limit) {
			return nil
		}
	}
	return fmt.Errorf("%s: limit %d bytes", EvidenceBudgetExceededReason, limit)
}

// fits reports whether the bundle's content, everything but the diff
// index, is within limit.
func fits(bundle *Bundle, limit int) bool {
	encoded, _ := json.Marshal(bundle)
	return len(encoded)-diffIndexBytes(bundle) <= limit
}

// diffIndexBytes is the encoded size of the diff index: every hunk's entry
// with its patch text left out. A textless marker that byte trimming set
// counts as content too, so trimming content never grows the index. The
// encoded bundle's size is this plus the content fits measures.
func diffIndexBytes(bundle *Bundle) int {
	index := slices.Clone(bundle.Diff)
	for i := range index {
		index[i].Patch = ""
		if strings.HasPrefix(index[i].TextlessReason, "trimmed_") {
			index[i].Textless, index[i].TextlessReason = false, ""
		}
	}
	encoded, _ := json.Marshal(index)
	return len(encoded)
}

func cutReviewConfigContent(bundle *Bundle) bool {
	if bundle.ReviewConfig == nil || bundle.ReviewConfig.Content == "" {
		return false
	}
	bundle.ReviewConfig.Content = ""
	return true
}

func cutAssistantReplies(bundle *Bundle) bool {
	cut := false
	for i := range bundle.Sessions {
		for p := range bundle.Sessions[i].Prompts {
			if bundle.Sessions[i].Prompts[p].Reply != "" {
				bundle.Sessions[i].Prompts[p].Reply = ""
				cut = true
			}
		}
	}
	return cut
}

// cutOlderPrompts keeps each session's first and latest prompts and those at
// the turns its current decisions cite.
func cutOlderPrompts(bundle *Bundle) bool {
	cut := false
	for i := range bundle.Sessions {
		prompts := bundle.Sessions[i].Prompts
		if len(prompts) <= 2 {
			continue
		}
		cited := currentDecisionTurns(bundle.Sessions[i])
		kept := make([]Prompt, 0, 2+len(cited))
		for p, prompt := range prompts {
			if p == 0 || p == len(prompts)-1 || cited[prompt.Index] {
				kept = append(kept, prompt)
			}
		}
		if len(kept) < len(prompts) {
			bundle.Sessions[i].Prompts = kept
			cut = true
		}
	}
	return cut
}

// currentDecisionTurns is the set of turns a session's current (not
// superseded) decisions cite.
func currentDecisionTurns(session Session) map[int]bool {
	if session.Decisions == nil {
		return nil
	}
	turns := map[int]bool{}
	for _, decision := range *session.Decisions {
		if !decision.Superseded {
			for _, turn := range decision.Turns {
				turns[turn] = true
			}
		}
	}
	return turns
}

// citesTurns reports whether any session's current decisions cite a turn.
func citesTurns(bundle *Bundle) bool {
	for _, session := range bundle.Sessions {
		if len(currentDecisionTurns(session)) > 0 {
			return true
		}
	}
	return false
}

func cutCommandEvents(bundle *Bundle) bool {
	cut := false
	for i := range bundle.Sessions {
		if len(bundle.Sessions[i].CommandEvents) > 0 {
			bundle.Sessions[i].CommandEvents = nil
			cut = true
		}
	}
	return cut
}

func cutOldestComments(bundle *Bundle) bool {
	var reviews, others []ConversationItem
	for _, item := range bundle.Conversation {
		if item.Kind == "review" {
			reviews = append(reviews, item)
		} else {
			others = append(others, item)
		}
	}
	if len(others) == 0 {
		return false
	}
	others = others[1:]
	bundle.Conversation = append(append([]ConversationItem{}, others...), reviews...)
	SortConversation(bundle.Conversation)
	return true
}

// cutGeneratedOrVendoredPatches marks the same Textless flag a genuinely
// binary/unreadable hunk carries (see diff.go), since either way the
// patch text is absent; TextlessReason ("trimmed_generated"/"trimmed_vendored"
// here vs. "binary" in diff.go) is what a downstream consumer (the checkout
// omissions, and later the model) must read to tell "cut for space" apart
// from "no text ever existed."
func cutGeneratedOrVendoredPatches(bundle *Bundle) bool {
	cut := false
	for i := range bundle.Diff {
		if (bundle.Diff[i].Role == RoleGenerated || bundle.Diff[i].Role == RoleVendored) && bundle.Diff[i].Patch != "" {
			bundle.Diff[i].Patch = ""
			bundle.Diff[i].Textless = true
			bundle.Diff[i].TextlessReason = "trimmed_" + bundle.Diff[i].Role
			cut = true
		}
	}
	return cut
}

// cutSupersededDecisions drops decisions a later decision of the same
// session reversed or replaced. The session keeps its (possibly now empty)
// list, so it still reads as extracted.
func cutSupersededDecisions(bundle *Bundle) bool {
	cut := false
	for i := range bundle.Sessions {
		decisions := bundle.Sessions[i].Decisions
		if decisions == nil {
			continue
		}
		kept := slices.DeleteFunc(slices.Clone(*decisions), func(d SessionDecision) bool { return d.Superseded })
		if len(kept) < len(*decisions) {
			*decisions = kept
			cut = true
		}
	}
	return cut
}

// CitedPromptHeadBytes is how much of a cited prompt's text the first
// shortening keeps.
const CitedPromptHeadBytes = 1024

// shortenCitedPrompts shortens one prompt a current decision cites per call:
// the longest one over CitedPromptHeadBytes to its leading bytes, or, once
// none is, the longest one with text to empty text. Ties go to the earlier
// session, then the earlier prompt, so the result is deterministic. The
// prompt's entry and index stay.
func shortenCitedPrompts(bundle *Bundle) bool {
	for _, over := range []int{CitedPromptHeadBytes, 0} {
		var longest *Prompt
		for i := range bundle.Sessions {
			cited := currentDecisionTurns(bundle.Sessions[i])
			for p := range bundle.Sessions[i].Prompts {
				prompt := &bundle.Sessions[i].Prompts[p]
				if cited[prompt.Index] && len(prompt.Text) > over && (longest == nil || len(prompt.Text) > len(longest.Text)) {
					longest = prompt
				}
			}
		}
		if longest != nil {
			longest.Text = strings.ToValidUTF8(longest.Text[:over], "")
			return true
		}
	}
	return false
}

func cutLowestConfidenceSession(bundle *Bundle) bool {
	for i := len(bundle.Sessions) - 1; i >= 0; i-- {
		if !bundle.Sessions[i].Restricted {
			bundle.Sessions = append(bundle.Sessions[:i], bundle.Sessions[i+1:]...)
			return true
		}
	}
	return false
}

// SortConversation orders items by creation time, undated items last, then
// by ID.
func SortConversation(items []ConversationItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.CreatedAt == nil || b.CreatedAt == nil {
			if a.CreatedAt != b.CreatedAt {
				return b.CreatedAt == nil
			}
		} else if !a.CreatedAt.Equal(*b.CreatedAt) {
			return a.CreatedAt.Before(*b.CreatedAt)
		}
		return a.ID < b.ID
	})
}

// sortSessionsForCaps orders sessions by highest confidence first then
// earliest start, mirroring the confidence order the PR comment uses.
func sortSessionsForCaps(sessions []Session) {
	sort.SliceStable(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		if a.Restricted != b.Restricted {
			return !a.Restricted
		}
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		return lessByStartedAt(a, b)
	})
}

// sortSessionsForPresentation orders sessions chronologically, restricted
// sessions last.
func sortSessionsForPresentation(sessions []Session) {
	sort.SliceStable(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		if a.Restricted != b.Restricted {
			return !a.Restricted
		}
		return lessByStartedAt(a, b)
	})
}

func lessByStartedAt(a, b Session) bool {
	switch {
	case a.HasStartedAt && b.HasStartedAt && !a.StartedAtValue.Equal(b.StartedAtValue):
		return a.StartedAtValue.Before(b.StartedAtValue)
	case a.HasStartedAt != b.HasStartedAt:
		return a.HasStartedAt
	}
	return a.RootSessionID < b.RootSessionID
}

// SetDigest stamps bundle.Digest with the SHA-256 of the encoded bundle, so
// identical content in any order produces an identical digest.
func SetDigest(bundle *Bundle) error {
	encoded, err := json.Marshal(bundle)
	if err != nil {
		return fmt.Errorf("encoding bundle for digest: %w", err)
	}
	sum := sha256.Sum256(encoded)
	bundle.Digest = hex.EncodeToString(sum[:])
	return nil
}
