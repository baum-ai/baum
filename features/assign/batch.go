package assign

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

// The batch path (features/README.md, "Batch"): one call assigns several pull
// requests at once, under the grouping rules of a released single-item
// prompt. A batch version's prompt is that prompt's
// definition of a feature and its "# Choosing" rules, cut out of it
// unchanged, in a frame that describes the batch input and reply, so the
// rules are written, and tuned, once. Each version has its own frame, whose
// fields describe a new feature as its single-item prompt does; a released
// frame, like a released prompt, is never edited.

// Batch is a released batch version: the single-item version whose rules it
// applies, the frame they are set in, and its seeded form.
type Batch struct {
	Single, Frame string
	// Seeded names the version a rebuild uses for a call that offers seeded
	// features (features/README.md, "Batch"): the same prompt with SeededNote after
	// the description of the candidates. Empty for none.
	Seeded, SeededNote string
}

// SeededBatch is the version a batch call offering seeded features uses:
// batch's seeded form, or batch itself when it has none.
func (p *Prompts) SeededBatch(batch string) string {
	if seeded, ok := p.seededOf[batch]; ok {
		return seeded
	}
	return batch
}

// CandidatesEnd ends a batch frame's description of the candidates; a
// seeded note follows it.
const CandidatesEnd = "There may be no candidates."

// NewPrompts composes the batch versions' prompts from the single-item
// versions and the frames. It panics when a batch version names an unknown
// single-item version or its prompt or frame cannot be composed, so a host
// fails at start-up.
func NewPrompts(single map[string]Version, batches map[string]Batch) *Prompts {
	p := &Prompts{single: single, batch: map[string]string{}, batchOf: map[string]string{}, seededOf: map[string]string{}}
	for batch, source := range batches {
		version, ok := single[source.Single]
		if !ok {
			panic(fmt.Sprintf("%s applies unknown version %s", batch, source.Single))
		}
		prompt, err := composeBatchPrompt(version.Prompt, source.Frame)
		if err != nil {
			panic(fmt.Sprintf("composing %s from %s: %v", batch, source.Single, err))
		}
		p.batch[batch], p.batchOf[batch] = prompt, source.Single
		if source.Seeded == "" {
			continue
		}
		if !strings.Contains(prompt, CandidatesEnd) {
			panic(batch + " does not end its description of the candidates as expected")
		}
		p.batch[source.Seeded] = strings.Replace(prompt, CandidatesEnd, CandidatesEnd+source.SeededNote, 1)
		p.seededOf[batch] = source.Seeded
	}
	return p
}

// Where a single-item prompt's definition and rules are cut: the definition
// follows its opening sentence and ends before its reply instruction; the
// rules run from "# Choosing" to the paragraph on sessions, which a batch of
// pull requests does not need.
const (
	singleOpening = "You assign one item of work in a software repository to a feature. "
	replyLine     = " Reply with JSON matching the given schema."
	rulesStart    = "# Choosing"
	rulesEnd      = "A session with no pull request can only join"
)

// composeBatchPrompt is frame with single's definition and rules.
func composeBatchPrompt(single, frame string) (string, error) {
	definition, rest, err := CutDefinition(single)
	if err != nil {
		return "", err
	}
	start := strings.Index(rest, rulesStart)
	end := strings.Index(rest, rulesEnd)
	if start < 0 || end < start {
		return "", errors.New("the prompt has no rules section before its paragraph on sessions")
	}
	rules := strings.TrimSpace(rest[start:end])
	return strings.NewReplacer("{{definition}}", definition, "{{rules}}", rules).Replace(frame), nil
}

// CutDefinition is a single-item prompt's definition of a feature and the
// rest of the prompt after its reply instruction.
func CutDefinition(single string) (definition, rest string, err error) {
	definition, rest, ok := strings.Cut(strings.TrimPrefix(single, singleOpening), replyLine)
	if !ok || !strings.HasPrefix(single, singleOpening) {
		return "", "", errors.New("the prompt has no definition between its opening and its reply instruction")
	}
	return definition, rest, nil
}

// BatchVersion is the batch version of single-item version version, if it
// has one.
func (p *Prompts) BatchVersion(version string) (string, bool) {
	for batch, single := range p.batchOf {
		if single == version {
			return batch, true
		}
	}
	return "", false
}

