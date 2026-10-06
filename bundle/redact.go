package bundle

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/lastpersonlabs/goredact"

	"github.com/lastpersonlabs/baum/redact"
)

// Redact runs goredact and identity redaction over every bundle string
// field, after reference extraction (which runs against the unredacted
// bundle) and before the byte-fit pass. Patch text is redacted line by line.
//
// Every free-text or URL field is covered, including References[].Value —
// ReferenceAuthorshipDisclosure copies up to 80 trailing characters of raw
// text and an image/video reference copies the matched URL, so a secret
// masked in Body/Title could otherwise still surface here — and branch
// names, check names/URLs, requested-reviewer logins and comment paths,
// none of which are structurally constrained enough to skip. SHA, ID and
// closed-enum fields (Kind, State, Role, Change, Outcome, ...) are not
// redacted: they are not free text, and running secret detection on a hex
// SHA risks a false-positive match corrupting the hash a hunk/commit anchor
// resolves against. DiffURL is a generated hash fragment, reference Source
// is a fixed tag with a numeric comment ID, and AuthorAssociation is a
// GitHub enum; these also contain no user-authored text. ReviewConfig.Content
// (REVIEW.md) is likewise not redacted: unlike a PR body or comment, it has
// no audience but the review agent itself — it never displays to a human
// viewer — and the model can already read the same unredacted file directly
// off the checkout via its Read tool, so masking the bundled copy would only
// create an inconsistency between the two, not add any protection.
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
		if bundle.Diff[i].Patch == "" {
			continue
		}
		redacted, count, err := redactor.RedactPatchText(ctx, bundle.Diff[i].Patch)
		if err != nil {
			return err
		}
		bundle.Diff[i].Patch, total = redacted, total+count
	}
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
			var redactContent func(any, string) (any, error)
			redactContent = func(value any, key string) (any, error) {
				switch value := value.(type) {
				case map[string]any:
					for key, child := range value {
						redacted, err := redactContent(child, key)
						if err != nil {
							return nil, err
						}
						value[key] = redacted
					}
				case []any:
					for i, child := range value {
						redacted, err := redactContent(child, key)
						if err != nil {
							return nil, err
						}
						value[i] = redacted
					}
				case string:
					// Preserve identifiers and closed vocabularies.
					if !strings.Contains("|id|hunks|refs|evidence|type|kind|tier|category|source|status|state|who|sha|session|event|input|", "|"+key+"|") {
						if err := redactField(&value); err != nil {
							return nil, err
						}
					}
					return value, nil
				}
				return value, nil
			}
			redacted, err := redactContent(content, "")
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

var bundleRedactionConfig = goredact.Config{
	Profile:       goredact.ProfileBalanced,
	MaskStrategy:  goredact.MaskFormatPreserving,
	RecordAligned: true,
}

// Redactor wraps one goredact engine, safe for concurrent Redact calls.
type Redactor struct {
	engine *goredact.Engine
}

func NewRedactor() (*Redactor, error) {
	engine, err := goredact.New(bundleRedactionConfig)
	if err != nil {
		return nil, err
	}
	return &Redactor{engine: engine}, nil
}

// RedactString masks secret spans (goredact) and then redacts identities
// (email, @mention), returning the result and the number of goredact
// findings confirmed in this string, which the bundle reports as its
// masked-span count. Identity redaction is a
// smaller, best-effort pass on top (as the PR comment text treats it) and is
// not separately counted.
func (r *Redactor) RedactString(ctx context.Context, s string) (string, int, error) {
	if s == "" {
		return "", 0, nil
	}
	var buf bytes.Buffer
	stats, err := r.engine.Redact(ctx, &buf, strings.NewReader(s))
	if err != nil {
		return "", 0, err
	}
	identityRedacted, _ := redact.Identities(buf.String())
	return identityRedacted, int(stats.Findings), nil
}

// RedactPatchText redacts a unified-diff patch one line at a time, so a
// masked span can never merge or split a line and hunk line numbers stay
// valid.
func (r *Redactor) RedactPatchText(ctx context.Context, patch string) (string, int, error) {
	if patch == "" {
		return "", 0, nil
	}
	lines := strings.Split(patch, "\n")
	total := 0
	for i, line := range lines {
		redacted, count, err := r.RedactString(ctx, line)
		if err != nil {
			return "", 0, err
		}
		lines[i] = redacted
		total += count
	}
	return strings.Join(lines, "\n"), total, nil
}
