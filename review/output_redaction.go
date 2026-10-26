package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	bundlepkg "github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/redact"
)

// OutputRedactor masks secret spans in what a model returned, before it is
// stored or shown: a review document, walked beside the authored contract so
// that references and closed values keep their exact text, or any other text.
// It counts the spans it masked across its calls and is not safe for
// concurrent use.
type OutputRedactor struct {
	o outputRedaction
}

// NewOutputRedactor returns a redactor for output about the bundle, which must
// be redacted already; with a nil bundle no reference resolves. checkout lists
// the repository paths of the run's checkout, either tree. With a nil
// validator a document has no contract: every string, keys included, is free
// text.
func NewOutputRedactor(v *DocumentValidator, redactor *bundlepkg.Redactor, b *bundlepkg.Bundle, checkout map[string]bool) (*OutputRedactor, error) {
	o := outputRedaction{redactor: redactor, bundle: b, checkout: checkout}
	if v != nil {
		o.schema = v.strict
	}
	o.defs, _ = o.schema["$defs"].(map[string]any)
	var err error
	if o.inherited, err = inheritedTileSources(b); err != nil {
		return nil, err
	}
	return &OutputRedactor{o: o}, nil
}

// Document redacts one document. A document with nothing to mask is returned
// byte for byte, and output that is not JSON is redacted as text.
func (r *OutputRedactor) Document(ctx context.Context, document []byte) ([]byte, error) {
	r.o.ctx = ctx
	return r.o.document(document)
}

// Identifiers masks the secrets in the identifiers of a document as the model
// wrote it, and must run before DocumentValidator.Prepare. Prepare respells
// every identifier, category and tile-local name in lowercase with
// underscores, and a secret respelled that way (sk-ant-api03-AbC... as
// sk_ant_api03_abc...) is one no rule detects any more, so Document cannot
// find it after Prepare. The values scanned are those normalization respells,
// taken from its own walk, each bounded by the maxLength of its respelling.
// Each is scanned as maskClosed scans a closed value, whole and from after
// every run of characters that are not letters or digits (the runs Prepare
// respells as one underscore), and is masked in place with the
// format-preserving mask, which Prepare respells to x, 9 and underscores. A
// value whose respelling is over its maxLength, which validation rejects
// anyway, is scanned the same way, in smaller windows past its first
// closedCopyLimit restarts, then masked whole, counted only if a secret was
// found in it.
//
// The same secret may be written in another identifier in a spelling that
// no rule detects, in upper case or already respelled. So the respelling of
// each finding in an identifier is a source, as is the key a rule detects
// inside it without the host or tail it took along (see addSources and
// keySources), and every identifier whose respelling holds a source as whole
// words is masked whole and counted. Free text is not scanned here, and
// Document masks it after Prepare, but its words that respell to a source
// are masked here (see freeSpellings). Every other string value of the
// document equal to a masked identifier is masked as it is, so a reference
// to it still matches.
//
// A value masked for a secret counts in Masked as its spans and once in
// MaskedClosed, however many places hold it, and a host rejects the run when
// MaskedClosed is not zero; a value with nothing detected never counts. A
// document with nothing to mask, or one that is not JSON, is returned byte for
// byte; Prepare rejects the latter and Document redacts it as text.
//
// A host calls Identifiers, then Prepare, then Document on what Prepare
// returned, all with one OutputRedactor, and checks MaskedClosed last.
func (r *OutputRedactor) Identifiers(ctx context.Context, document []byte) ([]byte, error) {
	r.o.ctx = ctx
	return r.o.identifiers(document)
}

// Text redacts a string that is not a document.
func (r *OutputRedactor) Text(ctx context.Context, s string) (string, error) {
	r.o.ctx = ctx
	return r.o.text(s)
}

// Masked returns the number of spans masked so far.
func (r *OutputRedactor) Masked() int { return r.o.masked }

// MaskedClosed returns the number of values masked so far that a schema closes
// by pattern or date-time format: identifiers, categories, SHAs and the like,
// whether Document found the secret or Identifiers found it in the value as
// the model wrote it. They are free lowercase text, so a secret the model
// copied into one is masked in place, but a value that held one is not what
// the model was asked to write, and a host rejects the run when this is not
// zero. Each value counts once, however many spans it holds; the spans are in
// Masked as well.
func (r *OutputRedactor) MaskedClosed() int { return r.o.maskedClosed }

type outputRedaction struct {
	ctx      context.Context
	redactor *bundlepkg.Redactor
	schema   map[string]any
	defs     map[string]any
	bundle   *bundlepkg.Bundle
	// inherited are the previous review's code sources.
	inherited []tileCode
	// checkout lists the repository paths of the run's checkout.
	checkout map[string]bool
	// slots are the strings of the document being walked.
	slots    []outputSlot
	patterns map[string]*regexp.Regexp
	masked   int
	// maskedClosed counts the values a pattern or date-time closed that were
	// masked.
	maskedClosed int
	changed      bool
	// sources are the respelled findings Identifiers collects in
	// identifiers, nil otherwise, and pending the finding texts keySources
	// has yet to trim, each up to sourceBytesMax bytes in all; scanned is the
	// text the closed-value and source scans joined, which tests read to
	// bound their cost.
	sources                   map[string]bool
	pending                   []string
	sourceBytes, pendingBytes int
	scanned                   int
	// hashSeed keys the windows closedCopies skips as seen.
	hashSeed maphash.Seed
	seeded   bool
}

func (o *outputRedaction) text(s string) (string, error) {
	if s == "" {
		return s, nil
	}
	redacted, count, err := o.redactor.RedactString(o.ctx, s)
	if err != nil {
		return "", err
	}
	o.masked += count
	o.changed = o.changed || redacted != s
	return redacted, nil
}