// KnownBatchPromptVersion reports whether version is a batch version. A
// batch version is never a host's configured single-item version.
func (p *Prompts) KnownBatchPromptVersion(version string) bool {
	_, ok := p.batch[version]
	return ok
}

// BatchSchemaName names BatchSchema in decisions.ModelRequest.SchemaName.
const BatchSchemaName = "feature_assignment_batch"

// Bounds of a batch: its items and candidates, the rendered input, and the
// reply, reasoning included. The input bound is in bytes: at the two to
// three bytes a token that JSON of paths and identifiers runs at, 768 KiB is
// about the default context of the decision service's models (272000
// tokens), so most batches too large for the model are refused here, where
// the caller can split them; a caller still treats a provider's refusal as a
// reason to split. It is well inside the decision service's request limit.
const (
	MaxBatchItems        = 50
	MaxBatchCandidates   = 48
	MaxBatchInputBytes   = 768 << 10
	MaxBatchOutputTokens = 32768
)

// Bounds of a batch item, tighter than a single item's so a batch stays a
// few hundred kilobytes.
const (
	maxBatchItemBodyRunes = 2000
	maxBatchItemPaths     = 40
)

// ErrBatchTooLarge is a batch whose rendered input is over
// MaxBatchInputBytes; the caller assigns fewer items at once.
var ErrBatchTooLarge = errors.New("the feature assignment batch is too large")

// BatchItem is one pull request of a batch.
type BatchItem struct {
	PullRequest
	Number int32
	// Links are, by candidate id, its links to that candidate's pull
	// requests.
	Links map[string]Links
	// Mentions are the indexes of the other items of the batch its title or
	// description mentions.
	Mentions []int
}

func batchRef(i int) string { return fmt.Sprintf("P%d", i+1) }

type renderedBatchLinks struct {
	Feature string `json:"feature"`
	renderedLinks
}

type renderedBatchItem struct {
	ID            string               `json:"id"`
	Number        int32                `json:"number"`
	Title         string               `json:"title"`
	Body          string               `json:"description,omitempty"`
	Branch        string               `json:"branch,omitempty"`
	Paths         []string             `json:"paths"`
	Links         []renderedBatchLinks `json:"links,omitempty"`
	MentionsItems []string             `json:"mentions_items,omitempty"`
}

type renderedBatchInput struct {
	Items      []renderedBatchItem `json:"items"`
	Candidates []renderedCandidate `json:"candidates"`
}

