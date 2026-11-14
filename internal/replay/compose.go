package replay

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // the review stores' driver

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/agent/prompt"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/compose"
	"github.com/baum-ai/baum/tiles/tilestest"
)

// The sources a ComposeGenerator reads its reviews from.
const (
	// ComposeFromStore composes the dataset's saved reviews
	// (reviews/<machine>/<repository>.versions.sqlite): for each case, the
	// version generated from the case's bundle, else the latest version at
	// the case's head, else the pull request's latest version.
	ComposeFromStore = "store"
	// ComposeFromRun composes the review documents an earlier run generated
	// (<run>/cases/<id>/document.json).
	ComposeFromRun = "run"
)

// ComposeRecordName is the schema of a compose case's document.json.
const ComposeRecordName = "replay.compose/1"

// ComposeGenerator runs only the compose stage (agent.ComposeRun) over
// reviews that already exist, with no analysis: the saved reviews of the
// dataset, or the documents of an earlier run. Its document is a
// ComposeRecord; the composition, the composed tile document, the template,
// the inventory and the composer's reply are artifacts beside it.
type ComposeGenerator struct {
	// Source is ComposeFromStore or ComposeFromRun; Dataset is the dataset
	// directory for the first, Run the earlier run's directory for the
	// second.
	Source, Dataset, Run string
	// PromptOverrides replaces the composition prompt.
	PromptOverrides prompt.Overrides

	once     sync.Once
	stores   *reviewStores
	redactor *bundle.Redactor
	err      error
}

var _ interface {
	Generator
	Meter
	Definer
} = (*ComposeGenerator)(nil)

func init() {
	RegisterGenerator("compose", func(c GeneratorConfig) (Generator, error) {
		g := &ComposeGenerator{Source: c.Options["source"], Dataset: c.Options["dataset"], Run: c.Options["run"]}
		for key := range c.Options {
			if !slices.Contains([]string{"source", "dataset", "run"}, key) {
				return nil, fmt.Errorf("the compose generator takes the options source, dataset and run, not %q", key)
			}
		}
		for id, text := range c.PromptOverrides {
			if prompt.ID(id) != prompt.IDReviewTilesCompose {
				return nil, fmt.Errorf("prompt override %q is not the composition prompt %s", id, prompt.IDReviewTilesCompose)
			}
			if err := prompt.Validate(prompt.ID(id), text); err != nil {
				return nil, fmt.Errorf("prompt override %s: %w", id, err)
			}
			g.PromptOverrides = prompt.Overrides{prompt.ID(id): text}
		}
		switch {
		case g.Source == ComposeFromStore && g.Dataset == "":
			return nil, errors.New("composing saved reviews needs the dataset directory")
		case g.Source == ComposeFromRun && g.Run == "":
			return nil, errors.New("--compose-only needs --from-run <run directory>")
		case g.Source != ComposeFromStore && g.Source != ComposeFromRun:
			return nil, errors.New("the compose generator needs --compose-from-store or --compose-only --from-run <run>")
		}
		return g, nil
	})
	RegisterMeter("compose", &ComposeGenerator{})
}

func (g *ComposeGenerator) Name() string { return "compose" }

// Close closes the copied review stores and removes their directory. The
// driver calls it when a run ends; a generator never opened is a no-op.
func (g *ComposeGenerator) Close() error {
	if g.stores == nil {
		return nil
	}
	err := g.stores.Close()
	g.stores = nil
	return err
}

func (g *ComposeGenerator) prompts() prompt.TilesPrompts {
	return prompt.TilesPrompts{Overrides: g.PromptOverrides}
}

func (g *ComposeGenerator) Describe(ctx context.Context, backend string) (map[string]any, error) {
	instructions, err := g.prompts().ComposeInstructions(ctx, backend)
	if err != nil {
		return nil, err
	}
	described := map[string]any{"source": g.Source, "prompt_version": instructions.Version, "prompt_digest": instructions.Digest}
	if g.Source == ComposeFromRun {
		described["from_run"] = filepath.Base(g.Run)
	}
	return described, nil
}

func (g *ComposeGenerator) init() error {
	g.once.Do(func() {
		if g.redactor, g.err = bundle.NewRedactor(); g.err != nil {
			return
		}
		if g.Source == ComposeFromStore {
			g.stores, g.err = openReviewStores(filepath.Join(g.Dataset, "reviews"))
		}
	})
	return g.err
}

