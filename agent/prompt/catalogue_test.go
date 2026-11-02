package prompt_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/decisions"
)

var backends = []string{agent.BackendClaude, agent.BackendAnthropic, agent.BackendCodex, agent.BackendCursorAgent}

// digests captured from the code before overrides existed.
var baseline = map[string]string{
	"claude review":       "b666ef1471b6a1db31217ee6ad34f14f4c7abfaea41f4040cbcf8dcd6eef499f",
	"claude group":        "47a28c59cd707efd66b8fe8f6c770d4224ba6dd7bd8b08e437316c6a19c8463f",
	"claude staged":       "453d63f9a9cd75da50659d7076510b70d2c25d3cf846763ba794854e76629792",
	"anthropic review":    "b666ef1471b6a1db31217ee6ad34f14f4c7abfaea41f4040cbcf8dcd6eef499f",
	"anthropic group":     "47a28c59cd707efd66b8fe8f6c770d4224ba6dd7bd8b08e437316c6a19c8463f",
	"anthropic staged":    "453d63f9a9cd75da50659d7076510b70d2c25d3cf846763ba794854e76629792",
	"codex review":        "d8593ec4a1ec9b8cf9713980d7328e152f6160c9c17a367ae3fd7d4c5ecc4d55",
	"codex group":         "aa337087b03b90f28e3bcf6d07e8b592ca8e665b3d8ee08020cb2815936e327c",
	"codex staged":        "3a984550f93ab9d3d2530798bcc9245d45986e3cc7fefc249d82f9f62b585b8d",
	"cursor-agent review": "94478c8269453fd045919a2849b2725ffc6b5efade3c84c25297d30ac3a4f2ea",
	"cursor-agent group":  "7c1b9df2dab338e2758f08bfdb6530420bc0fbfa5767d2c2a3f65e0b250df608",
	"cursor-agent staged": "5e1b2ba1f7891a7a8ec660930b29973d46d5cef2342c6a55a7baa6c418b52c03",
	"fix 0":               "5ff07cb79658980528df0d7dc38c2e8f2bd3c211422f66c769a21408237e4971",
	"fix 1":               "a01d046cb31bb35be38e4304c9957a47a87a8dfdd682def427ab00251a9848aa",
	"fix 2":               "de4b9899bccbfacc12a6661bcc20822f94a9f84dfab70e0916affeb24870489e",
	"fix 3":               "77e23a06289827b3c32e6b00e1d45db1e11d3241e927f66a48735aeca58ba082",
	"fix 4":               "8e02ce5a96bc1903eccf6724d5083133c405faf41ffa33005168f6aa447b84b4",
	"fix 5":               "e6d92e17a48ee22166dfc0b0ffdfc5f2f98c8cc0ec1e53b3e7859977dc9862dc",
	"fix 6":               "defc8bab46da2203065af95cc1f1c24dee74ae3af9504bfed0a70ce7064132f6",
	"fix 7":               "f21776eb2a4075460f23be290f8a4acb556d3fb073a48acd18f25c3570e537e6",
	"fix 8":               "3767be0d9d0e81ea6173e9b2bb2763184d29defdebcba8015770e41ca53aecec",
	"fix 9":               "16016895652a6f3fdc94430be7675d782e445fe5299e376ad19b21b6d01eef86",
	"fix 10":              "8439fadcb33063bbdfa427ece752f33c8d9cab8d4a4185100ff760e5592ffd73",
	"fix 11":              "121e23de3414c63c9c8b1b542a8d652f9b8f587c9bbbba98fd95158f7f1bdd25",
	"fix 12":              "1839eb22fbf2c3296b69984e52e616a5dff8fa3ab6a32b43aa6516cafcc96684",
	"fix 13":              "dd115a648650fa176de95287b3997141ea7fc68c798cf78519d2b30b6c06ce84",
	"fix 14":              "5bc9d09b8a5e8f6bfb7e5debd67f507bb82804c27bef1c245034678e831b3ebe",
	"fix 15":              "279238a2205c9da73f01828171b3b7362bb6f695546e617efa35c050b2280d2c",
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestZeroValueMatchesPreviousOutput(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends {
		for _, o := range []prompt.Overrides{nil, {}, {prompt.IDReview: "", prompt.IDFix: ""}} {
			p, err := prompt.Prompts{Overrides: o}.Instructions(ctx, b)
			if err != nil || p.Version != "review-public-v6" || p.Digest != baseline[b+" review"] || sha(p.Body) != p.Digest {
				t.Errorf("%s review: %v %q %s", b, err, p.Version, p.Digest)
			}
			g, err := prompt.GroupPrompts{Overrides: o}.Instructions(ctx, b)
			if err != nil || g.Version != "group-review-public-v4" || g.Digest != baseline[b+" group"] {
				t.Errorf("%s group: %v %q %s", b, err, g.Version, g.Digest)
			}
			s, err := prompt.StagedPrompts{Overrides: o}.StagedInstructions(ctx, b)
			if err != nil || s.Version != "review-staged-public-v1" || s.Digest != baseline[b+" staged"] {
				t.Errorf("%s staged: %v %q %s", b, err, s.Version, s.Digest)
			}
		}
	}
	for m := 0; m < 16; m++ {
		c, k, th, ch := m&1 != 0, m&2 != 0, m&4 != 0, m&8 != 0
		want := baseline[fmt.Sprintf("fix %d", m)]
		if got := sha(prompt.Fix(c, k, th, ch)); got != want {
			t.Errorf("Fix %d: %s", m, got)
		}
		text, version := prompt.FixWith(nil, c, k, th, ch)
		if sha(text) != want || version != prompt.FixVersion {
			t.Errorf("FixWith %d: %s %q", m, sha(text), version)
		}
	}
}

func TestOverridesAreUsed(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends {
		submit := readFile(t, map[string]string{"claude": "submit-claude.md", "anthropic": "submit-claude.md", "codex": "submit-codex.md", "cursor-agent": "submit-cursor.md"}[b])
		stageSubmit := readFile(t, map[string]string{"claude": "submit-stage-claude.md", "anthropic": "submit-stage-claude.md", "codex": "submit-stage-codex.md", "cursor-agent": "submit-stage-cursor.md"}[b])

		// review, with and without a trailing newline
		for _, text := range []string{"MY REVIEW\n", "MY REVIEW"} {
			p, _ := prompt.Prompts{Overrides: prompt.Overrides{prompt.IDReview: text}}.Instructions(ctx, b)
			if p.Body != "MY REVIEW\n## 10. Submitting\n\n"+submit || p.Version != "review-public-v6+custom" || p.Digest != sha(p.Body) {
				t.Errorf("%s review override: %q %q", b, p.Version, p.Body)
			}
		}
		// a fix or group override does not touch the review
		p, _ := prompt.Prompts{Overrides: prompt.Overrides{prompt.IDFix: "x", prompt.IDReviewGroup: "x", prompt.IDReviewStagedShared: "x"}}.Instructions(ctx, b)
		if p.Digest != baseline[b+" review"] || p.Version != prompt.Version {
			t.Errorf("%s: unrelated overrides changed the review", b)
		}

		// group: each of its two parts
		review := strings.TrimSuffix(readFile(t, "review-public-v6.md"), "## 10. Submitting\n")
		group := readFile(t, "group-public-v1.md")
		for o, want := range map[prompt.ID]string{
			prompt.IDReview:      "R\n" + group + "## 10. Submitting\n\n" + submit,
			prompt.IDReviewGroup: review + "G\n## 10. Submitting\n\n" + submit,
		} {
			text := "R"
			if o == prompt.IDReviewGroup {
				text = "G"
			}
			g, _ := prompt.GroupPrompts{Overrides: prompt.Overrides{o: text}}.Instructions(ctx, b)
			if g.Body != want || g.Version != "group-review-public-v4+custom" || g.Digest != sha(g.Body) {
				t.Errorf("%s group override %s: %q", b, o, g.Version)
			}
		}

		// staged: each of the four parts
		def, _ := prompt.StagedPrompts{}.StagedInstructions(ctx, b)
		for _, id := range []prompt.ID{prompt.IDReviewStagedShared, prompt.IDReviewStagedOutline, prompt.IDReviewStagedDetails, prompt.IDReviewStagedContext} {
			s, err := prompt.StagedPrompts{Overrides: prompt.Overrides{id: "CUSTOM"}}.StagedInstructions(ctx, b)
			if err != nil || s.Version != "review-staged-public-v1+custom" {
				t.Fatalf("%s %s: %v %q", b, id, err, s.Version)
			}
			got := map[prompt.ID]*[2]string{
				prompt.IDReviewStagedShared:  {def.Shared, s.Shared},
				prompt.IDReviewStagedOutline: {def.Outline, s.Outline},
				prompt.IDReviewStagedDetails: {def.Details, s.Details},
				prompt.IDReviewStagedContext: {def.Context, s.Context},
			}
			for other, pair := range got {
				if other == id && pair[0] == pair[1] {
					t.Errorf("%s %s not applied", b, id)
				}
				if other != id && pair[0] != pair[1] {
					t.Errorf("%s %s changed %s", b, id, other)
				}
			}
			var changed string
			switch id {
			case prompt.IDReviewStagedShared:
				changed = s.Shared
				if changed != "CUSTOM\n" {
					t.Errorf("shared %q", changed)
				}
			case prompt.IDReviewStagedOutline:
				changed = s.Outline
			case prompt.IDReviewStagedDetails:
				changed = s.Details
			case prompt.IDReviewStagedContext:
				changed = s.Context
			}
			if id != prompt.IDReviewStagedShared {
				part := map[prompt.ID]string{prompt.IDReviewStagedOutline: "the outline", prompt.IDReviewStagedDetails: "the section details", prompt.IDReviewStagedContext: "the context"}[id]
				if want := "CUSTOM\n## Submitting\n\n" + strings.ReplaceAll(stageSubmit, "{part}", part); changed != want {
					t.Errorf("%s %s: %q", b, id, changed)
				}
			}
			h := sha256.New()
			for _, x := range []string{s.Shared, s.Outline, s.Details, s.Context} {
				h.Write([]byte(x + "\x00"))
			}
			if s.Digest != hex.EncodeToString(h.Sum(nil)) || s.Digest == def.Digest {
				t.Errorf("%s %s digest", b, id)
			}
		}
		if _, err := (prompt.StagedPrompts{Overrides: prompt.Overrides{prompt.IDReview: "x"}}).StagedInstructions(ctx, b); err != nil {
			t.Fatal(err)
		}
		if s, _ := (prompt.StagedPrompts{Overrides: prompt.Overrides{prompt.IDReview: "x"}}).StagedInstructions(ctx, b); s.Digest != def.Digest || s.Version != prompt.StagedVersion {
			t.Errorf("%s: the one-pass override changed the staged review", b)
		}
	}
}

func TestFixWithOverrides(t *testing.T) {
	o := prompt.Overrides{prompt.IDFix: "RULES\n\n\n", prompt.IDFixChecks: "CHECKS", prompt.IDFixChat: "CHAT"}
	text, version := prompt.FixWith(o, true, true, false, false)
	if want := "RULES\n\n" + strings.TrimRight(readFile(t, "fix-conflicts.md"), "\n") + "\n\nCHECKS\n"; text != want || version != "fix-public-v1+custom" {
		t.Errorf("%q %q", version, text)
	}
	// only an included section counts
	text, version = prompt.FixWith(prompt.Overrides{prompt.IDFixChat: "CHAT"}, true, false, false, false)
	if version != prompt.FixVersion || text != prompt.Fix(true, false, false, false) {
		t.Errorf("an excluded section's override counted: %q", version)
	}
	if text, version = prompt.FixWith(prompt.Overrides{prompt.IDFixChat: "CHAT"}, false, false, false, true); version != "fix-public-v1+custom" || !strings.HasSuffix(text, "\n\nCHAT\n") {
		t.Errorf("%q %q", version, text)
	}
}

func TestCatalogue(t *testing.T) {
	var ids []prompt.ID
	for _, e := range prompt.Catalogue() {
		ids = append(ids, e.ID)
		if got, ok := prompt.Lookup(e.ID); !ok || !reflect.DeepEqual(got, e) {
			t.Errorf("Lookup(%s)", e.ID)
		}
		if e.Title == "" || e.UsedBy == "" || e.Default == "" || e.DefaultVersion == "" {
			t.Errorf("%s: incomplete entry", e.ID)
		}
		if !slices.Contains([]string{prompt.GroupReviews, prompt.GroupFixes, prompt.GroupExtraction}, e.Group) {
			t.Errorf("%s: group %q", e.ID, e.Group)
		}
		if strings.HasPrefix(string(e.ID), "review") && (strings.Contains(e.Default, "Submitting\n") || prompt.Validate(e.ID, e.Default) != nil) {
			t.Errorf("%s: the default must be valid and without the Submitting heading", e.ID)
		}
		if err := prompt.Validate(e.ID, e.Default); err != nil {
			t.Errorf("%s: its own default is invalid: %v", e.ID, err)
		}
		if len(prompt.MissingSafetyRules(e.ID, e.Default)) != 0 {
			t.Errorf("%s: default misses its own rules", e.ID)
		}
		seen := map[string]bool{}
		for _, r := range e.SafetyRules {
			if r.ID == "" || r.Label == "" || seen[r.ID] || !strings.HasPrefix(r.ID, string(e.ID)+".") {
				t.Errorf("%s: rule %q", e.ID, r.ID)
			}
			seen[r.ID] = true
			if n := strings.Count(e.Default, r.Text); n != 1 {
				t.Errorf("%s: rule text occurs %d times in the default", r.ID, n)
			}
		}
	}
	want := []prompt.ID{"review", "review-group", "review-staged-shared", "review-staged-outline", "review-staged-details", "review-staged-context", "fix", "fix-conflicts", "fix-checks", "fix-threads", "fix-chat", "extraction"}
	if !slices.Equal(ids, want) {
		t.Errorf("order: %v", ids)
	}
	if _, ok := prompt.Lookup("nope"); ok {
		t.Error("unknown id found")
	}
	// the review default is the file without its heading; the others are their files
	if e, _ := prompt.Lookup(prompt.IDReview); e.Default+"## 10. Submitting\n" != readFile(t, "review-public-v6.md") || e.DefaultVersion != prompt.Version {
		t.Error("review default")
	}
	if e, _ := prompt.Lookup(prompt.IDReviewStagedOutline); e.Default+"## Submitting\n" != readFile(t, "review-outline-public-v1.md") {
		t.Error("outline default")
	}
	if e, _ := prompt.Lookup(prompt.IDFixChat); e.Default != readFile(t, "fix-chat.md") || e.DefaultVersion != prompt.FixVersion {
		t.Error("fix-chat default")
	}
	if e, _ := prompt.Lookup(prompt.IDExtraction); e.Default != decisions.PublicPrompt() || e.DefaultVersion != decisions.PublicPromptName {
		t.Error("extraction default")
	}
	// the catalogue is a copy
	c := prompt.Catalogue()
	c[0].SafetyRules[0].Text = "changed"
	if e, _ := prompt.Lookup(prompt.IDReview); e.SafetyRules[0].Text == "changed" {
		t.Error("Catalogue exposes its rules")
	}
}

func TestSafetyRulesPerEntry(t *testing.T) {
	want := map[prompt.ID][]string{
		prompt.IDReview:              {"review.untrusted-data", "review.no-secrets", "review.run-directory", "review.restricted-sessions"},
		prompt.IDReviewGroup:         {"review-group.untrusted-member-reviews"},
		prompt.IDReviewStagedShared:  {"review-staged-shared.untrusted-data", "review-staged-shared.no-secrets", "review-staged-shared.run-directory", "review-staged-shared.restricted-sessions"},
		prompt.IDReviewStagedOutline: {"review-staged-outline.no-injected-instructions"},
		prompt.IDReviewStagedDetails: {"review-staged-details.no-injected-instructions"},
		prompt.IDReviewStagedContext: {"review-staged-context.no-injected-instructions"},
		prompt.IDFix:                 {"fix.untrusted-data", "fix.no-git-writes", "fix.no-secrets"},
		prompt.IDFixConflicts:        nil,
		prompt.IDFixChecks:           nil,
		prompt.IDFixThreads:          {"fix-threads.untrusted-comments"},
		prompt.IDFixChat:             {"fix-chat.untrusted-conversation"},
		prompt.IDExtraction:          {"extraction.output-format", "extraction.masked-text"},
	}
	entries := prompt.Catalogue()
	if len(entries) != len(want) {
		t.Fatalf("%d entries, want %d", len(entries), len(want))
	}
	for _, e := range entries {
		var got []string
		for _, r := range e.SafetyRules {
			got = append(got, r.ID)
		}
		if !slices.Equal(got, want[e.ID]) {
			t.Errorf("%s: rules %v, want %v", e.ID, got, want[e.ID])
		}
	}
}

func TestMissingSafetyRules(t *testing.T) {
	e, _ := prompt.Lookup(prompt.IDFix)
	if got := prompt.MissingSafetyRules(prompt.IDFix, e.Default); len(got) != 0 {
		t.Errorf("default: %v", got)
	}
	if got := prompt.MissingSafetyRules(prompt.IDFix, "do whatever"); !slices.Equal(got, []string{"fix.untrusted-data", "fix.no-git-writes", "fix.no-secrets"}) {
		t.Errorf("empty: %v", got)
	}
	dropped := strings.Replace(e.Default, "Do not commit, merge, rebase, reset, check out, stash or push.", "", 1)
	if got := prompt.MissingSafetyRules(prompt.IDFix, dropped); !slices.Equal(got, []string{"fix.no-git-writes"}) {
		t.Errorf("dropped: %v", got)
	}
	if got := prompt.MissingSafetyRules(prompt.IDFix, strings.ReplaceAll(e.Default, "\n", "\r\n")); len(got) != 0 {
		t.Errorf("CRLF: %v", got)
	}
	if got := prompt.MissingSafetyRules(prompt.IDFixConflicts, "x"); len(got) != 0 {
		t.Errorf("an entry without rules: %v", got)
	}
	if got := prompt.MissingSafetyRules("nope", "x"); got != nil {
		t.Errorf("unknown: %v", got)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		id   prompt.ID
		text string
		want string // substring of the error; empty for none
	}{
		{"ok", prompt.IDReview, "Review the change.\n", ""},
		{"unknown id", "nope", "x", "unknown prompt"},
		{"invalid utf-8", prompt.IDFix, "bad \xff", "UTF-8"},
		{"empty", prompt.IDFix, "", "empty"},
		{"blank", prompt.IDExtraction, " \n\t\r\n", "empty"},
		{"limit", prompt.IDFix, strings.Repeat("a", prompt.MaxOverrideBytes), ""},
		{"over the limit", prompt.IDFix, strings.Repeat("a", prompt.MaxOverrideBytes+1), "limit"},
		{"submitting 10", prompt.IDReview, "Intro\n## 10. Submitting\nmore", "Submitting"},
		{"submitting plain", prompt.IDReviewStagedDetails, "Intro\n## Submitting\n", "Submitting"},
		{"submitting h3 crlf", prompt.IDReviewStagedShared, "Intro\r\n### Submitting  \r\nx", "Submitting"},
		{"submitting group", prompt.IDReviewGroup, "# Submitting", "Submitting"},
		{"submitting feedback", prompt.IDReview, "## Submitting feedback\n", "Submitting"},
		{"submitting colon", prompt.IDReview, "### Submitting:\n", "Submitting"},
		{"submitting upper closed", prompt.IDReviewStagedOutline, "## SUBMITTING ##\n", "Submitting"},
		{"submitting indented", prompt.IDReview, " ## Submitting\n", "Submitting"},
		{"submitting numbered", prompt.IDReview, "## 3.2 Submitting\n", "Submitting"},
		{"heading ending in the word", prompt.IDReview, "## Results Submitting\n", ""},
		{"code-indented heading", prompt.IDReview, "    ## Submitting\n", ""},
		{"open backtick fence", prompt.IDFix, "Run:\n```sh\nmake\n", "fenced"},
		{"open tilde fence", prompt.IDExtraction, "~~~\ncode", "fenced"},
		{"closed fence", prompt.IDFix, "```sh\nmake\n```\n", ""},
		{"closed longer fence", prompt.IDFix, "````\n```\n````\n", ""},
		{"fence closed by wrong char", prompt.IDFix, "```\ncode\n~~~\n", "fenced"},
		{"submitting in prose", prompt.IDReview, "Say how Submitting works.\n", ""},
		{"submitting allowed in fix", prompt.IDFix, "## Submitting\n", ""},
		{"submitting allowed in extraction", prompt.IDExtraction, "## Submitting\n", ""},
	}
	for _, c := range cases {
		err := prompt.Validate(c.id, c.text)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
		}
	}
}

