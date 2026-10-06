package bundle

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// These test the individual trim-order steps directly (Fit:
// assistant replies, older prompts, command paraphrases,
// comment bodies oldest-first keeping review verdicts, generated/vendored
// patch text, then whole sessions from lowest confidence), which is more
// robust than reverse-engineering exact byte thresholds for the cascading
// Fit driver. TestFitBytesStopsAsSoonAsItFits below checks the
// cascade itself stops at the first stage that satisfies the limit.

const testHunk = "h_0123456789ab"

func mustJSON(t *testing.T, bundle *Bundle) []byte {
	t.Helper()
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return encoded
}

func mkCreatedAt(offsetHours int) *time.Time {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(offsetHours) * time.Hour)
	return &t
}

func TestCutAssistantReplies(t *testing.T) {
	bundle := &Bundle{Sessions: []Session{{Prompts: []Prompt{
		{ID: "p1", Text: "first", Reply: "reply one"},
		{ID: "p2", Text: "second", Reply: "reply two"},
	}}}}
	if !cutAssistantReplies(bundle) {
		t.Fatal("expected a cut")
	}
	for _, p := range bundle.Sessions[0].Prompts {
		if p.Reply != "" {
			t.Errorf("expected reply cleared, got %+v", p)
		}
		if p.Text == "" {
			t.Errorf("expected prompt text preserved, got %+v", p)
		}
	}
	if cutAssistantReplies(bundle) {
		t.Error("expected no further cut once replies are already empty")
	}
}

func TestCutOlderPromptsKeepsFirstAndLatest(t *testing.T) {
	bundle := &Bundle{Sessions: []Session{{Prompts: []Prompt{
		{ID: "p1"}, {ID: "p2"}, {ID: "p3"}, {ID: "p4"},
	}}}}
	if !cutOlderPrompts(bundle) {
		t.Fatal("expected a cut")
	}
	got := bundle.Sessions[0].Prompts
	if len(got) != 2 || got[0].ID != "p1" || got[1].ID != "p4" {
		t.Fatalf("expected [p1, p4], got %+v", got)
	}
}

func TestCutOldestCommentsKeepsReviewVerdicts(t *testing.T) {
	older := mkCreatedAt(1)
	newer := mkCreatedAt(2)
	bundle := &Bundle{Conversation: []ConversationItem{
		{ID: "c1", Kind: "comment", CreatedAt: older},
		{ID: "c2", Kind: "comment", CreatedAt: newer},
		{ID: "r1", Kind: "review", State: "approved", CreatedAt: older},
	}}
	if !cutOldestComments(bundle) {
		t.Fatal("expected a cut")
	}
	var kinds []string
	for _, item := range bundle.Conversation {
		kinds = append(kinds, item.ID)
	}
	if len(bundle.Conversation) != 2 {
		t.Fatalf("expected the oldest plain comment dropped, review kept, got %v", kinds)
	}
	for _, item := range bundle.Conversation {
		if item.ID == "c1" {
			t.Fatalf("expected oldest comment c1 cut first, got %v", kinds)
		}
	}
}

func TestCutGeneratedOrVendoredPatches(t *testing.T) {
	bundle := &Bundle{Diff: []Hunk{
		{ID: "h1", Role: RoleCode, Patch: "code patch"},
		{ID: "h2", Role: RoleGenerated, Patch: "generated patch"},
		{ID: "h3", Role: RoleVendored, Patch: "vendored patch"},
	}}
	if !cutGeneratedOrVendoredPatches(bundle) {
		t.Fatal("expected a cut")
	}
	if bundle.Diff[0].Patch == "" {
		t.Error("code-role patch must survive this stage")
	}
	if bundle.Diff[1].Patch != "" || !bundle.Diff[1].Textless {
		t.Errorf("generated patch should be cleared and marked textless: %+v", bundle.Diff[1])
	}
	if bundle.Diff[2].Patch != "" || !bundle.Diff[2].Textless {
		t.Errorf("vendored patch should be cleared and marked textless: %+v", bundle.Diff[2])
	}
}

