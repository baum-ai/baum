package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/githubauth"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/review"
	"github.com/baum-ai/baum/reviewv2"
)

// completeV2Details writes again the failed sections of a baum.review/2
// version, p.Version, and updates it in place, as completeDetails does a
// staged one's. The levels run over the version itself as the previous
// version, so its complete sections are reused without a call
// (orchestrate.Run.Previous) and only its failed ones are written, with the
// map and the overview. Only those sections, and the tiles they own, are
// spliced into the stored bytes (spliceV2Sections), so the rest of the
// version, its overview and its metadata stay as they were. A section the
// new map keeps with other hunks, or that fails again, stays failed.
func (l *Local) completeV2Details(ctx context.Context, versions *store.Store, manager *githubauth.Manager, key store.Key, p Prepared, o generate.Options, latest store.Version) (generate.Result, error) {
	v := *o.V2
	v.Previous, v.PreviousVersion, v.NoReuse = latest.Document, p.Version-1, false
	o.V2 = &v
	o.Continue, o.Request.Previous = nil, nil
	if p.Version > 1 {
		if earlier, e := versions.Get(ctx, key, p.Version-1); e == nil {
			// A baum.review/1 version before it reconciles the bundle as a
			// new version's does; a baum.review/2 one is not read so.
			o.Request.Previous, _ = review.PreviousVersionOf(earlier.Document, p.Version-1)
		}
	}
	result, e := generate.Run(ctx, o)
	if e != nil {
		return result, e
	}
	failed, _ := generate.Gaps(latest.Document)
	for range 2 {
		current, e := versions.Latest(ctx, key)
		if e != nil {
			return result, e
		}
		if current.Metadata.Version != p.Version {
			return result, ErrSuperseded
		}
		spliced, written, e := spliceV2Sections(current.Document, result.Document, failed)
		if e != nil {
			return result, ErrSuperseded
		}
		result.Metadata = current.Metadata
		result.FailedSections = nil
		for _, id := range failed {
			if !slices.Contains(written, id) {
				result.FailedSections = append(result.FailedSections, id)
			}
		}
		if len(written) == 0 {
			result.Document = current.Document
			return result, nil
		}
		if _, e := reviewv2.Read(spliced); e != nil {
			return result, &generate.Error{Category: generate.CategoryInternal, Err: errors.New("the completed version is not a valid baum.review/2 document")}
		}
		write := func() error { return versions.CompleteCurrent(ctx, key, current, spliced) }
		if manager != nil {
			e = manager.WithIdentity(ctx, write)
		} else {
			e = write()
		}
		if !errors.Is(e, store.ErrConflict) {
			result.Document = spliced
			return result, e
		}
	}
	return result, ErrSuperseded
}

// spliceV2Sections puts into current, a stored baum.review/2 document, the
// sections of ids that written has complete (no details state) over the
// same hunks, each with the tiles it owns in written in place of those it
// owned in current. It returns the document and the ids it put in; with
// none, current as it is. A written tile whose id another kept tile has is
// left out (the section's body then does not place it).
func spliceV2Sections(current, written []byte, ids []string) ([]byte, []string, error) {
	var cur, next map[string]json.RawMessage
	if err := json.Unmarshal(current, &cur); err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(written, &next); err != nil {
		return nil, nil, err
	}
	var curSections, nextSections, curTiles, nextTiles []json.RawMessage
	for _, part := range []struct {
		raw  json.RawMessage
		into *[]json.RawMessage
	}{{cur["sections"], &curSections}, {next["sections"], &nextSections}, {cur["tiles"], &curTiles}, {next["tiles"], &nextTiles}} {
		if len(part.raw) > 0 && string(part.raw) != "null" {
			if err := json.Unmarshal(part.raw, part.into); err != nil {
				return nil, nil, err
			}
		}
	}
	type head struct {
		ID      string   `json:"id"`
		Owner   string   `json:"owner"`
		Hunks   []string `json:"hunks"`
		Details string   `json:"details"`
	}
	read := func(raw json.RawMessage) head {
		var h head
		_ = json.Unmarshal(raw, &h)
		return h
	}
	writtenByID := map[string]json.RawMessage{}
	for _, raw := range nextSections {
		writtenByID[read(raw).ID] = raw
	}
	var done []string
	for i, raw := range curSections {
		was := read(raw)
		if !slices.Contains(ids, was.ID) {
			continue
		}
		replacement, ok := writtenByID[was.ID]
		if !ok {
			continue
		}
		now := read(replacement)
		if now.Details != "" || !sameHunks(was.Hunks, now.Hunks) {
			continue
		}
		curSections[i] = replacement
		done = append(done, was.ID)
	}
	if len(done) == 0 {
		return current, nil, nil
	}
	kept := slices.DeleteFunc(curTiles, func(raw json.RawMessage) bool { return slices.Contains(done, read(raw).Owner) })
	taken := map[string]bool{}
	for _, raw := range kept {
		taken[read(raw).ID] = true
	}
	for _, raw := range nextTiles {
		if t := read(raw); slices.Contains(done, t.Owner) && !taken[t.ID] {
			kept = append(kept, raw)
			taken[t.ID] = true
		}
	}
	var err error
	if cur["sections"], err = json.Marshal(curSections); err != nil {
		return nil, nil, err
	}
	if kept == nil {
		kept = []json.RawMessage{}
	}
	if cur["tiles"], err = json.Marshal(kept); err != nil {
		return nil, nil, err
	}
	out, err := json.Marshal(cur)
	return out, done, err
}

func sameHunks(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
