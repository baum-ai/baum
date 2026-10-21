package bundle

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/baum-ai/baum/redact"
)

// Redact masks secrets and e-mail addresses in every bundle string field,
// after reference extraction (which runs against the unredacted bundle) and
// before the byte-fit pass and the hunk line cap it starts with. Each field
// is redacted as one text, so a secret spanning lines is found, and keeps its
// length and lines. The hunks of one file are redacted together, as the
// file's patch, so a key that runs on from one hunk into the next is masked
// in both; each hunk keeps its diff markers and its header's line ranges
// (redactPatches). A patch still holds the margin diff parsing keeps past
// the line cap, and the lines an earlier hunk of the file drops are read
// too, so a secret the cap runs through is masked as in the whole patch.
// MaskedSpanCount is the number of spans masked, e-mail addresses included:
// a secret spanning hunks counts once, and a secret found only in text the
// cap drops counts too.
//
// Mentions are not rewritten: nothing in the bundle is posted to a code
// host as it is, and a field may hold code (a patch, a suggestion block, a
// prompt), where an "@" is an annotation, decorator or package scope. Text
// published to a code host has its mentions defanged where it is published.
//
// Every free-text or URL field is covered, including References[].Value —
// ReferenceAuthorshipDisclosure copies up to 80 trailing characters of raw
// text and an image/video reference copies the matched URL, so a secret
// masked in Body/Title could otherwise still surface here — and branch
// names, check names/URLs, requested-reviewer logins, comment paths and a
// session decision's title, summary, rationale, rejected alternative, evidence and
// files, none of which are structurally constrained enough to skip. SHA, ID
// and closed-enum fields (Kind, State, Role, Change, Outcome, a decision's
// Who, Category and Trigger, ...) are not redacted: they are not free text, and running
// secret detection on a hex SHA risks a false-positive match corrupting
// the hash a hunk/commit anchor resolves against. DiffURL is a generated
// hash fragment, reference Source is a fixed tag with a numeric comment ID,
// and AuthorAssociation is a GitHub enum; these also contain no
// user-authored text. ReviewConfig.Content (REVIEW.md) is likewise not
// redacted: unlike a PR body or comment, it has no audience but the review
// agent itself — it never displays to a human viewer — and the model can
// already read the same unredacted file directly off the checkout via its
// Read tool, so masking the bundled copy would only create an inconsistency
// between the two, not add any protection.
func Redact(ctx context.Context, bundle *Bundle, redactor *Redactor) error {
	total := 0
	redactField := func(s *string) error {
		redacted, count, err := redactor.RedactString(ctx, *s)
		if err != nil {
			return err
		}
		*s, total = redacted, total+count
		return nil
	}

	fields := []*string{&bundle.Repository, &bundle.PullRequest.AuthorLogin, &bundle.PullRequest.Title, &bundle.PullRequest.Body, &bundle.PullRequest.HeadRef, &bundle.PullRequest.BaseRef}
	for i := range bundle.PullRequest.RequestedReviewers {
		fields = append(fields, &bundle.PullRequest.RequestedReviewers[i])
	}
	for i := range bundle.PullRequest.Commits {
		fields = append(fields, &bundle.PullRequest.Commits[i].Subject)
	}
	for i := range bundle.Checks {
		fields = append(fields, &bundle.Checks[i].Name, &bundle.Checks[i].URL)
	}
	for i := range bundle.Conversation {
		fields = append(fields, &bundle.Conversation[i].AuthorLogin, &bundle.Conversation[i].Body, &bundle.Conversation[i].Path)
	}
	for i := range bundle.Threads {
		fields = append(fields, &bundle.Threads[i].Path, &bundle.Threads[i].URL)
	}
	for i := range bundle.References {
		fields = append(fields, &bundle.References[i].Value)
	}
	for i := range bundle.Attachments {
		fields = append(fields, &bundle.Attachments[i].SourceURL, &bundle.Attachments[i].Alt, &bundle.Attachments[i].Heading)
	}

	for i := range bundle.Truncated {
		fields = append(fields, &bundle.Truncated[i])
	}
	for i := range bundle.Diff {
		fields = append(fields, &bundle.Diff[i].Path, &bundle.Diff[i].OldPath)
	}
	masked, err := redactPatches(ctx, redactor, bundle.Diff)
	if err != nil {
		return err
	}
	total += masked
	for i := range bundle.Sessions {
		session := &bundle.Sessions[i]
		fields = append(fields, &session.Title, &session.Branch, &session.Vendor)
		for m := range session.Models {
			fields = append(fields, &session.Models[m])
		}
		for p := range session.Prompts {
			fields = append(fields, &session.Prompts[p].Text, &session.Prompts[p].Reply)
			for i := range session.Prompts[p].TouchedPaths {
				fields = append(fields, &session.Prompts[p].TouchedPaths[i])
			}
		}
		for e := range session.CommandEvents {
			for c := range session.CommandEvents[e].Commands {
				entry := &session.CommandEvents[e].Commands[c]
				fields = append(fields, &entry.Program, &entry.Target)
			}
		}
		if session.Decisions != nil {
			for d := range *session.Decisions {
				decision := &(*session.Decisions)[d]
				fields = append(fields, &decision.Title, &decision.Summary, &decision.Rationale, &decision.RejectedAlternative, &decision.Evidence)
				for f := range decision.Files {
					fields = append(fields, &decision.Files[f])
				}
			}
		}
	}

	if bundle.Previous != nil {
		for i := range bundle.Previous.Sections {
			section := &bundle.Previous.Sections[i]
			fields = append(fields, &section.Title)
			if len(section.Content) == 0 {
				continue
			}
			var content any
			if err := json.Unmarshal(section.Content, &content); err != nil {
				return err
			}
			// line is set for an element of a "lines" array, whose text is
			// redacted with its siblings' text.
			var redactContent func(value any, key string, line bool) (any, error)
			redactContent = func(value any, key string, line bool) (any, error) {
				switch value := value.(type) {
				case map[string]any:
					for childKey, child := range value {
						if _, text := child.(string); text && line && childKey == "text" {
							continue
						}
						// A behaviour step's evidence is an array of ev_ ids the
						// next document must carry verbatim; a decision's
						// evidence is one quoted string, which is scanned.
						if refs, ok := child.([]any); ok && childKey == "evidence" && allStrings(refs) {
							continue
						}
						redacted, err := redactContent(child, childKey, false)
						if err != nil {
							return nil, err
						}
						value[childKey] = redacted
					}
				case []any:
					if key == "lines" {
						// A code excerpt's lines are one text, so a secret
						// written across them is found as a whole.
						if err := redactLineTexts(ctx, redactor, value, &total); err != nil {
							return nil, err
						}
					}
					for i, child := range value {
						redacted, err := redactContent(child, key, key == "lines")
						if err != nil {
							return nil, err
						}
						value[i] = redacted
					}
				case string:
					// Preserve identifiers and closed vocabularies; free-text keys
					// (a decision's evidence, input, type) are scanned.
					if !strings.Contains("|id|hunks|refs|kind|tier|category|source|status|state|who|sha|session|event|", "|"+key+"|") {
						if err := redactField(&value); err != nil {
							return nil, err
						}
					}
					return value, nil
				}
				return value, nil
			}
			redacted, err := redactContent(content, "", false)
			if err != nil {
				return err
			}
			section.Content, err = json.Marshal(redacted)
			if err != nil {
				return err
			}
		}
	}
	for _, field := range fields {
		if err := redactField(field); err != nil {
			return err
		}
	}

	bundle.MaskedSpanCount = total
	return nil
}