func TestNewExtractorWith(t *testing.T) {
	public := decisions.NewPublicExtractor()
	same := decisions.NewExtractorWith(decisions.PublicPrompt())
	if same.PromptVersion() != public.PromptVersion() || same.SystemPrompt() != public.SystemPrompt() {
		t.Error("the public text must give the public extractor")
	}
	if empty := decisions.NewExtractorWith(""); empty.PromptVersion() != public.PromptVersion() || empty.SystemPrompt() != public.SystemPrompt() {
		t.Error("an empty prompt must give the public extractor")
	}
	custom := decisions.NewExtractorWith("Extract decisions my way.")
	if !strings.HasPrefix(custom.PromptVersion(), "decisions-public-v1+custom-") || custom.PromptVersion() == public.PromptVersion() || custom.SystemPrompt() != "Extract decisions my way." {
		t.Errorf("custom version %q", custom.PromptVersion())
	}
	if want := decisions.NewExtractor("Extract decisions my way.").PromptVersion(); !strings.HasSuffix(custom.PromptVersion(), "-"+want) {
		t.Errorf("hash differs from NewExtractor's: %q vs %q", custom.PromptVersion(), want)
	}
	if other := decisions.NewExtractorWith("Another."); other.PromptVersion() == custom.PromptVersion() {
		t.Error("different texts, same version")
	}
}
