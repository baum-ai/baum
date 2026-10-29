// Package consolidate holds the feature consolidation's model calls
// (features/README.md, "Consolidation"): the input rendered for a
// repository's merge call and for one feature's split call, the reply
// schemas, and the validation of a reply. The host supplies its prompt
// versions (Prompts); Public are the published ones. It makes no call and
// reads no database.
package consolidate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/baum-ai/baum/decisions"
)

// Version is a released prompt version: its merge and split prompt. A
// released prompt is never edited; a change is a new version.
type Version struct {
	Merge, Split string
	// Contextual marks a version whose merge call also sees each feature's
	// digest and its pull requests' summaries.
	Contextual bool
}

// Prompts are a host's released prompt versions, by name.
type Prompts map[string]Version

func (v Version) prompt(kind Kind) (string, bool) {
	switch kind {
	case Merge:
		return v.Merge, true
	case Split:
		return v.Split, true
	}
	return "", false
}

// UsesFeatureContext reports whether version's merge call reads each
// feature's digest and pull request summaries (Feature.Digest,
// PullRequestTitle.Summary).
func (p Prompts) UsesFeatureContext(version string) bool { return p[version].Contextual }

// Kind is which of a version's two calls a request is.
type Kind string

const (
	// Merge proposes groups of duplicate features of one repository.
	Merge Kind = "merge"
	// Split partitions one oversized feature's pull requests.
	Split Kind = "split"
)

// schemaNames name each kind's Schema in decisions.ModelRequest.
var schemaNames = map[Kind]string{Merge: "feature_merge", Split: "feature_split"}

// KnownPromptVersion reports whether version is a released prompt version.
func (p Prompts) KnownPromptVersion(version string) bool {
	_, ok := p[version]
	return ok
}

// PromptVersions lists the released prompt versions, sorted.
func (p Prompts) PromptVersions() []string {
	versions := make([]string, 0, len(p))
	for version := range p {
		versions = append(versions, version)
	}
	slices.Sort(versions)
	return versions
}

// MaxOutputTokens caps a reply, reasoning included, unless the host asks
// for more, up to MaxHostOutputTokens: a host whose model reasons within the
// reply's bound and cannot be told not to may need the room.
const (
	MaxOutputTokens     = 8192
	MaxHostOutputTokens = 32768
)

// Bounds of one call's rendered input in bytes, a host's setting.
const (
	DefaultMaxInputBytes = 200_000
	MinInputBytes        = 16 << 10
	MaxInputBytes        = 1 << 20
)

// Bounds of each rendered field, so no one field crowds the rest out.
const (
	maxFeatures                = 300
	maxFeatureTitleRunes       = 200
	maxFeatureDescriptionRunes = 1000
	maxFeaturePaths            = 20
	maxFeaturePullRequests     = 20
	maxPullTitleRunes          = 200
	maxDigestRunes             = 800
	maxPullSummaryRunes        = 300
	maxPathRunes               = 300
	// MaxSplitPullRequests is the most pull requests a split call is
	// offered; a larger feature is not split.
	MaxSplitPullRequests = 200
	maxSplitBodyRunes    = 1000
	maxSplitPaths        = 20
	maxShortRunes        = 40
)

// Bounds of a part's text in a split reply; a longer one is invalid.
const (
	MaxTitleRunes       = 120
	MaxDescriptionRunes = 1000
)

// ErrTooLarge is an input that cannot be cut down to its bound.
var ErrTooLarge = errors.New("the consolidation input does not fit its bound")

// ErrInvalidReply is a reply that does not match its schema, names an id
// outside its input, or does not place every member exactly once.
var ErrInvalidReply = errors.New("invalid feature consolidation reply")

// Feature is one of a repository's features as the merge call sees it:
// its generated text, paths and pull request titles, never a session's
// prompts or decisions. Digest and the pull requests' summaries are set
// only for a version that UsesFeatureContext, and only as far as the
// repository may read them.
type Feature struct {
	Title, Description string
	Digest             string
	// Withheld marks a feature whose text the repository may not read now:
	// it is rendered without its title and description.
	Withheld     bool
	Paths        []string
	PullRequests []PullRequestTitle
	// Sessions counts the connected sessions in it on their own that follow
	// no feature pull request.
	Sessions int
	// SplitFrom is the index among the features given to RenderMerge of the
	// feature a recent split created this one from, nil for none; set only
	// for a version that UsesFeatureContext.
	SplitFrom *int
}

