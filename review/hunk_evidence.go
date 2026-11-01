package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

// A hunk's evidence is the patch text (index.hunks[].diff) and the GitHub
// fragment (github_url) a document records for each indexed hunk. Both are
// optional in the schema. A local store keeps neither (StripHunkEvidence):
// the patch is read from the clone when a reader opens it, and the fragment
// is derived from the hunk's path and new range (DeriveHunkURLs). A document
// meant to be read without the clone gets the patches back
// (EmbedHunkPatches). These functions change only index.hunks; every other
// member of the document, and of each hunk, keeps its bytes and order.

// StripHunkEvidence removes diff and github_url from every hunk of
// index.hunks. It reports whether it removed anything; when it did not, the
// document is returned as given.
func StripHunkEvidence(document []byte) ([]byte, bool, error) {
	return editIndexHunks(document, func(hunk *jsonObject) (bool, error) {
		removed := hunk.remove("diff")
		return hunk.remove("github_url") || removed, nil
	})
}

// DeriveHunkURLs gives every hunk of index.hunks without a github_url the
// one generation records, bundle.HunkDiffURL of its path and first new line,
// so a document whose fragments were not stored reads as it did when they
// were. A hunk without a path or a two-number new_range is left alone, and a
// document with nothing to derive is returned as given.
func DeriveHunkURLs(document []byte) ([]byte, error) {
	out, _, err := editIndexHunks(document, deriveHunkURL)
	return out, err
}

func deriveHunkURL(hunk *jsonObject) (bool, error) {
	if hunk.has("github_url") {
		return false, nil
	}
	var path string
	var newRange []int
	if json.Unmarshal(hunk.get("path"), &path) != nil || path == "" || json.Unmarshal(hunk.get("new_range"), &newRange) != nil || len(newRange) != 2 {
		return false, nil
	}
	url, err := json.Marshal(bundlepkg.HunkDiffURL(path, newRange[0]))
	if err != nil {
		return false, err
	}
	hunk.insertAfter("deletions", "github_url", url)
	return true, nil
}

// FilePatch reads the masked patch of one changed file, from its first "@@"
// line, as locallink.Patcher answers it. ok is false when there is none to
// read: the file has no text patch, or the clone cannot provide it.
type FilePatch func(ctx context.Context, path string) (patch string, ok bool, err error)

// EmbedHunkPatches fills in the diff of every hunk of index.hunks that has
// none from its file's patch, read once per path with patch, and derives the
// hunk's github_url when it has none (DeriveHunkURLs), so the document can be
// read without the clone. A hunk's text is the patch's hunk with the same new
// range and the same counts of added and removed lines, as the viewer finds
// it; a hunk the patch does not hold, or whose file has no patch, keeps no
// diff. Reading stops once maxBytes of patch text have been read (0 is no
// limit), leaving later hunks without a diff. An error from patch, or the
// context ending, fails the call. The paths are asked for in the order of
// HunkPatchPaths, so a caller can read them ahead.
func EmbedHunkPatches(ctx context.Context, document []byte, patch FilePatch, maxBytes int64) ([]byte, error) {
	type read struct {
		text string
		ok   bool
	}
	reads := map[string]read{}
	var total int64
	out, _, err := editIndexHunks(document, func(hunk *jsonObject) (bool, error) {
		changed, err := deriveHunkURL(hunk)
		if err != nil {
			return changed, err
		}
		h, ok := patchableHunk(*hunk)
		if !ok {
			return changed, nil
		}
		if err := ctx.Err(); err != nil {
			return changed, err
		}
		r, seen := reads[h.Path]
		if !seen && (maxBytes <= 0 || total < maxBytes) {
			text, ok, err := patch(ctx, h.Path)
			if err != nil {
				return changed, err
			}
			total += int64(len(text))
			if ok && maxBytes > 0 && total > maxBytes {
				ok = false
			}
			r = read{text: text, ok: ok}
			reads[h.Path] = r
		}
		if !r.ok {
			return changed, nil
		}
		diff, err := hunkFromFilePatch(ctx, r.text, h.NewRange[0], h.NewRange[1], h.Additions, h.Deletions)
		if err != nil {
			return changed, err
		}
		if diff == nil {
			return changed, nil
		}
		encoded, err := json.Marshal(diff)
		if err != nil {
			return changed, err
		}
		if hunk.has("diff") {
			hunk.set("diff", encoded) // an explicit null
		} else {
			hunk.insertAfter("github_url", "diff", encoded)
		}
		return true, nil
	})
	return out, err
}

// HunkPatchPaths lists, each once and in the order EmbedHunkPatches asks for
// them, the paths of the hunks of index.hunks whose diff it would fill in.
func HunkPatchPaths(document []byte) ([]string, error) {
	var paths []string
	seen := map[string]bool{}
	_, _, err := editIndexHunks(document, func(hunk *jsonObject) (bool, error) {
		if h, ok := patchableHunk(*hunk); ok && !seen[h.Path] {
			seen[h.Path] = true
			paths = append(paths, h.Path)
		}
		return false, nil
	})
	return paths, err
}