// redactLineTexts redacts the string text of each line object as one text.
func redactLineTexts(ctx context.Context, redactor *Redactor, lines []any, total *int) error {
	var texts []string
	var owners []map[string]any
	for _, raw := range lines {
		line, _ := raw.(map[string]any)
		if text, ok := line["text"].(string); ok {
			texts, owners = append(texts, text), append(owners, line)
		}
	}
	masked, count, err := redactor.RedactLines(ctx, texts)
	if err != nil {
		return err
	}
	for i, line := range owners {
		line["text"] = masked[i]
	}
	*total += count
	return nil
}

// redactPatches masks the patch text of diff in place and returns the
// number of spans masked. The hunks diff parsing took from one file are
// redacted as one text, the file's patch, so a key that runs on from one
// hunk into the next, past the hunk header between them, is found and
// masked in both. That text is the file's whole patch when a hunk other
// than its last was cut (filePatch.text); otherwise it is the hunks' kept
// texts joined by newlines, which is the whole patch up to the last hunk's
// margin. A hunk with no file, or whose patch no longer lies where parsing
// put it in its file's text, is redacted on its own.
func redactPatches(ctx context.Context, redactor *Redactor, diff []Hunk) (int, error) {
	type group struct {
		text  string
		hunks []int
	}
	var groups []*group
	byFile := map[*filePatch]*group{}
	for i := range diff {
		hunk := &diff[i]
		file := hunk.file
		hunk.file = nil
		if hunk.Patch == "" {
			continue
		}
		if file != nil && file.text != "" && !strings.HasPrefix(file.text[min(hunk.offset, len(file.text)):], hunk.Patch) {
			file = nil
		}
		g := byFile[file]
		if g == nil {
			g = &group{}
			if file != nil {
				g.text, byFile[file] = file.text, g
			}
			groups = append(groups, g)
		}
		g.hunks = append(g.hunks, i)
	}
	total := 0
	for _, g := range groups {
		text, offsets := g.text, make([]int, len(g.hunks))
		if text == "" {
			patches := make([]string, len(g.hunks))
			at := 0
			for k, i := range g.hunks {
				patches[k], offsets[k] = diff[i].Patch, at
				at += len(diff[i].Patch) + 1
			}
			text = strings.Join(patches, "\n")
		} else {
			for k, i := range g.hunks {
				offsets[k] = diff[i].offset
			}
		}
		redacted, count, err := redactor.RedactString(ctx, text)
		if err != nil {
			return 0, err
		}
		if len(redacted) != len(text) {
			// The mask keeps the text's length, so this is not expected; no
			// hunk could be cut back out, so each is redacted on its own.
			for _, i := range g.hunks {
				redacted, count, err := redactor.RedactString(ctx, diff[i].Patch)
				if err != nil {
					return 0, err
				}
				if len(redacted) == len(diff[i].Patch) {
					redacted = keepDiffSyntax(diff[i].Patch, redacted)
				}
				diff[i].Patch, total = redacted, total+count
			}
			continue
		}
		total += count
		if redacted != text {
			redacted = keepDiffSyntax(text, redacted)
		}
		for k, i := range g.hunks {
			diff[i].Patch = redacted[offsets[k] : offsets[k]+len(diff[i].Patch)]
			if g.text != "" {
				// A copy, so the lines the cap drops are not held.
				diff[i].Patch = strings.Clone(diff[i].Patch)
			}
		}
	}
	return total, nil
}

