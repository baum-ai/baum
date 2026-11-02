package decisions

import _ "embed"

// PublicPromptName names the published extraction prompt.
const PublicPromptName = "decisions-public-v1"

//go:embed prompts/decisions-public-v1.txt
var publicPrompt string

// NewPublicExtractor returns an Extractor that sends the published prompt.
// Its PromptVersion is PublicPromptName and the hash NewExtractor's would
// be, so an extraction under it is never taken for one under another host's
// prompt, even one with the same text.
func NewPublicExtractor() *Extractor {
	extractor := NewExtractor(publicPrompt)
	extractor.version = PublicPromptName + "-" + extractor.version
	return extractor
}

// PublicPrompt returns the text of the published extraction prompt: the
// built-in text a custom prompt replaces.
func PublicPrompt() string { return publicPrompt }

// CustomVersionPrefix starts the PromptVersion of an extraction under a
// prompt other than the published one; "-" and the prompt's hash follow it.
const CustomVersionPrefix = PublicPromptName + "+custom"

// NewExtractorWith returns an Extractor that sends system. The published
// prompt's text, or an empty one, gives exactly NewPublicExtractor's Extractor. Any other text
// gives NewExtractor's, with a PromptVersion of CustomVersionPrefix, "-" and
// NewExtractor's hash, so an extraction under a custom prompt is never taken
// for one under the published prompt.
func NewExtractorWith(system string) *Extractor {
	if system == "" || system == publicPrompt {
		return NewPublicExtractor()
	}
	extractor := NewExtractor(system)
	extractor.version = CustomVersionPrefix + "-" + extractor.version
	return extractor
}