// ComposeRecord is a compose case's document: what the stage produced and
// what the metrics read.
type ComposeRecord struct {
	Schema string `json:"schema"`
	// Review says which review was composed: its pull request and version,
	// and how a saved one was matched to the case (bundle, head or latest).
	Review      ComposedReview  `json:"review"`
	Composition json.RawMessage `json:"composition"`
	// Template is the template's composition for the same review.
	Template       json.RawMessage `json:"template"`
	Fallback       bool            `json:"fallback"`
	FallbackReason string          `json:"fallback_reason,omitempty"`
	Repaired       bool            `json:"repaired"`
	// Fixes counts what normalisation fixed in the composer's layout, by kind.
	Fixes map[tiles.FixKind]int `json:"fixes,omitempty"`
	// Landing and TemplateLanding are the tiles each page shows on its landing
	// page, in order.
	Landing         []string `json:"landing"`
	TemplateLanding []string `json:"template_landing"`
	// DiffersFromTemplate says the layout differs from the template's in
	// order, sizes, groups or changes_shown.
	DiffersFromTemplate bool `json:"differs_from_template"`
	// Featured counts the current changes outside the changes group.
	Featured int `json:"featured"`
	// WastedRows and TemplateWastedRows count the landing rows each layout
	// leaves units of empty while a tile follows.
	WastedRows         int `json:"wasted_rows"`
	TemplateWastedRows int `json:"template_wasted_rows"`
	// QuietRisksHalf and TemplateQuietRisksHalf say a risks tile ranking
	// nothing above low takes half of the first row.
	QuietRisksHalf         bool `json:"quiet_risks_half"`
	TemplateQuietRisksHalf bool `json:"template_quiet_risks_half"`
	// Breaking says the review names a breaking change, and BreakingFeatured
	// that a change is featured then (a proxy for the change carrying it).
	Breaking         bool     `json:"breaking"`
	BreakingFeatured bool     `json:"breaking_featured"`
	Promotions       int      `json:"promotions"`
	Reasons          int      `json:"reasons"`
	ChangesShown     *int     `json:"changes_shown,omitempty"`
	Changes          int      `json:"changes"`
	InventoryBytes   int      `json:"inventory_bytes"`
	InventoryCut     []string `json:"inventory_cut,omitempty"`
	// Seconds is how long the stage took, its calls included.
	Seconds float64 `json:"seconds"`
	// CostUSD is what the stage cost, and Jump how many landing entries
	// moved from the provisional layout drafts showed, for a run of the
	// whole pipeline (TileGenerator); Problem says why such a run stored no
	// composition at all.
	CostUSD *float64 `json:"cost_usd,omitempty"`
	Jump    *int     `json:"jump,omitempty"`
	Problem string   `json:"problem,omitempty"`
}

// ComposedReview identifies the composed review.
type ComposedReview struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Version    int    `json:"version,omitempty"`
	Match      string `json:"match"`
}

// review returns the review document a case composes.
func (g *ComposeGenerator) review(c Case) ([]byte, ComposedReview, error) {
	if err := g.init(); err != nil {
		return nil, ComposedReview{}, err
	}
	which := ComposedReview{Repository: c.Bundle.Repository, Number: c.Bundle.PullRequest.Number}
	if g.Source == ComposeFromRun {
		raw, err := os.ReadFile(filepath.Join(g.Run, "cases", c.ID, "document.json"))
		if err != nil {
			return nil, which, fmt.Errorf("case %s: the run has no review document: %w", c.ID, errors.Unwrap(err))
		}
		which.Match = "run"
		return raw, which, nil
	}
	return g.stores.find(c)
}

// converted normalises a review and converts it.
func converted(document []byte) ([]byte, *tiles.Document, error) {
	normalized, err := review.NormalizeStored(document)
	if err != nil {
		return nil, nil, fmt.Errorf("normalising the review: %w", err)
	}
	doc, err := tiles.FromReview(normalized, tiles.Options{})
	if err != nil {
		return nil, nil, fmt.Errorf("converting the review: %w", err)
	}
	return normalized, doc, nil
}