// keepDiffSyntax puts back the diff syntax a mask spanning lines overwrites:
// the first byte of every patch line, its diff marker, and the line ranges
// of every hunk header. A key spanning lines is masked whole, and the mask
// would turn a context line's leading space into "*", or a hunk header
// inside the key, "@@ -36,7 +36,7 @@", into "@@*-99*9*+99*9*@@*", so neither
// would parse as diff syntax. A marker (space, "+", "-" or "\\") and a
// header's "@@ -a,b +c,d @@" and the space after it are diff syntax, never
// part of the secret. Any other first byte stays masked, as does the
// section heading git writes after a header: it copies a line of the file,
// which may be a line of the key.
func keepDiffSyntax(patch, redacted string) string {
	out := []byte(redacted)
	for start := 0; start < len(patch); {
		end := strings.IndexByte(patch[start:], '\n')
		if end < 0 {
			end = len(patch)
		} else {
			end += start
		}
		line := patch[start:end]
		if n := hunkHeaderLen(line); n > 0 {
			copy(out[start:], line[:n])
		} else if line != "" && strings.IndexByte(" +-\\", line[0]) >= 0 {
			out[start] = line[0]
		}
		start = end + 1
	}
	return string(out)
}

// hunkHeaderLen returns the length of the "@@ -a,b +c,d @@" a hunk header
// line starts with, with the space after it, or 0 for any other line. A
// count may be left out, as in "@@ -1 +1 @@".
func hunkHeaderLen(line string) int {
	i, ok := len("@@ -"), strings.HasPrefix(line, "@@ -")
	lineRange := func() bool {
		digits := func() bool {
			from := i
			for i < len(line) && '0' <= line[i] && line[i] <= '9' {
				i++
			}
			return i > from
		}
		if !digits() {
			return false
		}
		if i < len(line) && line[i] == ',' {
			i++
			return digits()
		}
		return true
	}
	if !ok || !lineRange() || !strings.HasPrefix(line[i:], " +") {
		return 0
	}
	i += len(" +")
	if !lineRange() || !strings.HasPrefix(line[i:], " @@") {
		return 0
	}
	i += len(" @@")
	if i < len(line) && line[i] == ' ' {
		i++
	}
	return i
}