// PullRequestTitle is a pull request of a feature in the merge call.
type PullRequestTitle struct {
	Number  int32
	Title   string
	Summary string
}

type renderedFeature struct {
	ID           string                `json:"id"`
	Title        string                `json:"title"`
	Description  string                `json:"description"`
	Digest       string                `json:"digest,omitempty"`
	Paths        []string              `json:"paths"`
	PullRequests []renderedPullRequest `json:"pull_requests"`
	Sessions     int                   `json:"sessions"`
	Withheld     bool                  `json:"withheld,omitempty"`
	SplitFrom    string                `json:"split_from,omitempty"`
	splitFrom    int
}

type renderedPullRequest struct {
	Number  int32  `json:"number"`
	Title   string `json:"title"`
	Summary string `json:"summary,omitempty"`
}

// RenderedMerge is the user message of a merge call.
type RenderedMerge struct {
	Input string
	// Features lists the indexes of the rendered features: ref Fi is
	// Features[i-1].
	Features []int
	// Cut counts the features left out to keep the input within its bound.
	Cut int
	// CutSummaries and CutDigests count the pull request summaries and
	// digests left out first, before any feature is.
	CutSummaries, CutDigests int
}

// featureRef is how the input names the i-th rendered feature, so the model
// never copies an id.
func featureRef(i int) string { return fmt.Sprintf("F%d", i+1) }

func pullRef(i int) string { return fmt.Sprintf("P%d", i+1) }

// RenderMerge is the user message of a merge call: the features in the
// order given (most recently active first), every field truncated to its
// bound, at most maxFeatures of them and at most maxBytes long. When it is
// longer, pull request summaries are left out first, each feature's
// earliest first, in turns over the features; then digests, of the least
// recently active feature first; then features at the end. A feature's
// digest, summaries and split_from (the ref of the feature it was recently
// split from, when that one is rendered too) are rendered only when set, so
// a version that does not UsesFeatureContext renders as before.
func RenderMerge(features []Feature, maxBytes int) (RenderedMerge, error) {
	const head, tail = `{"features":[`, `]}`
	result := RenderedMerge{}
	offered := features[:min(len(features), maxFeatures)]
	result.Cut = len(features) - len(offered)
	rendered := make([]renderedFeature, len(offered))
	encoded := make([]string, len(offered))
	total := len(head) + len(tail)
	for i, feature := range offered {
		title, description := withheldText(feature.Withheld, feature.Title, feature.Description)
		digest := ""
		if !feature.Withheld {
			digest = truncate(feature.Digest, maxDigestRunes)
		}
		rendered[i] = renderedFeature{
			ID: featureRef(i), Title: title, Description: description, Digest: digest, Withheld: feature.Withheld,
			Paths: boundPaths(feature.Paths, maxFeaturePaths), PullRequests: []renderedPullRequest{}, Sessions: feature.Sessions,
			splitFrom: -1,
		}
		if feature.SplitFrom != nil && *feature.SplitFrom >= 0 && *feature.SplitFrom < len(offered) && *feature.SplitFrom != i {
			rendered[i].SplitFrom, rendered[i].splitFrom = featureRef(*feature.SplitFrom), *feature.SplitFrom
		}
		for _, pull := range feature.PullRequests[:min(len(feature.PullRequests), maxFeaturePullRequests)] {
			rendered[i].PullRequests = append(rendered[i].PullRequests, renderedPullRequest{
				Number: pull.Number, Title: truncate(pull.Title, maxPullTitleRunes),
			})
			if !feature.Withheld {
				// A withheld feature shows no summaries, as the split and
				// the assigner show none.
				rendered[i].PullRequests[len(rendered[i].PullRequests)-1].Summary = truncate(pull.Summary, maxPullSummaryRunes)
			}
		}
		var err error
		if encoded[i], err = encode(rendered[i]); err != nil {
			return RenderedMerge{}, err
		}
		total += len(encoded[i])
		if i > 0 {
			total++
		}
	}
	// reencode measures feature i again after a cut.
	reencode := func(i int) error {
		again, err := encode(rendered[i])
		if err != nil {
			return err
		}
		total += len(again) - len(encoded[i])
		encoded[i] = again
		return nil
	}
	for turn := 0; total > maxBytes && turn < maxFeaturePullRequests; turn++ {
		for i := range rendered {
			if total <= maxBytes {
				break
			}
			if turn < len(rendered[i].PullRequests) && rendered[i].PullRequests[turn].Summary != "" {
				rendered[i].PullRequests[turn].Summary = ""
				result.CutSummaries++
				if err := reencode(i); err != nil {
					return RenderedMerge{}, err
				}
			}
		}
	}
	for i := len(rendered) - 1; i >= 0 && total > maxBytes; i-- {
		if rendered[i].Digest != "" {
			rendered[i].Digest = ""
			result.CutDigests++
			if err := reencode(i); err != nil {
				return RenderedMerge{}, err
			}
		}
	}
	size := len(head) + len(tail)
	var parts []string
	for i := range rendered {
		added := len(encoded[i])
		if len(parts) > 0 {
			added++
		}
		if size+added > maxBytes {
			result.Cut += len(rendered) - i
			break
		}
		size += added
		parts = append(parts, encoded[i])
		result.Features = append(result.Features, i)
	}
	if len(parts) == 0 && len(features) > 0 {
		return RenderedMerge{}, ErrTooLarge
	}
	// A split_from naming a feature left out names nothing: it goes, which
	// only shortens the input.
	for i := range parts {
		if rendered[i].splitFrom >= len(parts) {
			rendered[i].SplitFrom, rendered[i].splitFrom = "", -1
			again, err := encode(rendered[i])
			if err != nil {
				return RenderedMerge{}, err
			}
			parts[i] = again
		}
	}
	result.Input = head + strings.Join(parts, ",") + tail
	return result, nil
}

