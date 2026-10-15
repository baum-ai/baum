// Package pricing turns the raw model strings adapters record into canonical
// models with known token rates.
//
// Cost is computed at read time from the price list rather than stored on
// model_invocations. A stored cost_usd would freeze whatever the list said the
// day a row was written: a wrong rate, or a fix to the normalisation rules,
// would then apply only to new rows and need a backfill. Computing at read time
// means fixing the list fixes history.
//
// The normalisation rules are code and the rates are data (list.go). A model
// whose name the rules already resolve — any new Claude or GPT name — is
// priced by adding its rate alone, which is what lets a published list price it
// without a release. A name that needs a new alias still needs one.
package pricing

import (
	"regexp"
	"strconv"
	"strings"
)

// Family is the model vendor family a raw string resolved to.
type Family string

const (
	FamilyClaude  Family = "claude"
	FamilyGPT     Family = "gpt"
	FamilyGemini  Family = "gemini"
	FamilyGrok    Family = "grok"
	FamilyUnknown Family = ""
)

// Model is a normalised model identity.
type Model struct {
	// Canonical is the id the price table is keyed on, e.g. "claude-sonnet-4-5".
	// Empty when the raw string could not be resolved.
	Canonical string
	Family    Family
	// Size is the Claude tier (opus, sonnet, haiku, fable, mythos). Empty for
	// other families.
	Size string
	// Raw is the string the adapter recorded, kept so an unknown model can be
	// surfaced by name rather than as a silent zero.
	Raw string
}

// Known reports whether normalisation resolved the raw string to a canonical
// model. An unknown model never fails a request; it costs zero and is surfaced
// in the response's coverage block.
func (m Model) Known() bool { return m.Canonical != "" }

// Rate is a per-million-token price in USD.
type Rate struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// Vendor route prefixes. A model reaches us through several gateways and each
// one stamps its own prefix on the id; none of them change which model ran.
var routePrefixes = []string{
	"us.anthropic.", "eu.anthropic.", "apac.anthropic.", "anthropic.",
	"anthropic/", "openai/", "google/", "bedrock/", "vertex_ai/", "vertex/",
	"azure/", "azure_ai/", "github-copilot/", "copilot/", "openrouter/",
	// Cursor stamps its own name on the models it serves, so the same model
	// arrives as "grok-4.5" from one source and "cursor-grok-4.5" from another.
	"cursor-",
}

// Deployment and context-window suffixes. `[1m]` and `-1m` name a context
// window, `-fast` a serving mode, `-v1:0` a Bedrock revision — none of them are
// a different model for pricing purposes.
//
// `-high`, `-medium`, `-low` and `-minimal` name a reasoning effort, which is
// recorded on its own field of a model invocation and does not change the token
// rate. Leaving them on meant every effort-suffixed id a source records —
// Cursor writes `gpt-5.6-sol-medium` — failed to resolve and lost its cost.
var routeSuffixes = []string{
	"[1m]", "[200k]", "-1m", "-1024k", "-200k", "-fast", "-thinking",
	"-high", "-medium", "-low", "-minimal",
	"-v1:0", "-v2:0", "-v3:0", ":0",
}

var (
	datedSuffix   = regexp.MustCompile(`[-@]\d{8}$`)
	versionedPart = regexp.MustCompile(`^\d+$`)
)

// claudeSizes are the tier words, in the order the canonical id spells them.
var claudeSizes = []string{"opus", "sonnet", "haiku", "fable", "mythos", "instant"}

// aliases map raw strings that no decomposition rule would reach to a canonical
// id. Keys are already lowercased and stripped of route prefixes and suffixes.
var aliases = map[string]string{
	"claude-2":           "claude-2",
	"claude-2.0":         "claude-2",
	"claude-2.1":         "claude-2-1",
	"claude-instant-1":   "claude-instant-1",
	"claude-instant-1.2": "claude-instant-1",
	"gpt-5-codex":        "gpt-5-codex",
	"gpt-5-codex-mini":   "gpt-5-codex-mini",
	"gpt-5-mini":         "gpt-5-mini",
	"gpt-5-nano":         "gpt-5-nano",
	"gpt-5.5":            "gpt-5-5",
	"gpt-5.4":            "gpt-5-4",
	"gpt-5.4-mini":       "gpt-5-4-mini",
	"gpt-5.6-sol":        "gpt-5-6-sol",
	"gpt-5.6-terra":      "gpt-5-6-terra",
	"gpt-5.6-luna":       "gpt-5-6-luna",
	"codex-auto-review":  "codex-auto-review",
	// Not a model at all: the marker adapters write for a synthetic invocation
	// carrying a session-level aggregate. Recognised so it stops appearing in
	// the dashboard's "unknown model names" list as a name to chase.
	"<synthetic>":          "synthetic",
	"gpt-5-5":              "gpt-5-5",
	"gpt-5-4":              "gpt-5-4",
	"gpt-5-4-mini":         "gpt-5-4-mini",
	"gpt-5-6-sol":          "gpt-5-6-sol",
	"gpt-5-6-terra":        "gpt-5-6-terra",
	"gpt-5-6-luna":         "gpt-5-6-luna",
	"gpt-5":                "gpt-5",
	"gpt-4.1":              "gpt-4-1",
	"gpt-4o":               "gpt-4o",
	"o3":                   "o3",
	"o4-mini":              "o4-mini",
	"grok-4.5":             "grok-4-5",
	"gemini-2.5-pro":       "gemini-2-5-pro",
	"gemini-2.5-flash":     "gemini-2-5-flash",
	"gemini-3-pro":         "gemini-3-pro",
	"gemini-3-pro-preview": "gemini-3-pro",
	// The API id and the model version it currently serves.
	"deepseek-v4.1-flash": "deepseek-flash",
}