func TestCutLowestConfidenceSessionKeepsRestricted(t *testing.T) {
	bundle := &Bundle{Sessions: []Session{
		{RootSessionID: "primary", Confidence: 0.5},
		{RootSessionID: "restricted", Restricted: true},
	}}
	if !cutLowestConfidenceSession(bundle) {
		t.Fatal("expected a cut")
	}
	if len(bundle.Sessions) != 1 || bundle.Sessions[0].RootSessionID != "restricted" {
		t.Fatalf("expected only the restricted session to survive, got %+v", bundle.Sessions)
	}
	if cutLowestConfidenceSession(bundle) {
		t.Error("restricted sessions must never be cut by this stage")
	}
}

func TestFitBytesStopsAsSoonAsItFits(t *testing.T) {
	build := func() *Bundle {
		return &Bundle{Sessions: []Session{{
			RootSessionID: "s1",
			Prompts: []Prompt{
				{ID: "p1", Text: "first", Reply: strings.Repeat("a", 500)},
				{ID: "p2", Text: "second", Reply: strings.Repeat("b", 500)},
			},
		}}}
	}
	// Determine the exact size after only the assistant-reply cut (matching
	// Fit's own sequencing: cut, then append the truncation note, then
	// measure), and set MaxBytes to that — the cascade must stop there and
	// go no further.
	afterReplyCut := build()
	cutAssistantReplies(afterReplyCut)
	afterReplyCut.Truncated = append(afterReplyCut.Truncated, "assistant replies omitted to fit the size limit")
	limit := len(mustJSON(t, afterReplyCut))

	bundle := build()
	_ = Fit(bundle, Caps{MaxBytes: limit})

	if len(bundle.Sessions[0].Prompts) != 2 {
		t.Fatalf("expected both prompts to survive (only replies should be cut at this limit), got %+v", bundle.Sessions[0].Prompts)
	}
	for _, p := range bundle.Sessions[0].Prompts {
		if p.Reply != "" {
			t.Fatalf("expected replies cut, got %+v", p)
		}
	}
}

// TestFitBytesLoopsCommentCutUntilItFits guards against cutOldestComments
// being called only once by Fit: since it removes one comment per
// call (unlike the bulk stages around it), a bundle with enough stale
// comments that one cut isn't sufficient must not fall through to trimming
// patch text and sessions while comments remain.
func TestFitBytesLoopsCommentCutUntilItFits(t *testing.T) {
	var conversation []ConversationItem
	for i := 0; i < 5; i++ {
		conversation = append(conversation, ConversationItem{
			ID: strings.Repeat("c", 1) + string(rune('0'+i)), Kind: "comment",
			Body: strings.Repeat("x", 2000), CreatedAt: mkCreatedAt(i),
		})
	}
	conversation = append(conversation, ConversationItem{
		ID: "review", Kind: "review", State: "approved", Body: strings.Repeat("y", 500), CreatedAt: mkCreatedAt(0),
	})
	bundle := &Bundle{Conversation: conversation}

	// A limit that only the review verdict plus at most one comment can fit
	// in: reaching it requires cutting 4 of the 5 plain comments, not just 1.
	caps := Caps{MaxBytes: 3000}
	_ = Fit(bundle, caps)

	plainComments := 0
	hasReview := false
	for _, item := range bundle.Conversation {
		if item.Kind == "review" {
			hasReview = true
		} else {
			plainComments++
		}
	}
	if !hasReview {
		t.Fatalf("expected the review verdict to survive, got %+v", bundle.Conversation)
	}
	if plainComments > 1 {
		t.Fatalf("expected the comment cut to loop down to at most 1 plain comment, got %d: %+v", plainComments, bundle.Conversation)
	}
	if !fits(bundle, 3000) {
		t.Fatalf("bundle still exceeds the byte limit after Fit: %d bytes", len(mustJSON(t, bundle)))
	}
}