// Group is a set of duplicate features of a validated merge reply, as
// indexes into the features given to RenderMerge: the survivor, and the
// duplicates that join it.
type Group struct {
	Survivor   int
	Duplicates []int
}

type mergeReply struct {
	Groups *[]struct {
		Reason     *string   `json:"reason"`
		Survivor   *string   `json:"survivor"`
		Duplicates *[]string `json:"duplicates"`
	} `json:"groups"`
}

// ParseMergeReply decodes and validates a merge reply against the rendered
// input, keeping what is valid: an id outside the input is dropped (a group
// whose survivor is one with it), every group naming a feature the reply
// names more than once is dropped, and so is a group left without a
// duplicate. It returns the groups kept and how many of the
// reply's groups it dropped or cut. A reply that does not decode, or misses
// a field, is invalid.
func ParseMergeReply(data []byte, rendered RenderedMerge) ([]Group, int, error) {
	var r mergeReply
	if err := decodeStrict(data, &r); err != nil {
		return nil, 0, err
	}
	if r.Groups == nil {
		return nil, 0, fmt.Errorf("%w: groups are missing", ErrInvalidReply)
	}
	refs := map[string]int{}
	for i, index := range rendered.Features {
		refs[featureRef(i)] = index
	}
	// Every known id the reply names, in any group, counts: a feature it
	// names twice is in no group kept.
	type candidate struct {
		survivor   int
		known      bool
		duplicates []int
		cut        bool
	}
	candidates := make([]candidate, 0, len(*r.Groups))
	named := map[int]int{}
	for _, group := range *r.Groups {
		if group.Reason == nil || group.Survivor == nil || group.Duplicates == nil {
			return nil, 0, fmt.Errorf("%w: a field is missing", ErrInvalidReply)
		}
		var next candidate
		next.survivor, next.known = refs[strings.TrimSpace(*group.Survivor)]
		if next.known {
			named[next.survivor]++
		}
		for _, duplicate := range *group.Duplicates {
			if index, ok := refs[strings.TrimSpace(duplicate)]; ok {
				next.duplicates = append(next.duplicates, index)
				named[index]++
			}
		}
		next.cut = len(next.duplicates) < len(*group.Duplicates)
		candidates = append(candidates, next)
	}
	dropped := 0
	groups := []Group{}
	for _, next := range candidates {
		twice := slices.ContainsFunc(next.duplicates, func(index int) bool { return named[index] > 1 }) ||
			(next.known && named[next.survivor] > 1)
		if !next.known || len(next.duplicates) == 0 || twice {
			dropped++
			continue
		}
		if next.cut {
			dropped++
		}
		groups = append(groups, Group{Survivor: next.survivor, Duplicates: next.duplicates})
	}
	return groups, dropped, nil
}

