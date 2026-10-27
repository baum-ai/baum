// Package assign holds the feature assigner's model call
// (features/README.md, "Assigner"): the input it renders for one pull
// request or session and its candidate features, the reply schema, and the
// validation of a reply; and the batch path, which assigns several pull
// requests in one call under the same rules (batch.go). The host supplies
// its prompt versions (Prompts); Public are the published ones. It makes no
// call and reads no database.
package assign

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/baum-ai/baum/decisions"
)

// Version is a released single-item prompt version. A released prompt is
// never edited; a change is a new version.
type Version struct {
	Prompt string
	// Skips marks a version whose reply may skip a small change, leaving it
	// in no feature.
	Skips bool
	// Lists marks a version whose input shows each candidate's digest, its
	// number of pull requests and a sample of them with their summaries.
	Lists bool
}

// Prompts are a host's released prompt versions, single-item and batch
// (batch.go), by name.
type Prompts struct {
	single map[string]Version
	batch  map[string]string
	// batchOf names each batch version's single-item version.
	batchOf map[string]string
	// seededOf names each batch version's seeded form.
	seededOf map[string]string
}

// KnownPromptVersion reports whether version is a released prompt version.
func (p *Prompts) KnownPromptVersion(version string) bool {
	_, ok := p.single[version]
	return ok
}

// PromptVersions lists the released prompt versions, sorted.
func (p *Prompts) PromptVersions() []string {
	versions := make([]string, 0, len(p.single))
	for version := range p.single {
		versions = append(versions, version)
	}
	slices.Sort(versions)
	return versions
}

// SchemaName names Schema in decisions.ModelRequest.SchemaName.
const SchemaName = "feature_assignment"

// MaxOutputTokens caps a reply, reasoning included; a reply needs a few
// hundred.
const MaxOutputTokens = 4096

// MaxCandidates bounds the number of candidates a host offers one call.
const MaxCandidates = 32

// Bounds of the rendered input, so its size depends on the number of
// candidates and never on the repository's features or an item's size.
const (
	maxItemTitleRunes            = 300
	maxItemBodyRunes             = 4000
	maxItemBranchRunes           = 200
	maxItemPaths                 = 100
	maxDecisions                 = 40
	maxDecisionSummaryRunes      = 400
	maxDecisionFiles             = 10
	maxCandidateTitleRunes       = 200
	maxCandidateDescriptionRunes = 1000
	maxCandidatePaths            = 30
	maxPathRunes                 = 300
	maxCandidatePullRequests     = 12
	maxCandidatePullTitleRunes   = 150
	maxCandidatePullSummaryRunes = 300
	maxCandidateDigestRunes      = 800
)

// Bounds of a new feature's text in a reply; a longer one is invalid.
const (
	MaxNewTitleRunes       = 120
	MaxNewDescriptionRunes = 1000
)

// PullRequest is a pull request to assign, as the model sees it.
type PullRequest struct {
	Title, Body, Branch string
	Paths               []string
}

// Decision is one of a session's current decisions, as the model sees it:
// what was decided and the files it cites, never the session's prompts or
// the decision's quoted evidence.
type Decision struct {
	Category, Summary string
	Files             []string
}

// Session is a root session with no pull request to assign: its decisions
// and the paths they cite.
type Session struct {
	Decisions []Decision
}

// Item is the one pull request or session one call assigns.
type Item struct {
	PullRequest *PullRequest
	Session     *Session
}