// TestFitBytesBackstopTrimsWhatDocumentedStagesCannotReach covers content
// the documented trim order cannot shrink at all: a large PR body, a
// non-generated/vendored patch (so cutGeneratedOrVendoredPatches skips it),
// and a review verdict's body (cutOldestComments explicitly keeps review
// verdicts, and there are no sessions to fall back to). Without a final
// backstop, Fit would exhaust every stage and silently return a bundle
// well over MaxBytes.
func TestFitBytesBackstopTrimsWhatDocumentedStagesCannotReach(t *testing.T) {
	build := func() *Bundle {
		return &Bundle{
			PullRequest: PullRequest{Body: strings.Repeat("p", 5000)},
			Diff:        []Hunk{{ID: "h1", Path: "a.go", Role: RoleCode, Patch: strings.Repeat("d", 5000)}},
			Conversation: []ConversationItem{
				{ID: "r1", Kind: "review", State: "approved", Body: strings.Repeat("v", 5000)},
			},
		}
	}
	// A limit only the fully backstopped skeleton fits in — well below the
	// ~15KB of untrimmed content, but not so tight the structural JSON
	// overhead alone can't fit (that would test the limit, not the fix).
	skeleton := build()
	skeleton.PullRequest.Body = ""
	skeleton.Diff[0].Patch, skeleton.Diff[0].Textless, skeleton.Diff[0].TextlessReason = "", true, "trimmed_oversized"
	skeleton.Conversation[0].Body = ""
	skeleton.Truncated = append(skeleton.Truncated, "additional content shortened to fit the size limit after every documented stage")
	limit := len(mustJSON(t, skeleton)) - diffIndexBytes(skeleton)

	bundle := build()
	caps := Caps{MaxBytes: limit}
	_ = Fit(bundle, caps)

	if !fits(bundle, limit) {
		t.Fatalf("bundle still exceeds the byte limit after Fit: %d bytes, limit %d", len(mustJSON(t, bundle)), limit)
	}
	if bundle.PullRequest.Body != "" {
		t.Errorf("expected the PR body truncated by the backstop, got %q", bundle.PullRequest.Body)
	}
	if bundle.Diff[0].Patch != "" || !bundle.Diff[0].Textless {
		t.Errorf("expected the non-generated/vendored patch truncated by the backstop, got %+v", bundle.Diff[0])
	}
	if bundle.Conversation[0].Kind != "review" {
		t.Fatalf("the review verdict item itself must survive, got %+v", bundle.Conversation)
	}
	if bundle.Conversation[0].Body != "" {
		t.Errorf("expected the review verdict's body truncated by the backstop (the item itself is kept, only its body shrinks), got %q", bundle.Conversation[0].Body)
	}
	found := false
	for _, note := range bundle.Truncated {
		if note == "additional content shortened to fit the size limit after every documented stage" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a backstop truncation note, got %v", bundle.Truncated)
	}
}

func TestFitBytesNoopWhenAlreadyWithinLimit(t *testing.T) {
	caps := Caps{MaxBytes: 1 << 20}
	bundle := &Bundle{PullRequest: PullRequest{Title: "small"}}
	_ = Fit(bundle, caps)
	if len(bundle.Truncated) != 0 {
		t.Errorf("expected no truncation notes when already within limit, got %v", bundle.Truncated)
	}
}

// TestFitBytesCutsReviewConfigContentBeforeAnyDocumentedStage guards the gap
// a fixed per-field cap (maxReviewConfigContentBytes) leaves open:
// capReviewConfigContent bounds REVIEW.md content independently of MaxBytes,
// but nothing previously let Fit shrink it further, so a MaxBytes
// configured below that cap made every review for the repository fail with
// EvidenceBudgetExceededReason forever. REVIEW.md content is supplementary,
// not PR evidence, so it must be the first thing cut — this bundle has
// nothing else Fit's documented stages could trim (no sessions,
// comments or patches), so a passing result proves cutReviewConfigContent
// ran, not some other stage.
func TestFitBytesCutsReviewConfigContentBeforeAnyDocumentedStage(t *testing.T) {
	bundle := &Bundle{
		PullRequest:  PullRequest{Title: "small"},
		ReviewConfig: &ReviewConfig{Present: true, Content: strings.Repeat("g", 2000)},
	}
	// The skeleton Fit must reach: content emptied, Present kept, and
	// the truncation note itself added — the note's own bytes count too.
	limit := len(mustJSON(t, &Bundle{
		PullRequest:  bundle.PullRequest,
		ReviewConfig: &ReviewConfig{Present: true},
		Truncated:    []string{"REVIEW.md content omitted to fit the size limit"},
	}))

	caps := Caps{MaxBytes: limit}
	if err := Fit(bundle, caps); err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if bundle.ReviewConfig.Content != "" {
		t.Errorf("expected REVIEW.md content cut to fit the size limit, got %d bytes", len(bundle.ReviewConfig.Content))
	}
	if !bundle.ReviewConfig.Present {
		t.Errorf("expected Present to survive the cut, only Content should be cleared")
	}
	found := false
	for _, note := range bundle.Truncated {
		if note == "REVIEW.md content omitted to fit the size limit" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a REVIEW.md truncation note, got %v", bundle.Truncated)
	}
}

