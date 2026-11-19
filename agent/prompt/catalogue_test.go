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

// digests of the built-in texts; update them with the prompt versions.
var baseline = map[string]string{
	"claude review":       "8aee2f681ac5c7a854ff94a8d6d47f8725f8ade5d1d2a9151f51112fb3cfef84",
	"claude group":        "25e5ca654e5f6e50a7eb8b31e7ac35fe107dfb89c6faaca94c0ab0819d3d8ae5",
	"claude staged":       "20577aacd326cf267c58b66ee5f311fd5269f588bc9aeb994df5777055004269",
	"anthropic review":    "8aee2f681ac5c7a854ff94a8d6d47f8725f8ade5d1d2a9151f51112fb3cfef84",
	"anthropic group":     "25e5ca654e5f6e50a7eb8b31e7ac35fe107dfb89c6faaca94c0ab0819d3d8ae5",
	"anthropic staged":    "20577aacd326cf267c58b66ee5f311fd5269f588bc9aeb994df5777055004269",
	"codex review":        "98aa0b576212d2d4a09a4f22fa489ccadcdb8e984341a0255d2eebde22310d09",
	"codex group":         "753d3250ff6c10f62e2d000cc280946ce02377fcf025be4f45deea4a24e15fe3",
	"codex staged":        "9b0107271f1e54414fc05284752ab2c1570accaa66d641133dea18ab97c6721b",
	"cursor-agent review": "d5998d7816dd1f46a8649fe185259d8d457fd1969246d8ed56f043258999c269",
	"cursor-agent group":  "3d0a6153c400e943252ce317c4cc0e4939f4e90087bfeb6dfa1e8e7a22dc4129",
	"cursor-agent staged": "fbe1aded2dfd446ba70d8fdaf95085df1e90ac4d7e4ccfc0ce76bd603baaa3c9",
	"fix 0":               "fe66f520877324fce47415babd1b6a499ae14439c3b54228110ac37e642370ee",
	"fix 1":               "fb8a3cbd108bfcf1a9a3ddc8fa27cb32c5cd9617d20c69cde9af0cd389334871",
	"fix 2":               "ee1eb9f72e6c822e23501fb2c5dd47fae531f0c736ee5aea598397604af93d2e",
	"fix 3":               "8a2df39a4c292be76dcca3dc20ce389a04e7f380dc76ef53d57031f9eb5388c1",
	"fix 4":               "4beb31f7721ec77463cdf2866a53d6b765978096e3df60e972f42a23bdddbae1",
	"fix 5":               "1308b16c4a6ed70bdbb9f46b30f5cbf8cb7177f86539e090c0a593f0bf8284a5",
	"fix 6":               "09c781642505cfc34af89958d087b00819aa3182b678414ee5f7f60b5ab334ce",
	"fix 7":               "511d16941e76c519bcc0c5322e918dea36a354f2849c23c01826f624f7db2d0c",
	"fix 8":               "00f13a33f2faad8a1bf2521016e540d00985a96ea78d754ff0f62e6b96299e4f",
	"fix 9":               "41ef8ca3762fd9284f995a64b2f22c561f6b67d5e4d59379402c133b49457c89",
	"fix 10":              "e506d4035ec4c28ad0a3fbe2cafb67a35518a1c855bfc213076a4b432f3deed0",
	"fix 11":              "f4d8b609fde7355595ab9ef78944806ba43d98f1ac8f5b96e17ac9c5d21ffd52",
	"fix 12":              "6bc486fa191a75f4804b438cef4bde33b9e1974719242f4b3e2e9347802abd3c",
	"fix 13":              "6f81c43f35a64fa7742018b3d7273cfcc51c410a47ac3e55723913d8f3ede266",
	"fix 14":              "3aad90ee71a18938cf426c8959187f3e250d2867d0b708e40a209300bc34d1d2",
	"fix 15":              "49591bfd05ff37df5438d67f2f6a11d0d62bae90fb32592bd35f521c0fd2ff74",
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestZeroValueMatchesPreviousOutput(t *testing.T) {
	ctx := context.Background()
	for _, b := range backends {
		p, err := prompt.Prompts{}.Instructions(ctx, b)
		if err != nil || p.Version != prompt.Version || p.Digest != baseline[b+" review"] || sha(p.Body) != p.Digest {
			t.Errorf("%s review: %v %q %s", b, err, p.Version, p.Digest)
		}
		for _, o := range []prompt.Overrides{nil, {}, {prompt.IDReviewGroup: "", prompt.IDFix: ""}} {
			g, err := prompt.GroupPrompts{Overrides: o}.Instructions(ctx, b)
			if err != nil || g.Version != prompt.GroupVersion || g.Digest != baseline[b+" group"] {
				t.Errorf("%s group: %v %q %s", b, err, g.Version, g.Digest)
			}
			s, err := prompt.StagedPrompts{Overrides: o}.StagedInstructions(ctx, b)
			if err != nil || s.Version != prompt.StagedVersion || s.Digest != baseline[b+" staged"] {
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

		// group: one text before the Submitting heading, with and without a
		// trailing newline
		for _, text := range []string{"MY GROUP\n", "MY GROUP"} {
			g, _ := prompt.GroupPrompts{Overrides: prompt.Overrides{prompt.IDReviewGroup: text}}.Instructions(ctx, b)
			if g.Body != "MY GROUP\n## 10. Submitting\n\n"+submit || g.Version != prompt.GroupVersion+"+custom" || g.Digest != sha(g.Body) {
				t.Errorf("%s group override: %q %q", b, g.Version, g.Body)
			}
		}
		// a fix or staged override does not touch the group
		g, _ := prompt.GroupPrompts{Overrides: prompt.Overrides{prompt.IDFix: "x", prompt.IDReviewStagedShared: "x"}}.Instructions(ctx, b)
		if g.Digest != baseline[b+" group"] || g.Version != prompt.GroupVersion {
			t.Errorf("%s: unrelated overrides changed the group", b)
		}

		// staged: each of the four parts
		def, _ := prompt.StagedPrompts{}.StagedInstructions(ctx, b)
		for _, id := range []prompt.ID{prompt.IDReviewStagedShared, prompt.IDReviewStagedOutline, prompt.IDReviewStagedDetails, prompt.IDReviewStagedContext} {
			s, err := prompt.StagedPrompts{Overrides: prompt.Overrides{id: "CUSTOM"}}.StagedInstructions(ctx, b)
			if err != nil || s.Version != prompt.StagedVersion+"+custom" {
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
		if s, _ := (prompt.StagedPrompts{Overrides: prompt.Overrides{prompt.IDReviewGroup: "x"}}).StagedInstructions(ctx, b); s.Digest != def.Digest || s.Version != prompt.StagedVersion {
			t.Errorf("%s: the group override changed the staged review", b)
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
	want := []prompt.ID{"review-group", "review-staged-shared", "review-staged-outline", "review-staged-details", "review-staged-context", "review-view", "review-v2-shared", "review-map", "review-section", "review-context-v2", "review-tile", "review-overview", "review-triage", "fix", "fix-conflicts", "fix-checks", "fix-threads", "fix-chat", "extraction"}
	if !slices.Equal(ids, want) {
		t.Errorf("order: %v", ids)
	}
	if _, ok := prompt.Lookup("nope"); ok {
		t.Error("unknown id found")
	}
	// the group default is the review file without its heading, then the
	// group file; the others are their files
	review := strings.TrimSuffix(readFile(t, "review-public-v6.md"), "## 10. Submitting\n")
	if e, _ := prompt.Lookup(prompt.IDReviewGroup); e.Default != review+readFile(t, "group-public-v1.md") || e.DefaultVersion != prompt.GroupVersion {
		t.Error("group default")
	}
	if e, _ := prompt.Lookup(prompt.IDReviewStagedOutline); e.Default+"## Submitting\n" != readFile(t, "review-outline-public-v1.md") {
		t.Error("outline default")
	}
	if e, _ := prompt.Lookup(prompt.IDFixChat); e.Default != readFile(t, "fix-chat.md") || e.DefaultVersion != prompt.FixVersion {
		t.Error("fix-chat default")
	}
	if e, _ := prompt.Lookup(prompt.IDReviewTriage); e.Default != readFile(t, "review-triage-public-v1.md") || e.DefaultVersion != prompt.TriageVersion {
		t.Error("triage default")
	}
	if text, version := prompt.Triage(nil); text != readFile(t, "review-triage-public-v1.md") || version != prompt.TriageVersion {
		t.Errorf("built-in triage %q", version)
	}
	if text, version := prompt.Triage(prompt.Overrides{prompt.IDReviewTriage: "MINE"}); text != "MINE\n" || version != prompt.TriageVersion+prompt.CustomSuffix {
		t.Errorf("custom triage %q %q", text, version)
	}
	if e, _ := prompt.Lookup(prompt.IDExtraction); e.Default != decisions.PublicPrompt() || e.DefaultVersion != decisions.PublicPromptName {
		t.Error("extraction default")
	}
	// the catalogue is a copy
	c := prompt.Catalogue()
	c[0].SafetyRules[0].Text = "changed"
	if e, _ := prompt.Lookup(prompt.IDReviewGroup); e.SafetyRules[0].Text == "changed" {
		t.Error("Catalogue exposes its rules")
	}
}

func TestSafetyRulesPerEntry(t *testing.T) {
	want := map[prompt.ID][]string{
		prompt.IDReviewGroup:         {"review-group.untrusted-data", "review-group.no-secrets", "review-group.run-directory", "review-group.restricted-sessions", "review-group.untrusted-member-reviews"},
		prompt.IDReviewStagedShared:  {"review-staged-shared.untrusted-data", "review-staged-shared.no-secrets", "review-staged-shared.run-directory", "review-staged-shared.restricted-sessions"},
		prompt.IDReviewStagedOutline: {"review-staged-outline.no-injected-instructions"},
		prompt.IDReviewStagedDetails: {"review-staged-details.no-injected-instructions"},
		prompt.IDReviewStagedContext: {"review-staged-context.no-injected-instructions"},
		prompt.IDReviewView:          {"review-view.untrusted-input"},
		prompt.IDReviewV2Shared:      {"review-v2-shared.untrusted-data", "review-v2-shared.no-secrets", "review-v2-shared.restricted-sessions"},
		prompt.IDReviewMap:           {"review-map.no-injected-instructions"},
		prompt.IDReviewSection:       {"review-section.no-injected-instructions"},
		prompt.IDReviewContextV2:     nil,
		prompt.IDReviewTile:          nil,
		prompt.IDReviewOverview:      nil,
		prompt.IDReviewTriage:        {"review-triage.untrusted-data", "review-triage.when-unsure"},
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
	dropped := strings.Replace(e.Default, "Do not try to push.", "", 1)
	if got := prompt.MissingSafetyRules(prompt.IDFix, dropped); !slices.Equal(got, []string{"fix.no-git-writes"}) {
		t.Errorf("dropped: %v", got)
	}
	// A custom prompt based on the earlier wording keeps the rule.
	if got := prompt.MissingSafetyRules(prompt.IDFix, strings.Replace(e.Default, "Do not try to push.", "Do not commit, merge, rebase, reset, check out, stash or push.", 1)); len(got) != 0 {
		t.Errorf("earlier wording: %v", got)
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
		{"ok", prompt.IDReviewGroup, "Review the change.\n", ""},
		{"unknown id", "nope", "x", "unknown prompt"},
		{"invalid utf-8", prompt.IDFix, "bad \xff", "UTF-8"},
		{"empty", prompt.IDFix, "", "empty"},
		{"blank", prompt.IDExtraction, " \n\t\r\n", "empty"},
		{"limit", prompt.IDFix, strings.Repeat("a", prompt.MaxOverrideBytes), ""},
		{"over the limit", prompt.IDFix, strings.Repeat("a", prompt.MaxOverrideBytes+1), "limit"},
		{"submitting 10", prompt.IDReviewGroup, "Intro\n## 10. Submitting\nmore", "Submitting"},
		{"submitting plain", prompt.IDReviewStagedDetails, "Intro\n## Submitting\n", "Submitting"},
		{"submitting h3 crlf", prompt.IDReviewStagedShared, "Intro\r\n### Submitting  \r\nx", "Submitting"},
		{"submitting group", prompt.IDReviewGroup, "# Submitting", "Submitting"},
		{"submitting feedback", prompt.IDReviewGroup, "## Submitting feedback\n", "Submitting"},
		{"submitting colon", prompt.IDReviewGroup, "### Submitting:\n", "Submitting"},
		{"submitting upper closed", prompt.IDReviewStagedOutline, "## SUBMITTING ##\n", "Submitting"},
		{"submitting indented", prompt.IDReviewGroup, " ## Submitting\n", "Submitting"},
		{"submitting numbered", prompt.IDReviewGroup, "## 3.2 Submitting\n", "Submitting"},
		{"heading ending in the word", prompt.IDReviewGroup, "## Results Submitting\n", ""},
		{"code-indented heading", prompt.IDReviewGroup, "    ## Submitting\n", ""},
		{"open backtick fence", prompt.IDFix, "Run:\n```sh\nmake\n", "fenced"},
		{"open tilde fence", prompt.IDExtraction, "~~~\ncode", "fenced"},
		{"closed fence", prompt.IDFix, "```sh\nmake\n```\n", ""},
		{"closed longer fence", prompt.IDFix, "````\n```\n````\n", ""},
		{"fence closed by wrong char", prompt.IDFix, "```\ncode\n~~~\n", "fenced"},
		{"submitting in prose", prompt.IDReviewGroup, "Say how Submitting works.\n", ""},
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

// The view prompt is listed, since a review job with tile reviews on uses
// it, with its built-in text as the default; a text for it is validated and
// checked for its safety rule.
func TestViewIsListed(t *testing.T) {
	e, ok := prompt.Lookup(prompt.IDReviewView)
	builtin := readFile(t, "review-view-public-v1.md")
	if !ok || e.Group != prompt.GroupReviews || e.Default != builtin || e.DefaultVersion != prompt.ViewVersion {
		t.Errorf("the view prompt's entry %v %+v", ok, e.Title)
	}
	if err := prompt.Validate(prompt.IDReviewView, builtin); err != nil {
		t.Errorf("its own default is invalid: %v", err)
	}
	if got := prompt.MissingSafetyRules(prompt.IDReviewView, builtin); len(got) != 0 {
		t.Errorf("the default misses %v", got)
	}
	if got := prompt.MissingSafetyRules(prompt.IDReviewView, "Present."); len(got) != 1 || got[0] != "review-view.untrusted-input" {
		t.Errorf("missing %v", got)
	}
	p, err := prompt.TilesPrompts{}.ViewInstructions(context.Background(), agent.BackendCodex)
	if err != nil || p.Body != builtin || p.Version != prompt.ViewVersion || p.Digest != sha(p.Body) {
		t.Errorf("built in: %v %q", err, p.Version)
	}
	p, err = prompt.TilesPrompts{Overrides: prompt.Overrides{prompt.IDReviewView: "Present my way."}}.ViewInstructions(context.Background(), agent.BackendCodex)
	if err != nil || p.Body != "Present my way.\n" || p.Version != prompt.ViewVersion+"+custom" {
		t.Errorf("override: %v %q %q", err, p.Version, p.Body)
	}
}