func (g *ComposeGenerator) Generate(ctx context.Context, base generate.Options, c Case) (Generated, error) {
	document, which, err := g.review(c)
	if err != nil {
		return Generated{}, err
	}
	_, doc, err := converted(document)
	if err != nil {
		return Generated{}, fmt.Errorf("case %s: %w", c.ID, err)
	}
	// A backend that cannot complete gets no completer, so the fallback
	// says so rather than that a call failed.
	completer := generate.CompleterOf(base.Backend)
	started := time.Now()
	result, err := agent.ComposeRun(ctx, agent.ComposeRequest{Completer: completer, Backend: base.Backend.Name(), Prompts: g.prompts(), Document: doc,
		Redactor: g.redactor, CallTimeout: base.PassTimeout})
	out := Generated{Outcome: result.Outcome}
	if errors.Is(err, agent.ErrComposeInterrupted) || ctx.Err() != nil {
		return out, &generate.Error{Category: generate.CategoryInterrupted, Err: errors.Join(err, ctx.Err())}
	}
	if err != nil {
		return out, err
	}
	record, composed, template, err := recordOf(doc, result)
	if err != nil {
		return out, err
	}
	record.Review, record.Seconds = which, time.Since(started).Seconds()
	instructions, _ := g.prompts().ComposeInstructions(ctx, base.Backend.Name())
	out.PromptVersion, out.PromptDigest = instructions.Version, instructions.Digest
	if out.Document, err = json.MarshalIndent(record, "", "  "); err != nil {
		return out, err
	}
	out.Artifacts = map[string][]byte{"composition.json": record.Composition, "template.json": record.Template, "inventory.txt": []byte(result.Inventory)}
	if out.Artifacts["tiles.json"], err = json.MarshalIndent(composed, "", "  "); err != nil {
		return out, err
	}
	if out.Artifacts["template-tiles.json"], err = json.MarshalIndent(template, "", "  "); err != nil {
		return out, err
	}
	if len(result.Reply) > 0 {
		out.Artifacts["reply.json"] = result.Reply
	}
	if result.Fallback {
		out.Notes = append(out.Notes, "fallback")
	}
	if result.Repaired {
		out.Notes = append(out.Notes, "repaired")
	}
	out.Details = map[string]any{"fallback": result.Fallback, "fallback_reason": result.FallbackReason, "fixes": record.Fixes,
		"differs_from_template": record.DiffersFromTemplate, "match": which.Match}
	return out, nil
}

// recordOf measures a composition against the template of the same review.
func recordOf(doc *tiles.Document, result agent.ComposeOutcome) (ComposeRecord, *tiles.Document, *tiles.Document, error) {
	record := ComposeRecord{Schema: ComposeRecordName, Fallback: result.Fallback, FallbackReason: result.FallbackReason, Repaired: result.Repaired,
		ChangesShown: result.Composition.Layout.ChangesShown, InventoryBytes: len(result.Inventory)}
	if g := result.Composition.Generator; g != nil {
		record.Fixes = g.Fixes
	}
	template, _ := tiles.Template(doc, nil)
	var err error
	if record.Composition, err = json.MarshalIndent(result.Composition, "", "  "); err != nil {
		return record, nil, nil, err
	}
	if record.Template, err = json.MarshalIndent(template, "", "  "); err != nil {
		return record, nil, nil, err
	}
	composition := result.Composition
	composed, err := tiles.Compose(doc, &composition)
	if err != nil {
		return record, nil, nil, err
	}
	templateDoc, err := tiles.Compose(doc, &template)
	if err != nil {
		return record, nil, nil, err
	}
	record.Landing, record.TemplateLanding = landing(composed.Layout), landing(templateDoc.Layout)
	record.WastedRows, record.TemplateWastedRows = wastedRows(composed.Layout), wastedRows(templateDoc.Layout)
	record.QuietRisksHalf, record.TemplateQuietRisksHalf = quietRisksHalf(doc, composed.Layout), quietRisksHalf(doc, templateDoc.Layout)
	record.DiffersFromTemplate = !slices.Equal(composition.Layout.Entries, template.Layout.Entries) ||
		!equalInt(composition.Layout.ChangesShown, template.Layout.ChangesShown) || len(composition.Promotions) > 0
	record.Promotions = len(composition.Promotions)
	if composition.Notes != nil {
		record.Reasons = len(composition.Notes.Why)
	}
	current := map[string]bool{}
	for _, change := range doc.Changes {
		var derived struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(change.Derived, &derived)
		current[change.ID] = derived.State != "stale"
	}
	for _, tile := range doc.Tiles {
		if tile.Type == tiles.TypeOverview {
			var data tiles.OverviewData
			_ = json.Unmarshal(tile.Data, &data)
			var text string
			record.Breaking = json.Unmarshal(data.BreakingChange, &text) == nil && text != ""
		}
	}
	for _, entry := range composition.Layout.Entries {
		if current[entry.Tile] {
			record.Changes++
			if entry.Group == "" {
				record.Featured++
			}
		}
	}
	record.BreakingFeatured = record.Breaking && record.Featured > 0
	if result.Inventory != "" {
		var inv struct {
			Cut []string `json:"cut"`
		}
		_ = json.Unmarshal([]byte(result.Inventory[strings.Index(result.Inventory, "\n")+1:]), &inv)
		record.InventoryCut = inv.Cut
	}
	return record, composed, templateDoc, nil
}