// plantedSecret is a syntactically valid (but fake) GitHub personal access
// token goredact's balanced profile reliably detects. It is a fixture value
// goredact's validators accept, since they reject secrets that merely look
// pattern-shaped without passing their real format checks.
const plantedSecret = "ghp" + "_JTeKdNnFRIBXuDL7DxtpYlSXpfKtHF4vUCsM"

func TestRedactStringMasksSecretAndCountsSpans(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatalf("NewRedactor: %v", err)
	}
	redacted, count, err := redactor.RedactString(context.Background(), "token is "+plantedSecret+" in this body")
	if err != nil {
		t.Fatalf("RedactString: %v", err)
	}
	if strings.Contains(redacted, plantedSecret) {
		t.Fatalf("planted secret survived redaction: %q", redacted)
	}
	if count == 0 {
		t.Fatalf("expected at least one masked span, got 0")
	}
}

func TestRedactStringRedactsEmail(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatalf("NewRedactor: %v", err)
	}
	redacted, _, err := redactor.RedactString(context.Background(), "contact person@example.com for details")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(redacted, "person@example.com") {
		t.Fatalf("email survived redaction: %q", redacted)
	}
}

func TestRedactStringEmptyIsNoop(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	redacted, count, err := redactor.RedactString(context.Background(), "")
	if err != nil || redacted != "" || count != 0 {
		t.Fatalf("expected empty no-op, got (%q, %d, %v)", redacted, count, err)
	}
}

func TestRedactPatchTextPreservesLineCount(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	patch := "@@ -1,2 +1,2 @@\n-old " + plantedSecret + "\n+new line\n"
	redacted, count, err := redactor.RedactPatchText(context.Background(), patch)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(redacted, plantedSecret) {
		t.Fatalf("planted secret survived patch redaction: %q", redacted)
	}
	wantLines := len(strings.Split(patch, "\n"))
	gotLines := len(strings.Split(redacted, "\n"))
	if wantLines != gotLines {
		t.Fatalf("patch redaction changed line count: want %d, got %d (%q -> %q)", wantLines, gotLines, patch, redacted)
	}
	if count == 0 {
		t.Fatalf("expected masked spans counted in patch redaction")
	}
}