// Paths are the item's touched paths: a pull request's, or the files a
// session's decisions cite. Sorted and without duplicates.
func (i Item) Paths() []string {
	var paths []string
	switch {
	case i.PullRequest != nil:
		paths = slices.Clone(i.PullRequest.Paths)
	case i.Session != nil:
		for _, decision := range i.Session.Decisions {
			paths = append(paths, decision.Files...)
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// Candidate is an existing feature the item may join.
type Candidate struct {
	ID                 string
	Title, Description string
	Paths              []string
	// Current marks the feature the item is in now.
	Current bool
	// Withheld is a feature whose text may not be shown: it is rendered
	// with its paths and withheldText as its title, and no description.
	Withheld bool
	// Digest is its digest; empty when withheld.
	Digest string
	// PullRequests are its pull requests, earliest opened first; a listing
	// version shows their number and a sample.
	PullRequests []CandidatePullRequest
	// Links are its pull requests linked to the item pull request, which a
	// listing version shows.
	Links Links
}

// Links are a candidate's pull requests, by number, that the item pull
// request is stacked on or that are stacked on it, that share an agent
// session with its commits, or that its title or description mentions.
type Links struct {
	Stacked, SharedSessions, Mentioned []int32
}

type renderedLinks struct {
	Stacked        []int32 `json:"stacked,omitempty"`
	SharedSessions []int32 `json:"shared_sessions,omitempty"`
	Mentioned      []int32 `json:"mentioned,omitempty"`
}

// CandidatePullRequest is one of a candidate's pull requests: its title and
// the summary its feature text may be written from, empty for none.
type CandidatePullRequest struct {
	Title, Summary string
}

// withheldText stands in for a withheld candidate's title.
const withheldText = "(summary withheld)"

// ref is how the input names candidate i, so the model never copies an id.
func ref(i int) string { return fmt.Sprintf("F%d", i+1) }

type renderedItem struct {
	Kind      string             `json:"kind"`
	Title     string             `json:"title,omitempty"`
	Body      string             `json:"description,omitempty"`
	Branch    string             `json:"branch,omitempty"`
	Decisions []renderedDecision `json:"decisions,omitempty"`
	Paths     []string           `json:"paths"`
}

type renderedDecision struct {
	Category string   `json:"category"`
	Summary  string   `json:"summary"`
	Files    []string `json:"files"`
}

type renderedCandidate struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Paths       []string `json:"paths"`
	Current     bool     `json:"current,omitempty"`
	Withheld    bool     `json:"withheld,omitempty"`
	// Digest, PullRequestCount and PullRequests are a listing version's.
	Digest           string                `json:"digest,omitempty"`
	PullRequestCount *int                  `json:"pull_request_count,omitempty"`
	PullRequests     []renderedCandidatePR `json:"pull_requests,omitempty"`
	Links            *renderedLinks        `json:"links,omitempty"`
}

type renderedCandidatePR struct {
	Title   string `json:"title"`
	Summary string `json:"summary,omitempty"`
}

type renderedInput struct {
	Item       renderedItem        `json:"item"`
	Candidates []renderedCandidate `json:"candidates"`
}

// Render is the user message of one call under prompt version version: the
// item and its candidates as JSON, every field truncated to its bound. A
// candidate's paths list those the item touches first. A listing version
// also shows each candidate's digest, its number of pull requests and at
// most maxCandidatePullRequests of them with their summaries, spread over
// its history and always including its latest. A withheld candidate's
// digest and summaries are never shown.
func (p *Prompts) Render(version string, item Item, candidates []Candidate) (string, error) {
	listing := p.single[version].Lists
	itemPaths := item.Paths()
	input := renderedInput{Candidates: make([]renderedCandidate, len(candidates))}
	switch {
	case item.PullRequest != nil:
		pull := item.PullRequest
		input.Item = renderedItem{
			Kind: "pull_request", Title: truncate(pull.Title, maxItemTitleRunes), Body: truncate(pull.Body, maxItemBodyRunes),
			Branch: truncate(pull.Branch, maxItemBranchRunes), Paths: boundPaths(itemPaths, nil, maxItemPaths),
		}
	case item.Session != nil:
		input.Item = renderedItem{Kind: "session", Paths: boundPaths(itemPaths, nil, maxItemPaths), Decisions: []renderedDecision{}}
		for _, decision := range item.Session.Decisions[:min(len(item.Session.Decisions), maxDecisions)] {
			input.Item.Decisions = append(input.Item.Decisions, renderedDecision{
				Category: truncate(decision.Category, 40), Summary: truncate(decision.Summary, maxDecisionSummaryRunes),
				Files: boundPaths(decision.Files, nil, maxDecisionFiles),
			})
		}
	default:
		return "", errors.New("the item is neither a pull request nor a session")
	}
	for i, candidate := range candidates {
		input.Candidates[i] = renderCandidate(i, candidate, itemPaths, listing)
		if links := candidate.Links; listing && len(links.Stacked)+len(links.SharedSessions)+len(links.Mentioned) > 0 {
			input.Candidates[i].Links = &renderedLinks{
				Stacked: boundNumbers(links.Stacked), SharedSessions: boundNumbers(links.SharedSessions),
				Mentioned: boundNumbers(links.Mentioned),
			}
		}
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(input); err != nil {
		return "", fmt.Errorf("rendering the assignment input: %w", err)
	}
	return strings.TrimSuffix(encoded.String(), "\n"), nil
}

// renderCandidate is candidate i as the input shows it, its paths in first
// listed first; listed, also its digest, its number of pull requests and a
// sample of them with their summaries. A withheld candidate's text, digest
// and summaries are never shown. Links are the caller's.
func renderCandidate(i int, candidate Candidate, first []string, listed bool) renderedCandidate {
	title, description := candidate.Title, candidate.Description
	if candidate.Withheld {
		title, description = withheldText, ""
	}
	rendered := renderedCandidate{
		ID: ref(i), Title: truncate(title, maxCandidateTitleRunes),
		Description: truncate(description, maxCandidateDescriptionRunes),
		Paths:       boundPaths(candidate.Paths, first, maxCandidatePaths), Current: candidate.Current,
		Withheld: candidate.Withheld,
	}
	if !listed {
		return rendered
	}
	count := len(candidate.PullRequests)
	rendered.PullRequestCount = &count
	if !candidate.Withheld {
		rendered.Digest = truncate(candidate.Digest, maxCandidateDigestRunes)
	}
	for _, pull := range samplePullRequests(candidate.PullRequests, maxCandidatePullRequests) {
		sampled := renderedCandidatePR{Title: truncate(pull.Title, maxCandidatePullTitleRunes)}
		if !candidate.Withheld {
			sampled.Summary = truncate(pull.Summary, maxCandidatePullSummaryRunes)
		}
		rendered.PullRequests = append(rendered.PullRequests, sampled)
	}
	return rendered
}

// samplePullRequests is at most limit of pulls, evenly spread from the
// earliest to the latest, which is always included.
func samplePullRequests(pulls []CandidatePullRequest, limit int) []CandidatePullRequest {
	sample := make([]CandidatePullRequest, 0, min(len(pulls), limit))
	for _, i := range sampleIndexes(len(pulls), limit) {
		sample = append(sample, pulls[i])
	}
	return sample
}

func sampleIndexes(n, limit int) []int {
	indexes := make([]int, 0, min(n, limit))
	for i := range min(n, limit) {
		if n <= limit {
			indexes = append(indexes, i)
		} else {
			indexes = append(indexes, i*(n-1)/(limit-1))
		}
	}
	return indexes
}

// SampledPullRequests are the indexes, among a candidate's n pull requests,
// of those a listing version shows with their summaries, so a host reads
// only those summaries.
func SampledPullRequests(n int) []int { return sampleIndexes(n, maxCandidatePullRequests) }

// maxLinkNumbers bounds each kind of a candidate's links.
const maxLinkNumbers = 10

func boundNumbers(numbers []int32) []int32 { return numbers[:min(len(numbers), maxLinkNumbers)] }

var mention = regexp.MustCompile(`(?:^|[^\w&/])#([0-9]{1,9})\b`)

// MentionedPullRequests are the distinct pull request numbers a pull
// request's title or description mentions as #N, at most maxMentions, in
// order of appearance.
func MentionedPullRequests(title, body string) []int32 {
	const maxMentions = 50
	var numbers []int32
	for _, match := range mention.FindAllStringSubmatch(title+"\n"+truncate(body, maxItemBodyRunes), -1) {
		number, err := strconv.ParseInt(match[1], 10, 32)
		if err != nil || number <= 0 || slices.Contains(numbers, int32(number)) {
			continue
		}
		if numbers = append(numbers, int32(number)); len(numbers) == maxMentions {
			break
		}
	}
	return numbers
}

// ListsCandidates reports whether version's input shows each candidate's
// digest and pull requests, which the caller must then supply.
func (p *Prompts) ListsCandidates(version string) bool { return p.single[version].Lists }

// MaxInputBytes bounds the size of Render's output for candidates
// candidates under any version: every field at its bound, each rune escaped
// to six bytes, plus the JSON framing.
func MaxInputBytes(candidates int) int {
	const framing = 64
	pathBytes := 6*maxPathRunes + framing
	item := 6*(maxItemTitleRunes+maxItemBodyRunes+maxItemBranchRunes) + maxItemPaths*pathBytes +
		maxDecisions*(6*(40+maxDecisionSummaryRunes)+maxDecisionFiles*pathBytes+framing)
	candidate := 6*(maxCandidateTitleRunes+maxCandidateDescriptionRunes+maxCandidateDigestRunes) + maxCandidatePaths*pathBytes +
		maxCandidatePullRequests*(6*(maxCandidatePullTitleRunes+maxCandidatePullSummaryRunes)+framing) +
		3*maxLinkNumbers*11 + 2*framing
	return item + candidates*candidate + 4*framing
}

func truncate(s string, runes int) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "")
	if utf8.RuneCountInString(s) <= runes {
		return s
	}
	return string([]rune(s)[:runes])
}

// boundPaths is at most limit of paths, each truncated, those in first
// listed first, else in order.
func boundPaths(paths, first []string, limit int) []string {
	ordered := make([]string, 0, len(paths))
	for _, path := range paths {
		if slices.Contains(first, path) {
			ordered = append(ordered, path)
		}
	}
	for _, path := range paths {
		if !slices.Contains(first, path) {
			ordered = append(ordered, path)
		}
	}
	ordered = ordered[:min(len(ordered), limit)]
	for i, path := range ordered {
		ordered[i] = truncate(path, maxPathRunes)
	}
	return ordered
}

// maxSearchWords bounds the words SearchQuery ORs together.
const maxSearchWords = 32

// SearchQuery is the full-text query candidates are ranked by: the distinct
// words of at least three letters or digits of a pull request's title, the
// start of its description and its branch, or of a session's decision
// summaries, lower-cased and OR-ed in to_tsquery syntax, at most
// maxSearchWords of them in order of appearance. Only letters and digits
// reach it, so it always parses. Empty when there is no such word.
func SearchQuery(item Item) string {
	var texts []string
	switch {
	case item.PullRequest != nil:
		texts = []string{item.PullRequest.Title, truncate(item.PullRequest.Body, 1000), item.PullRequest.Branch}
	case item.Session != nil:
		for _, decision := range item.Session.Decisions {
			texts = append(texts, decision.Summary)
		}
	}
	var words []string
	for _, text := range texts {
		for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < '0' || r > '9')
		}) {
			if len(word) >= 3 && len(word) <= 40 && !slices.Contains(words, word) && len(words) < maxSearchWords {
				words = append(words, word)
			}
		}
	}
	return strings.Join(words, " | ")
}

