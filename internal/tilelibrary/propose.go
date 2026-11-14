package tilelibrary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// VerdictPass is the verdict of a render check that passed.
const VerdictPass = "pass"

// Checks looks up the result of a render check by its id. The checker lives
// beside this package (the render check); a proposal rests on its answer and
// nothing else it says. It reports false for an id it does not know or no
// longer holds.
type Checks interface {
	Result(ctx context.Context, id string) (CheckResult, bool)
}

// CheckResult is what a proposal needs of a render check.
type CheckResult struct {
	ID string
	// Verdict is VerdictPass when the check passed, else the check's own word.
	Verdict string
	// Digest is the digest of the definition that was checked.
	Digest string
	// Definition is that definition, with its HTML.
	Definition json.RawMessage
	// Samples are the samples the check ran.
	Samples []CheckedSample
}

// CheckedSample is a sample a check ran: whether the component rendered it
// without a problem, and whether the checker found it synthetic (its hunks
// and items its own).
type CheckedSample struct {
	Sample    library.Sample
	Passed    bool
	Synthetic bool
}

// Request is what a chat asks to save: the arguments of propose_tile_type.
// Nil members leave what the check's definition, or the type FromName names,
// has.
type Request struct {
	// FromTile is the tile of the pull request page the type comes from;
	// FromName is a saved or built-in type to start from. One is set.
	FromTile string
	FromName string
	Name     *string
	Title    *string
	UseWhen  *string
	Glance   *string
	Paths    *[]string
	// ReviewChecks is what the review should check when the type applies.
	ReviewChecks *string
	// Check is the id of the render check the request rests on.
	Check string
}

// Target is the target of a proposal about a type.
func Target(name string) string { return "tile_type:" + name }

// Propose checks a request and returns the change a proposal holds, which a
// save then writes (Create or Update). It refuses what could not be saved,
// with the reason for the agent: a check that is unknown or did not pass on
// at least one synthetic sample, a name the library does not allow or has,
// an edit of a built-in type, and every problem Prepare finds. The samples
// saved are the check's samples that passed and are synthetic, never any
// other: the library holds no real pull request data.
func (s *Store) Propose(ctx context.Context, checks Checks, req Request) (Change, error) {
	if checks == nil {
		return Change{}, errors.New("Render checks are not available in this baum")
	}
	result, ok := checks.Result(ctx, req.Check)
	switch {
	case !ok:
		return Change{}, errors.New("That check is unknown or has expired: run the render check again")
	case result.Verdict != VerdictPass:
		return Change{}, errors.New("That check did not pass: fix the tile and run the render check again")
	}
	if digest, err := tiles.DefinitionDigest(result.Definition); err != nil || digest != result.Digest {
		return Change{}, errors.New("That check's definition does not match its digest: run the render check again")
	}
	var samples []library.Sample
	for _, cs := range result.Samples {
		if cs.Passed && cs.Synthetic && IsSynthetic(cs.Sample) && len(samples) < MaxSamples {
			samples = append(samples, cs.Sample)
		}
	}
	if len(samples) == 0 {
		return Change{}, errors.New("That check did not pass on a synthetic sample: write a sample with hunks and items of its own, in a made-up repository, citing only them, and run the render check on it")
	}
	def, problems := tiles.NewValidator().CheckDefinition(result.Definition)
	if len(problems) > 0 {
		return Change{}, refuse("definition", problems)
	}
	var base *Type
	if req.FromName != "" {
		if _, builtin := library.Get(req.FromName); !builtin {
			t, err := s.Get(ctx, req.FromName)
			if err != nil {
				return Change{}, err
			}
			base = &t
		}
	}
	name := def.Name
	switch {
	case req.Name != nil:
		name = *req.Name
	case base != nil:
		name = base.Name
	}
	if err := CheckName(name); err != nil {
		return Change{}, err
	}
	edit := base != nil && base.Name == name
	if !edit {
		st, err := s.State(ctx, name)
		if err != nil {
			return Change{}, err
		}
		switch {
		case st.Present && st.Deleted:
			return Change{}, ErrDeleted
		case st.Present:
			return Change{}, fmt.Errorf("A tile type named %s is already in your library: start from it with from.name to change it, or choose another name", name)
		}
	}
	def.Name = name
	if req.Title != nil {
		def.Title = *req.Title
	}
	if req.UseWhen != nil {
		def.UseWhen = *req.UseWhen
	}
	sealed, err := library.Seal(&def)
	if err != nil {
		return Change{}, errors.New("The definition could not be sealed")
	}
	change := Change{Definition: sealed, Samples: samples, Origin: OriginChat, AppliesWith: []string{}, Paths: []string{}}
	if base != nil {
		change.Glance, change.ReviewChecks, change.AppliesWith, change.Paths = base.Entry.Glance, base.Entry.ReviewChecks, slices.Clone(base.Entry.AppliesWith), slices.Clone(base.Paths)
		change.Origin = base.Origin
	} else if builtin, ok := library.Get(req.FromName); ok {
		change.Glance, change.ReviewChecks, change.AppliesWith = builtin.Glance, builtin.ReviewChecks, slices.Clone(builtin.AppliesWith)
	}
	if req.Glance != nil {
		change.Glance = *req.Glance
	}
	if req.ReviewChecks != nil {
		change.ReviewChecks = *req.ReviewChecks
	}
	if req.Paths != nil {
		change.Paths = slices.Clone(*req.Paths)
	}
	if _, err := Prepare(change); err != nil {
		return Change{}, err
	}
	return change, nil
}