// TestRedactCoversEveryBundleStringField guards against the
// whole-bundle redact pass silently skipping a field: README.md ("Evidence
// bundle") requires secret redaction of every free-text field (goredact) and
// identity redaction over every string field of the bundle type — including
// bundle.Session.Title, which is free text (often an LLM-generated summary),
// and the command-paraphrase entries' Program/Target fields.
func TestRedactCoversEveryBundleStringField(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	bundle := &Bundle{
		PullRequest: PullRequest{
			Title: "PR", Body: "body",
			HeadRef:            "feature/leak-" + plantedSecret,
			BaseRef:            "main-" + plantedSecret,
			RequestedReviewers: []string{"octocat-" + plantedSecret},
		},
		Checks: []Check{{
			ID: "c1", Name: "build (" + plantedSecret + ")",
			URL: "https://ci.example.test/run?token=" + plantedSecret,
		}},
		Conversation: []ConversationItem{{
			ID: "cm1", Kind: "review_comment", Path: "src/" + plantedSecret + "/main.go",
		}},
		References: []Reference{{
			Kind: "authorship_disclosure", Value: "generated by an automated tool, contact " + plantedSecret, Source: "body",
		}},
		Sessions: []Session{{
			RootSessionID: "s1",
			Title:         "Fix the outage using token " + plantedSecret,
			CommandEvents: []CommandEvent{{
				ID: "e1", Outcome: "succeeded",
				Commands: []CommandEntry{{
					Program: plantedSecret, Category: "other", Target: "path/with/" + plantedSecret,
				}},
			}},
		}},
	}
	if err := Redact(context.Background(), bundle, redactor); err != nil {
		t.Fatalf("redact: %v", err)
	}
	if strings.Contains(bundle.Sessions[0].Title, plantedSecret) {
		t.Errorf("session title was not redacted: %q", bundle.Sessions[0].Title)
	}
	entry := bundle.Sessions[0].CommandEvents[0].Commands[0]
	if strings.Contains(entry.Program, plantedSecret) {
		t.Errorf("command entry Program was not redacted: %q", entry.Program)
	}
	if strings.Contains(entry.Target, plantedSecret) {
		t.Errorf("command entry Target was not redacted: %q", entry.Target)
	}
	if strings.Contains(bundle.PullRequest.HeadRef, plantedSecret) {
		t.Errorf("head ref was not redacted: %q", bundle.PullRequest.HeadRef)
	}
	if strings.Contains(bundle.PullRequest.BaseRef, plantedSecret) {
		t.Errorf("base ref was not redacted: %q", bundle.PullRequest.BaseRef)
	}
	if strings.Contains(bundle.PullRequest.RequestedReviewers[0], plantedSecret) {
		t.Errorf("requested reviewer was not redacted: %q", bundle.PullRequest.RequestedReviewers[0])
	}
	if strings.Contains(bundle.Checks[0].Name, plantedSecret) {
		t.Errorf("check name was not redacted: %q", bundle.Checks[0].Name)
	}
	if strings.Contains(bundle.Checks[0].URL, plantedSecret) {
		t.Errorf("check URL was not redacted: %q", bundle.Checks[0].URL)
	}
	if strings.Contains(bundle.Conversation[0].Path, plantedSecret) {
		t.Errorf("conversation path was not redacted: %q", bundle.Conversation[0].Path)
	}
	if strings.Contains(bundle.References[0].Value, plantedSecret) {
		t.Errorf("reference value was not redacted: %q", bundle.References[0].Value)
	}
	if bundle.MaskedSpanCount == 0 {
		t.Error("expected masked spans counted across the whole bundle")
	}
}

func TestFitBytesRetainsLeadingPatchLines(t *testing.T) {
	bundle := &Bundle{Diff: []Hunk{
		{ID: "one", Patch: strings.Repeat("one line of patch\n", 200)},
		{ID: "two", Patch: strings.Repeat("other line of patch\n", 200)},
	}}
	caps := Caps{MaxBytes: 3000, MaxHunkPatchLines: 200}
	_ = Fit(bundle, caps)
	if !fits(bundle, 3000) {
		t.Fatal("bundle exceeds cap")
	}
	for _, hunk := range bundle.Diff {
		if hunk.Patch == "" || hunk.Textless || len(splitLines(hunk.Patch)) >= 200 {
			t.Fatalf("lost all patch text: %+v", hunk)
		}
	}
}

func TestFitBytesRejectsStructuralOversize(t *testing.T) {
	caps := Caps{MaxBytes: 100}
	bundle := &Bundle{Diff: []Hunk{{ID: testHunk, Path: "main.go", Role: RoleCode}}, Sessions: []Session{{RootSessionID: "restricted", Vendor: "claude", Restricted: true}}}
	err := Fit(bundle, caps)
	if err == nil || !strings.HasPrefix(err.Error(), EvidenceBudgetExceededReason) {
		t.Fatalf("expected a named evidence budget failure, got %v", err)
	}
}