// RenderBatch is the user message of one batch call under batch version
// version: the items and the candidates as JSON, every field truncated to
// its bound. Candidates are rendered as a listing version renders them,
// their paths that an item shows first; links are on the items, never on the
// candidates. ErrBatchTooLarge when the result is over MaxBatchInputBytes.
func (p *Prompts) RenderBatch(version string, items []BatchItem, candidates []Candidate) (string, error) {
	switch {
	case !p.KnownBatchPromptVersion(version):
		return "", fmt.Errorf("unknown feature assignment batch version %q", version)
	case len(items) == 0 || len(items) > MaxBatchItems:
		return "", fmt.Errorf("a batch holds 1 to %d items, not %d", MaxBatchItems, len(items))
	case len(candidates) > MaxBatchCandidates:
		return "", fmt.Errorf("a batch offers at most %d candidates, not %d", MaxBatchCandidates, len(candidates))
	}
	refs := make(map[string]string, len(candidates))
	for i, candidate := range candidates {
		refs[candidate.ID] = ref(i)
	}
	input := renderedBatchInput{Items: make([]renderedBatchItem, len(items))}
	var touched []string
	for i, item := range items {
		paths := Item{PullRequest: &item.PullRequest}.Paths()
		// Only the paths the item shows put a candidate's first, so a
		// candidate lists the overlap the model can see.
		touched = append(touched, paths[:min(len(paths), maxBatchItemPaths)]...)
		rendered := renderedBatchItem{
			ID: batchRef(i), Number: item.Number, Title: truncate(item.Title, maxItemTitleRunes),
			Body: truncate(item.Body, maxBatchItemBodyRunes), Branch: truncate(item.Branch, maxItemBranchRunes),
			Paths: boundPaths(paths, nil, maxBatchItemPaths),
		}
		for j, candidate := range candidates {
			links := item.Links[candidate.ID]
			if len(links.Stacked)+len(links.SharedSessions)+len(links.Mentioned) == 0 {
				continue
			}
			rendered.Links = append(rendered.Links, renderedBatchLinks{Feature: ref(j), renderedLinks: renderedLinks{
				Stacked: boundNumbers(links.Stacked), SharedSessions: boundNumbers(links.SharedSessions),
				Mentioned: boundNumbers(links.Mentioned),
			}})
		}
		for _, mentioned := range item.Mentions {
			if mentioned >= 0 && mentioned < len(items) && mentioned != i && !slices.Contains(rendered.MentionsItems, batchRef(mentioned)) {
				rendered.MentionsItems = append(rendered.MentionsItems, batchRef(mentioned))
			}
		}
		input.Items[i] = rendered
	}
	slices.Sort(touched)
	touched = slices.Compact(touched)
	input.Candidates = make([]renderedCandidate, len(candidates))
	for i, candidate := range candidates {
		candidate.Current = false
		input.Candidates[i] = renderCandidate(i, candidate, touched, true)
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(input); err != nil {
		return "", fmt.Errorf("rendering the batch assignment input: %w", err)
	}
	if encoded.Len() > MaxBatchInputBytes {
		return "", ErrBatchTooLarge
	}
	return strings.TrimSuffix(encoded.String(), "\n"), nil
}

// BatchSchema is the JSON Schema of a batch reply, valid under OpenAI
// strict structured outputs.
func BatchSchema() map[string]any {
	str := map[string]any{"type": "string"}
	assignment := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"item", "reason", "choice", "feature_id", "new_id", "title", "description"},
		"properties": orderedObject{
			{"item", str},
			{"reason", str},
			{"choice", map[string]any{"type": "string", "enum": []any{"existing", "new", "skip"}}},
			{"feature_id", str},
			{"new_id", str},
			{"title", str},
			{"description", str},
		},
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"assignments"},
		"properties":           orderedObject{{"assignments", map[string]any{"type": "array", "items": assignment}}},
	}
}

// BatchChoice is the validated choice for one item of a batch: a candidate's
// id, a feature the reply starts (New, by its index in BatchReply.New), or
// a skip.
type BatchChoice struct {
	FeatureID string
	// New is the index in BatchReply.New of the feature the item is in when
	// it starts one or joins one an earlier item started; -1 otherwise.
	New    int
	Skip   bool
	Reason string
}

// BatchReply is a validated batch reply: a choice per item, in the order of
// the items, and the features it starts, in the order it starts them.
type BatchReply struct {
	Choices []BatchChoice
	New     []NewFeature
}

type batchReply struct {
	Assignments *[]batchAssignment `json:"assignments"`
}

