// Package summary holds the feature summarizer's model calls
// (features/README.md, "Summarizer"): the input rendered for one feature and
// for one pull request, the reply schemas, and the validation of a reply.
// The host supplies its prompt versions (Prompts); Public are the published
// ones. It makes no call and reads no database.
package summary

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/baum-ai/baum/decisions"
)

// Version is a released prompt version: its feature and pull request
// prompt. A released prompt is never edited; a change is a new version.
type Version struct {
	Feature, PullRequest string
	// PullRequestVersion names the version whose pull request prompt this
	// one reuses, so a generated pull request summary is hashed, and its
	// request named, under the version that owns the prompt and stays
	// reusable. Empty when it owns its own.
	PullRequestVersion string
	// CurrentText marks a version whose feature call also reads the
	// feature's current title and description (WithCurrent), so its title
	// stays stable.
	CurrentText bool
}

// Prompts are a host's released prompt versions, by name.
type Prompts map[string]Version

func (v Version) prompt(kind Kind) (string, bool) {
	switch kind {
	case Feature:
		return v.Feature, true
	case PullRequest:
		return v.PullRequest, true
	}
	return "", false
}

// PullRequestVersion is the version whose pull request prompt version uses.
func (p Prompts) PullRequestVersion(version string) string {
	if owner := p[version].PullRequestVersion; owner != "" {
		return owner
	}
	return version
}

// UsesCurrentText reports whether version's feature call reads the
// feature's current text.
func (p Prompts) UsesCurrentText(version string) bool { return p[version].CurrentText }

// Kind is which of a version's two calls a request is.
type Kind string

const (
	// Feature is a feature's text: title, description, digest and key
	// decisions.
	Feature Kind = "feature"
	// PullRequest is the summary of a pull request whose review has none.
	PullRequest Kind = "pull_request"
)

// schemaNames name each kind's Schema in decisions.ModelRequest.
var schemaNames = map[Kind]string{Feature: "feature_summary", PullRequest: "pull_request_summary"}

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

// MaxOutputTokens caps a reply, reasoning included.
const MaxOutputTokens = 8192

// Bounds of a feature's rendered input in bytes, a host's setting.
const (
	DefaultMaxInputBytes = 200_000
	MinInputBytes        = 16 << 10
	MaxInputBytes        = 1 << 20
)

// Bounds of each rendered field, so no one field crowds the rest out.
const (
	maxTitleRunes          = 300
	maxBodyRunes           = 4000
	maxSummaryRunes        = 2000
	maxFindings            = 20
	maxFindingRunes        = 600
	maxReviewDecisions     = 30
	maxDecisionTextRunes   = 600
	maxRationaleRunes      = 800
	maxDecisionFiles       = 10
	maxPaths               = 300
	maxPathRunes           = 300
	maxShortRunes          = 40
	maxPullRequestDecision = 40
)

// Bounds of a reply's text; a longer one is invalid.
const (
	MaxTitleRunes       = 120
	MaxDescriptionRunes = 1000
	MaxDigestRunes      = 3000
	MaxSummaryRunes     = 1500
	// MaxKeyDecisions is how many key decisions are kept.
	MaxKeyDecisions = 3
)

// PullRequestInfo is one of a feature's pull requests, as the model sees it.
type PullRequestInfo struct {
	Number      int32
	State       string
	Title, Body string
	// Summary is its review's summary, or the one generated for it.
	Summary string
	// Review is its current review; nil when it has none.
	Review *Review
}

// Review is what a feature's text takes from a pull request's review.
type Review struct {
	// Findings are its attention notes.
	Findings  []string
	Decisions []ReviewDecision
}

// ReviewDecision is a decision a review cites.
type ReviewDecision struct {
	By, Text, Rejected string
}

// Decision is a current decision attributed to the feature: what was decided
// and why, never a session's prompts or the decision's quoted evidence.
type Decision struct {
	Summary, Category, Who, Trigger, Rationale, RejectedAlternative string
	Superseded                                                      bool
	Files                                                           []string
}

// FeatureInput is everything one feature's call is written from. Pull
// requests and decisions are earliest first.
type FeatureInput struct {
	PullRequests []PullRequestInfo
	Decisions    []Decision
	Paths        []string
}

type renderedPullRequest struct {
	Number  int32           `json:"number"`
	State   string          `json:"state"`
	Title   string          `json:"title"`
	Body    string          `json:"description"`
	Summary string          `json:"summary"`
	Review  *renderedReview `json:"review,omitempty"`
}

