package bundle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// These test the individual trim-order steps directly (Fit:
// assistant replies, older prompts, command paraphrases,
// comment bodies oldest-first keeping review verdicts, generated/vendored
// patch text, superseded session decisions, ordinary patch text, cited prompt
// text, then whole sessions from lowest confidence), which is more
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
// the remaining stages cannot shrink: a large PR body and a review
// verdict's body (cutOldestComments explicitly keeps review
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
	skeleton.Truncated = append(skeleton.Truncated, "code patch text shortened to fit the size limit; hunk entries are kept", "additional content shortened to fit the size limit after every documented stage")
	limit := len(mustJSON(t, skeleton)) - diffIndexBytes(skeleton)

	bundle := build()
	caps := Caps{MaxBytes: limit}
	if err := Fit(bundle, caps); err != nil {
		t.Fatal(err)
	}

	if !fits(bundle, limit) {
		t.Fatalf("bundle still exceeds the byte limit after Fit: %d bytes, limit %d", len(mustJSON(t, bundle)), limit)
	}
	if bundle.PullRequest.Body != "" {
		t.Errorf("expected the PR body truncated by the backstop, got %q", bundle.PullRequest.Body)
	}
	if bundle.Diff[0].Patch != "" || !bundle.Diff[0].Textless {
		t.Errorf("expected the ordinary code patch truncated, got %+v", bundle.Diff[0])
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

func TestRedactStopsWhenTheContextEnds(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	bundle := &Bundle{Diff: []Hunk{{ID: testHunk, Path: "main.go", Patch: strings.Repeat("+line of code\n", 1<<16)}}}
	if err := Redact(ctx, bundle, redactor); !errors.Is(err, context.Canceled) {
		t.Fatalf("Redact with a cancelled context = %v, want context.Canceled", err)
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

// TestRedactPatchKeepsLinesAndFindsMultilineKeys checks a patch is redacted
// as one text: a key whose lines are all diff lines is found, and every
// line keeps its length and its diff marker, so hunk line numbers stay valid.
func TestRedactPatchKeepsLinesAndFindsMultilineKeys(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	patch := "@@ -1,8 +1,14 @@\n-old " + plantedSecret + "\n" + prefixLines("+", corpusPEM) + "\n" + prefixLines(" ", corpusPEM) + "\n+    @Override\n"
	bundle := &Bundle{Diff: []Hunk{{ID: testHunk, Path: "main.go", Patch: patch}}}
	if err := Redact(context.Background(), bundle, redactor); err != nil {
		t.Fatal(err)
	}
	redacted := bundle.Diff[0].Patch
	if strings.Contains(redacted, plantedSecret) || strings.Contains(redacted, "rpvGMXOAKGX6B") {
		t.Fatalf("secret survived patch redaction: %q", redacted)
	}
	inLines, outLines := strings.Split(patch, "\n"), strings.Split(redacted, "\n")
	if len(inLines) != len(outLines) {
		t.Fatalf("patch redaction changed line count: %q -> %q", patch, redacted)
	}
	for i := range inLines {
		if len(inLines[i]) != len(outLines[i]) {
			t.Errorf("line %d changed length: %q -> %q", i+1, inLines[i], outLines[i])
		} else if inLines[i] != "" && inLines[i][0] != outLines[i][0] {
			t.Errorf("line %d lost its diff marker: %q -> %q", i+1, inLines[i], outLines[i])
		}
	}
	if !strings.Contains(redacted, "+    @Override") {
		t.Errorf("an annotation in code was rewritten: %q", redacted)
	}
	if bundle.MaskedSpanCount < 2 {
		t.Fatalf("masked spans = %d, want the token and the key", bundle.MaskedSpanCount)
	}
}

// syntheticKeyLines returns a PEM private key block, one line per element,
// assembled at run time so no secret-shaped literal sits in the source. No
// line of it is detected on its own.
func syntheticKeyLines() []string {
	label := "RSA " + "PRIVATE KEY"
	lines := []string{"-----BEGIN " + label + "-----"}
	for i := range 6 {
		lines = append(lines, strings.Repeat("Qz7"+"Lw3Kp"+string(rune('a'+i))+"Xb9", 8)[:64])
	}
	return append(lines, "-----END "+label+"-----")
}

func requireNoKeyLine(t *testing.T, where, text string) {
	t.Helper()
	for _, line := range syntheticKeyLines() {
		if strings.Contains(text, line) {
			t.Fatalf("%s keeps key line %q: %s", where, line, text)
		}
	}
}

// RedactLines finds a key spread over lines and returns every line in place.
func TestRedactLinesMasksSecretsSpanningLines(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := syntheticKeyLines()
	for _, line := range key {
		if masked, count, err := redactor.RedactString(ctx, line); err != nil || count != 0 || masked != line {
			t.Fatalf("a lone key line is masked, so the case proves nothing: %q %d %v", masked, count, err)
		}
	}
	lines := append(append([]string{"const key = `"}, key...), "`", "")
	masked, count, err := redactor.RedactLines(ctx, lines)
	if err != nil {
		t.Fatal(err)
	}
	requireNoKeyLine(t, "lines", strings.Join(masked, "\n"))
	if count != 1 || len(masked) != len(lines) {
		t.Fatalf("masked spans = %d, lines = %d, want 1 and %d", count, len(masked), len(lines))
	}
	for i, line := range masked {
		if len(line) != len(lines[i]) {
			t.Fatalf("line %d %q does not keep the place of %q", i, line, lines[i])
		}
	}
	if masked[0] != lines[0] || masked[len(masked)-2] != "`" {
		t.Fatalf("lines around the key changed: %q", masked)
	}

	plain := []string{"func main() {", "", "\tfmt.Println(\"hello\")", "}"}
	if got, count, err := redactor.RedactLines(ctx, plain); err != nil || count != 0 || strings.Join(got, "\n") != strings.Join(plain, "\n") || len(got) != len(plain) {
		t.Fatalf("plain lines changed: %q %d %v", got, count, err)
	}
	if got, count, err := redactor.RedactLines(ctx, nil); err != nil || count != 0 || len(got) != 0 {
		t.Fatalf("no lines: %q %d %v", got, count, err)
	}
}

// A line holding a newline itself is cut back out whole.
func TestRedactLinesKeepsLinesHoldingNewlines(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	key := syntheticKeyLines()
	lines := []string{key[0] + "\n" + key[1], strings.Join(key[2:], "\n"), "done"}
	masked, count, err := redactor.RedactLines(context.Background(), lines)
	if err != nil {
		t.Fatal(err)
	}
	requireNoKeyLine(t, "lines", strings.Join(masked, "\n"))
	if count != 1 || len(masked) != len(lines) || masked[2] != "done" {
		t.Fatalf("masked = %q, %d spans", masked, count)
	}
	for i := range lines {
		if len(masked[i]) != len(lines[i]) || strings.Count(masked[i], "\n") != strings.Count(lines[i], "\n") {
			t.Fatalf("line %d %q does not keep the shape of %q", i, masked[i], lines[i])
		}
	}
}

// Only the elements of a "lines" array are redacted as one text; any other
// text under a "lines" key is redacted as it is.
func TestRedactPreviousTextUnderLinesObjects(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(map[string]any{
		"x": map[string]any{"lines": map[string]any{"text": "token " + plantedSecret}},
		"y": map[string]any{"lines": []any{map[string]any{"text": "a"}, "token " + plantedSecret}},
		"z": map[string]any{"lines": []any{map[string]any{"text": "a", "note": map[string]any{"text": "token " + plantedSecret}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle := &Bundle{Previous: &PreviousVersion{Sections: []PreviousSection{{ID: "sec_x", Title: "X", Content: content}}}}
	if err := Redact(context.Background(), bundle, redactor); err != nil {
		t.Fatal(err)
	}
	if redacted := string(bundle.Previous.Sections[0].Content); strings.Contains(redacted, plantedSecret) {
		t.Fatalf("text under lines kept the secret: %s", redacted)
	}
	if bundle.MaskedSpanCount != 3 {
		t.Fatalf("masked spans = %d, want 3", bundle.MaskedSpanCount)
	}
}

// The previous review's code excerpts are redacted a line array at a time.
func TestRedactMasksSecretsSpanningPreviousTileLines(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	key := syntheticKeyLines()
	var lines []any
	for i, line := range key {
		lines = append(lines, map[string]any{"kind": "add", "new_line": i + 1, "text": line})
	}
	content, err := json.Marshal(map[string]any{"tiles": []any{map[string]any{"id": "tile_key", "kind": "bug-fix", "code": []any{map[string]any{"path": "key.pem", "revision": strings.Repeat("a", 40), "why": "Adds the key", "review_version": 1, "diff": map[string]any{"hunks": []any{map[string]any{"header": fmt.Sprintf("@@ -0,0 +1,%d @@", len(key)), "lines": lines}}}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	bundle := &Bundle{Previous: &PreviousVersion{Version: 1, Sections: []PreviousSection{{ID: "sec_key", Title: "Key", Content: content}}}}
	if err := Redact(context.Background(), bundle, redactor); err != nil {
		t.Fatal(err)
	}
	redacted := string(bundle.Previous.Sections[0].Content)
	requireNoKeyLine(t, "previous section", redacted)
	if bundle.MaskedSpanCount != 1 {
		t.Fatalf("masked spans = %d, want 1", bundle.MaskedSpanCount)
	}
	var section struct {
		Tiles []struct {
			Code []struct {
				Path string `json:"path"`
				Diff struct {
					Hunks []struct {
						Lines []struct {
							Kind string `json:"kind"`
							Text string `json:"text"`
						} `json:"lines"`
					} `json:"hunks"`
				} `json:"diff"`
			} `json:"code"`
		} `json:"tiles"`
	}
	if err := json.Unmarshal(bundle.Previous.Sections[0].Content, &section); err != nil {
		t.Fatal(err)
	}
	code := section.Tiles[0].Code[0]
	got := code.Diff.Hunks[0].Lines
	if code.Path != "key.pem" || len(got) != len(key) {
		t.Fatalf("source changed shape: %s", redacted)
	}
	for i, line := range got {
		if line.Kind != "add" || len(line.Text) != len(key[i]) {
			t.Fatalf("line %d does not keep its place: %+v", i, line)
		}
	}
}

// TestRedactCoversEveryBundleStringField guards against the
// whole-bundle redact pass silently skipping a field: every string field of
// the bundle type is masked for secrets and e-mail addresses — including
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
			Decisions: decisionsOf(SessionDecision{
				Title:   "Rotate " + plantedSecret,
				Summary: "Rotate " + plantedSecret, Who: "human", Category: "behavior", Trigger: "failure", Turns: []int{1},
				Rationale: "Leaked " + plantedSecret, RejectedAlternative: "Keep " + plantedSecret,
				Evidence: "rotate " + plantedSecret + " now", EvidenceTurn: 1, Files: []string{"config/" + plantedSecret + ".toml"},
			}),
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
	decision := (*bundle.Sessions[0].Decisions)[0]
	for name, text := range map[string]string{
		"title":   decision.Title,
		"summary": decision.Summary, "rationale": decision.Rationale, "rejected alternative": decision.RejectedAlternative,
		"evidence": decision.Evidence, "file": decision.Files[0],
	} {
		if strings.Contains(text, plantedSecret) {
			t.Errorf("session decision %s was not redacted: %q", name, text)
		}
	}
	if decision.Who != "human" || decision.Category != "behavior" || decision.Trigger != "failure" {
		t.Errorf("session decision enums changed: %+v", decision)
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

func TestFitBytesKeepsDecisionsWhenCodePatchesExceedTheContentBudget(t *testing.T) {
	build := func() *Bundle {
		bundle := &Bundle{Repository: "org/repo"}
		for i := range 267 {
			bundle.Diff = append(bundle.Diff, Hunk{
				ID: fmt.Sprintf("h_%012x", i), Path: fmt.Sprintf("src/file_%d.go", i),
				Role: RoleCode, Additions: 200,
				Patch: "@@ -0,0 +1,200 @@\n" + strings.Repeat("+"+strings.Repeat("code", 20)+"\n", 200),
			})
		}
		for i := range 3 {
			bundle.Sessions = append(bundle.Sessions, Session{
				RootSessionID: fmt.Sprintf("session-%d", i), Confidence: 0.9,
				Prompts:   []Prompt{{Index: 1, ID: fmt.Sprintf("prompt-%d", i), Text: "Keep the requested behavior"}},
				Decisions: decisionsOf(SessionDecision{Summary: "Keep the requested behavior", Who: "human", Turns: []int{1}, Files: []string{"src/file_0.go"}}),
			})
		}
		return bundle
	}
	bundle := build()
	caps := Caps{MaxBytes: 256 << 10, MaxHunkPatchLines: 200}
	indexBytes := diffIndexBytes(bundle)
	if fits(bundle, caps.MaxBytes) {
		t.Fatal("fixture must exceed the content budget")
	}
	if err := Fit(bundle, caps); err != nil {
		t.Fatal(err)
	}
	if !fits(bundle, caps.MaxBytes) || len(bundle.Sessions) != 3 {
		t.Fatalf("want every session within the content budget, got %d sessions and %d bytes", len(bundle.Sessions), contentBytes(t, bundle))
	}
	for _, session := range bundle.Sessions {
		if session.Decisions == nil || len(*session.Decisions) != 1 || len(session.Prompts) != 1 || session.Prompts[0].Index != 1 || session.Prompts[0].Text != "Keep the requested behavior" {
			t.Fatalf("lost a current decision or its cited prompt: %+v", session)
		}
	}
	if len(bundle.Diff) != 267 || diffIndexBytes(bundle) != indexBytes {
		t.Fatal("trimming changed the diff index")
	}
	if patch := bundle.Diff[0].Patch; patch == "" || len(splitLines(patch)) >= 200 || !strings.HasPrefix(patch, "@@ -0,0 +1,200 @@") {
		t.Fatalf("want a leading patch excerpt, got %q", patch)
	}
	again := build()
	if err := Fit(again, caps); err != nil {
		t.Fatal(err)
	}
	if string(mustJSON(t, again)) != string(mustJSON(t, bundle)) {
		t.Fatal("trimming is not deterministic")
	}
}

func TestFitBytesDropsLowestConfidenceSessionAfterPatchesAreExhausted(t *testing.T) {
	bundle := &Bundle{
		Diff: []Hunk{{ID: testHunk, Path: "main.go", Role: RoleCode, Patch: strings.Repeat("+code\n", 200)}},
		Sessions: []Session{
			{RootSessionID: "high", Confidence: 0.9, Decisions: decisionsOf(SessionDecision{Summary: "Keep this decision"})},
			{RootSessionID: "low", Confidence: 0.1, Decisions: decisionsOf(SessionDecision{Summary: strings.Repeat("large decision ", 2000)})},
		},
	}
	caps := Caps{MaxBytes: 2000, MaxHunkPatchLines: 200}
	if err := Fit(bundle, caps); err != nil {
		t.Fatal(err)
	}
	if !fits(bundle, caps.MaxBytes) || len(bundle.Sessions) != 1 || bundle.Sessions[0].RootSessionID != "high" {
		t.Fatalf("want the high-confidence session within budget, got %+v", bundle.Sessions)
	}
	if hunk := bundle.Diff[0]; hunk.Patch != "" || !hunk.Textless || hunk.TextlessReason != "trimmed_oversized" {
		t.Fatalf("session removed before exhausting patch text: %+v", hunk)
	}
	if got := countNotes(bundle.Truncated, "session(s) omitted"); got != 1 || !slices.Contains(bundle.Truncated, "1 session(s) omitted to fit the size limit") {
		t.Fatalf("want one note counting 1 omitted session, got %v", bundle.Truncated)
	}
}

func countNotes(notes []string, fragment string) int {
	n := 0
	for _, note := range notes {
		if strings.Contains(note, fragment) {
			n++
		}
	}
	return n
}

func TestFitBytesNotesEverySessionCutOnceAndDeterministically(t *testing.T) {
	build := func() *Bundle {
		return &Bundle{
			PullRequest: PullRequest{Title: "t"},
			Sessions: []Session{
				{RootSessionID: "a", Confidence: 0.9, Decisions: decisionsOf(SessionDecision{Summary: strings.Repeat("a ", 1500)})},
				{RootSessionID: "b", Confidence: 0.5, Decisions: decisionsOf(SessionDecision{Summary: strings.Repeat("b ", 1500)})},
				{RootSessionID: "c", Confidence: 0.1, Decisions: decisionsOf(SessionDecision{Summary: strings.Repeat("c ", 1500)})},
			},
		}
	}
	caps := Caps{MaxBytes: 1000}
	first, second := build(), build()
	if err := Fit(first, caps); err != nil {
		t.Fatal(err)
	}
	if err := Fit(second, caps); err != nil {
		t.Fatal(err)
	}
	if len(first.Sessions) != 0 {
		t.Fatalf("want every session cut, got %+v", first.Sessions)
	}
	if countNotes(first.Truncated, "session(s) omitted") != 1 || !slices.Contains(first.Truncated, "3 session(s) omitted to fit the size limit") {
		t.Fatalf("want one note counting 3 sessions, got %v", first.Truncated)
	}
	if string(mustJSON(t, first)) != string(mustJSON(t, second)) {
		t.Error("same input produced different bundles")
	}
}

func TestFitBytesAddsNoSessionNoteWhenNoSessionIsCut(t *testing.T) {
	bundle := &Bundle{
		Diff:     []Hunk{{ID: testHunk, Path: "main.go", Role: RoleCode, Patch: strings.Repeat("+code\n", 200)}},
		Sessions: []Session{{RootSessionID: "keep", Confidence: 0.9, Decisions: decisionsOf(SessionDecision{Summary: "short"})}},
	}
	if err := Fit(bundle, Caps{MaxBytes: 1500, MaxHunkPatchLines: 200}); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Sessions) != 1 || countNotes(bundle.Truncated, "session(s) omitted") != 0 {
		t.Fatalf("want the session kept and no session note, got %+v %v", bundle.Sessions, bundle.Truncated)
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

func decisionsOf(items ...SessionDecision) *[]SessionDecision { return &items }

// contentBytes is the size Fit measures against MaxBytes.
func contentBytes(t *testing.T, bundle *Bundle) int {
	return len(mustJSON(t, bundle)) - diffIndexBytes(bundle)
}

func TestCutSupersededDecisionsKeepsCurrentOnesAndTheExtractedMarker(t *testing.T) {
	bundle := &Bundle{Sessions: []Session{
		{RootSessionID: "s1", Decisions: decisionsOf(
			SessionDecision{Summary: "Chose a map.", Who: "agent", Category: "implementation", Superseded: true, Turns: []int{1}},
			SessionDecision{Summary: "Chose a slice.", Who: "human", Category: "implementation", Turns: []int{2}},
		)},
		{RootSessionID: "s2", Decisions: decisionsOf(
			SessionDecision{Summary: "Chose a cache.", Who: "agent", Category: "architecture", Superseded: true, Turns: []int{1}},
		)},
		{RootSessionID: "s3"},
	}}
	if !cutSupersededDecisions(bundle) {
		t.Fatal("expected a cut")
	}
	if got := *bundle.Sessions[0].Decisions; len(got) != 1 || got[0].Summary != "Chose a slice." {
		t.Fatalf("current decision not kept alone: %+v", got)
	}
	if got := bundle.Sessions[1].Decisions; got == nil || len(*got) != 0 {
		t.Fatalf("an emptied session must still read as extracted: %+v", got)
	}
	if encoded := string(mustJSON(t, bundle)); !strings.Contains(encoded, `"id":"s2","vendor":"","restricted":false,"decisions":[]`) {
		t.Fatalf("emptied decisions do not encode as an empty list: %s", encoded)
	}
	if bundle.Sessions[2].Decisions != nil {
		t.Fatal("a session without extracted decisions gained them")
	}
	if cutSupersededDecisions(bundle) {
		t.Error("expected no further cut once superseded decisions are gone")
	}
}

// Current decisions outlast every cut of prompt text: the session's older
// prompts and superseded decisions go first, and current decisions only with
// the whole session.
func TestFitBytesCutsDecisionsAfterPromptTextAndOnlySupersededOnesBeforeSessions(t *testing.T) {
	build := func() *Bundle {
		var prompts []Prompt
		for i := 1; i <= 6; i++ {
			prompts = append(prompts, Prompt{ID: fmt.Sprint("p", i), Index: i, Text: strings.Repeat("prompt text ", 20)})
		}
		return &Bundle{Sessions: []Session{
			{RootSessionID: "high", Confidence: 0.9, Prompts: prompts, Decisions: decisionsOf(
				SessionDecision{Summary: strings.Repeat("old choice ", 20), Who: "agent", Category: "implementation", Superseded: true, Turns: []int{2}},
				SessionDecision{Summary: "Kept choice.", Who: "agent_proposed_human_approved", Category: "implementation", Turns: []int{3}, Files: []string{"main.go"}},
			)},
			{RootSessionID: "low", Confidence: 0.1, Decisions: decisionsOf(
				SessionDecision{Summary: strings.Repeat("low choice ", 20), Who: "human", Category: "scope", Turns: []int{1}},
			)},
		}}
	}
	afterPrompts := build()
	cutOlderPrompts(afterPrompts)
	afterPrompts.Truncated = []string{citedPromptsNote}
	afterSuperseded := build()
	cutOlderPrompts(afterSuperseded)
	cutSupersededDecisions(afterSuperseded)
	afterSuperseded.Truncated = append(slices.Clone(afterPrompts.Truncated), "superseded session decisions omitted to fit the size limit; current decisions are kept")
	afterShortened := build()
	cutOlderPrompts(afterShortened)
	cutSupersededDecisions(afterShortened)
	for shortenCitedPrompts(afterShortened) {
	}
	afterShortened.Truncated = append(slices.Clone(afterSuperseded.Truncated), shortenedNote)

	t.Run("prompt text goes first", func(t *testing.T) {
		bundle := build()
		if err := Fit(bundle, Caps{MaxBytes: contentBytes(t, afterPrompts)}); err != nil {
			t.Fatal(err)
		}
		if len(*bundle.Sessions[0].Decisions) != 2 || len(bundle.Sessions[0].Prompts) != 3 {
			t.Fatalf("decisions were cut before prompt text: %+v", bundle.Sessions[0])
		}
	})
	t.Run("then superseded decisions", func(t *testing.T) {
		bundle := build()
		if err := Fit(bundle, Caps{MaxBytes: contentBytes(t, afterSuperseded)}); err != nil {
			t.Fatal(err)
		}
		if got := *bundle.Sessions[0].Decisions; len(got) != 1 || got[0].Summary != "Kept choice." || len(bundle.Sessions) != 2 {
			t.Fatalf("want only the superseded decision cut, got %+v", bundle.Sessions)
		}
		if !slices.Contains(bundle.Truncated, "superseded session decisions omitted to fit the size limit; current decisions are kept") {
			t.Fatalf("cut not noted: %q", bundle.Truncated)
		}
	})
	t.Run("then current decisions only with their session", func(t *testing.T) {
		bundle := build()
		if err := Fit(bundle, Caps{MaxBytes: contentBytes(t, afterShortened) - 1}); err != nil {
			t.Fatal(err)
		}
		if len(bundle.Sessions) != 1 || bundle.Sessions[0].RootSessionID != "high" || len(*bundle.Sessions[0].Decisions) != 1 || len(bundle.Sessions[0].Prompts) != 3 {
			t.Fatalf("want the low-confidence session dropped whole and the other's current decision kept, got %+v", bundle.Sessions)
		}
	})
}

func TestBundleWithoutDecisionsEncodesWithoutTheField(t *testing.T) {
	encoded := string(mustJSON(t, &Bundle{Sessions: []Session{{RootSessionID: "s1", Prompts: []Prompt{{ID: "p1", Index: 1, Text: "go"}}}}}))
	if strings.Contains(encoded, "decisions") {
		t.Fatalf("a session without extracted decisions changed the encoding: %s", encoded)
	}
}

const citedPromptsNote = "older prompts omitted to fit the size limit; the first and latest prompts of each session and the prompts their current decisions cite are kept"

// The older-prompts stage spares the turns a session's current decisions
// cite; a superseded decision's turns get no such protection.
func TestCutOlderPromptsKeepsThePromptsCurrentDecisionsCite(t *testing.T) {
	var prompts []Prompt
	for i := 1; i <= 6; i++ {
		prompts = append(prompts, Prompt{ID: fmt.Sprint("p", i), Index: i})
	}
	bundle := &Bundle{Sessions: []Session{
		{RootSessionID: "s1", Prompts: prompts, Decisions: decisionsOf(
			SessionDecision{Summary: "Chose a slice.", Who: "human", Category: "implementation", Turns: []int{3, 6}},
			SessionDecision{Summary: "Chose a map.", Who: "agent", Category: "implementation", Superseded: true, Turns: []int{4}},
		)},
		{RootSessionID: "s2", Prompts: slices.Clone(prompts)},
	}}
	if !cutOlderPrompts(bundle) {
		t.Fatal("expected a cut")
	}
	indexes := func(session Session) []int {
		var got []int
		for _, p := range session.Prompts {
			got = append(got, p.Index)
		}
		return got
	}
	if got := indexes(bundle.Sessions[0]); !slices.Equal(got, []int{1, 3, 6}) {
		t.Fatalf("decided session kept %v, want [1 3 6]", got)
	}
	if got := indexes(bundle.Sessions[1]); !slices.Equal(got, []int{1, 6}) {
		t.Fatalf("session without decisions kept %v, want [1 6]", got)
	}
	if cutOlderPrompts(bundle) {
		t.Error("expected no further cut once only kept prompts remain")
	}
}

// Within any budget, a prompt a current decision cites survives as long as
// the decision does: it goes only with its whole session.
func TestFitBytesKeepsCitedPromptsWhileTheirDecisionsSurvive(t *testing.T) {
	build := func() *Bundle {
		session := func(id string, confidence float64) Session {
			var prompts []Prompt
			for i := 1; i <= 8; i++ {
				prompts = append(prompts, Prompt{ID: fmt.Sprint(id, i), Index: i, Text: strings.Repeat("prompt text ", 30), Reply: strings.Repeat("reply ", 30)})
			}
			return Session{RootSessionID: id, Confidence: confidence, Prompts: prompts, Decisions: decisionsOf(
				SessionDecision{Summary: "Chose a slice.", Who: "human", Category: "implementation", Turns: []int{3, 5}},
			)}
		}
		return &Bundle{Sessions: []Session{session("high", 0.9), session("low", 0.1)}}
	}
	full, droppedSession := contentBytes(t, build()), false
	for limit := full; ; limit -= 97 {
		bundle := build()
		if err := Fit(bundle, Caps{MaxBytes: limit}); err != nil {
			// Below what a bundle without sessions and with its notes needs.
			if !droppedSession {
				t.Fatalf("limit %d: %v before any session was dropped", limit, err)
			}
			break
		}
		droppedSession = droppedSession || len(bundle.Sessions) < 2
		if got := contentBytes(t, bundle); got > limit {
			t.Fatalf("limit %d: content %d bytes over the budget", limit, got)
		}
		for _, session := range bundle.Sessions {
			kept := map[int]bool{}
			for _, p := range session.Prompts {
				kept[p.Index] = true
			}
			for turn := range currentDecisionTurns(session) {
				if !kept[turn] {
					t.Fatalf("limit %d: session %s keeps its decision but lost the prompt at turn %d", limit, session.RootSessionID, turn)
				}
			}
		}
		if limit < full && len(bundle.Sessions) == 2 && len(bundle.Sessions[0].Prompts) < 8 && !slices.Contains(bundle.Truncated, citedPromptsNote) {
			t.Fatalf("limit %d: older prompts cut without the cited-prompts note: %q", limit, bundle.Truncated)
		}
	}
}

// A bundle without decisions trims and encodes exactly as before decisions
// cited prompts: the digest below was recorded before the change.
func TestFitWithoutDecisionsKeepsItsDigest(t *testing.T) {
	var prompts []Prompt
	for i := 1; i <= 6; i++ {
		prompts = append(prompts, Prompt{ID: fmt.Sprint("p", i), Index: i, At: mkCreatedAt(i), Text: strings.Repeat("prompt text ", 20), Reply: "reply"})
	}
	bundle := &Bundle{
		Repository: "org/repo",
		Sessions:   []Session{{RootSessionID: "s1", Vendor: "claude_code", Confidence: 0.9, Prompts: prompts, CommandEvents: []CommandEvent{{ID: "e1", Outcome: "succeeded"}}}},
		Conversation: []ConversationItem{
			{ID: "1", Kind: "comment", Body: strings.Repeat("comment ", 30), CreatedAt: mkCreatedAt(1)},
			{ID: "2", Kind: "review", State: "approved", Body: "ok", CreatedAt: mkCreatedAt(2)},
		},
	}
	limit := contentBytes(t, bundle) - 400
	if err := Fit(bundle, Caps{MaxBytes: limit}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(bundle.Truncated, "older prompts omitted to fit the size limit; the first and latest prompts of each session are kept") {
		t.Fatalf("fixture no longer reaches the older-prompts stage: %q", bundle.Truncated)
	}
	if err := SetDigest(bundle); err != nil {
		t.Fatal(err)
	}
	if want := "584b8c31a78525735be159d78a3cd67b42269e4a48274b600ba2f08e090d5d8b"; bundle.Digest != want {
		t.Fatalf("digest = %s, want %s", bundle.Digest, want)
	}
}

const shortenedNote = "text of prompts that current decisions cite shortened to fit the size limit, longest first; the prompts and their decisions are kept"

// An oversized cited prompt is shortened, longest first, before any session
// goes: it costs neither its own session nor a lower-confidence one, and its
// entry and index stay so the decision's anchor still resolves.
func TestFitBytesShortensAnOversizedCitedPromptBeforeDroppingSessions(t *testing.T) {
	build := func() *Bundle {
		session := func(id string, confidence float64, text string) Session {
			return Session{RootSessionID: id, Confidence: confidence, Prompts: []Prompt{
				{ID: id + "1", Index: 1, Text: "first"},
				{ID: id + "4", Index: 4, Text: text},
				{ID: id + "9", Index: 9, Text: "latest"},
			}, Decisions: decisionsOf(SessionDecision{Summary: "Chose a parser.", Who: "human", Category: "implementation", Turns: []int{4}})}
		}
		return &Bundle{Sessions: []Session{
			session("high", 0.9, strings.Repeat("é pasted log ", 20000)),
			session("low", 0.1, strings.Repeat("medium ", 300)),
		}}
	}
	for _, tc := range []struct {
		name  string
		limit func(*Bundle) int
		high  func(string) bool
		low   int
	}{
		{"to its head", func(b *Bundle) int { return contentBytes(t, b) - 200000 }, func(text string) bool {
			return len(text) <= CitedPromptHeadBytes && len(text) > CitedPromptHeadBytes-4 && utf8.ValidString(text)
		}, len(strings.Repeat("medium ", 300))},
		{"then both heads", func(b *Bundle) int {
			shortened := build()
			shortenCitedPrompts(shortened)
			shortenCitedPrompts(shortened)
			shortened.Truncated = []string{shortenedNote}
			return contentBytes(t, shortened)
		}, func(text string) bool { return len(text) <= CitedPromptHeadBytes }, CitedPromptHeadBytes},
		{"then to empty text", func(b *Bundle) int {
			empty := build()
			for _, s := range empty.Sessions {
				s.Prompts[1].Text = ""
			}
			empty.Truncated = []string{shortenedNote}
			return contentBytes(t, empty)
		}, func(text string) bool { return text == "" }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := build()
			limit := tc.limit(bundle)
			if err := Fit(bundle, Caps{MaxBytes: limit}); err != nil {
				t.Fatal(err)
			}
			if got := contentBytes(t, bundle); got > limit {
				t.Fatalf("content %d bytes over the %d budget", got, limit)
			}
			if len(bundle.Sessions) != 2 || len(*bundle.Sessions[0].Decisions) != 1 || len(bundle.Sessions[0].Prompts) != 3 || len(bundle.Sessions[1].Prompts) != 3 {
				t.Fatalf("a session, decision or prompt entry was dropped: %+v", bundle.Sessions)
			}
			if bundle.Sessions[0].Prompts[1].Index != 4 || !tc.high(bundle.Sessions[0].Prompts[1].Text) {
				t.Fatalf("high session's cited prompt is %d bytes", len(bundle.Sessions[0].Prompts[1].Text))
			}
			if got := len(bundle.Sessions[1].Prompts[1].Text); got != tc.low && (tc.low != CitedPromptHeadBytes || got > tc.low) {
				t.Fatalf("low session's cited prompt is %d bytes, want %d", got, tc.low)
			}
			if bundle.Sessions[0].Prompts[0].Text != "first" || bundle.Sessions[0].Prompts[2].Text != "latest" {
				t.Fatalf("uncited prompts were shortened: %+v", bundle.Sessions[0].Prompts)
			}
			if !slices.Contains(bundle.Truncated, shortenedNote) {
				t.Fatalf("shortening not noted: %q", bundle.Truncated)
			}
		})
	}
}

// A previous section's free-text keys are redacted, not exempted.
func TestRedactPreviousScansFreeTextKeys(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(map[string]any{
		"decisions": []any{map[string]any{"id": "d1", "evidence": "quote " + plantedSecret}},
		"anchor":    map[string]any{"type": "prompt", "input": "mail person@example.com"},
		"column":    map[string]any{"type": "text " + plantedSecret},
		"step":      map[string]any{"evidence": []any{"ev_one", "ev_two"}, "category": "security"},
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle := &Bundle{Previous: &PreviousVersion{Sections: []PreviousSection{{ID: "sec_x", Title: "X", Content: content}}}}
	if err := Redact(context.Background(), bundle, redactor); err != nil {
		t.Fatal(err)
	}
	redacted := string(bundle.Previous.Sections[0].Content)
	if strings.Contains(redacted, plantedSecret) || strings.Contains(redacted, "person@example.com") {
		t.Fatalf("free-text keys kept a leak: %s", redacted)
	}
	if bundle.MaskedSpanCount != 3 {
		t.Fatalf("masked spans = %d, want 3", bundle.MaskedSpanCount)
	}
	if !strings.Contains(redacted, `"type":"prompt"`) || !strings.Contains(redacted, `"id":"d1"`) ||
		!strings.Contains(redacted, `"evidence":["ev_one","ev_two"]`) || !strings.Contains(redacted, `"category":"security"`) {
		t.Fatalf("discriminator, id, evidence refs or category changed: %s", redacted)
	}
}

// A review thread's path and URL are redacted like the conversation's.
func TestRedactMasksThreadPathAndURL(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	bundle := &Bundle{Threads: []Thread{{ID: "t1", Path: "docs/people/ada@example.com.md", URL: "https://x.test/?token=" + plantedSecret}}}
	if err := Redact(context.Background(), bundle, redactor); err != nil {
		t.Fatal(err)
	}
	thread := bundle.Threads[0]
	if strings.Contains(thread.Path, "ada@example.com") || strings.Contains(thread.URL, plantedSecret) || thread.ID != "t1" {
		t.Fatalf("thread = %+v", thread)
	}
	if bundle.MaskedSpanCount != 2 {
		t.Fatalf("masked spans = %d, want 2", bundle.MaskedSpanCount)
	}
}
