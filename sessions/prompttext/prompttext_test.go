package prompttext

import "testing"

func TestIsBoilerplateRecognisesWrapperOnlyTurns(t *testing.T) {
	// Every one of these is a real shape observed in Claude Code transcripts.
	// A turn made only of them is not a prompt anybody typed, and showing one
	// as "the prompt that produced this commit" is the failure this prevents.
	boilerplateOnly := []string{
		"<command-name>/model</command-name>",
		"<command-name>/clear</command-name>\n<command-message>clear</command-message>",
		"<command-args>--verbose</command-args>",
		"<local-command-stdout>Set model to opus</local-command-stdout>",
		"<local-command-caveat>Do not respond to this</local-command-caveat>",
		"<ide_opened_file>/src/main.go</ide_opened_file>",
		"<ide_selection>lines 4-9</ide_selection>",
		"[Request interrupted by user]",
		"[Request interrupted by user for tool use]",
		// Whitespace between wrappers is still nothing.
		"  <command-name>/model</command-name>\n\n  ",
		"",
		"   ",
	}
	for _, text := range boilerplateOnly {
		if !IsBoilerplate(text) {
			t.Errorf("IsBoilerplate(%q) = false, want true", text)
		}
	}

	realPrompts := []string{
		"add a widget to the parser so it handles nested braces",
		// The common case: a slash command echoed alongside the prose typed
		// after it. The prose is what matters, so this is NOT boilerplate.
		"<command-name>/model</command-name>\nnow refactor the parser",
		"<ide_opened_file>/src/main.go</ide_opened_file>\nwhy does this leak?",
		"[Request interrupted by user]\ntry again but keep the tests",
	}
	for _, text := range realPrompts {
		if IsBoilerplate(text) {
			t.Errorf("IsBoilerplate(%q) = true, want false", text)
		}
	}
}

func TestStripLeavesOnlyTheProse(t *testing.T) {
	got := Strip("<command-name>/model</command-name>\nnow refactor the parser")
	if got != "now refactor the parser" {
		t.Errorf("Strip() = %q, want the prose alone", got)
	}

	// Multiline wrappers: the patterns are (?s) so a newline inside one does
	// not end it early and leave a dangling closing tag in the preview.
	got = Strip("<local-command-stdout>line one\nline two</local-command-stdout>\nkeep this")
	if got != "keep this" {
		t.Errorf("Strip() = %q, want the prose after a multiline wrapper", got)
	}
}

func TestPreviewCutsCharactersNotBytes(t *testing.T) {
	// Ten runes, twenty bytes. A byte-wise cut at 10 would split a rune and
	// emit U+FFFD; the preview must be readable text either way.
	text := "ααααααααααββββββββββ"
	preview, truncated := Preview(text, 10)
	if !truncated {
		t.Fatal("Preview() reported no truncation on a 20-rune string cut to 10")
	}
	if count := len([]rune(preview)); count > 10 {
		t.Errorf("Preview() returned %d runes, want at most 10", count)
	}
	for _, r := range preview {
		if r == '�' {
			t.Fatalf("Preview() split a rune: %q", preview)
		}
	}
}

func TestPreviewPrefersAWordBoundary(t *testing.T) {
	preview, truncated := Preview("the quick brown fox jumps over the lazy dog", 20)
	if !truncated {
		t.Fatal("Preview() reported no truncation")
	}
	// Cutting mid-word reads as corruption rather than as truncation, so the
	// cut backs up to the last space when one is close enough.
	if preview != "the quick brown fox" {
		t.Errorf("Preview() = %q, want a cut on the word boundary", preview)
	}
}

func TestPreviewStripsBeforeMeasuring(t *testing.T) {
	// The wrapper must not eat the budget: a preview of a slash-command turn
	// should show the prose, not 200 characters of markup.
	text := "<local-command-caveat>" + string(make([]byte, 0)) + "ignore me</local-command-caveat>keep"
	preview, truncated := Preview(text, 10)
	if preview != "keep" || truncated {
		t.Errorf("Preview() = %q, %v; want the prose untruncated", preview, truncated)
	}
}

func TestPreviewOfShortTextIsNotTruncated(t *testing.T) {
	preview, truncated := Preview("short", 100)
	if preview != "short" || truncated {
		t.Errorf("Preview() = %q, %v; want the text unchanged", preview, truncated)
	}
}