// document redacts one JSON document against the authored contract and
// re-encodes it only when a string changed. Output that is not JSON is
// redacted as text.
func (o *outputRedaction) document(document []byte) ([]byte, error) {
	if len(document) == 0 {
		return document, nil
	}
	if !json.Valid(document) {
		redacted, err := o.text(string(document))
		return []byte(redacted), err
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	o.changed, o.slots = false, nil
	o.value(value, o.schema, "", nil, slotFree, func(s string) { value = s })
	if err := o.maskSlots(); err != nil || !o.changed {
		return document, err
	}
	return json.Marshal(value)
}

// slotKind says what may happen to a string of a document.
type slotKind int

const (
	// slotFree is model-written text, masked wherever a span touches it.
	slotFree slotKind = iota
	// slotBundle is an excerpt line the bundle supplied: it keeps its text
	// unless a span reaching into model-written text touches it.
	slotBundle
	// slotKept keeps its exact text: a reference that resolves to something
	// the redacted bundle or the checkout holds, a key the schema names, a
	// canonical hunk header, or a value the schema fixes (a constant or an
	// enum member).
	slotKept
	// slotClosed is context for the strings around it, like slotKept, but kept
	// only because it fully matches a closed pattern or is a date-time. Such a
	// pattern still admits any lowercase text, so the value is also scanned
	// on its own (see maskClosed).
	slotClosed
)

// outputSlot is one string of a document: its text, its kind, and how to
// replace it. Every slot takes part in detection.
type outputSlot struct {
	text string
	kind slotKind
	// key marks an object key, whose setter renames its entry; paired marks
	// one whose value is a string, the slot right after it.
	key, paired bool
	// limit is a slotClosed value's maxLength, or 0 when its schema sets
	// none, and length is the length it bounds: the value's own, or for an
	// identifier as the model wrote it, its respelling's.
	limit, length int
	// counted marks a closed value a finding masked and counted.
	counted bool
	set     func(string)
}

// maskedWhole reports a closed value maskClosed masks whole, after a scan
// whose later restarts get smaller windows: one longer than its maxLength,
// which validation rejects, or, without a maxLength, one with more than
// closedCopyLimit restarts, which fits none of the schema's patterns.
func (s outputSlot) maskedWhole() bool {
	if s.limit > 0 {
		return s.length > s.limit
	}
	return scanRestarts(s.text) > closedCopyLimit
}

// errOutputMaskLength reports a mask that changed the length of the text,
// which would misplace every string after it.
var errOutputMaskLength = errors.New("output redaction changed the text length")

// value walks a document beside its schema, in a fixed order (object keys
// sorted, arrays in order), and collects every string for maskSlots. A string
// keeps its exact text only when it is a reference that resolves (see
// resolvedReference) or its value meets the schema's closing constraint (see
// closedValue), though a value closed only by a pattern or date-time is still
// scanned on its own; every other string is free text. Within a code source's
// excerpt, excerpt holds the lines the bundle supplies, whose text is the
// bundle's, and a canonical hunk header holds only line numbers.
func (o *outputRedaction) value(value any, schema map[string]any, scope string, excerpt map[tileLine]bool, kind slotKind, set func(string)) {
	ref, _ := schema["$ref"].(string)
	def := strings.TrimPrefix(ref, "#/$defs/")
	schema = resolveSchema(schema, o.defs)
	switch v := value.(type) {
	case map[string]any:
		if branch := schemaBranch(o.defs, v, schema); branch != nil {
			schema = branch
		}
		props, _ := schema["properties"].(map[string]any)
		if typeSchema, _ := props["type"].(map[string]any); typeSchema != nil {
			if constant, ok := typeSchema["const"].(string); ok && v["type"] == constant {
				scope = constant
			}
		}
		switch def {
		case "tileCodeFile":
			scope, excerpt = def, o.excerptLines(v)
		case "contextRef":
			scope = def
		}
		bundleLine := def == "tileCodeLine" && excerpt != nil && excerpt[exactLine(v)]
		for _, key := range slices.Sorted(maps.Keys(v)) {
			child := v[key]
			prop, _ := props[key].(map[string]any)
			// A key the schema names is closed; any other key is model-written
			// text, scanned just before its value and renamed if masked.
			entry := &mapEntry{object: v, key: key}
			keyKind := slotFree
			if prop != nil {
				keyKind = slotKept
			}
			_, paired := child.(string)
			o.slots = append(o.slots, outputSlot{text: key, kind: keyKind, key: true, paired: paired, set: entry.rename})
			kind := slotFree
			if text, ok := child.(string); ok && prop != nil {
				switch {
				case o.resolvedReference(scope, key, text), excerpt != nil && key == "header" && canonicalHunkHeader(text):
					kind = slotKept
				case bundleLine && key == "text":
					kind = slotBundle
				}
			}
			o.value(child, prop, key, excerpt, kind, entry.set)
		}
	case []any:
		items, _ := schema["items"].(map[string]any)
		for i, child := range v {
			o.value(child, items, scope, excerpt, slotFree, func(s string) { v[i] = s })
		}
	case string:
		limit := 0
		if kind == slotFree && schema != nil {
			kind, limit = o.closedValue(schema, v)
		}
		o.slots = append(o.slots, outputSlot{text: v, kind: kind, limit: limit, length: len(v), set: set})
	}
}

// mapEntry is one entry of a document object, followed through a rename of
// its key so that its value's setter still finds it.
type mapEntry struct {
	object  map[string]any
	key     string
	dropped bool
}

// rename moves the entry to its masked key. An entry whose masked key
// another entry already has is dropped.
func (e *mapEntry) rename(key string) {
	if e.dropped || key == e.key {
		return
	}
	value := e.object[e.key]
	delete(e.object, e.key)
	if _, taken := e.object[key]; taken {
		e.dropped = true
		return
	}
	e.object[key], e.key = value, key
}

func (e *mapEntry) set(value string) {
	if !e.dropped {
		e.object[e.key] = value
	}
}

// resolvedReference reports a reference value that names something the
// redacted bundle or the checkout holds, so its text came from there:
//   - a code source's, file anchor's or context reference's path: a file in
//     either checkout tree, or a path or old path of the bundle's diff;
//   - a pr_comment ID: a conversation item; a bot_report ID: one that is a
//     bot report; a review_thread ID: a thread; a check ID: a check;
//   - a prompt's or session event's session: a session's root session ID; a
//     session event's event: a command event of that session;
//   - an issue key: a reference value the bundle extracted.
//
// An issue's system and a prompt's input name nothing on their own, so they
// are free text, as is every value that does not resolve.
func (o *outputRedaction) resolvedReference(scope, key, value string) bool {
	b := o.bundle
	if b == nil || value == "" {
		return false
	}
	switch scope + "." + key {
	case "tileCodeFile.path", "file.path", "contextRef.path":
		return o.checkout[value] || slices.ContainsFunc(b.Diff, func(h bundlepkg.Hunk) bool { return h.Path == value || h.OldPath == value })
	case "pr_comment.id", "bot_report.id":
		return slices.ContainsFunc(b.Conversation, func(c bundlepkg.ConversationItem) bool {
			return c.ID == value && (scope == "pr_comment" || c.BotReport)
		})
	case "review_thread.id":
		return slices.ContainsFunc(b.Threads, func(t bundlepkg.Thread) bool { return t.ID == value })
	case "check.id":
		return slices.ContainsFunc(b.Checks, func(c bundlepkg.Check) bool { return c.ID == value })
	case "prompt.session", "session_event.session":
		return slices.ContainsFunc(b.Sessions, func(s bundlepkg.Session) bool { return s.RootSessionID == value })
	case "session_event.event":
		return slices.ContainsFunc(b.Sessions, func(s bundlepkg.Session) bool {
			return slices.ContainsFunc(s.CommandEvents, func(e bundlepkg.CommandEvent) bool { return e.ID == value })
		})
	case "issue.key":
		return slices.ContainsFunc(b.References, func(r bundlepkg.Reference) bool { return r.Value == value })
	}
	return false
}

// maskSlots redacts a document's strings as one text: in walk order, joined
// by newlines. A secret the document spreads over several strings, such as a
// key quoted one code line, table row or step at a time, is found as a whole,
// and every free-text string a masked span touches is masked in place. The
// mask keeps the text's length, so each string is cut back out at its own
// offsets.
//
// Object keys are strings of the text too, each just before its value: a key
// the schema names is kept, any other is free text, and a masked key renames
// its entry (dropping it should the masked key already be taken).
//
// The text is scanned twice: with every string, so kept strings are context
// for the strings around them and a key for its value, and with only the
// values that are not kept, so the identifiers, constants and keys that sit
// between, say, a document's steps do not break up a secret written across
// them. A string is masked where either scan masks it, and the larger scan's
// count is recorded.
//
// A kept string never changes, except that a value closed only by a pattern or
// date-time is then masked on its own (see maskClosed). An excerpt line the bundle supplied keeps its
// text unless a span reaching into model-written text touches it: the bundle
// was redacted already, and the line must keep matching it. A bundle line
// that such a span does mask no longer matches the bundle, so completion
// rejects the document: the model can fail its own run this way, never keep
// a secret.
func (o *outputRedaction) maskSlots() error {
	if len(o.slots) == 0 {
		return nil
	}
	var all, unkept []int
	for i, slot := range o.slots {
		all = append(all, i)
		if slot.kind != slotKept && slot.kind != slotClosed && !slot.key {
			unkept = append(unkept, i)
		}
	}
	withContext, contextCount, err := o.scan(all, true)
	if err != nil {
		return err
	}
	withoutKept, unkeptCount, err := o.scan(unkept, false)
	if err != nil {
		return err
	}
	o.masked += max(contextCount, unkeptCount)
	var closed []int
	for i, slot := range o.slots {
		if slot.kind == slotClosed {
			closed = append(closed, i)
		}
		text := []byte(slot.text)
		for _, masked := range []string{withContext[i], withoutKept[i]} {
			for j := range min(len(masked), len(text)) {
				if masked[j] != slot.text[j] {
					text[j] = masked[j]
				}
			}
		}
		if string(text) != slot.text {
			slot.set(string(text))
			o.changed = true
		}
	}
	return o.maskClosed(closed)
}

// closedCopyLimit is how many restarts of a value maskedWhole names are
// scanned with a closedScanWindow; the rest get a closedRestartWindow. No
// value the schema closes without a maxLength has more than a few restarts
// (a date-time has six at most).
const closedCopyLimit = 16

// closedScanWindow is how much of a value a copy from a restart holds. It is
// well over the longest secret a rule detects, and the copy from the start of
// the value holds all of it, so a long value costs the scan its own length
// plus a window per restart, not a copy of every suffix.
const closedScanWindow = 4096

// closedRestartWindow is how much a copy from a restart of a value
// maskedWhole names holds past its first closedCopyLimit restarts: twice the
// longest source, so a secret that could be one is seen whole, while a value
// with a restart every few bytes costs a bounded multiple of its length.
const closedRestartWindow = 2 * closedSourceMax

// closedRestartBudget and closedRestartDocumentBudget cap the bytes of
// closedRestartWindow copies one value, and one pass over a document, may
// scan. Past either, a value's later restarts are not scanned: it is masked
// whole all the same, so an adversarial value costs a fixed amount however
// many runs it has, and a secret past the cap is masked but not found.
const (
	closedRestartBudget         = 256 << 10
	closedRestartDocumentBudget = 1 << 20
)

// closedScanBatch caps the text one scan of closed values joins, so the copies
// of a document's values never sit in memory all at once; a single copy
// longer than it is scanned in a batch of its own.
const closedScanBatch = 1 << 20

// maskClosed scans on their own the values a pattern or date-time closed,
// which the joined scans keep whole. A lowercase pattern such as a risk
// category or a prefixed ID admits any text the model copies, a secret
// included, and the prefix or the words around it can hide the secret from a
// rule that needs a word boundary (sec_sk_live_...). Each value is scanned
// whole and, a window at a time, from after every run of characters in it
// that are not letters or digits (see scanRestart and closedCopies), so
// sec_sk_live_... and sec_auth_sk_live_... both expose sk_live_...; the
// copies are one text joined by newlines, as in scan, in batches of at most
// closedScanBatch. Each finding is masked in the value with formatMaskByte,
// which keeps its length. The mask writes only x, X, 9 and the characters it
// keeps, so a masked value still fits a pattern of lowercase identifiers, but
// not one of hex digits, which then fails validation.
//
// A value within its maxLength has at most one restart per character of its
// bounded length, so its copies cost at most maxLength+1 windows. A value
// whose schema sets no maxLength (a SHA, a hunk ID, a date-time) may be valid
// at any length and is scanned whatever its length. A value maskedWhole
// names, over its maxLength or with more than closedCopyLimit restarts and no
// maxLength, is scanned the same way, its later restarts in smaller windows,
// then masked whole: validation rejects it or no pattern admits it, so
// nothing is lost.
//
// A value counts when a finding masked it: as its distinct findings in
// masked, and once in maskedClosed. A finding whose text already reads as
// its mask, such as an address the redactor wrote, is no finding, and a
// value masked whole with nothing found counts nowhere. Each finding that
// counts is also a source for Identifiers (see addSources).
func (o *outputRedaction) maskClosed(indexes []int) error {
	found := map[int][][2]int{}
	var copies []closedCopy
	var builder strings.Builder
	budget := closedRestartDocumentBudget
	flush := func() error {
		if builder.Len() == 0 {
			return nil
		}
		joined := builder.String()
		builder.Reset()
		o.scanned += len(joined)
		spans, err := o.redactor.Find(o.ctx, joined)
		if err != nil {
			return err
		}
		// Each finding is cut back to the copies it lies in, as offsets into
		// their values.
		for _, span := range spans {
			if alreadyMasked(joined[span.Start:span.End], span.Email) {
				continue
			}
			k := sort.Search(len(copies), func(k int) bool { return copies[k].at+copies[k].n > span.Start })
			for ; k < len(copies) && copies[k].at < span.End; k++ {
				c := copies[k]
				start, end := max(span.Start, c.at)-c.at+c.from, min(span.End, c.at+c.n)-c.at+c.from
				if start < end {
					found[c.slot] = append(found[c.slot], [2]int{start, end})
				}
			}
		}
		copies = copies[:0]
		return nil
	}
	for _, i := range indexes {
		text := o.slots[i].text
		err := o.closedCopies(o.slots[i], &budget, func(from, n int) error {
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			copies = append(copies, closedCopy{i, from, builder.Len(), n})
			builder.WriteString(text[from : from+n])
			if builder.Len() >= closedScanBatch {
				return flush()
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if err := flush(); err != nil {
		return err
	}
	for _, i := range indexes {
		o.maskClosedSlot(i, found[i])
	}
	return nil
}

// closedCopy is one copy of a closed value in the joined text of a scan:
// the value's bytes from..from+n at offset at.
type closedCopy struct{ slot, from, at, n int }

// closedCopies calls visit with each copy of a closed value maskClosed scans:
// the whole value, then a closedScanWindow from each restart. A value
// maskedWhole names has no bound on its restarts, so past its first
// closedCopyLimit each copy holds a closedRestartWindow, and a window whose
// text an earlier one of the value had is skipped: a value repeating itself
// costs its distinct windows only. Those copies stop once they would exceed
// closedRestartBudget for the value or the document's budget, which they
// draw from.
func (o *outputRedaction) closedCopies(slot outputSlot, budget *int, visit func(from, n int) error) error {
	text := slot.text
	if err := visit(0, len(text)); err != nil {
		return err
	}
	whole := slot.maskedWhole()
	var seen map[uint64]bool
	restarts, spent := 0, 0
	for from := 1; from < len(text); from++ {
		if !scanRestart(text, from) {
			continue
		}
		restarts++
		n := min(len(text)-from, closedScanWindow)
		if whole && restarts > closedCopyLimit {
			n = min(len(text)-from, closedRestartWindow)
			if seen == nil {
				seen = map[uint64]bool{}
			}
			key := maphash.String(o.seed(), text[from:from+n])
			if seen[key] {
				continue
			}
			if spent+n > closedRestartBudget || n > *budget {
				break
			}
			seen[key] = true
			spent += n
			*budget -= n
		}
		if err := visit(from, n); err != nil {
			return err
		}
	}
	return nil
}

// seed returns the hash seed closedCopies keys windows with, random per
// redaction so no text can be written to collide.
func (o *outputRedaction) seed() maphash.Seed {
	if !o.seeded {
		o.hashSeed, o.seeded = maphash.MakeSeed(), true
	}
	return o.hashSeed
}

// scanRestart reports an offset a closed value is scanned again from: the end
// of a run of boundary bytes, before a letter or digit. The underscore is a
// word character, so a rule that needs a word boundary does not see a token
// right after one; a copy from there starts the token on a line of its own.
// A value has at most one restart per underscore of its respelling, plus one.
func scanRestart(text string, from int) bool {
	return from > 0 && from < len(text) && scanBoundary(text[from-1]) && !scanBoundary(text[from])
}

// scanBoundary reports a byte that is not an ASCII letter or digit: the
// bytes normalization respells, a run of them as one underscore.
func scanBoundary(b byte) bool {
	return (b < 'a' || b > 'z') && (b < 'A' || b > 'Z') && (b < '0' || b > '9')
}

func scanRestarts(text string) int {
	count := 0
	for from := range len(text) {
		if scanRestart(text, from) {
			count++
		}
	}
	return count
}

// formatMaskByte is goredact's format-preserving mask of one byte.
func formatMaskByte(b byte) byte {
	switch {
	case b >= 'A' && b <= 'Z':
		return 'X'
	case b >= 'a' && b <= 'z':
		return 'x'
	case b >= '0' && b <= '9':
		return '9'
	case strings.IndexByte("-_.:/+=@", b) >= 0:
		return b
	}
	return '*'
}

// formatMask masks every byte of a text with formatMaskByte.
func formatMask(text string) string {
	masked := []byte(text)
	for j := range masked {
		masked[j] = formatMaskByte(masked[j])
	}
	return string(masked)
}

// maskClosedSlot masks the findings maskClosed made in one closed value, or
// the whole value when maskedWhole names it, and counts it.
func (o *outputRedaction) maskClosedSlot(i int, ranges [][2]int) {
	slot := &o.slots[i]
	whole := slot.maskedWhole()
	masked := []byte(slot.text)
	if whole {
		masked = []byte(formatMask(slot.text))
	} else {
		for _, r := range ranges {
			for j := r[0]; j < r[1]; j++ {
				masked[j] = formatMaskByte(slot.text[j])
			}
		}
	}
	changed := string(masked) != slot.text
	if changed {
		slot.set(string(masked))
		o.changed = true
	}
	if len(ranges) > 0 && (whole || changed) {
		o.masked += distinctFindings(ranges)
		o.maskedClosed++
		slot.counted = true
		// Only a finding that counts, and so fails the run, is a source.
		for _, r := range ranges {
			o.addSources(slot.text, r)
		}
	}
}

// alreadyMasked reports a finding that is the redactor's own output found
// again, and so no finding: text that already reads as its mask, or an
// address glued behind an identifier's words whose domain and last local
// word do (ev-author-xxxxx.xxx@xxxxxxx.xxx). The domain alone does not
// tell: a real address can be User@xxx.xxx.
func alreadyMasked(text string, email bool) bool {
	if formatMask(text) == text {
		return true
	}
	if at := strings.LastIndexByte(text, '@'); email && at >= 0 {
		local, domain := text[:at], text[at+1:]
		word := local[strings.LastIndexAny(local, "-_")+1:]
		return word != "" && formatMask(word) == word && formatMask(domain) == domain
	}
	return false
}

// distinctFindings counts the findings in a value, where the copies that
// found the same secret overlap and count once.
func distinctFindings(ranges [][2]int) int {
	sorted := slices.Clone(ranges)
	slices.SortFunc(sorted, func(a, b [2]int) int { return a[0] - b[0] })
	count, end := 0, -1
	for _, r := range sorted {
		if r[0] >= end {
			count++
		}
		end = max(end, r[1])
	}
	return count
}

// closedSourceMax bounds the respelled findings kept as sources: a longer one
// cannot fit in an identifier within its bound, 128 bytes. There is no
// lower bound: a source exists only once a finding has failed the run, so
// masking more spellings costs nothing more.
const closedSourceMax = 128

// sourceBytesMax bounds the bytes of the sources Identifiers keeps, and of
// the findings it trims into more, so the matcher and the scans of
// keySources cost a bounded amount whatever the document holds. Sources from
// identifiers exist only in a run that fails already, so past the bound the
// run still fails; only other spellings of the secrets left out may stay in
// the rejected document.
const sourceBytesMax = 64 << 10

// addSources records, while Identifiers runs, the respellings otherSpellings
// looks for in every identifier: of a counted finding's text, and of that
// text widened to the words of the value it lies in, so a key glued to
// letters in its own identifier (evAKIA...) is still found in another
// spelling of that identifier (evakia...). A finding with an @ is a
// credential or address with its host: each part of what comes before the
// last @, split at : and /, is a source too, so a password no rule detects
// alone is matched without the host. The finding also waits in pending for
// keySources, which finds the key a rule detects inside it.
func (o *outputRedaction) addSources(text string, r [2]int) {
	if o.sources == nil {
		return
	}
	start, end := r[0], r[1]
	for start > 0 && !scanBoundary(text[start-1]) {
		start--
	}
	for end < len(text) && !scanBoundary(text[end]) {
		end++
	}
	finding := text[r[0]:r[1]]
	sources := []string{finding, text[start:end]}
	if at := strings.LastIndexByte(finding, '@'); at > 0 {
		sources = append(sources, strings.FieldsFunc(finding[:at], sourcePieceSeparator)...)
	}
	for _, source := range sources {
		o.addSource(source)
	}
	if o.pendingBytes+len(finding) <= sourceBytesMax {
		o.pending = append(o.pending, finding)
		o.pendingBytes += len(finding)
	}
}

func (o *outputRedaction) addSource(text string) {
	token := identifierToken(text)
	if token == "" || len(token) > closedSourceMax || o.sources[token] || o.sourceBytes+len(token) > sourceBytesMax {
		return
	}
	o.sources[token] = true
	o.sourceBytes += len(token)
}

// sourcePieceSeparator reports the characters a credential or address is
// split at: a URL's user, password and host, an address's local part and
// domain.
func sourcePieceSeparator(r rune) bool { return r == '@' || r == ':' || r == '/' }

// keySources returns the keys a rule detects on their own in finding texts,
// with the context a rule took with them trimmed off. A rule can take a
// credential or address whole with its host (KEY@db.example.com), or a key
// with a hyphenated tail (sk-ant-...-prod-east), and the key alone in another
// spelling would not hold that whole text as words. So the parts of a text
// with @, : or /, split there, are scanned again, which finds the key apart
// from the host, and of each text and each key found in a part, the shortest
// prefix ending before a run of separators that a rule other than the
// e-mail rule still detects whole is a key. A text nothing detects alone,
// such as a password a keyword rule found, gives none.
//
// The prefixes taken are those whose respelling fits closedSourceMax and
// that are at most closedScanWindow bytes long, at most one per underscore
// of that respelling, so each text costs a bounded multiple of its length.
func (o *outputRedaction) keySources(texts []string) ([]string, error) {
	candidates := slices.Clone(texts)
	var pieces []string
	for _, text := range texts {
		if strings.ContainsAny(text, "@:/") {
			pieces = append(pieces, strings.FieldsFunc(text, sourcePieceSeparator)...)
		}
	}
	err := o.findLines(pieces, func(k int, span redact.Span) {
		if end := min(span.End, len(pieces[k])); span.Start < end {
			candidates = append(candidates, pieces[k][span.Start:end])
		}
	})
	if err != nil {
		return nil, err
	}
	var prefixes []string
	var owners []int
	for c, text := range candidates {
		for p := 1; p <= min(len(text), closedScanWindow); p++ {
			if p < len(text) && (!scanBoundary(text[p]) || scanBoundary(text[p-1])) {
				continue
			}
			if len(identifierToken(text[:p])) > closedSourceMax {
				break
			}
			prefixes = append(prefixes, text[:p])
			owners = append(owners, c)
		}
	}
	shortest := make([]string, len(candidates))
	err = o.findLines(prefixes, func(k int, span redact.Span) {
		prefix, owner := prefixes[k], owners[k]
		if !span.Email && span.Start == 0 && span.End == len(prefix) && (shortest[owner] == "" || len(prefix) < len(shortest[owner])) {
			shortest[owner] = prefix
		}
	})
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, key := range shortest {
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

// findLines scans lines as one text joined by newlines, in batches of about
// closedScanBatch, and calls visit with each span the redactor reports, as
// offsets into the line it starts in; a span may run past that line's end.
func (o *outputRedaction) findLines(lines []string, visit func(line int, span redact.Span)) error {
	var builder strings.Builder
	var starts []int
	first := 0
	flush := func(next int) error {
		if builder.Len() == 0 {
			return nil
		}
		joined := builder.String()
		builder.Reset()
		o.scanned += len(joined)
		spans, err := o.redactor.Find(o.ctx, joined)
		if err != nil {
			return err
		}
		for _, span := range spans {
			k := sort.SearchInts(starts, span.Start+1) - 1
			visit(first+k, redact.Span{Start: span.Start - starts[k], End: span.End - starts[k], Email: span.Email})
		}
		starts, first = starts[:0], next
		return nil
	}
	for k, line := range lines {
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		starts = append(starts, builder.Len())
		builder.WriteString(line)
		if builder.Len() >= closedScanBatch {
			if err := flush(k + 1); err != nil {
				return err
			}
		}
	}
	return flush(len(lines))
}

// scan redacts the joined text of the slots it is given and returns, by slot
// index, the text each may take, with the number of spans masked. With pairs,
// a key is joined to its string value by "=" rather than a newline. Spans
// found within kept and bundle text alone (redacted again with only that text
// left) are neither applied nor counted.
func (o *outputRedaction) scan(indexes []int, pairs bool) (map[int]string, int, error) {
	var builder strings.Builder
	protected := false
	for j, i := range indexes {
		slot := o.slots[i]
		if j > 0 {
			// A key reads as an assignment of its string value, so keyword
			// rules (password=…) see the pair; the separator is one byte
			// either way, so offsets are the same.
			if previous := o.slots[indexes[j-1]]; pairs && previous.paired && indexes[j-1]+1 == i {
				builder.WriteByte('=')
			} else {
				builder.WriteByte('\n')
			}
		}
		builder.WriteString(slot.text)
		protected = protected || slot.kind != slotFree
	}
	joined := builder.String()
	masked, count, err := o.redactor.RedactString(o.ctx, joined)
	if err != nil {
		return nil, 0, err
	}
	if len(masked) != len(joined) {
		return nil, 0, errOutputMaskLength
	}
	// protectedOnly is the joined text with only kept and bundle strings left,
	// the rest blanked in place: what it masks is not the model's doing.
	protectedOnly := ""
	if protected && masked != joined {
		blanked, offset := []byte(joined), 0
		for _, i := range indexes {
			slot := o.slots[i]
			if slot.kind == slotFree {
				for k := offset; k < offset+len(slot.text); k++ {
					if blanked[k] != '\n' {
						blanked[k] = ' '
					}
				}
			}
			offset += len(slot.text) + 1
		}
		var protectedCount int
		if protectedOnly, protectedCount, err = o.redactor.RedactString(o.ctx, string(blanked)); err != nil {
			return nil, 0, err
		}
		if len(protectedOnly) != len(joined) {
			return nil, 0, errOutputMaskLength
		}
		count = max(count-protectedCount, 0)
	}
	out := make(map[int]string, len(indexes))
	offset := 0
	for _, i := range indexes {
		slot := o.slots[i]
		end := offset + len(slot.text)
		text := masked[offset:end]
		switch slot.kind {
		case slotKept, slotClosed:
			text = slot.text
		case slotBundle:
			if protectedOnly == "" || protectedOnly[offset:end] == text {
				text = slot.text
			}
		}
		out[i] = text
		offset = end + 1
	}
	return out, count, nil
}

// identifiers is Identifiers: it scans the values normalization respells, as
// slotClosed slots of their own bounded by their respelling, masks the other
// spellings of what it found in them in identifiers (see otherSpellings) and
// in free text (see freeSpellings), and masks each string value of the
// document equal to an identifier it masked.
func (o *outputRedaction) identifiers(document []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(document, &value); err != nil {
		return document, nil
	}
	// The walk normalizes value; raw keeps the document as written.
	var raw any
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	masks := map[string]string{}
	o.slots, o.sources, o.pending, o.sourceBytes, o.pendingBytes = nil, map[string]bool{}, nil, 0, 0
	defer func() { o.slots, o.sources, o.pending = nil, nil, nil }()
	var indexes []int
	identifiers := map[string]bool{}
	for _, id := range authoredIdentifiers(value, o.schema) {
		text := id.text
		identifiers[text] = true
		slot := outputSlot{text: text, kind: slotClosed, limit: id.limit, length: len(id.normalized), set: func(masked string) { masks[text] = masked }}
		// One text may sit at positions of different bounds. It is scanned,
		// and counted, once: fully when any position bounds it to a scan;
		// masked whole only when every position would.
		if last := len(o.slots) - 1; last >= 0 && o.slots[last].text == text {
			if o.slots[last].maskedWhole() && !slot.maskedWhole() {
				o.slots[last] = slot
			}
			continue
		}
		indexes = append(indexes, len(o.slots))
		o.slots = append(o.slots, slot)
	}
	if err := o.maskClosed(indexes); err != nil {
		return document, err
	}
	keys, err := o.keySources(o.pending)
	if err != nil {
		return document, err
	}
	for _, key := range keys {
		o.addSource(key)
	}
	if len(o.sources) > 0 {
		matcher := newTokenMatcher(slices.Sorted(maps.Keys(o.sources)))
		o.otherSpellings(matcher, masks)
		o.freeSpellings(matcher, freeStrings(raw, identifiers), masks)
	}
	if len(masks) == 0 {
		return document, nil
	}
	return json.Marshal(replaceStrings(raw, masks))
}

// otherSpellings masks whole, and counts, each identifier slot no finding
// counted yet whose respelling (identifierToken) holds a source: the
// respelling of a finding in another identifier. That is the same secret
// upper-cased, already respelled or behind a prefix, which no rule may
// detect. A source matches only as whole words of the respelling, from an
// underscore or its start to an underscore or its end, so it cannot match
// inside a longer word. A value masked whole uncounted counts once it holds
// one. An identifier it masks that held no secret changes no outcome that
// matters: the finding that made the source fails the run already. The
// sources are matched all at once (see tokenMatcher), so the work is linear
// in the identifiers' size.
func (o *outputRedaction) otherSpellings(matcher *tokenMatcher, masks map[string]string) {
	for i := range o.slots {
		slot := &o.slots[i]
		if slot.counted || !matcher.contains(identifierToken(slot.text)) {
			continue
		}
		masks[slot.text] = formatMask(slot.text)
		o.masked++
		o.maskedClosed++
		slot.counted = true
	}
}

// freeSpellings masks, in each string of free text, the words that respell to
// a source: a secret found in an identifier, repeated in prose upper-cased,
// respelled or otherwise in a spelling Document may not detect. Only the
// words are masked, each match counted in masked; free text never counts as
// a closed value, and the run fails already for the finding that made the
// source.
func (o *outputRedaction) freeSpellings(matcher *tokenMatcher, texts []string, masks map[string]string) {
	for _, text := range texts {
		var masked []byte
		matcher.spellings(text, func(start, end int) {
			if masked == nil {
				masked = []byte(text)
			}
			changed := false
			for j := start; j < end; j++ {
				b := formatMaskByte(text[j])
				changed = changed || masked[j] != b
				masked[j] = b
			}
			if changed {
				o.masked++
			}
		})
		if masked != nil && string(masked) != text {
			masks[text] = string(masked)
		}
	}
}

// freeStrings lists once each, in a fixed order, the string values of a
// decoded document other than the identifiers given.
func freeStrings(value any, identifiers map[string]bool) []string {
	seen := map[string]bool{}
	var texts []string
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for _, key := range slices.Sorted(maps.Keys(v)) {
				walk(v[key])
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		case string:
			if v != "" && !identifiers[v] && !seen[v] {
				seen[v] = true
				texts = append(texts, v)
			}
		}
	}
	walk(value)
	return texts
}

// tokenMatcher finds any of a set of respelled tokens, as whole words, in a
// respelling in one pass over it: an Aho-Corasick automaton over the tokens,
// each framed by underscores, run over the respelling framed the same way.
// Each state keeps its first child and next sibling instead of a map, as a
// respelling has 37 symbols at most, so a large set of tokens costs a few
// bytes per byte of them. out is the length of the longest framed token
// ending at a state, 0 for none; a token longer than fits is left out, as
// no source is.
type tokenMatcher struct {
	label          []byte
	child, sibling []int32
	fail           []int32
	out            []uint8
}

func newTokenMatcher(tokens []string) *tokenMatcher {
	m := &tokenMatcher{label: []byte{0}, child: []int32{0}, sibling: []int32{0}, fail: []int32{0}, out: []uint8{0}}
	for _, token := range tokens {
		if token == "" || len(token)+2 > 255 {
			continue
		}
		state := int32(0)
		for _, b := range []byte("_" + token + "_") {
			next, ok := m.step(state, b)
			if !ok {
				next = int32(len(m.label))
				m.label = append(m.label, b)
				m.child = append(m.child, 0)
				m.sibling = append(m.sibling, m.child[state])
				m.fail = append(m.fail, 0)
				m.out = append(m.out, 0)
				m.child[state] = next
			}
			state = next
		}
		m.out[state] = uint8(len(token) + 2)
	}
	// Breadth first, each state falls back to the longest proper suffix of
	// its string that is a prefix of some token.
	var queue []int32
	for c := m.child[0]; c != 0; c = m.sibling[c] {
		queue = append(queue, c)
	}
	for k := 0; k < len(queue); k++ {
		state := queue[k]
		for c := m.child[state]; c != 0; c = m.sibling[c] {
			fallback := m.fail[state]
			target, ok := m.step(fallback, m.label[c])
			for !ok && fallback > 0 {
				fallback = m.fail[fallback]
				target, ok = m.step(fallback, m.label[c])
			}
			if ok && target != c {
				m.fail[c] = target
			}
			// A token ending here is longer than any ending at its fallback.
			if m.out[c] == 0 {
				m.out[c] = m.out[m.fail[c]]
			}
			queue = append(queue, c)
		}
	}
	return m
}

// step follows the edge labelled b out of a state.
func (m *tokenMatcher) step(state int32, b byte) (int32, bool) {
	for c := m.child[state]; c != 0; c = m.sibling[c] {
		if m.label[c] == b {
			return c, true
		}
	}
	return 0, false
}

// next follows b out of a state, falling back until some state has an edge
// labelled b, or to the root.
func (m *tokenMatcher) next(state int32, b byte) int32 {
	next, ok := m.step(state, b)
	for !ok && state > 0 {
		state = m.fail[state]
		next, ok = m.step(state, b)
	}
	if ok {
		return next
	}
	return state
}

// contains reports whether a respelling holds any of the tokens as whole
// words.
func (m *tokenMatcher) contains(respelling string) bool {
	state := int32(0)
	framed := "_" + respelling + "_"
	for i := range len(framed) {
		state = m.next(state, framed[i])
		if m.out[state] > 0 {
			return true
		}
	}
	return false
}

// spellings calls visit with the byte range, in text as written, of each run
// of whole words whose respelling (identifierToken) is a token. It respells
// the text as it goes, one rune at a time, and remembers where each of the
// last 256 respelled bytes came from, which outlasts any token.
func (m *tokenMatcher) spellings(text string, visit func(start, end int)) {
	type origin struct{ start, end int }
	var ring [256]origin
	fed := 0
	state := int32(0)
	feed := func(b byte, from origin) {
		ring[fed%len(ring)] = from
		fed++
		state = m.next(state, b)
		if n := int(m.out[state]); n > 0 {
			// The match is the last n bytes fed, framed by underscores.
			visit(ring[(fed-n+1)%len(ring)].start, ring[(fed-2)%len(ring)].end)
		}
	}
	feed('_', origin{})
	separated := false
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if lower := unicode.ToLower(r); lower >= 'a' && lower <= 'z' || lower >= '0' && lower <= '9' {
			if separated {
				feed('_', origin{i, i})
				separated = false
			}
			feed(byte(lower), origin{i, i + size})
		} else if fed > 1 {
			separated = true
		}
		i += size
	}
	feed('_', origin{len(text), len(text)})
}

// replaceStrings replaces each string value of a decoded document found in
// replacements; object keys are left as they are.
func replaceStrings(value any, replacements map[string]string) any {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			v[key] = replaceStrings(child, replacements)
		}
	case []any:
		for i, child := range v {
			v[i] = replaceStrings(child, replacements)
		}
	case string:
		if replacement, ok := replacements[v]; ok {
			return replacement
		}
	}
	return value
}

// excerptLines lists the excerpt lines a code source may carry unmasked: the
// head or merge-base comparison lines its revision names, and the lines the
// previous review's source for the same file, revision and version showed.
// It is nil for a source without an excerpt.
func (o *outputRedaction) excerptLines(source map[string]any) map[tileLine]bool {
	code, ok := exactTileCode(source)
	if !ok || o.bundle == nil {
		return nil
	}
	lines := map[tileLine]bool{}
	for _, prior := range o.inherited {
		if prior.Path == code.Path && prior.Revision == code.Revision && prior.Version == code.Version && prior.Diff != nil {
			for _, hunk := range prior.Diff.Hunks {
				for _, line := range hunk.Lines {
					lines[line] = true
				}
			}
		}
	}
	root := ""
	switch code.Revision {
	case o.bundle.PullRequest.HeadSHA:
		root = "head"
	case o.bundle.PullRequest.MergeBaseSHA:
		root = "merge-base"
	}
	if root != "" {
		for _, line := range comparisonLines(o.bundle, code.Path, root) {
			lines[line] = true
		}
	}
	return lines
}

// exactTileCode and exactLine read a code source by its exact key names;
// encoding/json matches keys case-insensitively, which would let a
// case-variant key stand in for the one the contract names.
func exactTileCode(source map[string]any) (tileCode, bool) {
	code := tileCode{}
	code.Path, _ = source["path"].(string)
	code.Revision, _ = source["revision"].(string)
	if version, ok := jsonInt(source["review_version"]); ok {
		code.Version = version
	}
	diff, ok := source["diff"].(map[string]any)
	if !ok {
		return code, false
	}
	code.Diff = &struct {
		Hunks []tileHunk `json:"hunks"`
	}{}
	for _, raw := range jsonObjects(diff["hunks"]) {
		hunk := tileHunk{}
		hunk.Header, _ = raw["header"].(string)
		for _, line := range jsonObjects(raw["lines"]) {
			hunk.Lines = append(hunk.Lines, exactLine(line))
		}
		code.Diff.Hunks = append(code.Diff.Hunks, hunk)
	}
	return code, true
}

func exactLine(value map[string]any) tileLine {
	line := tileLine{}
	line.Kind, _ = value["kind"].(string)
	line.Text, _ = value["text"].(string)
	line.Old, _ = jsonInt(value["old_line"])
	line.New, _ = jsonInt(value["new_line"])
	return line
}

func jsonInt(value any) (int, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := number.Int64()
		return int(parsed), err == nil
	case float64:
		return int(number), number == float64(int(number))
	}
	return 0, false
}

// canonicalHunkHeader reports a header of the form normalization writes,
// which holds nothing but line numbers.
func canonicalHunkHeader(value any) bool {
	header, _ := value.(string)
	old, oldCount, next, newCount, ok := tileHunkRange(header)
	return ok && header == fmt.Sprintf("@@ -%d,%d +%d,%d @@", old, oldCount, next, newCount)
}

// closedValue reports how a value meets its schema's closing constraint:
// slotKept when it equals the constant or is one of the enum, slotClosed when
// it fully matches a closed pattern (see closedPattern) or is a date-time, and
// slotFree when it does not, which makes it free text. Under oneOf or anyOf,
// it is the first branch's that closes it. For slotClosed it also returns the
// maxLength of the schema that closed the value, or of the one around that
// branch, and 0 when neither sets one.
func (o *outputRedaction) closedValue(schema map[string]any, value string) (slotKind, int) {
	schema = resolveSchema(schema, o.defs)
	limit := 0
	if maxLength, ok := jsonInt(schema["maxLength"]); ok && maxLength > 0 {
		limit = maxLength
	}
	if constant, ok := schema["const"]; ok {
		return keptIf(constant == value), 0
	}
	if enum, ok := schema["enum"].([]any); ok {
		return keptIf(slices.Contains(enum, any(value))), 0
	}
	if pattern, _ := schema["pattern"].(string); closedPattern(pattern) {
		compiled, ok := o.patterns[pattern]
		if !ok {
			compiled, _ = regexp.Compile(pattern)
			if o.patterns == nil {
				o.patterns = map[string]*regexp.Regexp{}
			}
			o.patterns[pattern] = compiled
		}
		return closedIf(compiled != nil && compiled.MatchString(value)), limit
	}
	if schema["format"] == "date-time" {
		_, err := time.Parse(time.RFC3339Nano, value)
		return closedIf(err == nil), limit
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		branches, _ := schema[key].([]any)
		for _, raw := range branches {
			if branch, _ := raw.(map[string]any); branch != nil && acceptsString(resolveSchema(branch, o.defs)) {
				if kind, branchLimit := o.closedValue(branch, value); kind != slotFree {
					if branchLimit == 0 {
						branchLimit = limit
					}
					return kind, branchLimit
				}
			}
		}
	}
	return slotFree, 0
}

func keptIf(ok bool) slotKind {
	if ok {
		return slotKept
	}
	return slotFree
}

func closedIf(ok bool) slotKind {
	if ok {
		return slotClosed
	}
	return slotFree
}

// literalAlternatives matches the inside of a group of plain alternatives.
var literalAlternatives = regexp.MustCompile(`^[A-Za-z0-9_]+(\|[A-Za-z0-9_]+)*$`)

// closedPattern reports a pattern that pins a whole string to a closed
// alphabet, such as an identifier, SHA or prefixed ID: anchored at both ends,
// with no wildcard, negated class or negated shorthand that would let free
// text through. A pattern such as \S only asks for some text and closes
// nothing. One group of plain literal alternatives, as in ^(ev|rsk)_[a-z]+$,
// is closed; any other alternation is not.
func closedPattern(pattern string) bool {
	if open := strings.IndexByte(pattern, '('); open >= 0 {
		if end := strings.IndexByte(pattern[open:], ')'); end > 0 && literalAlternatives.MatchString(pattern[open+1:open+end]) {
			pattern = pattern[:open] + "x" + pattern[open+end+1:]
		}
	}
	if len(pattern) < 2 || pattern[0] != '^' || pattern[len(pattern)-1] != '$' || strings.Contains(pattern, "[^") || strings.Contains(pattern, "|") {
		return false
	}
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '.':
			return false
		case '\\':
			if i+1 < len(pattern) && strings.IndexByte("SWDsp", pattern[i+1]) >= 0 {
				return false
			}
			i++
		}
	}
	return true
}

func acceptsString(schema map[string]any) bool {
	switch value := schema["type"].(type) {
	case string:
		return value == "string"
	case []any:
		for _, t := range value {
			if t == "string" {
				return true
			}
		}
		return false
	}
	_, constant := schema["const"].(string)
	return constant || schema["pattern"] != nil || schema["enum"] != nil
}
