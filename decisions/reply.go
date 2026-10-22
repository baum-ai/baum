package decisions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Dropped is a citation, a field, or a whole decision, removed by citation
// validation.
type Dropped struct {
	// Decision is the decision's index in the model's reply.
	Decision int    `json:"decision"`
	Summary  string `json:"summary"`
	// Field names a removed field (trigger, decided_turn or evidence); Turn
	// then holds a removed decided turn. Without Field, Turn or File is the
	// removed citation. All three are empty when the whole decision was
	// dropped. Turn is a pointer so a removed turn 0 is kept.
	Field  string `json:"field,omitempty"`
	Turn   *int   `json:"turn,omitempty"`
	File   string `json:"file,omitempty"`
	Reason string `json:"reason"`
}

// replyDecision mirrors one item of ExtractSchema. Pointers tell a missing
// field from its zero value. Title is optional when decoding older replies.
type replyDecision struct {
	Title               string    `json:"title"`
	Summary             *string   `json:"summary"`
	Who                 *Who      `json:"who"`
	Category            *Category `json:"category"`
	Trigger             *Trigger  `json:"trigger"`
	Rationale           *string   `json:"rationale"`
	RejectedAlternative *string   `json:"rejected_alternative"`
	Superseded          *bool     `json:"superseded"`
	Turns               *[]int    `json:"turns"`
	DecidedTurn         *int      `json:"decided_turn"`
	Evidence            *string   `json:"evidence"`
	Files               *[]string `json:"files"`
}

// replyText trims a reply string and removes NUL characters, which no
// rendered transcript holds and PostgreSQL text and JSONB reject.
func replyText(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\x00", ""))
}

