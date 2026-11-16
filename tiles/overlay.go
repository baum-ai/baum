package tiles

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// A layout overlay (baum.layout-overlay/1) is a person's own edits to the
// tile layout of one pull request's review: an ordered list of edit batches,
// each a few operations keyed by tile ID. It is kept on the person's
// computer, never in the saved version, and applied to whichever version is
// served (ApplyOverlay), so the edits carry over to the pull request's later
// versions. An operation whose tile or anchor a version does not have is
// dropped there, silently.
//
// Anchors are relative (before, after, first, last), never indices, because
// the generated order changes from one version to the next. A batch's
// summary is the host's own text, one line per operation that applied when it
// was made (OpSummary), never an agent's.
type Overlay struct {
	Schema string `json:"schema"`
	// Batches are oldest first.
	Batches []Batch `json:"batches"`
	// Undone are the batches an undo took off, oldest undone first: Redo puts
	// the last back. A new edit or a reset clears them. They count in the
	// overlay's bounds.
	Undone []Batch `json:"undone,omitempty"`
}

// OverlayName is the value of an overlay's "schema" field.
const OverlayName = "baum.layout-overlay/1"

// Batch is one edit: what one tool call or one action on the page changed.
// Undo removes the newest batch. A reset batch drops every batch before it
// from the replay, so undoing it brings them back.
type Batch struct {
	// ID is "edt_" and 12 lowercase hex digits, or BaseBatch.
	ID string `json:"id"`
	// At is when the batch was made, in RFC 3339.
	At string `json:"at"`
	// By is "chat" (the session panel's agent) or "page" (the person).
	By      string   `json:"by"`
	Reset   bool     `json:"reset,omitempty"`
	Ops     []Op     `json:"ops,omitempty"`
	Summary []string `json:"summary"`
}

// Op is one operation of a batch.
type Op struct {
	// Op is move, resize, hide, show, pin, unpin, changes_shown or add.
	Op string `json:"op"`
	// Tile is the tile's ID: every op but changes_shown names one.
	Tile string `json:"tile,omitempty"`
	// To is where a move (or an add) places the tile.
	To *Place `json:"to,omitempty"`
	// Size is a resize's new size, or an added tile's.
	Size Size `json:"size,omitempty"`
	// Count is how many changes the changes group shows before folding.
	Count *int `json:"count,omitempty"`
	// Add names the library type an add places.
	Add *Added `json:"add,omitempty"`
}