// TestFitBytesKeepsEveryHunkOfALargeDiff covers a pull request with 1,000
// hunks, whose entries alone exceed the content limit and whose patch text
// must be trimmed: the diff index has its own limit, so every hunk stays
// listed, content is trimmed to its limit, and the markers trimming sets do
// not push an index that fit over its limit.
func TestFitBytesKeepsEveryHunkOfALargeDiff(t *testing.T) {
	bundle := &Bundle{PullRequest: PullRequest{Title: "large"}}
	for i := range 1000 {
		path, role := fmt.Sprintf("src/file_%04d.go", i), RoleCode
		if i%2 == 0 {
			role = RoleGenerated
		}
		bundle.Diff = append(bundle.Diff, Hunk{
			ID: fmt.Sprintf("h_%012x", i), Path: path, Change: "modified", Role: role,
			StartLine: 10, LineCount: 1, Additions: 1, DiffURL: fmt.Sprintf("diff-%064xR10", i), Patch: "@@ -10,0 +10,1 @@" + strings.Repeat("\n+changed line", 20),
		})
	}
	limit, index := 64<<10, diffIndexBytes(bundle)
	if index <= limit || len(mustJSON(t, bundle))-index <= limit {
		t.Fatalf("fixture index %d and content %d bytes must both exceed the %d-byte content limit", index, len(mustJSON(t, bundle))-index, limit)
	}
	caps := Caps{MaxBytes: limit, MaxDiffIndexBytes: index, MaxHunkPatchLines: 200}
	if err := Fit(bundle, caps); err != nil {
		t.Fatalf("Fit: %v", err)
	}
	if len(bundle.Diff) != 1000 {
		t.Fatalf("diff index has %d hunks, want 1000", len(bundle.Diff))
	}
	if !fits(bundle, limit) || len(bundle.Truncated) == 0 {
		t.Fatalf("content not trimmed to its limit: truncated %v", bundle.Truncated)
	}
	if bundle.Diff[0].TextlessReason != "trimmed_generated" {
		t.Fatalf("generated patch not trimmed: %+v", bundle.Diff[0])
	}
	if diffIndexBytes(bundle) != index {
		t.Fatalf("trimming changed the diff index from %d to %d bytes", index, diffIndexBytes(bundle))
	}
	// The index sits exactly at its limit and is accepted; the whole bundle
	// is over the content limit but within the sum the generation side takes.
	if encoded := len(mustJSON(t, bundle)); encoded <= limit || encoded > caps.MaxBytes+caps.MaxDiffIndexBytes {
		t.Fatalf("encoded bundle is %d bytes, want over %d and within %d", encoded, limit, caps.MaxBytes+caps.MaxDiffIndexBytes)
	}
}

func TestFitBytesRejectsDiffIndexOverItsLimit(t *testing.T) {
	bundle := &Bundle{Diff: []Hunk{{ID: testHunk, Path: "main.go", Role: RoleCode, DiffURL: "diff-" + strings.Repeat("0", 64) + "R1"}}}
	caps := Caps{MaxBytes: 1 << 20, MaxDiffIndexBytes: diffIndexBytes(bundle) - 1}
	err := Fit(bundle, caps)
	if err == nil || !strings.HasPrefix(err.Error(), EvidenceBudgetExceededReason) || !strings.Contains(err.Error(), "diff index") {
		t.Fatalf("expected a named diff index budget failure, got %v", err)
	}
}

func TestRedactMetadataPathsAndSessionFields(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	bundle := &Bundle{Repository: "org/" + plantedSecret, PullRequest: PullRequest{AuthorLogin: plantedSecret}, Diff: []Hunk{{ID: testHunk, Path: plantedSecret + "/a.go", OldPath: plantedSecret + "/b.go"}}, Sessions: []Session{{Branch: plantedSecret, Vendor: "claude", Models: []string{plantedSecret}, Prompts: []Prompt{{TouchedPaths: []string{plantedSecret}}}}}, Truncated: []string{plantedSecret}}
	if err := Redact(context.Background(), bundle, redactor); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mustJSON(t, bundle)), plantedSecret) {
		t.Fatal("secret survived metadata redaction")
	}
	if bundle.Diff[0].ID != testHunk {
		t.Fatal("redaction corrupted a structural hunk identifier")
	}
}

func TestCapConversationBoundary(t *testing.T) {
	older := mkCreatedAt(1)
	newer := mkCreatedAt(2)
	newest := mkCreatedAt(3)
	items := []ConversationItem{
		{ID: "c1", Kind: "comment", CreatedAt: older},
		{ID: "c2", Kind: "comment", CreatedAt: newer},
		{ID: "c3", Kind: "comment", CreatedAt: newest},
		{ID: "r1", Kind: "review", State: "approved", CreatedAt: older},
	}
	// At the limit: nothing should be cut.
	atLimit := capConversation(items, 3)
	if len(atLimit) != 4 {
		t.Fatalf("at the limit, expected all comments plus the review kept, got %+v", atLimit)
	}
	// One over the limit: exactly the oldest plain comment is cut, the
	// review verdict survives regardless of the limit.
	overLimit := capConversation(items, 2)
	if len(overLimit) != 3 {
		t.Fatalf("over the limit, want 3 items (2 comments + 1 review), got %+v", overLimit)
	}
	for _, item := range overLimit {
		if item.ID == "c1" {
			t.Fatalf("expected the oldest comment c1 dropped at the boundary, got %+v", overLimit)
		}
	}
}