// PullRequest is one of a feature's pull requests as the split call sees
// it.
type PullRequest struct {
	Number      int32
	Title, Body string
	// Summary is what it changed, as far as the repository may read it;
	// set only for a version that UsesFeatureContext.
	Summary  string
	State    string
	OpenedAt time.Time
	Paths    []string
}

// SplitInput is one oversized feature: its text and its pull requests,
// earliest opened first. Withheld renders it without its text.
type SplitInput struct {
	Title, Description string
	Withheld           bool
	PullRequests       []PullRequest
}

type renderedSplitFeature struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Withheld    bool   `json:"withheld,omitempty"`
}

// withheldTitle stands in for the title of a feature whose text is
// withheld; its description is empty.
const withheldTitle = "(summary withheld)"

func withheldText(withheld bool, title, description string) (string, string) {
	if withheld {
		return withheldTitle, ""
	}
	return truncate(title, maxFeatureTitleRunes), truncate(description, maxFeatureDescriptionRunes)
}

type renderedSplitPullRequest struct {
	ID      string   `json:"id"`
	Number  int32    `json:"number"`
	Title   string   `json:"title"`
	Summary string   `json:"summary,omitempty"`
	Body    string   `json:"description"`
	State   string   `json:"state"`
	Opened  string   `json:"opened"`
	Paths   []string `json:"paths"`
}

type renderedSplit struct {
	Feature      renderedSplitFeature       `json:"feature"`
	PullRequests []renderedSplitPullRequest `json:"pull_requests"`
}

// SplitCut counts what was left out of a split call's input.
type SplitCut struct {
	Descriptions, Paths, Summaries int
}

// Total is everything left out.
func (c SplitCut) Total() int { return c.Descriptions + c.Paths + c.Summaries }

// RenderedSplit is the user message of a split call.
type RenderedSplit struct {
	Input string
	// PullRequests is how many pull requests it lists: ref Pi is the
	// (i-1)-th of SplitInput.PullRequests.
	PullRequests int
	Cut          SplitCut
}

// RenderSplit is the user message of a split call: the feature and every
// one of its pull requests, each field truncated to its bound, at most
// maxBytes long. When it is longer, pull request descriptions are left out
// first, earliest first, then their paths, then their summaries, so a large
// feature is still split on what its pull requests changed. A summary is
// rendered only when set and the feature is not withheld, so a version that
// does not UsesFeatureContext renders as before. A feature with more than
// MaxSplitPullRequests pull requests, or one that does not fit with neither,
// is ErrTooLarge: every pull request must be placed, so none is left out.
func RenderSplit(input SplitInput, maxBytes int) (RenderedSplit, error) {
	if len(input.PullRequests) > MaxSplitPullRequests {
		return RenderedSplit{}, ErrTooLarge
	}
	title, description := withheldText(input.Withheld, input.Title, input.Description)
	split := renderedSplit{
		Feature:      renderedSplitFeature{Title: title, Description: description, Withheld: input.Withheld},
		PullRequests: []renderedSplitPullRequest{},
	}
	for i, pull := range input.PullRequests {
		opened := ""
		if !pull.OpenedAt.IsZero() {
			opened = pull.OpenedAt.UTC().Format(time.DateOnly)
		}
		summary := ""
		if !input.Withheld {
			summary = truncate(pull.Summary, maxPullSummaryRunes)
		}
		split.PullRequests = append(split.PullRequests, renderedSplitPullRequest{
			ID: pullRef(i), Number: pull.Number, Title: truncate(pull.Title, maxPullTitleRunes), Summary: summary,
			Body: truncate(pull.Body, maxSplitBodyRunes), State: truncate(pull.State, maxShortRunes), Opened: opened,
			Paths: boundPaths(pull.Paths, maxSplitPaths),
		})
	}
	var cut SplitCut
	size, err := encodedSize(split)
	if err != nil {
		return RenderedSplit{}, err
	}
	for size > maxBytes {
		removed, ok := cutSplit(&split, &cut)
		if !ok {
			return RenderedSplit{}, ErrTooLarge
		}
		if size -= removed; size <= maxBytes {
			// The running count is an estimate; measure again.
			if size, err = encodedSize(split); err != nil {
				return RenderedSplit{}, err
			}
		}
	}
	encoded, err := encode(split)
	if err != nil {
		return RenderedSplit{}, err
	}
	return RenderedSplit{Input: encoded, PullRequests: len(split.PullRequests), Cut: cut}, nil
}