// Redactor masks bundle text with the redact package's engine, the one
// redaction configuration baum uses for input, output and logs. It is safe
// for concurrent use.
type Redactor struct {
	engine *redact.Engine
}

// NewRedactor compiles the redaction rules.
func NewRedactor() (*Redactor, error) {
	engine, err := redact.New()
	if err != nil {
		return nil, err
	}
	return &Redactor{engine: engine}, nil
}

// RedactString masks every secret and e-mail address in s and returns the
// result with the number of spans masked, which the bundle reports as its
// masked-span count. Secrets are found over the whole text, so a key that
// spans lines is caught, and the result keeps the length and the lines of
// s: a patch keeps its line numbers. Mentions are left as written; a code
// host is protected from them where text is published, not here.
func (r *Redactor) RedactString(ctx context.Context, s string) (string, int, error) {
	if s == "" {
		return "", 0, nil
	}
	redacted, summary, err := r.engine.RedactContext(ctx, s)
	if err != nil {
		return "", 0, err
	}
	return redacted, summary.Findings, nil
}

// Find returns the spans RedactString masks in s, one per finding, so two
// secrets written side by side are told apart.
func (r *Redactor) Find(ctx context.Context, s string) ([]redact.Span, error) {
	return r.engine.FindContext(ctx, s)
}

// RedactLines redacts lines that read as one text, such as a code excerpt's
// lines, as their newline-joined text, so a secret written across several of
// them is found as a whole. It returns as many lines as it was given, each
// masked in place, and the spans masked in the joined text. A line may hold
// a newline itself: each is cut back out at its own offsets. Should the mask
// ever change the text's length, every line is masked whole instead.
func (r *Redactor) RedactLines(ctx context.Context, lines []string) ([]string, int, error) {
	joined := strings.Join(lines, "\n")
	if joined == "" {
		return append([]string(nil), lines...), 0, nil
	}
	redacted, count, err := r.RedactString(ctx, joined)
	if err != nil {
		return nil, 0, err
	}
	masked := make([]string, len(lines))
	if len(redacted) != len(joined) {
		for i, line := range lines {
			masked[i] = strings.Repeat("*", len(line))
		}
		return masked, count, nil
	}
	offset := 0
	for i, line := range lines {
		masked[i] = redacted[offset : offset+len(line)]
		offset += len(line) + 1
	}
	return masked, count, nil
}

func allStrings(values []any) bool {
	for _, value := range values {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	return true
}