// wastedRows counts the landing rows a layout leaves units of empty while a
// tile follows them.
func wastedRows(layout tiles.Layout) int {
	rows := compose.LandingRows(layout)
	wasted := 0
	for i, row := range rows {
		if row.Empty > 0 && (i < len(rows)-1 || tiles.More(layout) > 0) {
			wasted++
		}
	}
	return wasted
}

// quietRisksHalf reports whether a risks tile ranking nothing above low takes
// half of the landing page's first row.
func quietRisksHalf(doc *tiles.Document, layout tiles.Layout) bool {
	for _, tile := range doc.Tiles {
		if tile.Type == tiles.TypeRisks && !slices.Contains(tiles.SizesIn(doc, tile), tiles.SizeThird) {
			return false
		}
	}
	rows := compose.LandingRows(layout)
	if len(rows) == 0 || !slices.Contains(rows[0].Tiles, tiles.TypeRisks) {
		return false
	}
	i := slices.IndexFunc(layout.Entries, func(e tiles.Entry) bool { return e.Tile == tiles.TypeRisks })
	return i >= 0 && layout.Entries[i].Size == tiles.SizeHalf
}

func equalInt(a, b *int) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// landing lists the tiles a layout shows on its landing page.
func landing(layout tiles.Layout) []string {
	visible := tiles.Visible(layout)
	shown := []string{}
	for i, entry := range layout.Entries {
		if visible[i] {
			shown = append(shown, entry.Tile)
		}
	}
	return shown
}

// Validate checks the record's composition as a host would before storing
// it: it passes the composition schema's rules as NormalizeComposition leaves
// them (normalising it again changes nothing), and the tile document composed
// from it passes the tile checks and every invariant of a generated document
// (tilestest.CheckGenerated).
func (g *ComposeGenerator) Validate(document []byte, c Case) []string {
	var record ComposeRecord
	if err := json.Unmarshal(document, &record); err != nil || record.Schema != ComposeRecordName {
		return []string{"the document is not a compose record"}
	}
	raw, _, err := g.review(c)
	if err != nil {
		return []string{err.Error()}
	}
	return checkComposition(raw, record.Composition)
}

// checkComposition checks a composition of a review document as a host
// would before storing it (see ComposeGenerator.Validate).
func checkComposition(document, raw []byte) []string {
	composition, err := tiles.ParseComposition(raw)
	if err != nil {
		return []string{err.Error()}
	}
	normalized, doc, err := converted(document)
	if err != nil {
		return []string{err.Error()}
	}
	var findings []string
	again := *composition
	again.Generator = nil
	if _, report := tiles.NormalizeComposition(doc, &again); report.Total > 0 {
		findings = append(findings, fmt.Sprintf("normalising the stored composition again fixes %d things", report.Total))
	}
	composed, err := tiles.Compose(doc, composition)
	if err != nil {
		return append(findings, err.Error())
	}
	encoded, err := json.Marshal(composed)
	if err != nil {
		return append(findings, err.Error())
	}
	if _, err := tiles.NewValidator().Validate(encoded); err != nil {
		findings = append(findings, err.Error())
	}
	return append(findings, tilestest.CheckGenerated(normalized, composition, composed)...)
}

