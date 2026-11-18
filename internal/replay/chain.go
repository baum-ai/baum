package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/generate"
)

// chainOrder says how a chain's revisions are ordered; chain.json records it.
const chainOrder = "previous version number in the bundle (none counts as 0), then capture time, then case id"

// ChainRevision is one revision of a chain.
type ChainRevision struct {
	Position int    `json:"position"`
	CaseID   string `json:"case_id"`
	// Case is the revision's case directory, relative to the chain directory.
	Case   string `json:"case"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// PreviousFrom is the case whose generated document this revision read as
	// its previous version, and PreviousVersion the version number it carried
	// (the document's own); both are empty for the first revision, which keeps
	// the previous version frozen in its bundle (if any).
	PreviousFrom    string `json:"previous_from,omitempty"`
	PreviousVersion int    `json:"previous_version,omitempty"`
	// BundleBytes is the encoded size of the bundle the model read, and Fitted
	// says that fitting it to the evidence budget cut something.
	BundleBytes int  `json:"bundle_bytes,omitempty"`
	Fitted      bool `json:"fitted,omitempty"`
	// Pair is the file, in the chain directory, that holds the metrics of this
	// revision against the one before it.
	Pair string `json:"pair,omitempty"`
}

// ChainRecord is chains/<pull request>/chain.json.
type ChainRecord struct {
	PullRequest string          `json:"pull_request"`
	Order       string          `json:"order"`
	Revisions   []ChainRevision `json:"revisions"`
	// Stopped says why the chain ended before its last revision.
	Stopped string `json:"stopped,omitempty"`
	// Warnings say where the order rests on a guess.
	Warnings []string `json:"warnings,omitempty"`
}

// runChain replays a pull request's revisions in order. Each one reads the
// document the one before it generated as its previous version (the
// generator's Next, then the masking and fitting assembly applies), so
// identifiers can be compared across versions of the generator's own output. A
// revision that fails ends the chain: the rest are skipped.
func (p Plan) runChain(ctx context.Context, runner *Runner, u unit, halted func() bool, add func(Row)) {
	chainer := runner.generator.(Chainer)
	meter, _ := runner.generator.(Meter)
	dir := filepath.Join(p.RunDir(), "chains", u.chain.Name())
	// What an earlier run recorded of the revisions a resume keeps.
	kept := map[string]ChainRevision{}
	if raw, err := os.ReadFile(filepath.Join(dir, "chain.json")); err == nil {
		var old ChainRecord
		if json.Unmarshal(raw, &old) == nil {
			for _, r := range old.Revisions {
				kept[r.CaseID] = r
			}
		}
	}
	// The links and the pair records of revisions that run again start over, so
	// nothing of an earlier run stays.
	_ = os.MkdirAll(dir, 0o700)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, entry := range entries {
			var position int
			_, scanErr := fmt.Sscanf(entry.Name(), "pair-%02d.json", &position)
			if entry.Type()&os.ModeSymlink != 0 || scanErr == nil && position > u.done {
				_ = os.Remove(filepath.Join(dir, entry.Name()))
			}
		}
	}
	rec := ChainRecord{PullRequest: fmt.Sprintf("%s#%d", u.chain.Repository, u.chain.Number), Order: chainOrder, Warnings: u.chain.Warnings}
	for i, id := range u.ids {
		rec.Revisions = append(rec.Revisions, ChainRevision{Position: i + 1, CaseID: id, Case: "../../cases/" + id, Status: "pending"})
		_ = os.Symlink(rec.Revisions[i].Case, filepath.Join(dir, fmt.Sprintf("%02d-%s", i+1, id)))
	}
	save := func() { _ = writeJSON(filepath.Join(dir, "chain.json"), rec) }
	caps := assemble.LimitsFrom(runner.Caps).Caps
	redactor, redactorErr := bundle.NewRedactor()

	var previous []byte
	// ended says why the chain stopped; skip is the reason its remaining
	// revisions' rows carry.
	var ended, skip string
	for i := range u.done {
		rev := &rec.Revisions[i]
		*rev = kept[rev.CaseID]
		rev.Position, rev.CaseID, rev.Case, rev.Status = i+1, u.ids[i], "../../cases/"+u.ids[i], "resumed"
		if rev.Pair != "" {
			if _, err := os.Stat(filepath.Join(dir, rev.Pair)); err != nil {
				rev.Pair = ""
			}
		}
	}
	if u.done > 0 {
		var err error
		if previous, err = os.ReadFile(filepath.Join(p.RunDir(), "cases", u.ids[u.done-1], "document.json")); err != nil {
			ended, skip = fmt.Sprintf("the document of revision %d (%s), which an earlier run generated, cannot be read", u.done, u.ids[u.done-1]), "chain"
		}
	}
	for i := u.done; i < len(u.ids); i++ {
		id, rev := u.ids[i], &rec.Revisions[i]
		if ended == "" && halted() {
			ended, skip = fmt.Sprintf("the run was stopped before revision %d", i+1), "stopped"
		}
		if ended != "" {
			rec.Stopped = ended
			rev.Status, rev.Reason = "skipped", skip
			// What an earlier run left of a skipped revision is not a result of
			// this chain: neither metrics nor a resume may take it for one.
			_ = os.RemoveAll(filepath.Join(p.RunDir(), "cases", id))
			add(Row{CaseID: id, Status: "skipped", Reason: skip})
			continue
		}
		var prepare func(Case) (Case, error)
		if previous != nil {
			handed := previous
			rev.PreviousFrom = u.ids[i-1]
			prepare = func(c Case) (Case, error) {
				if redactorErr != nil {
					return Case{}, redactorErr
				}
				b, err := chainer.Next(c, handed)
				if err != nil {
					return Case{}, err
				}
				if b.Previous != nil {
					rev.PreviousVersion = b.Previous.Version
				}
				fitted, err := c.Refit(ctx, b, caps, redactor)
				if err != nil {
					// Assembly reports a bundle that cannot be built as a failure of
					// the run's input.
					return Case{}, &generate.Error{Category: generate.CategoryInput, Err: err}
				}
				rev.BundleBytes, rev.Fitted = len(fitted.BundleJSON), fitted.Fitted
				return fitted, nil
			}
		}
		row, document := p.runCase(ctx, runner, id, prepare)
		rev.Status, rev.Reason = row.Status, row.Reason
		add(row)
		if row.Status != "ok" {
			ended, skip = fmt.Sprintf("revision %d (%s) did not succeed: %s", i+1, id, row.Status+reasonText(row.Reason)), "chain"
			rec.Stopped = ended
			save()
			continue
		}
		if previous != nil && meter != nil {
			pair := PairRecord{From: u.ids[i-1], To: id, First: i == 1}
			if values, err := meter.PairMetrics(previous, document); err != nil {
				pair.Skipped = err.Error()
			} else {
				pair.Values = values
			}
			rev.Pair = fmt.Sprintf("pair-%02d.json", i+1)
			_ = writeJSON(filepath.Join(dir, rev.Pair), pair)
		}
		previous = document
		save()
	}
	save()
}

func reasonText(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}