// Schema is the JSON Schema of a reply under prompt version version, valid
// under OpenAI strict structured outputs: every field required, "" where a
// field does not apply. Only a skipping version offers "skip".
func (p *Prompts) Schema(version string) map[string]any {
	str := map[string]any{"type": "string"}
	choices := []any{"existing", "new"}
	if p.single[version].Skips {
		choices = append(choices, "skip")
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"reason", "choice", "feature_id", "title", "description"},
		"properties": orderedObject{
			{"reason", str},
			{"choice", map[string]any{"type": "string", "enum": choices}},
			{"feature_id", str},
			{"title", str},
			{"description", str},
		},
	}
}

// orderedObject marshals its keys in order, so the model writes the reason
// before its choice (decisions.ExtractSchema does the same).
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

// Request is the call for a rendered input under prompt version version, a
// single-item or a batch version.
func (p *Prompts) Request(version, input string, maxOutputTokens int64) (decisions.ModelRequest, error) {
	if prompt, ok := p.batch[version]; ok {
		if maxOutputTokens <= 0 || maxOutputTokens > MaxBatchOutputTokens {
			maxOutputTokens = MaxBatchOutputTokens
		}
		return decisions.ModelRequest{
			System: prompt, User: input, SchemaName: BatchSchemaName, Schema: BatchSchema(), MaxOutputTokens: maxOutputTokens,
		}, nil
	}
	single, ok := p.single[version]
	if !ok {
		return decisions.ModelRequest{}, fmt.Errorf("unknown feature assignment prompt version %q", version)
	}
	if maxOutputTokens <= 0 || maxOutputTokens > MaxOutputTokens {
		maxOutputTokens = MaxOutputTokens
	}
	return decisions.ModelRequest{
		System: single.Prompt, User: input, SchemaName: SchemaName, Schema: p.Schema(version), MaxOutputTokens: maxOutputTokens,
	}, nil
}