// The compose metrics.
const (
	MetricComposeFallback = "compose_fallback"
	MetricComposeRepaired = "compose_repaired"
	MetricComposeFixes    = "compose_fixes"
	MetricComposeDiffers  = "compose_differs_from_template"
	MetricComposeLanding  = "compose_landing_differs"
	MetricComposeFeatured = "compose_featured_changes"
	MetricComposePromoted = "compose_promotions"
	MetricComposeReasons  = "compose_reasons"
	MetricComposeTiles    = "compose_landing_tiles"
	MetricComposeBytes    = "compose_inventory_bytes"
	MetricComposeCut      = "compose_inventory_cut"
	MetricComposeSeconds  = "compose_seconds"
	MetricComposeWasted   = "compose_wasted_rows"
	MetricComposeQuiet    = "compose_quiet_risks_half"
	MetricComposeBreaking = "compose_breaking_featured"
	// metricTemplate prefixes the template's own value of a metric.
	metricTemplate = "template_"
	// metricComposeFix prefixes the count of one kind of fix.
	metricComposeFix = "compose_fix_"
)

// DocumentMetrics measures a compose record.
func (g *ComposeGenerator) DocumentMetrics(document []byte) (map[string]float64, error) {
	var record ComposeRecord
	if err := json.Unmarshal(document, &record); err != nil || record.Schema != ComposeRecordName {
		return nil, errors.New("the document is not a compose record")
	}
	flag := func(b bool) float64 {
		if b {
			return 1
		}
		return 0
	}
	values := map[string]float64{
		MetricComposeFallback: flag(record.Fallback), MetricComposeRepaired: flag(record.Repaired), MetricComposeDiffers: flag(record.DiffersFromTemplate),
		MetricComposeLanding: flag(!slices.Equal(record.Landing, record.TemplateLanding)), MetricComposeFeatured: float64(record.Featured),
		MetricComposePromoted: float64(record.Promotions), MetricComposeReasons: float64(record.Reasons), MetricComposeTiles: float64(len(record.Landing)),
		MetricComposeBytes: float64(record.InventoryBytes), MetricComposeCut: flag(len(record.InventoryCut) > 0), MetricComposeSeconds: record.Seconds,
	}
	values[MetricComposeWasted], values[metricTemplate+MetricComposeWasted] = float64(record.WastedRows), float64(record.TemplateWastedRows)
	values[MetricComposeQuiet], values[metricTemplate+MetricComposeQuiet] = flag(record.QuietRisksHalf), flag(record.TemplateQuietRisksHalf)
	if record.Breaking {
		values[MetricComposeBreaking] = flag(record.BreakingFeatured)
	}
	total := 0
	for kind, n := range record.Fixes {
		values[metricComposeFix+string(kind)] = float64(n)
		total += n
	}
	values[MetricComposeFixes] = float64(total)
	return values, nil
}

// PairMetrics is not measured for compositions: a compose run has no chains.
func (g *ComposeGenerator) PairMetrics(_, _ []byte) (map[string]float64, error) {
	return nil, errors.New("compose runs are not chained")
}

// Definitions says what each compose metric means.
func (g *ComposeGenerator) Definitions() map[string]string {
	return map[string]string{
		MetricComposeFallback: "1 when the composition is the template because the stage could not use the composer's layout (see the record's fallback_reason)",
		MetricComposeRepaired: "1 when an invalid reply needed the repair call",
		MetricComposeFixes:    "fixes normalisation made to the composer's layout (compose_fix_<kind> by kind)",
		MetricComposeDiffers:  "1 when the layout differs from the template's",
		MetricComposeLanding:  "1 when the landing page shows other tiles, or in another order, than the template's",
		MetricComposeFeatured: "current changes placed outside the changes group",
		MetricComposePromoted: "blocks promoted to tiles of their own",
		MetricComposeReasons:  "tiles the composer gave a reason for",
		MetricComposeTiles:    "tiles on the landing page",
		MetricComposeBytes:    "bytes of the composer's user message",
		MetricComposeCut:      "1 when the inventory was cut to fit its budget",
		MetricComposeSeconds:  "seconds the compose stage took",
		MetricComposeWasted:   "landing rows left with empty units while a tile follows (template_ prefix: the template's)",
		MetricComposeQuiet:    "1 when a risks tile ranking nothing above low takes half of the first row (template_ prefix: the template's)",
		MetricComposeBreaking: "on reviews with a breaking change: 1 when a change is featured",
	}
}