// cutSplit leaves out the next description, else the next pull request's
// paths, else its summary, and returns about how many bytes that saved;
// false when nothing is left to cut.
func cutSplit(split *renderedSplit, cut *SplitCut) (int, bool) {
	for i := range split.PullRequests {
		if pull := &split.PullRequests[i]; pull.Body != "" {
			removed := elementSize(pull.Body) - 3
			pull.Body = ""
			cut.Descriptions++
			return removed, true
		}
	}
	for i := range split.PullRequests {
		if pull := &split.PullRequests[i]; len(pull.Paths) > 0 {
			removed := elementSize(pull.Paths) - 3
			pull.Paths = []string{}
			cut.Paths++
			return removed, true
		}
	}
	for i := range split.PullRequests {
		if pull := &split.PullRequests[i]; pull.Summary != "" {
			removed := elementSize(pull.Summary) + len(`,"summary":`)
			pull.Summary = ""
			cut.Summaries++
			return removed, true
		}
	}
	return 0, false
}

// Part is one deliverable of a validated split reply: its provisional text
// and its pull requests, as sorted indexes into SplitInput.PullRequests.
type Part struct {
	Title, Description string
	PullRequests       []int
}

// Partition is a validated split reply. Keeper is the part that keeps the
// feature's id: the largest, ties to the one with the earliest pull request.
type Partition struct {
	Parts  []Part
	Keeper int
}

type splitReply struct {
	Parts *[]struct {
		Title        *string   `json:"title"`
		Description  *string   `json:"description"`
		PullRequests *[]string `json:"pull_requests"`
	} `json:"parts"`
}

// ParseSplitReply decodes and validates a split reply against the rendered
// input: every pull request of the input is in exactly one part, no part
// names an id outside it or is empty, and each has a title and a
// description within their bounds. Any violation invalidates the whole
// reply.
func ParseSplitReply(data []byte, rendered RenderedSplit) (Partition, error) {
	var r splitReply
	if err := decodeStrict(data, &r); err != nil {
		return Partition{}, err
	}
	if r.Parts == nil || len(*r.Parts) == 0 {
		return Partition{}, fmt.Errorf("%w: there is no part", ErrInvalidReply)
	}
	refs := map[string]int{}
	for i := range rendered.PullRequests {
		refs[pullRef(i)] = i
	}
	placed := map[int]bool{}
	var partition Partition
	for _, part := range *r.Parts {
		if part.Title == nil || part.Description == nil || part.PullRequests == nil {
			return Partition{}, fmt.Errorf("%w: a field is missing", ErrInvalidReply)
		}
		parsed := Part{Title: strings.Join(strings.Fields(clean(*part.Title)), " "), Description: strings.TrimSpace(clean(*part.Description))}
		switch {
		case parsed.Title == "" || parsed.Description == "":
			return Partition{}, fmt.Errorf("%w: a part's title or description is empty", ErrInvalidReply)
		case utf8.RuneCountInString(parsed.Title) > MaxTitleRunes, utf8.RuneCountInString(parsed.Description) > MaxDescriptionRunes:
			return Partition{}, fmt.Errorf("%w: a part's title or description is too long", ErrInvalidReply)
		case len(*part.PullRequests) == 0:
			return Partition{}, fmt.Errorf("%w: a part has no pull request", ErrInvalidReply)
		}
		for _, ref := range *part.PullRequests {
			index, ok := refs[strings.TrimSpace(ref)]
			if !ok {
				return Partition{}, fmt.Errorf("%w: a pull request id is not in the input", ErrInvalidReply)
			}
			if placed[index] {
				return Partition{}, fmt.Errorf("%w: a pull request is in two parts", ErrInvalidReply)
			}
			placed[index] = true
			parsed.PullRequests = append(parsed.PullRequests, index)
		}
		slices.Sort(parsed.PullRequests)
		partition.Parts = append(partition.Parts, parsed)
	}
	if len(placed) != rendered.PullRequests {
		return Partition{}, fmt.Errorf("%w: a pull request is in no part", ErrInvalidReply)
	}
	for i, part := range partition.Parts {
		keeper := partition.Parts[partition.Keeper]
		if len(part.PullRequests) > len(keeper.PullRequests) ||
			(len(part.PullRequests) == len(keeper.PullRequests) && part.PullRequests[0] < keeper.PullRequests[0]) {
			partition.Keeper = i
		}
	}
	return partition, nil
}

