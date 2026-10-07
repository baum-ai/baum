package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
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
// the repository paths of the run's checkout, either tree.
func NewOutputRedactor(v *DocumentValidator, redactor *bundlepkg.Redactor, b *bundlepkg.Bundle, checkout map[string]bool) (*OutputRedactor, error) {
	o := outputRedaction{redactor: redactor, schema: v.strict, bundle: b, checkout: checkout}
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

// Text redacts a string that is not a document.
func (r *OutputRedactor) Text(ctx context.Context, s string) (string, error) {
	r.o.ctx = ctx
	return r.o.text(s)
}

// Masked returns the number of spans masked so far.
func (r *OutputRedactor) Masked() int { return r.o.masked }

// MaskedClosed returns the number of values masked so far that a schema closes
// by pattern or date-time format: identifiers, categories, SHAs and the like.
// They are free lowercase text, so a secret the model copied into one is
// masked in place, but a value that held one is not what the model was asked
// to write, and a host rejects the run when this is not zero. Each value
// counts once, however many spans it holds; the spans are in Masked as well.
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
	set         func(string)
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
		if def == "tileCodeFile" {
			scope, excerpt = def, o.excerptLines(v)
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
		if kind == slotFree && schema != nil {
			kind = o.closedValue(schema, v)
		}
		o.slots = append(o.slots, outputSlot{text: v, kind: kind, set: set})
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
//   - a code source's or file anchor's path: a file in either checkout tree,
//     or a path or old path of the bundle's diff;
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
	case "tileCodeFile.path", "file.path":
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

// maskClosed scans on their own the values a pattern or date-time closed,
// which the joined scans keep whole. A lowercase pattern such as a risk
// category or a prefixed ID admits any text the model copies, a secret
// included, and the prefix or the words around it can hide the secret from a
// rule that needs a word boundary (sec_sk_live_...). Each value is scanned
// whole and from after every underscore in it, so sec_sk_live_... and
// sec_auth_sk_live_... both expose sk_live_...; all the copies are one text
// joined by newlines, as in scan. A byte any copy masks is masked in the
// value, which keeps its length. The mask writes only x, 9 and the characters
// it keeps, so a masked value still fits a pattern of lowercase identifiers,
// but not one of hex digits, which then fails validation.
//
// A value counts as its spans: runs of masked bytes, where a gap of characters
// the mask leaves as they are (the separators it keeps, x and 9) does not
// split a run. Copies that mask the same secret, or a secret with an x in it,
// are one span. maskedClosed counts the values masked.
func (o *outputRedaction) maskClosed(indexes []int) error {
	if len(indexes) == 0 {
		return nil
	}
	type copyOf struct{ slot, from, at int }
	var copies []copyOf
	var builder strings.Builder
	for _, i := range indexes {
		text := o.slots[i].text
		for from := range len(text) {
			if from > 0 && text[from-1] != '_' {
				continue
			}
			if builder.Len() > 0 {
				builder.WriteByte('\n')
			}
			copies = append(copies, copyOf{i, from, builder.Len()})
			builder.WriteString(text[from:])
		}
	}
	joined := builder.String()
	masked, _, err := o.redactor.RedactString(o.ctx, joined)
	if err != nil {
		return err
	}
	if len(masked) != len(joined) {
		return errOutputMaskLength
	}
	out := make(map[int][]byte, len(indexes))
	for _, c := range copies {
		text := o.slots[c.slot].text
		if out[c.slot] == nil {
			out[c.slot] = []byte(text)
		}
		for j := range len(text) - c.from {
			if got := masked[c.at+j]; got != text[c.from+j] && out[c.slot][c.from+j] == text[c.from+j] {
				out[c.slot][c.from+j] = got
			}
		}
	}
	for _, i := range indexes {
		slot := o.slots[i]
		if spans := maskedSpans(slot.text, string(out[i])); spans > 0 {
			slot.set(string(out[i]))
			o.masked += spans
			o.maskedClosed++
			o.changed = true
		}
	}
	return nil
}

// maskedSpans counts the runs of bytes that differ between a text and its mask.
// A run goes on through bytes the mask leaves as they are.
func maskedSpans(text, masked string) int {
	spans, inRun := 0, false
	for j := range len(text) {
		switch {
		case masked[j] != text[j]:
			if !inRun {
				spans++
			}
			inRun = true
		case !strings.ContainsRune("x9-_.:/+=@", rune(text[j])):
			inRun = false
		}
	}
	return spans
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
// it is the first branch's that closes it.
func (o *outputRedaction) closedValue(schema map[string]any, value string) slotKind {
	schema = resolveSchema(schema, o.defs)
	if constant, ok := schema["const"]; ok {
		return keptIf(constant == value)
	}
	if enum, ok := schema["enum"].([]any); ok {
		return keptIf(slices.Contains(enum, any(value)))
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
		return closedIf(compiled != nil && compiled.MatchString(value))
	}
	if schema["format"] == "date-time" {
		_, err := time.Parse(time.RFC3339Nano, value)
		return closedIf(err == nil)
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		branches, _ := schema[key].([]any)
		for _, raw := range branches {
			if branch, _ := raw.(map[string]any); branch != nil && acceptsString(resolveSchema(branch, o.defs)) {
				if kind := o.closedValue(branch, value); kind != slotFree {
					return kind
				}
			}
		}
	}
	return slotFree
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

// closedPattern reports a pattern that pins a whole string to a closed
// alphabet, such as an identifier, SHA or prefixed ID: anchored at both ends,
// with no wildcard, negated class or negated shorthand that would let free
// text through. A pattern such as \S only asks for some text and closes
// nothing.
func closedPattern(pattern string) bool {
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