// reviewStores indexes the dataset's saved review versions by pull request.
// The stores are copied, with their write-ahead logs, and the copies opened
// read-only; the dataset is never opened for writing.
type reviewStores struct {
	dir      string
	dbs      []*sql.DB
	versions map[string][]storedVersion
}

type storedVersion struct {
	store        int
	version      int
	bundleDigest string
	head         string
}

func openReviewStores(dir string) (*reviewStores, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".versions.sqlite") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("the dataset's saved reviews: %w", err)
	}
	if len(paths) == 0 {
		return nil, errors.New("the dataset has no saved reviews (reviews/*/*.versions.sqlite)")
	}
	slices.Sort(paths)
	tmp, err := os.MkdirTemp("", "baum-replay-reviews-")
	if err != nil {
		return nil, err
	}
	s := &reviewStores{dir: tmp, versions: map[string][]storedVersion{}}
	opened := false
	defer func() {
		if !opened {
			_ = s.Close()
		}
	}()
	for n, path := range paths {
		copied := filepath.Join(tmp, fmt.Sprintf("%d.sqlite", n))
		for _, suffix := range []string{"", "-wal", "-shm"} {
			raw, err := os.ReadFile(path + suffix)
			if errors.Is(err, fs.ErrNotExist) && suffix != "" {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("reading saved review store %d: %w", n, errors.Unwrap(err))
			}
			if err := os.WriteFile(copied+suffix, raw, 0o600); err != nil {
				return nil, err
			}
		}
		db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.ToSlash(copied)}).String()+"?mode=ro")
		if err != nil {
			return nil, err
		}
		s.dbs = append(s.dbs, db)
		rows, err := db.Query(`SELECT repository, pr, version, metadata FROM versions ORDER BY repository, pr, version`)
		if err != nil {
			return nil, fmt.Errorf("reading saved review store %d: %w", n, err)
		}
		for rows.Next() {
			var repository string
			var pr, version int
			var metadata []byte
			if err := rows.Scan(&repository, &pr, &version, &metadata); err != nil {
				_ = rows.Close()
				return nil, err
			}
			var meta struct {
				BundleDigest string `json:"bundle_digest"`
				Head         string `json:"head"`
			}
			_ = json.Unmarshal(metadata, &meta)
			key := fmt.Sprintf("%s#%d", repository, pr)
			s.versions[key] = append(s.versions[key], storedVersion{store: n, version: version, bundleDigest: meta.BundleDigest, head: meta.Head})
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	opened = true
	return s, nil
}

// Close closes the stores' copies and removes them.
func (s *reviewStores) Close() error {
	var errs []error
	for _, db := range s.dbs {
		errs = append(errs, db.Close())
	}
	s.dbs = nil
	errs = append(errs, os.RemoveAll(s.dir))
	return errors.Join(errs...)
}

// find returns the saved review a case composes.
func (s *reviewStores) find(c Case) ([]byte, ComposedReview, error) {
	which := ComposedReview{Repository: c.Bundle.Repository, Number: c.Bundle.PullRequest.Number}
	versions := s.versions[fmt.Sprintf("%s#%d", which.Repository, which.Number)]
	if len(versions) == 0 {
		return nil, which, fmt.Errorf("case %s: the dataset has no saved review of its pull request", c.ID)
	}
	var chosen *storedVersion
	for _, rule := range []struct {
		match string
		ok    func(storedVersion) bool
	}{
		{"bundle", func(v storedVersion) bool { return v.bundleDigest != "" && v.bundleDigest == c.Digest }},
		{"head", func(v storedVersion) bool { return v.head != "" && v.head == c.Bundle.PullRequest.HeadSHA }},
		{"latest", func(storedVersion) bool { return true }},
	} {
		for i := range versions {
			if rule.ok(versions[i]) && (chosen == nil || versions[i].version > chosen.version) {
				chosen = &versions[i]
			}
		}
		if chosen != nil {
			which.Match = rule.match
			break
		}
	}
	which.Version = chosen.version
	var document []byte
	err := s.dbs[chosen.store].QueryRow(`SELECT document FROM versions WHERE repository = ? AND pr = ? AND version = ?`,
		which.Repository, which.Number, chosen.version).Scan(&document)
	if err != nil {
		return nil, which, fmt.Errorf("case %s: reading its saved review: %w", c.ID, err)
	}
	return document, which, nil
}