type renderedReview struct {
	Findings  []string                 `json:"findings"`
	Decisions []renderedReviewDecision `json:"decisions"`
}

type renderedReviewDecision struct {
	By       string `json:"by"`
	Text     string `json:"text"`
	Rejected string `json:"rejected,omitempty"`
	index    int
}

type renderedDecision struct {
	ID                  string   `json:"id,omitempty"`
	Summary             string   `json:"summary"`
	Category            string   `json:"category"`
	Who                 string   `json:"who"`
	Trigger             string   `json:"trigger,omitempty"`
	Rationale           string   `json:"rationale,omitempty"`
	RejectedAlternative string   `json:"rejected_alternative,omitempty"`
	Superseded          bool     `json:"superseded,omitempty"`
	Files               []string `json:"files"`
	index               int
}

type renderedFeature struct {
	PullRequests []renderedPullRequest `json:"pull_requests"`
	Decisions    []renderedDecision    `json:"decisions"`
	Paths        []string              `json:"paths"`
}

// Cut counts what was left out of a feature's input to keep it within its
// bound, in the order it is left out.
type Cut struct {
	SupersededDecisions     int
	ReviewFindings          int
	ReviewDecisions         int
	PullRequestDescriptions int
	Decisions               int
	Paths                   int
	PullRequests            int
}

// Total is everything left out.
func (c Cut) Total() int {
	return c.SupersededDecisions + c.ReviewFindings + c.ReviewDecisions + c.PullRequestDescriptions +
		c.Decisions + c.Paths + c.PullRequests
}

// RenderedFeature is the user message of a feature's call.
type RenderedFeature struct {
	Input string
	// Current maps the id of each rendered decision that is not superseded
	// to its index in FeatureInput.Decisions; only these can be key
	// decisions.
	Current map[string]int
	// Rendered lists the indexes in FeatureInput.Decisions of every
	// rendered decision.
	Rendered []int
	// PullRequests lists the indexes in FeatureInput.PullRequests of every
	// rendered pull request.
	PullRequests []int
	// ReviewDecisions lists, for each pull request in FeatureInput, the
	// indexes in its Review.Decisions of those rendered.
	ReviewDecisions [][]int
	Cut             Cut
}

// ErrTooLarge is a feature whose input cannot be cut down to its bound.
var ErrTooLarge = errors.New("the feature's input does not fit its bound")

// RenderFeature is the user message of a feature's call: the input as JSON,
// every field truncated to its bound, and at most maxBytes long. When it is
// longer, superseded decisions are left out first, earliest first; then
// review findings and then review decisions, of the earliest pull request
// first; then pull request descriptions, earliest first; then current
// decisions, earliest first; then paths; then pull requests, earliest first.
func RenderFeature(input FeatureInput, maxBytes int) (RenderedFeature, error) {
	feature := renderedFeature{PullRequests: []renderedPullRequest{}, Decisions: []renderedDecision{}, Paths: []string{}}
	pullIndex := []int{}
	for i, pull := range input.PullRequests {
		rendered := renderedPullRequest{
			Number: pull.Number, State: truncate(pull.State, maxShortRunes), Title: truncate(pull.Title, maxTitleRunes),
			Body: truncate(pull.Body, maxBodyRunes), Summary: truncate(pull.Summary, maxSummaryRunes),
		}
		if pull.Review != nil {
			review := &renderedReview{Findings: []string{}, Decisions: []renderedReviewDecision{}}
			for _, finding := range pull.Review.Findings {
				if finding = strings.TrimSpace(finding); finding != "" && len(review.Findings) < maxFindings {
					review.Findings = append(review.Findings, truncate(finding, maxFindingRunes))
				}
			}
			for j, decision := range pull.Review.Decisions {
				if strings.TrimSpace(decision.Text) != "" && len(review.Decisions) < maxReviewDecisions {
					review.Decisions = append(review.Decisions, renderedReviewDecision{
						By: truncate(decision.By, maxShortRunes), Text: truncate(decision.Text, maxDecisionTextRunes),
						Rejected: truncate(decision.Rejected, maxDecisionTextRunes), index: j,
					})
				}
			}
			rendered.Review = review
		}
		feature.PullRequests = append(feature.PullRequests, rendered)
		pullIndex = append(pullIndex, i)
	}
	for i, decision := range input.Decisions {
		rendered := renderDecision(decision)
		rendered.ID, rendered.Superseded, rendered.index = fmt.Sprintf("D%d", i+1), decision.Superseded, i
		feature.Decisions = append(feature.Decisions, rendered)
	}
	for _, path := range input.Paths[:min(len(input.Paths), maxPaths)] {
		feature.Paths = append(feature.Paths, truncate(path, maxPathRunes))
	}

	var cut Cut
	size, err := encodedSize(feature)
	if err != nil {
		return RenderedFeature{}, err
	}
	for size > maxBytes {
		removed, ok := cutOne(&feature, &pullIndex, &cut)
		if !ok {
			return RenderedFeature{}, ErrTooLarge
		}
		if size -= removed; size <= maxBytes {
			// The running count is an estimate; measure again.
			if size, err = encodedSize(feature); err != nil {
				return RenderedFeature{}, err
			}
		}
	}
	encoded, err := encode(feature)
	if err != nil {
		return RenderedFeature{}, err
	}
	result := RenderedFeature{Input: encoded, Current: map[string]int{}, PullRequests: pullIndex, Cut: cut,
		ReviewDecisions: make([][]int, len(input.PullRequests))}
	for i, pull := range feature.PullRequests {
		if pull.Review == nil {
			continue
		}
		for _, decision := range pull.Review.Decisions {
			result.ReviewDecisions[pullIndex[i]] = append(result.ReviewDecisions[pullIndex[i]], decision.index)
		}
	}
	for _, decision := range feature.Decisions {
		result.Rendered = append(result.Rendered, decision.index)
		if !decision.Superseded {
			result.Current[decision.ID] = decision.index
		}
	}
	return result, nil
}

