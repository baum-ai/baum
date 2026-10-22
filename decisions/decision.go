// Package decisions extracts the decisions made in an agent session: the
// choices between plausible alternatives that land in the repository.
//
// It holds what every host shares: the decision schema, the cleaned transcript
// a model reads (Render), the strict reply schema and parser, and the citation
// validator that checks a reply against the transcript (Extractor). It ships no
// prompt: each host supplies its own system prompt to NewExtractor and its own
// Model. See ARCHITECTURE.md, "decisions".
package decisions

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// Who records which party a decision belongs to.
type Who string

const (
	WhoHuman    Who = "human"
	WhoAgent    Who = "agent"
	WhoApproved Who = "agent_proposed_human_approved"
)

// Valid reports whether w is a known value.
func (w Who) Valid() bool {
	switch w {
	case WhoHuman, WhoAgent, WhoApproved:
		return true
	}
	return false
}

// Category is the kind of choice a decision makes.
type Category string

const (
	CategoryArchitecture   Category = "architecture"
	CategoryDataModel      Category = "data_model"
	CategoryInterface      Category = "interface"
	CategoryBehavior       Category = "behavior"
	CategoryImplementation Category = "implementation"
	CategoryDependency     Category = "dependency"
	CategoryTesting        Category = "testing"
	CategoryScope          Category = "scope"
	// CategoryProcess is CI, build and dev tooling changed in the repository,
	// never issue tracking, orchestration or PR mechanics.
	CategoryProcess Category = "process"
)

// Valid reports whether c is a known category.
func (c Category) Valid() bool {
	switch c {
	case CategoryArchitecture, CategoryDataModel, CategoryInterface, CategoryBehavior,
		CategoryImplementation, CategoryDependency, CategoryTesting, CategoryScope, CategoryProcess:
		return true
	}
	return false
}

// Trigger is what prompted a decision.
type Trigger string

const (
	TriggerRequest    Trigger = "request"
	TriggerReview     Trigger = "review"
	TriggerFailure    Trigger = "failure"
	TriggerFinding    Trigger = "finding"
	TriggerInitiative Trigger = "initiative"
)

// Valid reports whether t is a known trigger.
func (t Trigger) Valid() bool {
	switch t {
	case TriggerRequest, TriggerReview, TriggerFailure, TriggerFinding, TriggerInitiative:
		return true
	}
	return false
}

// Decision is one decision made in a session. Turns are the root session's
// UserInput ordinals; files are repository-relative paths edited in one of
// those turns. DecidedTurn and EvidenceTurn are cited turns, zero for none;
// Evidence is a verbatim quote from EvidenceTurn. Trigger, Rationale,
// DecidedTurn and Evidence are empty in extractions from earlier prompt
// versions; labels carry no evidence.
type Decision struct {
	Title               string   `json:"title,omitempty"`
	Summary             string   `json:"summary"`
	Category            Category `json:"category"`
	Who                 Who      `json:"who"`
	Trigger             Trigger  `json:"trigger,omitempty"`
	Rationale           string   `json:"rationale,omitempty"`
	RejectedAlternative string   `json:"rejected_alternative,omitempty"`
	Superseded          bool     `json:"superseded"`
	Turns               []int    `json:"turns"`
	DecidedTurn         int      `json:"decided_turn,omitempty"`
	Evidence            string   `json:"evidence,omitempty"`
	EvidenceTurn        int      `json:"evidence_turn,omitempty"`
	Files               []string `json:"files"`
	// ID is the decision's ID, given by the host when it first stores the
	// extraction holding it (Identified); empty in a model reply.
	ID string `json:"decision_id,omitempty"`
}

// Identified returns decisions with a random ID given to each one without
// one. The model never supplies an ID and none is derived, so a host calls it
// once, when it first stores an extraction, and keeps the IDs from then on.
func Identified(decisions []Decision) []Decision {
	identified := slices.Clone(decisions)
	for i := range identified {
		if identified[i].ID == "" {
			identified[i].ID = uuid.NewString()
		}
	}
	return identified
}

// SessionDecisions is the decisions extracted from one root session.
type SessionDecisions struct {
	SessionID string     `json:"session_id"`
	Decisions []Decision `json:"decisions"`
}

// Validate checks the decision's own shape. Whether its turns exist and its
// files were edited in a cited turn needs the session, so the extractor checks that.
func (d Decision) Validate() error {
	var errs []error
	if strings.TrimSpace(d.Summary) == "" {
		errs = append(errs, errors.New("summary is empty"))
	}
	if !d.Who.Valid() {
		errs = append(errs, fmt.Errorf("unknown who %q", d.Who))
	}
	if !d.Category.Valid() {
		errs = append(errs, fmt.Errorf("unknown category %q", d.Category))
	}
	if d.Trigger != "" && !d.Trigger.Valid() {
		errs = append(errs, fmt.Errorf("unknown trigger %q", d.Trigger))
	}
	if len(d.Turns) == 0 {
		errs = append(errs, errors.New("turns is empty"))
	}
	if d.DecidedTurn != 0 && !slices.Contains(d.Turns, d.DecidedTurn) {
		errs = append(errs, fmt.Errorf("decided turn %d is not a cited turn", d.DecidedTurn))
	}
	if (d.Evidence == "") != (d.EvidenceTurn == 0) {
		errs = append(errs, errors.New("evidence and its turn must be set together"))
	} else if d.EvidenceTurn != 0 && !slices.Contains(d.Turns, d.EvidenceTurn) {
		errs = append(errs, fmt.Errorf("evidence turn %d is not a cited turn", d.EvidenceTurn))
	}
	for i, turn := range d.Turns {
		if turn < 1 {
			errs = append(errs, fmt.Errorf("turn %d is not a valid ordinal", turn))
		}
		if i > 0 && turn <= d.Turns[i-1] {
			errs = append(errs, errors.New("turns are not strictly ascending"))
			break
		}
	}
	for _, file := range d.Files {
		if !repoRelative(file) {
			errs = append(errs, fmt.Errorf("file %q is not a clean repository-relative path", file))
		}
	}
	return errors.Join(errs...)
}

func repoRelative(file string) bool {
	return file != "" && file != "." && !strings.HasPrefix(file, "/") && path.Clean(file) == file &&
		file != ".." && !strings.HasPrefix(file, "../")
}
