package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/tiles"
)

// ScreenCase is what a picture of one case of a run needs: the review the run
// composed or generated, the composition it got and the template's for the
// same review, and where the review's earlier versions are, since a store
// takes a version only after the ones before it. ScreenInputs builds them;
// the real-reviews fixture stores each layout as the version's composition
// and serves it through the app's own routes (see README.md, "Pictures of a
// run").
type ScreenCase struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	// Version is the shown review's version.
	Version int `json:"version"`
	// Size is the case's size bucket, from the run's manifest.
	Size string `json:"size,omitempty"`
	// Store is the saved review store, relative to the dataset's reviews
	// directory (<machine>/<file>), that holds the versions before Version,
	// and Version itself when Saved. Empty when Version is 1 and the review
	// is not saved.
	Store string `json:"store,omitempty"`
	// Saved says the shown review is the store's own version Version, as a
	// compose run over saved reviews composed it; otherwise Document is the
	// review, which the run generated, with Metadata and Attachments for
	// storing it.
	Saved       bool               `json:"saved"`
	Document    json.RawMessage    `json:"-"`
	Metadata    store.Metadata     `json:"-"`
	Attachments []store.Attachment `json:"-"`
	// Composition is the run's layout and Template the change kind's template
	// for the same review; both baum.composition/1.
	Composition json.RawMessage `json:"-"`
	Template    json.RawMessage `json:"-"`
	// Problem says why the case cannot be shown; the other fields may then
	// be partial.
	Problem string `json:"problem,omitempty"`
}

// ScreenInputs reads the cases a run completed and returns what showing each
// needs, in the run's case order. It reads the dataset (cases and saved
// review stores, through copies in the temporary directory that it removes)
// and the run directory, and writes into neither. A run of the compose
// generator over an earlier run's documents finds that run beside this one,
// by the name its manifest records. A case that cannot be shown is returned
// with its Problem; only a run or dataset that cannot be read at all is an
// error.
func ScreenInputs(ctx context.Context, datasetDir, runDir string) ([]ScreenCase, error) {
	manifest, err := ReadManifest(runDir)
	if err != nil {
		return nil, err
	}
	var source string
	switch manifest.Generator {
	case "compose":
		source, _ = manifest.GeneratorSettings["source"].(string)
	case "tiles":
	default:
		return nil, fmt.Errorf("%s: a run of the %s generator has no layouts to show; use a compose or tiles run", runDir, manifest.Generator)
	}
	fromRun := ""
	if source == ComposeFromRun {
		name, _ := manifest.GeneratorSettings["from_run"].(string)
		if name == "" {
			return nil, fmt.Errorf("%s: the manifest does not name the run whose documents were composed", runDir)
		}
		fromRun = filepath.Join(filepath.Dir(filepath.Clean(runDir)), name)
	}
	dataset, err := OpenDataset(datasetDir)
	if err != nil {
		return nil, err
	}
	stores, err := openReviewStores(filepath.Join(datasetDir, "reviews"))
	switch {
	case err == nil:
		defer func() { _ = stores.Close() }()
	case manifest.Generator == "compose" && source == ComposeFromStore:
		return nil, err
	default:
		// Generated reviews of first versions need no saved history.
		stores = &reviewStores{versions: map[string][]storedVersion{}}
	}
	var out []ScreenCase
	for _, id := range manifest.CaseIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		outcome, err := ReadOutcome(filepath.Join(runDir, "cases", id))
		if err != nil || !outcome.OK() {
			continue
		}
		sc := ScreenCase{ID: id, Size: manifest.CaseFacts[id].Size}
		if err := screenCase(ctx, &sc, dataset, stores, runDir, fromRun, manifest.Generator, outcome); err != nil {
			sc.Problem = err.Error()
		}
		out = append(out, sc)
	}
	return out, nil
}