func renderDecision(decision Decision) renderedDecision {
	files := []string{}
	for _, file := range decision.Files[:min(len(decision.Files), maxDecisionFiles)] {
		files = append(files, truncate(file, maxPathRunes))
	}
	return renderedDecision{
		Summary: truncate(decision.Summary, maxDecisionTextRunes), Category: truncate(decision.Category, maxShortRunes),
		Who: truncate(decision.Who, maxShortRunes), Trigger: truncate(decision.Trigger, maxShortRunes),
		Rationale:           truncate(decision.Rationale, maxRationaleRunes),
		RejectedAlternative: truncate(decision.RejectedAlternative, maxDecisionTextRunes), Files: files,
	}
}

// cutOne leaves out the next element in cut order and returns about how many
// bytes that saved; false when nothing is left to cut.
func cutOne(feature *renderedFeature, pullIndex *[]int, cut *Cut) (int, bool) {
	for i, decision := range feature.Decisions {
		if decision.Superseded {
			feature.Decisions = slices.Delete(feature.Decisions, i, i+1)
			cut.SupersededDecisions++
			return elementSize(decision), true
		}
	}
	for _, pull := range feature.PullRequests {
		if pull.Review != nil && len(pull.Review.Findings) > 0 {
			finding := pull.Review.Findings[0]
			pull.Review.Findings = pull.Review.Findings[1:]
			cut.ReviewFindings++
			return elementSize(finding), true
		}
	}
	for _, pull := range feature.PullRequests {
		if pull.Review != nil && len(pull.Review.Decisions) > 0 {
			decision := pull.Review.Decisions[0]
			pull.Review.Decisions = pull.Review.Decisions[1:]
			cut.ReviewDecisions++
			return elementSize(decision), true
		}
	}
	for i := range feature.PullRequests {
		if pull := &feature.PullRequests[i]; pull.Body != "" {
			removed := elementSize(pull.Body) - 2
			pull.Body = ""
			cut.PullRequestDescriptions++
			return removed, true
		}
	}
	if len(feature.Decisions) > 0 {
		decision := feature.Decisions[0]
		feature.Decisions = feature.Decisions[1:]
		cut.Decisions++
		return elementSize(decision), true
	}
	if n := len(feature.Paths); n > 0 {
		path := feature.Paths[n-1]
		feature.Paths = feature.Paths[:n-1]
		cut.Paths++
		return elementSize(path), true
	}
	if len(feature.PullRequests) > 0 {
		pull := feature.PullRequests[0]
		feature.PullRequests = feature.PullRequests[1:]
		*pullIndex = (*pullIndex)[1:]
		cut.PullRequests++
		return elementSize(pull), true
	}
	return 0, false
}

func elementSize(value any) int {
	encoded, err := encode(value)
	if err != nil {
		return 0
	}
	return len(encoded) + 1
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
		return "", fmt.Errorf("rendering the summary input: %w", err)
	}
	return strings.TrimSuffix(encoded.String(), "\n"), nil
}