// ParseReply decodes a model reply to ExtractSchema strictly: invalid JSON,
// unknown or missing fields and trailing data are errors. It normalizes each
// decision: text trimmed and NUL-free, turns sorted and deduplicated, files
// cleaned and deduplicated, evidence whitespace collapsed. Who and category
// are not checked here, so a bad value drops one decision in
// ValidateCitations, not the reply. It also returns the superseded_existing
// handles, trimmed; a reply without them supersedes nothing.
func ParseReply(data []byte) ([]Decision, []string, error) {
	var reply struct {
		Decisions          *[]replyDecision `json:"decisions"`
		SupersededExisting []string         `json:"superseded_existing"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reply); err != nil {
		return nil, nil, fmt.Errorf("decode model reply: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("decode model reply: trailing data after the JSON value")
	}
	if reply.Decisions == nil {
		return nil, nil, errors.New("decode model reply: decisions is missing")
	}
	decisions := make([]Decision, 0, len(*reply.Decisions))
	for i, item := range *reply.Decisions {
		if item.Summary == nil || item.Who == nil || item.Category == nil || item.Trigger == nil ||
			item.Rationale == nil || item.RejectedAlternative == nil || item.Superseded == nil ||
			item.Turns == nil || item.DecidedTurn == nil || item.Evidence == nil || item.Files == nil {
			return nil, nil, fmt.Errorf("decode model reply: decision %d is missing a field", i)
		}
		turns := slices.Clone(*item.Turns)
		slices.Sort(turns)
		files := make([]string, 0, len(*item.Files))
		for _, file := range *item.Files {
			if file = path.Clean(replyText(file)); !slices.Contains(files, file) {
				files = append(files, file)
			}
		}
		decisions = append(decisions, Decision{
			Title:               replyText(item.Title),
			Summary:             replyText(*item.Summary),
			Who:                 *item.Who,
			Category:            *item.Category,
			Trigger:             *item.Trigger,
			Rationale:           replyText(*item.Rationale),
			RejectedAlternative: replyText(*item.RejectedAlternative),
			Superseded:          *item.Superseded,
			Turns:               slices.Compact(turns),
			DecidedTurn:         *item.DecidedTurn,
			Evidence:            strings.Join(strings.Fields(replyText(*item.Evidence)), " "),
			Files:               files,
		})
	}
	superseded := make([]string, len(reply.SupersededExisting))
	for i, handle := range reply.SupersededExisting {
		superseded[i] = replyText(handle)
	}
	return decisions, superseded, nil
}

// minEvidenceWords is the fewest words a quote needs to be kept, so a lone
// word that happens to occur in a cited turn is not taken as evidence.
const minEvidenceWords = 2

// maxEvidenceWords bounds a quote, which the prompt asks to keep to about
// 30 words: a review carries current decisions whole, quote included.
const maxEvidenceWords = 60

// quoteKey is the form a quote and the transcript text are compared in:
// lower case, without the Markdown emphasis and code marks models drop when
// quoting, with typographic quotes made plain and whitespace runs as one
// space.
func quoteKey(s string) string {
	s = quoteKeyReplacer.Replace(strings.ToLower(s))
	return strings.Join(strings.Fields(s), " ")
}

var quoteKeyReplacer = strings.NewReplacer("*", "", "`", "", "’", "'", "‘", "'", "“", `"`, "”", `"`)

// turnPassages is each turn's quotable text, by ordinal, in quoteKey form:
// the human prompt, agent text, plans, reports, questions with their answers
// and notes, and human feedback, each its own passage and without the labels
// Text adds. Offered options are not quotable: only the answer was chosen.
func turnPassages(transcript *Transcript) map[int][]string {
	passages := make(map[int][]string, len(transcript.Turns))
	for _, turn := range transcript.Turns {
		var texts []string
		add := func(s string) {
			if key := quoteKey(s); key != "" {
				texts = append(texts, key)
			}
		}
		add(turn.Human)
		for _, event := range turn.Events {
			add(event.Text)
			add(event.Feedback)
			for _, question := range event.Questions {
				add(question.Text)
				add(question.Answer)
				add(question.Note)
			}
		}
		passages[turn.Ordinal] = texts
	}
	return passages
}

// unquoted is s without the quotation marks around all of it, plain or
// typographic, double or single, or "" when s is not so wrapped.
func unquoted(s string) string {
	runes := []rune(s)
	if len(runes) > 2 && strings.ContainsRune(`"“'‘`, runes[0]) && strings.ContainsRune(`"”'’`, runes[len(runes)-1]) {
		return strings.TrimSpace(string(runes[1 : len(runes)-1]))
	}
	return ""
}

// ValidateCitations checks parsed decisions against the transcript they were
// extracted from. It removes turns that are not transcript ordinals and files
// not edited in a remaining cited turn. A decision is dropped whole when no
// turn remains or its own shape is invalid. An unknown trigger, a decided
// turn that is not a remaining cited turn, and evidence not quoted from one
// are removed on their own. With a positive cutoff, a decision none of whose
// remaining turns is after it is dropped: an extension's existing decisions
// already hold it. Order is kept, and each removal is reported.
func ValidateCitations(decisions []Decision, transcript *Transcript, cutoff int) ([]Decision, []Dropped) {
	edited := make(map[int][]string, len(transcript.Turns))
	for _, turn := range transcript.Turns {
		edited[turn.Ordinal] = turn.EditedFiles
	}
	passages := turnPassages(transcript)
	kept := []Decision{}
	var dropped []Dropped
	for i, decision := range decisions {
		drop := func(reason string) {
			dropped = append(dropped, Dropped{Decision: i, Summary: decision.Summary, Reason: reason})
		}
		shape := decision
		shape.Turns, shape.Files = []int{1}, nil
		shape.Trigger, shape.DecidedTurn, shape.Evidence, shape.EvidenceTurn = "", 0, "", 0
		if err := shape.Validate(); err != nil {
			drop(err.Error())
			continue
		}
		turns := make([]int, 0, len(decision.Turns))
		var removed []Dropped
		for _, turn := range decision.Turns {
			if _, ok := edited[turn]; ok {
				turns = append(turns, turn)
			} else {
				removed = append(removed, Dropped{Decision: i, Summary: decision.Summary, Turn: &turn, Reason: "turn is not in the transcript"})
			}
		}
		if len(turns) == 0 {
			drop(noValidTurnReason(decision.Turns))
			continue
		}
		if cutoff > 0 && turns[len(turns)-1] <= cutoff {
			drop(fmt.Sprintf("no cited turn is after the cutoff turn %d", cutoff))
			continue
		}
		files := make([]string, 0, len(decision.Files))
		for _, file := range decision.Files {
			if slices.ContainsFunc(turns, func(turn int) bool { return slices.Contains(edited[turn], file) }) {
				files = append(files, file)
			} else {
				removed = append(removed, Dropped{Decision: i, Summary: decision.Summary, File: file, Reason: "file was not edited in a cited turn"})
			}
		}
		decision.Turns, decision.Files = turns, files
		if decision.Trigger != "" && !decision.Trigger.Valid() {
			removed = append(removed, Dropped{Decision: i, Summary: decision.Summary, Field: "trigger",
				Reason: fmt.Sprintf("unknown trigger %q", decision.Trigger)})
			decision.Trigger = ""
		}
		if decided := decision.DecidedTurn; decided != 0 && !slices.Contains(turns, decided) {
			removed = append(removed, Dropped{Decision: i, Summary: decision.Summary, Field: "decided_turn", Turn: &decided,
				Reason: "decided turn is not a cited turn"})
			decision.DecidedTurn = 0
		}
		if quote := decision.Evidence; quote != "" {
			if words := len(strings.Fields(quoteKey(quote))); words > maxEvidenceWords {
				removed = append(removed, Dropped{Decision: i, Summary: decision.Summary, Field: "evidence",
					Reason: fmt.Sprintf("evidence has %d words, more than %d", words, maxEvidenceWords)})
				decision.Evidence = ""
			} else if decision.Evidence, decision.EvidenceTurn = findEvidence(decision, passages); decision.EvidenceTurn == 0 {
				removed = append(removed, Dropped{Decision: i, Summary: decision.Summary, Field: "evidence",
					Reason: fmt.Sprintf("evidence %q is not quoted from one passage of a cited turn", quote)})
			}
		}
		if err := decision.Validate(); err != nil {
			drop(err.Error())
			continue
		}
		kept = append(kept, decision)
		dropped = append(dropped, removed...)
	}
	return kept, dropped
}

// findEvidence returns the decision's evidence and the cited turn one of
// whose passages holds it in quoteKey form: the decided turn when one does,
// else the first such turn. The quote is tried as given, then without
// quotation marks around it, and the form that matched is returned. A quote
// of fewer than minEvidenceWords words never matches. It returns "", 0 when
// nothing matches.
func findEvidence(decision Decision, passages map[int][]string) (string, int) {
	candidates := decision.Turns
	if decision.DecidedTurn != 0 {
		candidates = append([]int{decision.DecidedTurn}, candidates...)
	}
	for _, quote := range []string{decision.Evidence, unquoted(decision.Evidence)} {
		key := quoteKey(quote)
		if len(strings.Fields(key)) < minEvidenceWords {
			continue
		}
		for _, turn := range candidates {
			if slices.ContainsFunc(passages[turn], func(passage string) bool { return strings.Contains(passage, key) }) {
				return quote, turn
			}
		}
	}
	return "", 0
}

// noValidTurnReason explains dropping a decision none of whose cited turns is
// in the transcript.
func noValidTurnReason(cited []int) string {
	if len(cited) == 0 {
		return "no turn is cited"
	}
	ordinals := make([]string, len(cited))
	for i, turn := range cited {
		ordinals[i] = strconv.Itoa(turn)
	}
	return "no cited turn is in the transcript (cited " + strings.Join(ordinals, ", ") + ")"
}