// Place is a relative position: exactly one of an edge (first or last), a
// tile to stand before or a tile to stand after.
type Place struct {
	Edge   string `json:"edge,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// Added names the library tile type an add places: its kind ("library"),
// its name, which is its tile's ID (LibraryTileID), and the digest of the
// definition the person added.
type Added struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// The operations.
const (
	OpMove         = "move"
	OpResize       = "resize"
	OpHide         = "hide"
	OpShow         = "show"
	OpPin          = "pin"
	OpUnpin        = "unpin"
	OpChangesShown = "changes_shown"
	OpAdd          = "add"
)

// The edges of a move.
const (
	EdgeFirst = "first"
	EdgeLast  = "last"
)

// The bounds of an overlay. A write that would make a batch past
// MaxOverlayBatches, or an overlay past MaxOverlayBytes encoded, folds the
// oldest batches into one base batch (BaseBatch), which cannot be undone.
const (
	MaxOverlayOps     = 20
	MaxOverlayBatches = 100
	MaxOverlayBytes   = 64 << 10
	// MaxChangesShown bounds changes_shown's count.
	MaxChangesShown = 50
	// BaseBatch is the ID of the batch folding holds the oldest edits in.
	BaseBatch = "edt_base"
	// maxSummaryLine bounds a summary line, in characters.
	maxSummaryLine = 200
)

var batchID = regexp.MustCompile(`^edt_[0-9a-f]{12}$`)

// ErrOverlayTooLarge is a write the overlay's bounds cannot take even once
// folded.
var ErrOverlayTooLarge = errors.New("tiles: the layout overlay would be larger than its bound")

// DropReason says why ApplyOverlay dropped an operation.
type DropReason string

// The reasons an operation is dropped.
const (
	// DropGone: the version has no tile of that ID.
	DropGone DropReason = "gone"
	// DropAnchorGone: the version has no tile the move is anchored to.
	DropAnchorGone DropReason = "anchor_gone"
	// DropObsolete: the tile or anchor is an obsolete change, which the
	// host places last.
	DropObsolete DropReason = "obsolete"
	// DropSize: the tile cannot have that size in this version (SizesIn).
	DropSize DropReason = "size"
	// DropKept: the tile must stay shown (the overview, or the risks tile
	// while it ranks an active critical or high risk).
	DropKept DropReason = "kept"
	// DropNoGroup: the version has no changes group.
	DropNoGroup DropReason = "no_group"
	// DropUnavailable: an added tile with no data for this version, or past
	// MaxLibraryTiles.
	DropUnavailable DropReason = "unavailable"
	// DropInvalid: an operation no check would let through (a move next to
	// itself, a count out of range); a stored overlay never holds one.
	DropInvalid DropReason = "invalid"
)

// OverlayReport counts what a replay applied and dropped. It names no tile,
// so it may be logged.
type OverlayReport struct {
	Applied int                `json:"applied"`
	Dropped map[DropReason]int `json:"dropped,omitempty"`
}

// DroppedTotal is how many operations the replay dropped.
func (r OverlayReport) DroppedTotal() int {
	n := 0
	for _, count := range r.Dropped {
		n += count
	}
	return n
}

func (r *OverlayReport) drop(reason DropReason) {
	if r.Dropped == nil {
		r.Dropped = map[DropReason]int{}
	}
	r.Dropped[reason]++
}

// Active returns the batches the replay applies: those after the last reset.
func (o *Overlay) Active() []Batch {
	if o == nil {
		return nil
	}
	for i := len(o.Batches) - 1; i >= 0; i-- {
		if o.Batches[i].Reset {
			return o.Batches[i+1:]
		}
	}
	return o.Batches
}

// Edited reports whether the overlay has an operation to apply.
func (o *Overlay) Edited() bool {
	return slices.ContainsFunc(o.Active(), func(b Batch) bool { return len(b.Ops) > 0 })
}

// CanUndo reports whether the overlay has a batch Undo removes.
func (o *Overlay) CanUndo() bool {
	return o != nil && len(o.Batches) > 0 && o.Batches[len(o.Batches)-1].ID != BaseBatch
}

// Last returns the newest batch Undo would remove, or nil.
func (o *Overlay) Last() *Batch {
	if !o.CanUndo() {
		return nil
	}
	last := o.Batches[len(o.Batches)-1]
	return &last
}

// With returns the overlay with the batch appended, folding the oldest
// batches into the base batch while it holds more than MaxOverlayBatches or
// encodes to more than MaxOverlayBytes. The batch is checked first. A new
// batch clears the redo list.
func (o *Overlay) With(b Batch) (Overlay, error) {
	if err := checkBatch(b); err != nil {
		return Overlay{}, err
	}
	out := Overlay{Schema: OverlayName}
	if o != nil {
		out.Batches = slices.Clone(o.Batches)
	}
	return out.appended(b, false)
}

// WithReset is With for a reset batch, which always fits: when folding
// cannot make room, the oldest batches, the base batch included, are
// dropped until it does. Undoing the reset brings back what remained.
func (o *Overlay) WithReset(b Batch) (Overlay, error) {
	if !b.Reset {
		return Overlay{}, errors.New("tiles: not a reset batch")
	}
	if err := checkBatch(b); err != nil {
		return Overlay{}, err
	}
	out := Overlay{Schema: OverlayName}
	if o != nil {
		out.Batches = slices.Clone(o.Batches)
	}
	return out.appended(b, true)
}

// appended adds b after the batches of o (which has no redo list) and
// folds, or with force drops, the oldest batches until the bounds hold.
func (o Overlay) appended(b Batch, force bool) (Overlay, error) {
	o.Batches = append(o.Batches, b)
	for {
		encoded, err := json.Marshal(o)
		if err != nil {
			return Overlay{}, err
		}
		if len(o.Batches) <= MaxOverlayBatches && len(encoded) <= MaxOverlayBytes {
			return o, nil
		}
		// The newest batch is never folded: it is the one just made.
		switch {
		case force && len(encoded) > MaxOverlayBytes && len(o.Batches) >= 2:
			o.Batches = o.Batches[1:]
		case len(o.Batches) >= 3:
			o.fold()
		default:
			return Overlay{}, ErrOverlayTooLarge
		}
	}
}

// CanRedo reports whether an undo can be taken back.
func (o *Overlay) CanRedo() bool { return o != nil && len(o.Undone) > 0 }

// NextRedo returns the batch Redo would put back, or nil.
func (o *Overlay) NextRedo() *Batch {
	if !o.CanRedo() {
		return nil
	}
	next := o.Undone[len(o.Undone)-1]
	return &next
}

// Undo returns the overlay without its newest batch, and that batch, which
// Redo can put back; ok is false when there is none to undo (CanUndo). The
// redo list is bounded as the overlay is: the oldest undone batches are
// forgotten when it would not fit.
func (o *Overlay) Undo() (Overlay, Batch, bool) {
	if !o.CanUndo() {
		return Overlay{}, Batch{}, false
	}
	undone := o.Batches[len(o.Batches)-1]
	out := Overlay{Schema: OverlayName, Batches: slices.Clone(o.Batches[:len(o.Batches)-1]), Undone: append(slices.Clone(o.Undone), undone)}
	for len(out.Undone) > 1 {
		encoded, err := json.Marshal(out)
		if err == nil && len(out.Undone) <= MaxOverlayBatches && len(encoded) <= MaxOverlayBytes {
			break
		}
		out.Undone = out.Undone[1:]
	}
	return out, undone, true
}

// Redo returns the overlay with the batch the last undo took off put back,
// and that batch; ok is false when there is none (CanRedo).
func (o *Overlay) Redo() (Overlay, Batch, bool) {
	if !o.CanRedo() {
		return Overlay{}, Batch{}, false
	}
	back := o.Undone[len(o.Undone)-1]
	out := Overlay{Schema: OverlayName, Batches: slices.Clone(o.Batches), Undone: slices.Clone(o.Undone[:len(o.Undone)-1])}
	out.Batches = append(out.Batches, back)
	if len(out.Batches) > MaxOverlayBatches {
		out.fold()
	}
	return out, back, true
}

// fold merges the two oldest batches (the base batch, if any, and the batch
// after it) into the base batch. Per tile it keeps the last move, the last
// resize, the last of hide and show, the last of pin and unpin and the last
// add, and the last changes_shown, in the order they were last made. A move
// relative to a tile moved earlier may land slightly differently once
// folded; that is accepted at this depth.
func (o *Overlay) fold() {
	first, second := o.Batches[0], o.Batches[1]
	ops := compactOps(append(slices.Clone(first.Ops), second.Ops...))
	if second.Reset {
		ops = nil
	}
	base := Batch{ID: BaseBatch, At: second.At, By: second.By, Ops: ops, Summary: []string{"Earlier layout edits"}}
	o.Batches = append([]Batch{base}, o.Batches[2:]...)
}

// compactOps keeps, of ops in order, the last of each kind per tile.
func compactOps(ops []Op) []Op {
	kind := func(op Op) string {
		switch op.Op {
		case OpHide, OpShow:
			return "visibility " + op.Tile
		case OpPin, OpUnpin:
			return "pin " + op.Tile
		case OpChangesShown:
			return OpChangesShown
		}
		return op.Op + " " + op.Tile
	}
	last := map[string]int{}
	for i, op := range ops {
		last[kind(op)] = i
	}
	var out []Op
	for i, op := range ops {
		if last[kind(op)] == i {
			out = append(out, op)
		}
	}
	return out
}

// ParseOverlay decodes a stored overlay and checks it (CheckOverlay).
func ParseOverlay(raw []byte) (*Overlay, error) {
	if len(raw) > MaxOverlayBytes {
		return nil, ErrOverlayTooLarge
	}
	var o Overlay
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, fmt.Errorf("tiles: decoding the layout overlay: %w", err)
	}
	if err := CheckOverlay(&o); err != nil {
		return nil, err
	}
	return &o, nil
}

// CheckOverlay checks an overlay's shape and bounds: its schema, at most
// MaxOverlayBatches batches each with a valid ID (BaseBatch only first),
// By chat or page, at most MaxOverlayOps operations (the base batch is
// bounded by its bytes), every operation well formed (CheckOpShape), at most
// MaxOverlayOps summary lines of at most 200 characters, and at most
// MaxOverlayBytes encoded.
func CheckOverlay(o *Overlay) error {
	if o.Schema != OverlayName {
		return fmt.Errorf("tiles: the layout overlay's schema is not %q", OverlayName)
	}
	if len(o.Batches) > MaxOverlayBatches {
		return fmt.Errorf("tiles: the layout overlay has more than %d batches", MaxOverlayBatches)
	}
	ids := map[string]bool{}
	for i, b := range o.Batches {
		if b.ID == BaseBatch && i != 0 {
			return errors.New("tiles: only the first batch of a layout overlay may be its base")
		}
		if ids[b.ID] {
			return errors.New("tiles: two batches of the layout overlay share an ID")
		}
		ids[b.ID] = true
		if err := checkBatch(b); err != nil {
			return err
		}
	}
	if len(o.Undone) > MaxOverlayBatches {
		return fmt.Errorf("tiles: the layout overlay has more than %d batches to redo", MaxOverlayBatches)
	}
	for _, b := range o.Undone {
		if b.ID == BaseBatch {
			return errors.New("tiles: the base batch cannot be redone")
		}
		if ids[b.ID] {
			return errors.New("tiles: two batches of the layout overlay share an ID")
		}
		ids[b.ID] = true
		if err := checkBatch(b); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(o)
	if err != nil {
		return err
	}
	if len(encoded) > MaxOverlayBytes {
		return ErrOverlayTooLarge
	}
	return nil
}

func checkBatch(b Batch) error {
	if b.ID != BaseBatch && !batchID.MatchString(b.ID) {
		return errors.New("tiles: a layout batch's ID is not edt_ and 12 hex digits")
	}
	if b.By != "chat" && b.By != "page" {
		return errors.New("tiles: a layout batch is by chat or page")
	}
	if len(b.At) > 64 {
		return errors.New("tiles: a layout batch's time is too long")
	}
	if b.ID != BaseBatch && len(b.Ops) > MaxOverlayOps {
		return fmt.Errorf("tiles: a layout batch has more than %d operations", MaxOverlayOps)
	}
	if b.Reset && len(b.Ops) > 0 {
		return errors.New("tiles: a reset batch has no operations")
	}
	if len(b.Summary) > MaxOverlayOps {
		return fmt.Errorf("tiles: a layout batch's summary has more than %d lines", MaxOverlayOps)
	}
	for _, line := range b.Summary {
		if utf8.RuneCountInString(line) > maxSummaryLine || strings.ContainsAny(line, "\n\r") {
			return errors.New("tiles: a layout batch's summary line is too long or not on one line")
		}
	}
	for _, op := range b.Ops {
		if err := CheckOpShape(op); err != nil {
			return err
		}
	}
	return nil
}

// CheckOpShape checks that an operation is well formed, whatever the review:
// a known op, a plain tile ID where it needs one, exactly one place for a
// move, a size for a resize, a count of 1 to MaxChangesShown, and a library
// type for an add. The error is the host's own text.
func CheckOpShape(op Op) error {
	plain := func(id string) bool { return tileID.MatchString(id) }
	switch op.Op {
	case OpMove, OpResize, OpHide, OpShow, OpPin, OpUnpin:
		if !plain(op.Tile) {
			return fmt.Errorf("a %s needs a tile ID (lowercase letters, digits and underscores)", op.Op)
		}
	case OpChangesShown:
		if op.Tile != "" {
			return errors.New("changes_shown names no tile")
		}
		if op.Count == nil || *op.Count < 1 || *op.Count > MaxChangesShown {
			return fmt.Errorf("changes_shown needs a count from 1 to %d", MaxChangesShown)
		}
	case OpAdd:
		if op.Add == nil || op.Add.Kind != "library" || !plain(op.Add.Name) || len(op.Add.Name) > 64 || len(op.Add.Digest) > 80 {
			return errors.New("an add names a library tile type")
		}
		if op.Tile != "" && op.Tile != LibraryTileID(op.Add.Name) {
			return errors.New("an add's tile is its type's tile")
		}
	default:
		return fmt.Errorf("unknown op %q: use move, resize, hide, show, pin, unpin or changes_shown", op.Op)
	}
	if op.Op == OpMove || op.Op == OpAdd && op.To != nil {
		if err := checkPlace(op.To); err != nil {
			return err
		}
		if op.To.Before == op.Tile && op.Tile != "" || op.To.After == op.Tile && op.Tile != "" {
			return errors.New("a tile cannot move next to itself")
		}
	} else if op.To != nil {
		return fmt.Errorf("a %s has no place", op.Op)
	}
	switch {
	case op.Op == OpResize && !validSize(op.Size):
		return errors.New("a resize needs a size: full, half or third")
	case op.Op == OpAdd && op.Size != "" && !validSize(op.Size):
		return errors.New("a size is full, half or third")
	case op.Op != OpResize && op.Op != OpAdd && op.Size != "":
		return fmt.Errorf("a %s has no size", op.Op)
	case op.Op != OpChangesShown && op.Count != nil:
		return fmt.Errorf("a %s has no count", op.Op)
	case op.Op != OpAdd && op.Add != nil:
		return fmt.Errorf("a %s adds nothing", op.Op)
	}
	return nil
}

func validSize(size Size) bool {
	return size == SizeFull || size == SizeHalf || size == SizeThird
}

func checkPlace(p *Place) error {
	if p == nil {
		return errors.New("a move needs a place: edge (first or last), before or after")
	}
	set := 0
	for _, value := range []string{p.Edge, p.Before, p.After} {
		if value != "" {
			set++
		}
	}
	switch {
	case set != 1:
		return errors.New("a move needs exactly one of edge (first or last), before or after")
	case p.Edge != "" && p.Edge != EdgeFirst && p.Edge != EdgeLast:
		return errors.New("an edge is first or last")
	case p.Before != "" && !tileID.MatchString(p.Before), p.After != "" && !tileID.MatchString(p.After):
		return errors.New("a move's anchor is a tile ID")
	}
	return nil
}

// ApplyOverlay replays a person's layout overlay on a version: doc is the
// tile document FromReview converted from the version's review, c the
// version's stored composition (nil for none), and added the filled library
// tiles the host holds for tiles the person added, for this version. It
// returns the composition to Compose and what the replay applied and
// dropped. It is pure, and changes none of its inputs.
//
// The base is the stored composition normalised as Compose reads it or,
// without one, the converter's own layout as a composition. Each operation
// of the active batches (Overlay.Active) then applies in order, or is
// dropped silently and counted by reason:
//
//   - move places the tile first, last, or before or after its anchor. A
//     change tile joins the changes group when its anchor is a member, and
//     leaves it otherwise; a tile that cannot be a member and is anchored
//     inside the group goes to the group's nearer edge, so the group stays
//     one block. A hidden tile that moves is shown again.
//   - resize sets a size the tile may have in this version (SizesIn), and
//     keeps its row from being grown to fill.
//   - hide and show add the tile to, or remove it from, the hidden list; the
//     overview, and the risks tile while it ranks an active critical or high
//     risk, are never hidden.
//   - pin and unpin set the entry's pin; a pinned change leaves the group.
//   - changes_shown sets how many changes the group shows before folding.
//   - add places a library tile the composition already has (generation
//     filled it), or the one added holds for this version, appended to the
//     library while it has fewer than MaxLibraryTiles; without either it is
//     dropped as unavailable.
//
// When any operation applies, the layout is marked edited, which keeps the
// guard rails from replacing it; Compose still normalises and arranges it,
// so its guarantees hold whatever the overlay: exact coverage, every item
// presented once, the overview and a severe risks tile on the landing page,
// hidden tiles reachable in the last group, and no recommended change
// folded. With no operation applied, the composition is c unchanged.
func ApplyOverlay(doc *Document, c *Composition, o *Overlay, added []LibraryTile) (Composition, OverlayReport) {
	var report OverlayReport
	unchanged := func() Composition {
		if c == nil {
			return Composition{}
		}
		return cloneComposition(c)
	}
	var ops []Op
	for _, b := range o.Active() {
		ops = append(ops, b.Ops...)
	}
	if len(ops) == 0 || doc == nil || doc.Origin.Kind != OriginConverted {
		for range ops {
			report.drop(DropInvalid)
		}
		return unchanged(), report
	}
	r := newReplay(doc, c)
	if r == nil {
		for range ops {
			report.drop(DropInvalid)
		}
		return unchanged(), report
	}
	for _, op := range ops {
		if reason := r.apply(op, added); reason != "" {
			report.drop(reason)
			continue
		}
		report.Applied++
	}
	if report.Applied == 0 {
		return unchanged(), report
	}
	return r.composition(), report
}

// replay is an overlay being applied to a version.
type replay struct {
	doc  *Document
	base Composition
	// entries, hidden and resized are the layout as edited so far.
	entries      []Entry
	hidden       []string
	resized      map[string]bool
	changesShown *int
	// tiles are the version's tiles, the composition's included, by ID.
	tiles     map[string]*Tile
	obsolete  map[string]bool
	groupable map[string]bool
	// must is what the page must show: the hard rules hold in a person's
	// edits.
	must mustBeSeen
}

func newReplay(doc *Document, c *Composition) *replay {
	r := &replay{doc: doc, must: mustSeeOf(doc), resized: map[string]bool{}, tiles: map[string]*Tile{}, obsolete: map[string]bool{}, groupable: map[string]bool{}}
	for i := range doc.Tiles {
		r.tiles[doc.Tiles[i].ID] = &doc.Tiles[i]
	}
	for _, entry := range doc.Layout.Entries {
		switch {
		case entry.Group == GroupObsolete:
			r.obsolete[entry.Tile] = true
		case entry.Group == GroupChanges && r.tiles[entry.Tile] != nil && r.tiles[entry.Tile].Type == TypeChange:
			r.groupable[entry.Tile] = true
		}
	}
	if c.Empty() {
		// The converter's own layout, as a composition.
		r.base = Composition{Schema: CompositionName}
		for _, entry := range doc.Layout.Entries {
			if !r.obsolete[entry.Tile] {
				r.entries = append(r.entries, entry)
			}
		}
		for _, group := range doc.Layout.Groups {
			if group.ID == GroupChanges && group.CollapseAfter != nil {
				r.changesShown = new(*group.CollapseAfter)
			}
		}
		return r
	}
	cm := newComposer(doc, modeRead)
	n, err := cm.normalize(c)
	if err != nil {
		return nil
	}
	r.base = n
	r.entries = slices.Clone(n.Layout.Entries)
	r.hidden = slices.Clone(n.Layout.Hidden)
	for _, id := range n.Layout.Resized {
		r.resized[id] = true
	}
	if n.Layout.ChangesShown != nil {
		r.changesShown = new(*n.Layout.ChangesShown)
	}
	for _, cands := range [][]candidate{cm.library, cm.promoted, cm.authored} {
		for i := range cands {
			r.tiles[cands[i].tile.ID] = &cands[i].tile
		}
	}
	return r
}

func (r *replay) index(id string) int {
	return slices.IndexFunc(r.entries, func(e Entry) bool { return e.Tile == id })
}

// missing says why an op on a tile that has no entry is dropped.
func (r *replay) missing(id string) DropReason {
	if r.obsolete[id] {
		return DropObsolete
	}
	return DropGone
}

func (r *replay) apply(op Op, added []LibraryTile) DropReason {
	if CheckOpShape(op) != nil {
		return DropInvalid
	}
	switch op.Op {
	case OpMove:
		return r.move(op.Tile, *op.To)
	case OpResize:
		at := r.index(op.Tile)
		if at < 0 {
			return r.missing(op.Tile)
		}
		if !slices.Contains(SizesIn(r.doc, *r.tiles[op.Tile]), op.Size) {
			return DropSize
		}
		r.entries[at].Size = op.Size
		r.resized[op.Tile] = true
	case OpHide:
		if r.index(op.Tile) < 0 {
			return r.missing(op.Tile)
		}
		if r.must.kept(op.Tile) {
			return DropKept
		}
		if !slices.Contains(r.hidden, op.Tile) {
			r.hidden = append(r.hidden, op.Tile)
		}
	case OpShow:
		if r.index(op.Tile) < 0 {
			return r.missing(op.Tile)
		}
		r.hidden = slices.DeleteFunc(r.hidden, func(id string) bool { return id == op.Tile })
	case OpPin, OpUnpin:
		at := r.index(op.Tile)
		if at < 0 {
			return r.missing(op.Tile)
		}
		r.entries[at].Pin = op.Op == OpPin
		if op.Op == OpPin {
			r.entries[at].Group = ""
		}
	case OpChangesShown:
		if !slices.ContainsFunc(r.entries, func(e Entry) bool { return e.Group == GroupChanges }) {
			return DropNoGroup
		}
		r.changesShown = new(*op.Count)
	case OpAdd:
		return r.add(op, added)
	}
	return ""
}

func (r *replay) move(id string, to Place) DropReason {
	at := r.index(id)
	if at < 0 {
		return r.missing(id)
	}
	anchor := cmp.Or(to.Before, to.After)
	if anchor != "" && r.index(anchor) < 0 {
		if r.obsolete[anchor] {
			return DropObsolete
		}
		return DropAnchorGone
	}
	if to.Edge == EdgeLast && slices.Contains(r.must.named, id) {
		return DropKept
	}
	entry := r.entries[at]
	rest := slices.Delete(slices.Clone(r.entries), at, at+1)
	r.hidden = slices.DeleteFunc(r.hidden, func(hidden string) bool { return hidden == id })
	var pos int
	switch to.Edge {
	case EdgeFirst:
		entry.Group, pos = "", 0
	case EdgeLast:
		entry.Group, pos = "", len(rest)
	default:
		j := slices.IndexFunc(rest, func(e Entry) bool { return e.Tile == anchor })
		pos = j
		if to.After != "" {
			pos = j + 1
		}
		inGroup := rest[j].Group == GroupChanges
		if inGroup && r.groupable[id] && !entry.Pin {
			entry.Group = GroupChanges
			break
		}
		entry.Group = ""
		if inGroup {
			start := slices.IndexFunc(rest, func(e Entry) bool { return e.Group == GroupChanges })
			end := lastIndex(rest, GroupChanges) + 1
			if pos > start && pos < end {
				if pos-start <= end-pos {
					pos = start
				} else {
					pos = end
				}
			}
		}
	}
	r.entries = slices.Insert(rest, pos, entry)
	return ""
}

func (r *replay) add(op Op, added []LibraryTile) DropReason {
	id := LibraryTileID(op.Add.Name)
	place := func() DropReason {
		r.hidden = slices.DeleteFunc(r.hidden, func(hidden string) bool { return hidden == id })
		if op.Size != "" {
			if at := r.index(id); at >= 0 && slices.Contains(SizesIn(r.doc, *r.tiles[id]), op.Size) {
				r.entries[at].Size = op.Size
				r.resized[id] = true
			}
		}
		if op.To != nil {
			return r.move(id, *op.To)
		}
		return ""
	}
	if tile := r.tiles[id]; tile != nil && r.index(id) >= 0 {
		if tile.Type != TypeCustom {
			return DropInvalid
		}
		return place()
	}
	if len(r.base.Library) >= MaxLibraryTiles {
		return DropUnavailable
	}
	for _, lib := range added {
		def, _ := defaultValidator.CheckDefinition(lib.Definition)
		if def.Name != op.Add.Name {
			continue
		}
		r.base.Library = append(slices.Clone(r.base.Library), lib)
		tile := &Tile{ID: id, Type: TypeCustom, Definition: lib.Definition}
		r.tiles[id] = tile
		size := AllowedSizes(*tile)[0]
		r.entries = append(r.entries, Entry{Tile: id, Size: size})
		return place()
	}
	return DropUnavailable
}

// composition is the base with the edited layout, marked edited.
func (r *replay) composition() Composition {
	out := cloneComposition(&r.base)
	out.Schema = CompositionName
	out.Layout = CompositionLayout{Entries: nonNil(slices.Clone(r.entries)), Hidden: slices.Clone(r.hidden), Edited: true}
	if r.changesShown != nil {
		out.Layout.ChangesShown = new(*r.changesShown)
	}
	for _, id := range slices.Sorted(maps.Keys(r.resized)) {
		if r.index(id) >= 0 {
			out.Layout.Resized = append(out.Layout.Resized, id)
		}
	}
	return out
}

// CheckOp says why an operation would be refused on the document a person
// is shown (the served, composed document with their overlay applied), in
// the host's own words, or "" when it applies there. The rules are those of
// the replay (ApplyOverlay), plus what would not change the page: a tile
// that already has that size, pin or visibility, and a move of the overview,
// or of the risks tile while it ranks an active critical or high risk, off
// the landing page, which would not happen.
func CheckOp(doc *Document, op Op) string {
	if err := CheckOpShape(op); err != nil {
		return capitalize(err.Error())
	}
	byID := map[string]*Tile{}
	for i := range doc.Tiles {
		byID[doc.Tiles[i].ID] = &doc.Tiles[i]
	}
	entries := map[string]Entry{}
	visible := Visible(doc.Layout)
	landing := map[string]bool{}
	for i, entry := range doc.Layout.Entries {
		entries[entry.Tile] = entry
		landing[entry.Tile] = visible[i]
	}
	title := func(id string) string { return TileTitle(doc, id) }
	unknown := func(id string) string {
		return fmt.Sprintf("No tile %s on this review; tile_layout lists them", id)
	}
	must := mustSeeOf(doc)
	keeps := func(id string) string {
		if !must.kept(id) {
			return ""
		}
		switch id {
		case TypeOverview:
			return "The overview always stays on the landing page"
		case TypeRisks:
			return "The risks tile stays on the landing page while it lists a critical or high risk"
		}
		return "baum's recommendation points at this change, so it stays on the page"
	}
	if op.Op == OpChangesShown {
		group := false
		for _, entry := range doc.Layout.Entries {
			group = group || entry.Group == GroupChanges
		}
		if !group {
			return "This review has no changes group"
		}
		for _, g := range doc.Layout.Groups {
			if g.ID == GroupChanges && g.CollapseAfter != nil && *g.CollapseAfter == *op.Count {
				return fmt.Sprintf("The changes group already shows %d before folding", *op.Count)
			}
		}
		return ""
	}
	if op.Op == OpAdd {
		id := LibraryTileID(op.Add.Name)
		entry, ok := entries[id]
		switch {
		case !ok || byID[id] == nil || byID[id].Type != TypeCustom:
			return "Adding a tile that has to be filled is not available yet; show a hidden tile with show"
		case entry.Group != GroupHidden:
			return fmt.Sprintf("%s is already on the review; move it with move", title(id))
		}
		return ""
	}
	entry, ok := entries[op.Tile]
	if !ok || byID[op.Tile] == nil {
		return unknown(op.Tile)
	}
	if entry.Group == GroupObsolete {
		return fmt.Sprintf("%s is a change an earlier version had; it always stays last", title(op.Tile))
	}
	hidden := entry.Group == GroupHidden
	switch op.Op {
	case OpMove:
		anchor := cmp.Or(op.To.Before, op.To.After)
		if anchor != "" {
			a, ok := entries[anchor]
			switch {
			case !ok:
				return unknown(anchor)
			case a.Group == GroupObsolete:
				return fmt.Sprintf("%s is a change an earlier version had, which always stays last; move next to another tile", title(anchor))
			case a.Group == GroupHidden:
				return fmt.Sprintf("%s is hidden; show it first, or move next to another tile", title(anchor))
			}
		}
		if reason := keeps(op.Tile); reason != "" && (op.To.Edge == EdgeLast || anchor != "" && !landing[anchor]) {
			return reason
		}
	case OpResize:
		sizes := SizesIn(doc, *byID[op.Tile])
		if !slices.Contains(sizes, op.Size) {
			names := make([]string, len(sizes))
			for i, size := range sizes {
				names[i] = string(size)
			}
			return fmt.Sprintf("%s can be %s, not %s", title(op.Tile), strings.Join(names, ", "), op.Size)
		}
		if entry.Size == op.Size && !hidden {
			return fmt.Sprintf("%s is already %s", title(op.Tile), sizeWords(op.Size))
		}
	case OpHide:
		if reason := keeps(op.Tile); reason != "" {
			return reason
		}
		if hidden {
			return fmt.Sprintf("%s is already hidden", title(op.Tile))
		}
	case OpShow:
		if !hidden {
			return fmt.Sprintf("%s is not hidden", title(op.Tile))
		}
	case OpPin:
		if entry.Pin {
			return fmt.Sprintf("%s is already pinned", title(op.Tile))
		}
	case OpUnpin:
		if !entry.Pin {
			return fmt.Sprintf("%s is not pinned", title(op.Tile))
		}
	}
	return ""
}

func capitalize(text string) string {
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

// TileTitle is a tile's title as a summary names it: on one line, at most 80
// characters, its ID when it has none or the document has no such tile.
func TileTitle(doc *Document, id string) string {
	for _, tile := range doc.Tiles {
		if tile.ID == id {
			title := strings.Join(strings.Fields(stripControls(tile.Title, false)), " ")
			if title == "" {
				return id
			}
			return truncate(title, MaxComposedTitle)
		}
	}
	return id
}

func sizeWords(size Size) string {
	switch size {
	case SizeFull:
		return "full width"
	case SizeHalf:
		return "half width"
	case SizeThird:
		return "a third wide"
	}
	return string(size)
}

// OpSummary is the host's line for an operation a person's edit applied, in
// the words of the page shown (doc, before the edit): "Moved Risks to the
// top", "Removed Context".
func OpSummary(doc *Document, op Op) string {
	title := func(id string) string { return TileTitle(doc, id) }
	var line string
	switch op.Op {
	case OpMove:
		switch {
		case op.To.Edge == EdgeFirst:
			line = fmt.Sprintf("Moved %s to the top", title(op.Tile))
		case op.To.Edge == EdgeLast:
			line = fmt.Sprintf("Moved %s to the end", title(op.Tile))
		case op.To.Before != "":
			line = fmt.Sprintf("Moved %s before %s", title(op.Tile), title(op.To.Before))
		default:
			line = fmt.Sprintf("Moved %s after %s", title(op.Tile), title(op.To.After))
		}
	case OpResize:
		line = fmt.Sprintf("Made %s %s", title(op.Tile), sizeWords(op.Size))
	case OpHide:
		line = fmt.Sprintf("Removed %s", title(op.Tile))
	case OpShow:
		line = fmt.Sprintf("Brought back %s", title(op.Tile))
	case OpPin:
		line = fmt.Sprintf("Pinned %s", title(op.Tile))
	case OpUnpin:
		line = fmt.Sprintf("Unpinned %s", title(op.Tile))
	case OpChangesShown:
		line = fmt.Sprintf("Showing %d changes before folding", *op.Count)
	case OpAdd:
		line = fmt.Sprintf("Added %s", title(LibraryTileID(op.Add.Name)))
	}
	return truncate(line, maxSummaryLine)
}