// PullRequestInput is what a pull request's generated summary is written
// from: its title, description and the current decisions attributed to it.
type PullRequestInput struct {
	Title, Body string
	Decisions   []Decision
}

type renderedPullRequestInput struct {
	Title     string             `json:"title"`
	Body      string             `json:"description"`
	Decisions []renderedDecision `json:"decisions"`
}

// RenderPullRequest is the user message of a pull request's call, every
// field truncated to its bound, superseded decisions left out, at most
// maxPullRequestDecision decisions. Its size depends on no input's. It also
// returns the indexes in input.Decisions of the decisions rendered.
func RenderPullRequest(input PullRequestInput) (string, []int, error) {
	rendered := renderedPullRequestInput{
		Title: truncate(input.Title, maxTitleRunes), Body: truncate(input.Body, maxBodyRunes), Decisions: []renderedDecision{},
	}
	var indexes []int
	for i, decision := range input.Decisions {
		if !decision.Superseded && len(rendered.Decisions) < maxPullRequestDecision {
			rendered.Decisions = append(rendered.Decisions, renderDecision(decision))
			indexes = append(indexes, i)
		}
	}
	encoded, err := encode(rendered)
	return encoded, indexes, err
}

// MaxPullRequestInputBytes bounds RenderPullRequest's output: every field at
// its bound, each rune escaped to six bytes, plus the JSON framing.
func MaxPullRequestInputBytes() int {
	const framing = 64
	decision := 6*(maxDecisionTextRunes*2+maxRationaleRunes+3*maxShortRunes) +
		maxDecisionFiles*(6*maxPathRunes+framing) + 4*framing
	return 6*(maxTitleRunes+maxBodyRunes) + maxPullRequestDecision*decision + 4*framing
}

// ContentHash identifies a pull request's rendered input under a prompt
// version: an unchanged hash means an unchanged summary.
func ContentHash(version, input string) string {
	sum := sha256.Sum256([]byte(version + "\x00" + input))
	return hex.EncodeToString(sum[:])
}

func truncate(s string, runes int) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "")
	if utf8.RuneCountInString(s) <= runes {
		return s
	}
	return string([]rune(s)[:runes])
}

// Schema is the JSON Schema of a reply of the kind, valid under OpenAI strict
// structured outputs.
func Schema(kind Kind) map[string]any {
	str := map[string]any{"type": "string"}
	if kind == PullRequest {
		return map[string]any{
			"type": "object", "additionalProperties": false,
			"required":   []any{"summary"},
			"properties": map[string]any{"summary": str},
		}
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []any{"title", "description", "digest", "key_decisions"},
		"properties": orderedObject{
			{"title", str},
			{"description", str},
			{"digest", str},
			{"key_decisions", map[string]any{"type": "array", "items": str}},
		},
	}
}

// orderedObject marshals its keys in order, so the model writes the title
// and description before the digest and its choice of key decisions.
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
// version.
func (p Prompts) Request(version string, kind Kind, input string, maxOutputTokens int64) (decisions.ModelRequest, error) {
	versioned, ok := p[version]
	prompt, known := versioned.prompt(kind)
	if !ok || !known {
		return decisions.ModelRequest{}, fmt.Errorf("unknown feature summary prompt version %q or kind %q", version, kind)
	}
	if maxOutputTokens <= 0 || maxOutputTokens > MaxOutputTokens {
		maxOutputTokens = MaxOutputTokens
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
		for _, kind := range []Kind{Feature, PullRequest} {
			prompt, _ := versioned.prompt(kind)
			if kind == PullRequest && p.PullRequestVersion(version) != version {
				// A reused prompt is named by the version that owns it.
				continue
			}
			if req.SchemaName == schemaNames[kind] && req.System == prompt {
				return version, kind, true
			}
		}
	}
	return "", "", false
}

// maxRepositoryRunes bounds the repository name WithRepository adds.
const maxRepositoryRunes = 200

// MaxCurrentTextBytes bounds what WithRepository and WithCurrent add to an
// input: every field at its bound, each rune escaped to six bytes, plus the
// framing. A version that UsesCurrentText renders its input this much
// smaller.
const MaxCurrentTextBytes = 6*(MaxTitleRunes+MaxDescriptionRunes+maxRepositoryRunes) + 96

