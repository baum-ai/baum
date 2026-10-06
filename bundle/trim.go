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

// ApplyCaps enforces the count ceilings, after sorting, so the same rows in
// any order produce the same selection: sessions by
// confidence then earliest start; each session's prompts to first+latest;
// each session's command events already capped to latest N by the SQL
// layer; comments to the newest N plus every review verdict; each hunk's
// patch to its leading lines.
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

	if limit := max(caps.MaxHunkPatchLines, 0); limit > 0 {
		cutHunks := 0
		for i := range bundle.Diff {
			if cutLines, cut := capPatchLines(bundle.Diff[i].Patch, limit); cut {
				bundle.Diff[i].Patch = cutLines
				cutHunks++
			}
		}
		if cutHunks > 0 {
			notes = append(notes, fmt.Sprintf("patch text capped to the leading %d line(s) for %d hunk(s)", limit, cutHunks))
		}
	}

	bundle.Truncated = append(bundle.Truncated, notes...)
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
// (keeping review verdicts), generated and vendored patch text, then whole
// sessions from the lowest confidence. REVIEW.md content is cut first,
// ahead of every documented stage, because it is supplementary guidance
// rather than PR evidence, and because a fixed content cap would otherwise
// make a MaxBytes configured below it permanently unsatisfiable. The attachments part counts as content but no stage cuts
// it: the files it lists reach the agent whole (the description-image rules).
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
	stages := []struct {
		cut  func(*Bundle) bool
		note string
	}{
		{cutReviewConfigContent, "REVIEW.md content omitted to fit the size limit"},
		{cutAssistantReplies, "assistant replies omitted to fit the size limit"},
		{cutOlderPrompts, "older prompts omitted to fit the size limit; the first and latest prompts of each session are kept"},
		{cutCommandEvents, "command paraphrases omitted to fit the size limit"},
		{cutOldestComments, "older comments omitted to fit the size limit; review verdicts are kept"},
		{cutGeneratedOrVendoredPatches, "generated and vendored patch text omitted to fit the size limit"},
		{cutLowestConfidenceSession, ""},
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

	// Every documented stage is exhausted (bundle.Sessions is now empty) and
	// the content still does not fit: a long PR body, long patches of
	// ordinary hunks, or retained review-verdict bodies exceed MaxBytes. The
	// documented stages assume trimming always succeeds; this is the
	// backstop for when it does not, so the encoded bundle a provider
	// receives never silently exceeds its configured byte ceiling.
	bundle.Truncated = append(bundle.Truncated, "additional content shortened to fit the size limit after every documented stage")
	bundle.PullRequest.Body = ""
	if fits(bundle, limit) {
		return nil
	}
	// Shorten every patch equally, retaining leading lines and every hunk ID.
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

func cutOlderPrompts(bundle *Bundle) bool {
	cut := false
	for i := range bundle.Sessions {
		prompts := bundle.Sessions[i].Prompts
		if len(prompts) > 2 {
			bundle.Sessions[i].Prompts = append([]Prompt{prompts[0]}, prompts[len(prompts)-1])
			cut = true
		}
	}
	return cut
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
