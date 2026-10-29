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