type patchable struct {
	Path      string `json:"path"`
	NewRange  []int  `json:"new_range"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// patchableHunk reads what EmbedHunkPatches matches a hunk by, and reports
// whether the hunk is one it fills in: it has no diff (or a null one) and a
// path, a two-number new_range and counts.
func patchableHunk(hunk jsonObject) (patchable, bool) {
	var h patchable
	if diff := hunk.get("diff"); diff != nil && !bytes.Equal(bytes.TrimSpace(diff), []byte("null")) {
		return h, false
	}
	for key, target := range map[string]any{"path": &h.Path, "new_range": &h.NewRange, "additions": &h.Additions, "deletions": &h.Deletions} {
		if json.Unmarshal(hunk.get(key), target) != nil {
			return h, false
		}
	}
	return h, h.Path != "" && len(h.NewRange) == 2
}

// hunkFromFilePatch is the hunk of a file's patch whose header names the
// new range start,count and that adds and removes the given numbers of
// lines, as a document's diff; nil when no hunk of the patch is that one.
func hunkFromFilePatch(ctx context.Context, patch string, start, count, additions, deletions int) (*tileHunk, error) {
	var boundaries []int
	offset := 0
	for line := range strings.SplitAfterSeq(patch, "\n") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.HasPrefix(line, "@@ ") {
			boundaries = append(boundaries, offset)
		}
		offset += len(line)
	}
	boundaries = append(boundaries, len(patch))
	for i := 0; i+1 < len(boundaries); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		text := patch[boundaries[i]:boundaries[i+1]]
		header, _, _ := strings.Cut(text, "\n")
		_, _, next, newCount, ok := tileHunkRange(header)
		if !ok || next != start || newCount != count {
			continue
		}
		diff := indexedHunkDiff(bundlepkg.Hunk{Patch: text})
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if diff == nil {
			return nil, nil
		}
		added, removed := 0, 0
		for _, line := range diff.Lines {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			switch line.Kind {
			case "add":
				added++
			case "remove":
				removed++
			}
		}
		if added != additions || removed != deletions {
			return nil, nil
		}
		return diff, nil
	}
	return nil, nil
}

// editIndexHunks calls edit with each object of index.hunks and encodes the
// document again only when an edit reports a change; otherwise it returns the
// document as given. A document without index.hunks has nothing to edit.
func editIndexHunks(document []byte, edit func(*jsonObject) (bool, error)) ([]byte, bool, error) {
	doc, err := decodeJSONObject(document)
	if err != nil {
		return nil, false, err
	}
	rawIndex := doc.get("index")
	if rawIndex == nil || bytes.Equal(rawIndex, []byte("null")) {
		return document, false, nil
	}
	index, err := decodeJSONObject(rawIndex)
	if err != nil {
		return nil, false, err
	}
	rawHunks := index.get("hunks")
	if rawHunks == nil || bytes.Equal(rawHunks, []byte("null")) {
		return document, false, nil
	}
	var hunks []json.RawMessage
	if err := json.Unmarshal(rawHunks, &hunks); err != nil {
		return nil, false, err
	}
	changed := false
	for i, raw := range hunks {
		hunk, err := decodeJSONObject(raw)
		if err != nil {
			return nil, false, err
		}
		edited, err := edit(&hunk)
		if err != nil {
			return nil, false, err
		}
		if edited {
			hunks[i], changed = hunk.encode(), true
		}
	}
	if !changed {
		return document, false, nil
	}
	index.set("hunks", joinJSONArray(hunks))
	doc.set("index", index.encode())
	return doc.encode(), true, nil
}

// jsonObject is a decoded JSON object whose members keep their order and the
// exact bytes of their values.
type jsonObject []jsonMember

type jsonMember struct {
	key   string
	value json.RawMessage
}

func decodeJSONObject(raw []byte) (jsonObject, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil {
		return nil, err
	} else if token != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var object jsonObject
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, _ := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		object = append(object, jsonMember{key: key, value: value})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return object, nil
}

// get is the value of key's last member, as encoding/json reads a duplicate
// key; nil when there is none.
func (o jsonObject) get(key string) json.RawMessage {
	if i := o.last(key); i >= 0 {
		return o[i].value
	}
	return nil
}

func (o jsonObject) last(key string) int {
	for i := len(o) - 1; i >= 0; i-- {
		if o[i].key == key {
			return i
		}
	}
	return -1
}

func (o jsonObject) has(key string) bool { return o.last(key) >= 0 }

// set replaces the value of key's last member, the one a reader takes, or
// adds key last.
func (o *jsonObject) set(key string, value json.RawMessage) {
	if i := o.last(key); i >= 0 {
		(*o)[i].value = value
		return
	}
	*o = append(*o, jsonMember{key: key, value: value})
}

// insertAfter adds key just after the last member named after, or last when
// there is no such member.
func (o *jsonObject) insertAfter(after, key string, value json.RawMessage) {
	for i := len(*o) - 1; i >= 0; i-- {
		if (*o)[i].key == after {
			*o = append((*o)[:i+1], append(jsonObject{{key: key, value: value}}, (*o)[i+1:]...)...)
			return
		}
	}
	*o = append(*o, jsonMember{key: key, value: value})
}

func (o *jsonObject) remove(key string) bool {
	removed := false
	kept := (*o)[:0]
	for _, member := range *o {
		if member.key == key {
			removed = true
			continue
		}
		kept = append(kept, member)
	}
	*o = kept
	return removed
}

func (o jsonObject) encode() json.RawMessage {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, member := range o {
		if i > 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(member.key)
		out.Write(key)
		out.WriteByte(':')
		out.Write(member.value)
	}
	out.WriteByte('}')
	return out.Bytes()
}

func joinJSONArray(items []json.RawMessage) json.RawMessage {
	out := []byte{'['}
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}