// WithRepository adds the name of the feature's repository to a rendered
// feature input as a field, "repository", for a version that
// UsesCurrentText, so its text can place the work in its product.
func WithRepository(input, name string) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &fields); err != nil || !strings.HasSuffix(input, "}") {
		return "", fmt.Errorf("adding the repository to an input that is not a JSON object: %v", err)
	}
	if _, ok := fields["repository"]; ok {
		return "", errors.New("the input already has the repository")
	}
	encoded, err := encode(truncate(name, maxRepositoryRunes))
	if err != nil {
		return "", err
	}
	if len(fields) == 0 {
		return `{"repository":` + encoded + "}", nil
	}
	return strings.TrimSuffix(input, "}") + `,"repository":` + encoded + "}", nil
}

// WithCurrent adds the feature's current title and description to a
// rendered feature input as its last field, "current", for a version that
// UsesCurrentText. They are truncated to a reply's bounds, so the input grows
// by a bounded amount.
func WithCurrent(input, title, description string) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &fields); err != nil || !strings.HasSuffix(input, "}") {
		return "", fmt.Errorf("adding the current text to an input that is not a JSON object: %v", err)
	}
	if _, ok := fields["current"]; ok {
		return "", errors.New("the input already has the current text")
	}
	current, err := encode(struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}{truncate(title, MaxTitleRunes), truncate(description, MaxDescriptionRunes)})
	if err != nil {
		return "", err
	}
	if len(fields) == 0 {
		return `{"current":` + current + "}", nil
	}
	return strings.TrimSuffix(input, "}") + `,"current":` + current + "}", nil
}

// ErrInvalidReply is a reply that does not match its schema or lacks the
// text it must have.
var ErrInvalidReply = errors.New("invalid feature summary reply")

// Text is a validated reply to a feature's call.
type Text struct {
	Title, Description, Digest string
	// KeyDecisions are indexes in FeatureInput.Decisions, at most
	// MaxKeyDecisions, each a rendered, current decision.
	KeyDecisions []int
	// DroppedKeyDecisions counts the ids the reply named that are not a
	// rendered, current decision, or past MaxKeyDecisions.
	DroppedKeyDecisions int
}

type featureReply struct {
	Title        *string   `json:"title"`
	Description  *string   `json:"description"`
	Digest       *string   `json:"digest"`
	KeyDecisions *[]string `json:"key_decisions"`
}

// ParseFeatureReply decodes and validates a reply against the rendered
// input. A key decision that is not one of its current decisions is dropped.
func ParseFeatureReply(data []byte, rendered RenderedFeature) (Text, error) {
	var r featureReply
	if err := decodeStrict(data, &r); err != nil {
		return Text{}, err
	}
	if r.Title == nil || r.Description == nil || r.Digest == nil || r.KeyDecisions == nil {
		return Text{}, fmt.Errorf("%w: a field is missing", ErrInvalidReply)
	}
	text := Text{
		Title:       strings.Join(strings.Fields(clean(*r.Title)), " "),
		Description: strings.TrimSpace(clean(*r.Description)),
		Digest:      strings.TrimSpace(clean(*r.Digest)),
	}
	switch {
	case text.Title == "" || text.Description == "":
		return Text{}, fmt.Errorf("%w: the title or description is empty", ErrInvalidReply)
	case utf8.RuneCountInString(text.Title) > MaxTitleRunes, utf8.RuneCountInString(text.Description) > MaxDescriptionRunes,
		utf8.RuneCountInString(text.Digest) > MaxDigestRunes:
		return Text{}, fmt.Errorf("%w: the title, description or digest is too long", ErrInvalidReply)
	}
	for _, id := range *r.KeyDecisions {
		index, ok := rendered.Current[strings.TrimSpace(id)]
		if !ok || slices.Contains(text.KeyDecisions, index) || len(text.KeyDecisions) == MaxKeyDecisions {
			text.DroppedKeyDecisions++
			continue
		}
		text.KeyDecisions = append(text.KeyDecisions, index)
	}
	return text, nil
}

// ParsePullRequestReply decodes and validates a reply to a pull request's
// call and returns its summary.
func ParsePullRequestReply(data []byte) (string, error) {
	var r struct {
		Summary *string `json:"summary"`
	}
	if err := decodeStrict(data, &r); err != nil {
		return "", err
	}
	if r.Summary == nil {
		return "", fmt.Errorf("%w: the summary is missing", ErrInvalidReply)
	}
	summary := strings.TrimSpace(clean(*r.Summary))
	if summary == "" || utf8.RuneCountInString(summary) > MaxSummaryRunes {
		return "", fmt.Errorf("%w: the summary is empty or too long", ErrInvalidReply)
	}
	return summary, nil
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
