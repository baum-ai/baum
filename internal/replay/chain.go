package replay

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// chainOrder says how a chain's revisions are ordered; chain.json records it.
const chainOrder = "previous version number in the bundle, then capture time, then case id"

// ChainRevision is one revision of a chain.
type ChainRevision struct {
	Position int    `json:"position"`
	CaseID   string `json:"case_id"`
	// Case is the revision's case directory, relative to the chain directory.
	Case   string `json:"case"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// PreviousFrom is the case whose generated document this revision read as
	// its previous version, and PreviousVersion the version number it was given;
	// both are empty for the first revision, which keeps the previous version
	// frozen in its bundle (if any).
	PreviousFrom    string `json:"previous_from,omitempty"`
	PreviousVersion int    `json:"previous_version,omitempty"`
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
}

// runChain replays a pull request's revisions in order. Each one reads the
// document the one before it generated as its previous version (the
// generator's Next, as the app carries a stored version forward), so identifiers
// can be compared across versions of the generator's own output. A revision that
// fails ends the chain: the rest are skipped.
func (p Plan) runChain(ctx context.Context, runner *Runner, u unit, halted func() bool, add func(Row)) {
	chainer := runner.generator.(Chainer)
	meter, _ := runner.generator.(Meter)
	dir := filepath.Join(p.RunDir(), "chains", u.chain.Name())
	_ = os.MkdirAll(dir, 0o700)
	rec := ChainRecord{PullRequest: fmt.Sprintf("%s#%d", u.chain.Repository, u.chain.Number), Order: chainOrder}
	for i, id := range u.ids {
		rec.Revisions = append(rec.Revisions, ChainRevision{Position: i + 1, CaseID: id, Case: "../../cases/" + id, Status: "pending"})
		link := filepath.Join(dir, fmt.Sprintf("%02d-%s", i+1, id))
		_ = os.Remove(link)
		_ = os.Symlink(rec.Revisions[i].Case, link)
	}
	save := func() { _ = writeJSON(filepath.Join(dir, "chain.json"), rec) }

	var previous []byte
	// ended says why the chain stopped; skip is the reason its remaining
	// revisions' rows carry.
	var ended, skip string
	for i := range u.done {
		rec.Revisions[i].Status = "resumed"
		if i > 0 {
			rec.Revisions[i].PreviousFrom, rec.Revisions[i].PreviousVersion = u.ids[i-1], i
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
			add(Row{CaseID: id, Status: "skipped", Reason: skip})
			continue
		}
		var prepare func(Case) (Case, error)
		if previous != nil {
			handed, version := previous, i
			rev.PreviousFrom, rev.PreviousVersion = u.ids[i-1], version
			prepare = func(c Case) (Case, error) { return chainer.Next(c, handed, version) }
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
			pair := PairRecord{From: u.ids[i-1], To: id}
			if pair.Values, _ = meter.PairMetrics(previous, document); pair.Values == nil {
				pair.Skipped = "the documents could not be measured"
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