// VersionOf returns the prompt version, single-item or batch, req was built
// with by Request, so a model that sends only the input can name it to the
// far side.
func (p *Prompts) VersionOf(req decisions.ModelRequest) (string, bool) {
	switch req.SchemaName {
	case SchemaName:
		for version, single := range p.single {
			if req.System == single.Prompt {
				return version, true
			}
		}
	case BatchSchemaName:
		for version, prompt := range p.batch {
			if req.System == prompt {
				return version, true
			}
		}
	}
	return "", false
}

// Limits are the bounds of a call under a version: its rendered input and
// its reply.
type Limits struct {
	MaxInputBytes   int
	MaxOutputTokens int64
}

// LimitsOf are the bounds of a call under version, single-item or batch;
// false for an unknown version.
func (p *Prompts) LimitsOf(version string) (Limits, bool) {
	if p.KnownBatchPromptVersion(version) {
		return Limits{MaxInputBytes: MaxBatchInputBytes, MaxOutputTokens: MaxBatchOutputTokens}, true
	}
	if p.KnownPromptVersion(version) {
		return Limits{MaxInputBytes: MaxInputBytes(MaxCandidates), MaxOutputTokens: MaxOutputTokens}, true
	}
	return Limits{}, false
}

// Choice is a validated reply: a candidate's id, a new feature, or a
// small change to leave in no feature.
type Choice struct {
	// FeatureID is the chosen candidate's id; empty for a new feature or a
	// skip.
	FeatureID string
	New       *NewFeature
	Skip      bool
	Reason    string
}