type batchAssignment struct {
	Item        *string `json:"item"`
	Reason      *string `json:"reason"`
	Choice      *string `json:"choice"`
	FeatureID   *string `json:"feature_id"`
	NewID       *string `json:"new_id"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

// ParseBatchReply decodes and validates a batch reply for items items
// against the candidates the input listed. Every item is answered exactly
// once; a new feature's id is defined once, and joined only by an item after
// the one that started it; nothing outside the candidates and the reply's
// own features is ever chosen. Any violation fails the whole reply.
func ParseBatchReply(data []byte, items int, candidates []Candidate) (BatchReply, error) {
	var r batchReply
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return BatchReply{}, fmt.Errorf("%w: %v", ErrInvalidReply, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return BatchReply{}, fmt.Errorf("%w: trailing data after the JSON value", ErrInvalidReply)
	}
	if items <= 0 || items > MaxBatchItems {
		return BatchReply{}, fmt.Errorf("a batch holds 1 to %d items, not %d", MaxBatchItems, items)
	}
	if r.Assignments == nil {
		return BatchReply{}, fmt.Errorf("%w: assignments are missing", ErrInvalidReply)
	}
	choices := make([]*BatchChoice, items)
	reply := BatchReply{Choices: make([]BatchChoice, items)}
	// started is, by new id, the feature's index in reply.New and the item
	// that started it.
	type start struct{ index, item int }
	started := map[string]start{}
	// Joins of reply features are checked once every new id is known: an
	// item may only join a feature an earlier item started.
	type join struct {
		item int
		id   string
	}
	var joins []join
	for _, a := range *r.Assignments {
		if a.Item == nil || a.Reason == nil || a.Choice == nil || a.FeatureID == nil || a.NewID == nil || a.Title == nil || a.Description == nil {
			return BatchReply{}, fmt.Errorf("%w: a field is missing", ErrInvalidReply)
		}
		item := -1
		for i := range items {
			if strings.TrimSpace(*a.Item) == batchRef(i) {
				item = i
			}
		}
		switch {
		case item < 0:
			return BatchReply{}, fmt.Errorf("%w: an assignment names no item of the batch", ErrInvalidReply)
		case choices[item] != nil:
			return BatchReply{}, fmt.Errorf("%w: %s is answered twice", ErrInvalidReply, batchRef(item))
		}
		choice := &BatchChoice{New: -1, Reason: truncate(strings.TrimSpace(*a.Reason), 500)}
		switch *a.Choice {
		case "existing":
			id := strings.TrimSpace(*a.FeatureID)
			if strings.HasPrefix(id, "N") {
				joins = append(joins, join{item: item, id: id})
				break
			}
			found := false
			for i, candidate := range candidates {
				if id == ref(i) {
					choice.FeatureID, found = candidate.ID, true
				}
			}
			if !found {
				return BatchReply{}, fmt.Errorf("%w: %s's feature is not a candidate", ErrInvalidReply, batchRef(item))
			}
		case "new":
			id := strings.TrimSpace(*a.NewID)
			if !validNewID(id) {
				return BatchReply{}, fmt.Errorf("%w: %s starts a feature without a new id", ErrInvalidReply, batchRef(item))
			}
			if _, ok := started[id]; ok {
				return BatchReply{}, fmt.Errorf("%w: new id %s is started twice", ErrInvalidReply, id)
			}
			text, err := newFeature(*a.Title, *a.Description)
			if err != nil {
				return BatchReply{}, err
			}
			started[id] = start{index: len(reply.New), item: item}
			choice.New = len(reply.New)
			reply.New = append(reply.New, text)
		case "skip":
			choice.Skip = true
		default:
			return BatchReply{}, fmt.Errorf("%w: unknown choice", ErrInvalidReply)
		}
		choices[item] = choice
	}
	for _, j := range joins {
		s, ok := started[j.id]
		if !ok || s.item >= j.item {
			return BatchReply{}, fmt.Errorf("%w: %s joins %s, which no earlier item started", ErrInvalidReply, batchRef(j.item), j.id)
		}
		choices[j.item].New = s.index
	}
	for i, choice := range choices {
		if choice == nil {
			return BatchReply{}, fmt.Errorf("%w: %s is not answered", ErrInvalidReply, batchRef(i))
		}
		reply.Choices[i] = *choice
	}
	return reply, nil
}

// validNewID is "N" and a positive number without leading zeros.
func validNewID(id string) bool {
	digits, ok := strings.CutPrefix(id, "N")
	if !ok || digits == "" || digits[0] == '0' || len(digits) > 4 {
		return false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// newFeature is a reply's new feature text, validated as ParseReply does.
func newFeature(rawTitle, rawDescription string) (NewFeature, error) {
	title := strings.Join(strings.Fields(strings.ReplaceAll(rawTitle, "\x00", "")), " ")
	description := strings.TrimSpace(strings.ReplaceAll(rawDescription, "\x00", ""))
	switch {
	case title == "" || description == "":
		return NewFeature{}, fmt.Errorf("%w: a new feature needs a title and a description", ErrInvalidReply)
	case utf8.RuneCountInString(title) > MaxNewTitleRunes, utf8.RuneCountInString(description) > MaxNewDescriptionRunes:
		return NewFeature{}, fmt.Errorf("%w: the new feature's title or description is too long", ErrInvalidReply)
	case !utf8.ValidString(title) || !utf8.ValidString(description):
		return NewFeature{}, fmt.Errorf("%w: the new feature's text is not valid UTF-8", ErrInvalidReply)
	}
	return NewFeature{Title: title, Description: description}, nil
}