func screenCase(ctx context.Context, sc *ScreenCase, dataset *Dataset, stores *reviewStores, runDir, fromRun, generator string, outcome Outcome) error {
	dir := filepath.Join(runDir, "cases", sc.ID)
	c, err := dataset.LoadCase(ctx, sc.ID)
	if err != nil {
		return err
	}
	sc.Repository, sc.Number = c.Bundle.Repository, c.Bundle.PullRequest.Number
	recordFile := "document.json"
	if generator == "tiles" {
		recordFile = "compose.json"
	}
	raw, err := os.ReadFile(filepath.Join(dir, recordFile))
	if err != nil {
		return fmt.Errorf("reading the compose record: %w", errors.Unwrap(err))
	}
	var record ComposeRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.Schema != ComposeRecordName {
		return errors.New("the case has no compose record")
	}
	if record.Problem != "" || len(record.Composition) == 0 {
		return errors.New("the run stored no composition for the case")
	}
	sc.Composition, sc.Template = record.Composition, record.Template

	// The saved review a compose run over the stores composed.
	if generator == "compose" && fromRun == "" {
		document, which, err := stores.find(c)
		if err != nil {
			return err
		}
		if which.Version != record.Review.Version {
			return fmt.Errorf("the dataset's saved review is version %d, the run composed version %d", which.Version, record.Review.Version)
		}
		sc.Version, sc.Saved, sc.Document = which.Version, true, document
		sc.Store = stores.storeOf(sc.Repository, sc.Number, which.Version, document)
		if sc.Store == "" {
			return errors.New("the composed version is in no single store")
		}
		return checkTemplate(sc)
	}

	// A review the run (or the run it composed) generated.
	documentDir := dir
	if fromRun != "" {
		documentDir = filepath.Join(fromRun, "cases", sc.ID)
	}
	if sc.Document, err = os.ReadFile(filepath.Join(documentDir, "document.json")); err != nil {
		return fmt.Errorf("reading the generated review: %w", errors.Unwrap(err))
	}
	var doc struct {
		Schema string `json:"schema"`
		Review struct {
			Head        string `json:"head"`
			Base        string `json:"base"`
			MergeBase   string `json:"merge_base"`
			Version     int    `json:"version"`
			GeneratedAt string `json:"generated_at"`
		} `json:"review"`
		Index struct {
			Attachments []struct {
				ID string `json:"id"`
			} `json:"attachments"`
		} `json:"index"`
	}
	if err := json.Unmarshal(sc.Document, &doc); err != nil || doc.Review.Version < 1 {
		return errors.New("the generated review has no version")
	}
	sc.Version = doc.Review.Version
	sc.Metadata = store.Metadata{Version: doc.Review.Version, Head: doc.Review.Head, Base: doc.Review.Base, MergeBase: doc.Review.MergeBase,
		BundleDigest: c.Digest, Backend: outcome.Backend, BackendVersion: outcome.BackendVersion, Model: outcome.Model, Schema: doc.Schema,
		SchemaRevision: outcome.SchemaRevision, PromptVersion: outcome.PromptVersion, PromptDigest: outcome.PromptDigest, GeneratedAt: doc.Review.GeneratedAt}
	listed := map[string]bool{}
	for _, a := range doc.Index.Attachments {
		listed[a.ID] = true
	}
	if sc.Attachments, err = caseAttachments(c, listed); err != nil {
		return err
	}
	if sc.Version > 1 {
		if sc.Store = stores.historyOf(sc.Repository, sc.Number, sc.Version-1); sc.Store == "" {
			return fmt.Errorf("no saved review store holds versions 1 to %d of the pull request", sc.Version-1)
		}
	}
	return checkTemplate(sc)
}

// checkTemplate fills in the template when the record has none, as the
// compose stage builds it.
func checkTemplate(sc *ScreenCase) error {
	if len(sc.Template) > 0 {
		return nil
	}
	_, doc, err := converted(sc.Document)
	if err != nil {
		return err
	}
	template, _ := tiles.Template(doc, nil)
	sc.Template, err = json.Marshal(template)
	return err
}

// caseAttachments are the case's images the document lists, as a generated
// version stores them.
func caseAttachments(c Case, listed map[string]bool) ([]store.Attachment, error) {
	var out []store.Attachment
	for _, a := range c.Bundle.Attachments {
		if !listed[a.ID] {
			continue
		}
		raw, err := os.ReadFile(c.attachmentFile(a))
		if err != nil {
			return nil, fmt.Errorf("image %s: %w", a.ID, errors.Unwrap(err))
		}
		mime, _, _, ok := bundle.SniffImage(raw)
		if !ok {
			return nil, fmt.Errorf("image %s is not an image the store keeps", a.ID)
		}
		out = append(out, store.Attachment{ID: a.ID, ContentType: mime, Bytes: raw})
	}
	return out, nil
}

// storeOf names the store whose version of a pull request is document.
func (s *reviewStores) storeOf(repository string, number, version int, document []byte) string {
	for _, v := range s.versions[fmt.Sprintf("%s#%d", repository, number)] {
		if v.version != version {
			continue
		}
		var stored []byte
		if err := s.dbs[v.store].QueryRow(`SELECT document FROM versions WHERE repository = ? AND pr = ? AND version = ?`, repository, number, version).Scan(&stored); err == nil &&
			string(stored) == string(document) {
			return s.names[v.store]
		}
	}
	return ""
}

// historyOf names a store that holds every version of a pull request from 1
// to through, preferring the one that holds the most.
func (s *reviewStores) historyOf(repository string, number, through int) string {
	held := map[int][]int{}
	for _, v := range s.versions[fmt.Sprintf("%s#%d", repository, number)] {
		held[v.store] = append(held[v.store], v.version)
	}
	best, most := -1, 0
	for store, versions := range held {
		slices.Sort(versions)
		whole := len(versions) >= through
		for i := 0; whole && i < through; i++ {
			whole = versions[i] == i+1
		}
		if whole && (len(versions) > most || len(versions) == most && store < best) {
			best, most = store, len(versions)
		}
	}
	if best < 0 {
		return ""
	}
	return s.names[best]
}