// NewFeature is the provisional text of a feature the reply starts.
type NewFeature struct {
	Title, Description string
}

// ErrInvalidReply is a reply that does not match the schema, names no
// candidate, or starts a feature without a usable title or description.
var ErrInvalidReply = errors.New("invalid feature assignment reply")

type reply struct {
	Reason      *string `json:"reason"`
	Choice      *string `json:"choice"`
	FeatureID   *string `json:"feature_id"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

// ParseReply decodes and validates a reply under prompt version version
// against the candidates the input listed. Nothing outside them is ever
// chosen, and only a skipping version skips.
func (p *Prompts) ParseReply(version string, data []byte, candidates []Candidate) (Choice, error) {
	var r reply
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return Choice{}, fmt.Errorf("%w: %v", ErrInvalidReply, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Choice{}, fmt.Errorf("%w: trailing data after the JSON value", ErrInvalidReply)
	}
	if r.Reason == nil || r.Choice == nil || r.FeatureID == nil || r.Title == nil || r.Description == nil {
		return Choice{}, fmt.Errorf("%w: a field is missing", ErrInvalidReply)
	}
	reason := truncate(strings.TrimSpace(*r.Reason), 500)
	switch *r.Choice {
	case "existing":
		id := strings.TrimSpace(*r.FeatureID)
		for i, candidate := range candidates {
			if id == ref(i) {
				return Choice{FeatureID: candidate.ID, Reason: reason}, nil
			}
		}
		return Choice{}, fmt.Errorf("%w: the chosen feature is not a candidate", ErrInvalidReply)
	case "new":
		text, err := newFeature(*r.Title, *r.Description)
		if err != nil {
			return Choice{}, err
		}
		return Choice{New: &text, Reason: reason}, nil
	case "skip":
		if !p.single[version].Skips {
			return Choice{}, fmt.Errorf("%w: %s does not skip", ErrInvalidReply, version)
		}
		return Choice{Skip: true, Reason: reason}, nil
	default:
		return Choice{}, fmt.Errorf("%w: unknown choice", ErrInvalidReply)
	}
}