func TestCapPatchLinesBoundary(t *testing.T) {
	patch := "line1\nline2\nline3"
	if got, cut := capPatchLines(patch, 3); cut || got != patch {
		t.Fatalf("at the limit, expected no cut, got (%q, %v)", got, cut)
	}
	if got, cut := capPatchLines(patch, 2); !cut || got != "line1\nline2" {
		t.Fatalf("one over the limit, want (\"line1\\nline2\", true), got (%q, %v)", got, cut)
	}
}

func TestCutLowestConfidenceSessionsBoundary(t *testing.T) {
	sessions := []Session{
		{RootSessionID: "a", Confidence: 0.9},
		{RootSessionID: "b", Confidence: 0.5},
		{RootSessionID: "restricted", Restricted: true},
	}
	// At the limit (2 primary sessions): nothing cut.
	atLimit := cutLowestConfidenceSessions(sessions, 2)
	if len(atLimit) != 3 {
		t.Fatalf("at the limit, expected all 3 sessions kept, got %+v", atLimit)
	}
	// One over the limit: the lowest-confidence primary session is cut, the
	// restricted session is never subject to this cap.
	overLimit := cutLowestConfidenceSessions(sessions, 1)
	if len(overLimit) != 2 {
		t.Fatalf("over the limit, want 2 sessions kept, got %+v", overLimit)
	}
	for _, s := range overLimit {
		if s.RootSessionID == "b" {
			t.Fatalf("expected lowest-confidence session b cut at the boundary, got %+v", overLimit)
		}
	}
}

func TestApplyCapsCutsExactlyAtTheBoundary(t *testing.T) {
	caps := Caps{
		MaxSessions: 1, MaxComments: 1, MaxHunkPatchLines: 1,
		MaxPromptsPerSession: 10, MaxCommandEventsPerSession: 10, MaxBytes: 1 << 20,
	}
	older := mkCreatedAt(1)
	newer := mkCreatedAt(2)
	bundle := &Bundle{
		Sessions: []Session{
			{RootSessionID: "high", Confidence: 0.9},
			{RootSessionID: "low", Confidence: 0.1},
		},
		Conversation: []ConversationItem{
			{ID: "old", Kind: "comment", CreatedAt: older},
			{ID: "new", Kind: "comment", CreatedAt: newer},
		},
		Diff: []Hunk{{ID: "h1", Patch: "l1\nl2\nl3"}},
	}
	ApplyCaps(bundle, caps)
	if len(bundle.Sessions) != 1 || bundle.Sessions[0].RootSessionID != "high" {
		t.Errorf("expected only the high-confidence session kept, got %+v", bundle.Sessions)
	}
	if len(bundle.Conversation) != 1 || bundle.Conversation[0].ID != "new" {
		t.Errorf("expected only the newest comment kept, got %+v", bundle.Conversation)
	}
	if bundle.Diff[0].Patch != "l1" {
		t.Errorf("expected patch capped to 1 leading line, got %q", bundle.Diff[0].Patch)
	}
	if len(bundle.Truncated) == 0 {
		t.Error("expected truncation notes recorded for the cuts made")
	}
}

func TestCapAttachmentsShortensAltAndHeadingAndNotes(t *testing.T) {
	bundle := &Bundle{Attachments: []Attachment{
		{ID: "a", Alt: strings.Repeat("x", 50), Heading: "short"},
		{ID: "b", Alt: "ok"},
	}}
	CapAttachments(bundle, Caps{MaxImageAltBytes: 10})
	if len(bundle.Attachments[0].Alt) != 10 || bundle.Attachments[0].Heading != "short" || bundle.Attachments[1].Alt != "ok" {
		t.Fatalf("attachments = %+v", bundle.Attachments)
	}
	if len(bundle.Truncated) != 1 || !strings.Contains(bundle.Truncated[0], "for 1 description image(s)") {
		t.Fatalf("truncated = %v", bundle.Truncated)
	}
	CapAttachments(bundle, Caps{})
	if len(bundle.Truncated) != 1 {
		t.Fatal("a zero limit must not cap or note")
	}
}