// Schema is the JSON Schema of a reply of the kind, valid under OpenAI strict
// structured outputs.
func Schema(kind Kind) map[string]any {
	str := map[string]any{"type": "string"}
	strs := map[string]any{"type": "array", "items": str}
	if kind == Split {
		return map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []any{"parts"},
			"properties": map[string]any{"parts": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "additionalProperties": false,
				"required":   []any{"title", "description", "pull_requests"},
				"properties": orderedObject{{"title", str}, {"description", str}, {"pull_requests", strs}},
			}}},
		}
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []any{"groups"},
		"properties": map[string]any{"groups": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false,
			"required":   []any{"reason", "survivor", "duplicates"},
			"properties": orderedObject{{"reason", str}, {"survivor", str}, {"duplicates", strs}},
		}}},
	}
}

// orderedObject marshals its keys in order, so the model writes a group's
// reason before its choice, and a part's text before its members.
type orderedObject []orderedField

type orderedField struct {
	Key   string
	Value any
}

func (o orderedObject) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, field := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(field.Key)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(field.Value)
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// Request is the call of the kind for a rendered input under prompt version
// version, its reply bounded by maxOutputTokens (MaxOutputTokens for 0, at
// most MaxHostOutputTokens).
func (p Prompts) Request(version string, kind Kind, input string, maxOutputTokens int64) (decisions.ModelRequest, error) {
	versioned, ok := p[version]
	prompt, known := versioned.prompt(kind)
	if !ok || !known {
		return decisions.ModelRequest{}, fmt.Errorf("unknown feature consolidation prompt version %q or kind %q", version, kind)
	}
	switch {
	case maxOutputTokens <= 0:
		maxOutputTokens = MaxOutputTokens
	case maxOutputTokens > MaxHostOutputTokens:
		maxOutputTokens = MaxHostOutputTokens
	}
	return decisions.ModelRequest{
		System: prompt, User: input, SchemaName: schemaNames[kind], Schema: Schema(kind), MaxOutputTokens: maxOutputTokens,
	}, nil
}

// VersionOf returns the prompt version and kind req was built with by
// Request, so a model that sends only the input can name them to the far
// side.
func (p Prompts) VersionOf(req decisions.ModelRequest) (string, Kind, bool) {
	for version, versioned := range p {
		for _, kind := range []Kind{Merge, Split} {
			prompt, _ := versioned.prompt(kind)
			if req.SchemaName == schemaNames[kind] && req.System == prompt {
				return version, kind, true
			}
		}
	}
	return "", "", false
}

func boundPaths(paths []string, limit int) []string {
	bounded := []string{}
	for _, path := range paths[:min(len(paths), limit)] {
		bounded = append(bounded, truncate(path, maxPathRunes))
	}
	return bounded
}

func elementSize(value any) int {
	encoded, err := encode(value)
	if err != nil {
		return 0
	}
	return len(encoded)
}

func encodedSize(value any) (int, error) {
	encoded, err := encode(value)
	return len(encoded), err
}

func encode(value any) (string, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", fmt.Errorf("rendering the consolidation input: %w", err)
	}
	return strings.TrimSuffix(encoded.String(), "\n"), nil
}

func truncate(s string, runes int) string {
	s = clean(s)
	if utf8.RuneCountInString(s) <= runes {
		return s
	}
	return string([]rune(s)[:runes])
}

func decodeStrict(data []byte, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidReply, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data after the JSON value", ErrInvalidReply)
	}
	return nil
}

func clean(s string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "")
}