// Normalize resolves a raw model string to a canonical model under the active
// list. A calculation that normalises or prices more than once should take
// one list with Active and use its methods, so a list installed mid-way does
// not split it across two versions.
func Normalize(raw, vendor string) Model { return Active().Normalize(raw, vendor) }

// Normalize resolves a raw model string to a canonical model.
//
// The hard case is Claude, where two orderings are both in the wild:
// Anthropic writes `claude-sonnet-4-5` and Cursor writes `claude-4.5-sonnet` —
// family and size transposed. Prefix matching fails on that pair, so the
// decomposition below finds the size word and the version numbers wherever they
// sit rather than assuming an order.
//
// vendor is the last resort, and only disambiguates a bare version string. The
// session model stores no provider column: which API served a model is not a
// fact any mapper reliably recorded, and the vendor implies it wherever it
// mattered.
func (l *List) Normalize(raw, vendor string) Model {
	cleaned := clean(raw)
	if cleaned == "" {
		return Model{Raw: raw}
	}
	if canonical, ok := aliases[cleaned]; ok {
		return withFamily(canonical, raw)
	}
	if model, ok := normalizeClaude(cleaned, raw); ok {
		return model
	}
	// Non-Claude families are not transposed in practice, so the cleaned string
	// is the canonical id as long as the table knows it.
	if _, priced := l.rate(cleaned); priced {
		return withFamily(cleaned, raw)
	}
	if _, unpriced := knownUnpriced[cleaned]; unpriced {
		return withFamily(cleaned, raw)
	}
	// Last resort: a bare version string with a vendor that implies a family.
	if guess := guessFromVendor(l, cleaned, vendor); guess != "" {
		return withFamily(guess, raw)
	}
	return Model{Raw: raw}
}

func clean(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return ""
	}
	for _, prefix := range routePrefixes {
		value = strings.TrimPrefix(value, prefix)
	}
	// Suffix stripping repeats: `claude-opus-4-6[1m]-v1:0` carries two.
	for changed := true; changed; {
		changed = false
		for _, suffix := range routeSuffixes {
			if trimmed := strings.TrimSuffix(value, suffix); trimmed != value {
				value, changed = trimmed, true
			}
		}
		if trimmed := datedSuffix.ReplaceAllString(value, ""); trimmed != value {
			value, changed = trimmed, true
		}
		if trimmed := strings.TrimSuffix(value, "-latest"); trimmed != value {
			value, changed = trimmed, true
		}
	}
	return strings.Trim(value, "-.")
}

// normalizeClaude decomposes a cleaned string into (size, major, minor)
// accepting either field order.
func normalizeClaude(cleaned, raw string) (Model, bool) {
	if !strings.Contains(cleaned, "claude") {
		return Model{}, false
	}
	// "4.5" and "4-5" mean the same version; splitting on both makes the two
	// orderings converge on the same token list.
	parts := strings.FieldsFunc(cleaned, func(r rune) bool { return r == '-' || r == '.' || r == '_' })

	var size string
	var numbers []int
	for _, part := range parts {
		if part == "claude" {
			continue
		}
		if matched := matchSize(part); matched != "" && size == "" {
			size = matched
			continue
		}
		if versionedPart.MatchString(part) {
			value, err := strconv.Atoi(part)
			if err == nil {
				numbers = append(numbers, value)
			}
		}
	}
	if size == "" {
		return Model{}, false
	}

	canonical := "claude-" + size
	for i, number := range numbers {
		if i >= 2 {
			break
		}
		canonical += "-" + strconv.Itoa(number)
	}
	return Model{Canonical: canonical, Family: FamilyClaude, Size: size, Raw: raw}, true
}

func matchSize(part string) string {
	for _, size := range claudeSizes {
		if part == size {
			return size
		}
	}
	return ""
}

// guessFromVendor handles a bare version with no family word.
func guessFromVendor(list *List, cleaned, vendor string) string {
	prefix := ""
	switch vendor {
	case VendorCodex:
		prefix = "gpt-"
	case VendorClaudeCode:
		prefix = "claude-"
	default:
		return ""
	}
	candidate := prefix + cleaned
	if _, priced := list.rate(candidate); priced {
		return candidate
	}
	return ""
}

func withFamily(canonical, raw string) Model {
	model := Model{Canonical: canonical, Raw: raw}
	switch {
	case strings.HasPrefix(canonical, "claude"):
		model.Family = FamilyClaude
		for _, size := range claudeSizes {
			if strings.Contains(canonical, size) {
				model.Size = size
				break
			}
		}
	case strings.HasPrefix(canonical, "gpt"), strings.HasPrefix(canonical, "o3"), strings.HasPrefix(canonical, "o4"):
		model.Family = FamilyGPT
	case strings.HasPrefix(canonical, "gemini"):
		model.Family = FamilyGemini
	case strings.HasPrefix(canonical, "grok"):
		model.Family = FamilyGrok
	}
	return model
}

// Rates returns the per-million-token prices for a model. The second return is
// false for a model with no published rate we can cite — including every
// unknown model. Callers must treat that as "not priced", never as free.
func Rates(model Model) (Rate, bool) { return Active().Rates(model) }

// Rates returns the list's per-million-token prices for a model.
func (l *List) Rates(model Model) (Rate, bool) {
	if !model.Known() {
		return Rate{}, false
	}
	return l.rate(model.Canonical)
}
