package assemble

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/sessionimages"
	"github.com/baum-ai/baum/sessions/vendors"
)

// SessionImage is a retained native image, read only after its session was
// selected as evidence for the pull request. Bytes never enter bundle JSON.
type SessionImage struct {
	Bytes  []byte
	Origin bundle.ImageOrigin
}

type SessionImages interface {
	ImageEvidence(context.Context, []bundle.Session, int, int64) ([]SessionImage, []string, error)
}

func (l LocalSessions) ImageEvidence(ctx context.Context, linked []bundle.Session, maxImages int, maxBytes int64) ([]SessionImage, []string, error) {
	if len(linked) == 0 || maxImages <= 0 || maxBytes <= 0 {
		return nil, nil, nil
	}
	var ids []string
	for _, s := range linked {
		if !s.Restricted && s.RootSessionID != "" && !slices.Contains(ids, s.RootSessionID) {
			ids = append(ids, s.RootSessionID)
		}
	}
	if len(ids) == 0 {
		return nil, nil, nil
	}
	opts := l.Stores
	// Only the linked sessions are read; mapping every session of the
	// repository decoded gigabytes of unrelated transcripts.
	opts.SessionIDs = ids
	opts.Sources = nil
	for _, source := range []vendors.Slug{vendors.ClaudeCode, vendors.Codex} {
		if len(l.Stores.Sources) == 0 || slices.Contains(l.Stores.Sources, source) {
			opts.Sources = append(opts.Sources, source)
		}
	}
	if len(opts.Sources) == 0 {
		return nil, nil, nil
	}
	found, _, err := sessions.Load(ctx, l.Dir, opts)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]sessions.Session{}
	for _, s := range found {
		if !sessions.IsGenerationRun(s.Graph) {
			byID[s.ID] = s
		}
	}
	var images []SessionImage
	var notes []string
	unique := map[[32]byte]int{}
	originCounts := map[[32]byte]int{}
	budget := int64(64 << 20)
	for _, linked := range linked {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if linked.Restricted {
			continue
		}
		s, ok := byID[linked.RootSessionID]
		if !ok {
			notes = append(notes, "native image transcript unavailable for a linked session")
			continue
		}
		if budget <= 0 {
			notes = append(notes, "session image discovery stopped at the aggregate decoded-byte limit")
			break
		}
		// Bound raw candidates separately from unique attachments, so repeated
		// captures do not crowd distinct images out of later linked sessions.
		result := sessionimages.Read(ctx, s, sessionimages.Limits{MaxImages: min(maxImages, 20) * 40, MaxImageBytes: maxBytes, MaxTotalBytes: budget})
		for _, diagnostic := range result.Diagnostics {
			// Reader diagnostics contain categories, never transcript text or paths.
			notes = append(notes, fmt.Sprintf("session image unavailable: %v", diagnostic))
		}
		promptOf := locallink.ImagePromptResolver(s)
		for _, image := range result.Images {
			budget -= int64(len(image.Bytes))
			mime, width, height, valid := bundle.SniffImage(image.Bytes)
			if !valid || width < 1 || height < 1 || height > 40_000_000/width || mime != image.MediaType {
				notes = append(notes, "session image omitted: unsupported image bytes or dimensions")
				continue
			}
			hash := sha256.Sum256(image.Bytes)
			if at, seen := unique[hash]; seen {
				if originCounts[hash] >= 40 {
					if !slices.Contains(notes, "additional session image origins omitted beyond the 40-origin limit") {
						notes = append(notes, "additional session image origins omitted beyond the 40-origin limit")
					}
					continue
				}
				image.Bytes = images[at].Bytes
			} else {
				if len(unique) >= maxImages {
					if !slices.Contains(notes, "session image omitted beyond the unique-image limit") {
						notes = append(notes, "session image omitted beyond the unique-image limit")
					}
					continue
				}
			}
			origin, ok := promptOf(image.Source.TurnID)
			if !ok {
				notes = append(notes, "session image omitted because its owning prompt is unavailable")
				continue
			}
			origin.Kind, origin.Tool, origin.ToolName = image.Source.Kind, image.Source.ToolUseID, image.Source.ToolName
			origin.Position, origin.Timestamp = image.Source.Position, image.Source.Timestamp
			if originCounts[hash] == 0 {
				unique[hash] = len(images)
			}
			originCounts[hash]++
			images = append(images, SessionImage{Bytes: image.Bytes, Origin: origin})
		}
	}
	return images, notes, nil
}

func mergeImages(descriptions []bundle.DescriptionImage, contents map[string][]byte, candidates []SessionImage, limits Limits) ([]bundle.DescriptionImage, map[string][]byte, []string) {
	if contents == nil {
		contents = map[string][]byte{}
	}
	var images []bundle.DescriptionImage
	var notes []string
	byHash := map[string]int{}
	add := func(image bundle.DescriptionImage, data []byte) {
		if at, ok := byHash[image.SHA256]; ok {
			previous := &images[at]
			if image.SourceURL != "" && image.SourceURL != previous.SourceURL && !slices.Contains(previous.SourceURLs, image.SourceURL) {
				previous.SourceURLs = append(previous.SourceURLs, image.SourceURL)
			}
			if len(previous.Origins) < 40 {
				previous.Origins = append(previous.Origins, image.Origins[:min(len(image.Origins), 40-len(previous.Origins))]...)
			}
			return
		}
		if len(images) >= limits.MaxImages {
			notes = append(notes, "image omitted beyond the shared description/session image-count limit")
			return
		}
		image.Position = len(images) + 1
		byHash[image.SHA256] = len(images)
		images = append(images, image)
		contents[image.ID] = data
	}
	for _, image := range descriptions {
		if image.State == bundle.AttachmentStored {
			add(image, contents[image.ID])
		}
	}
	for _, candidate := range candidates {
		if int64(len(candidate.Bytes)) > limits.MaxImageBytes {
			notes = append(notes, "session image omitted beyond the per-image byte limit")
			continue
		}
		mime, width, height, ok := bundle.SniffImage(candidate.Bytes)
		if !ok || width < 1 || height < 1 || height > 40_000_000/width || candidate.Origin.Turn < 1 {
			notes = append(notes, "session image omitted: unsupported image bytes, dimensions or prompt provenance")
			continue
		}
		sum := sha256.Sum256(candidate.Bytes)
		original := hex.EncodeToString(sum[:])
		data, hash := candidate.Bytes, original
		// Deduplicate on the captured bytes first, so a session copy of a
		// description image, or a repeat of an already resized image, is not
		// decoded again and merges with the copy already kept.
		if at, seen := byHash[original]; seen {
			hash = images[at].SHA256
		} else if len(images) >= limits.MaxImages {
			// A new image would be dropped; do not decode it first.
			notes = append(notes, "image omitted beyond the shared description/session image-count limit")
			continue
		} else if small, smallMime, w, h, ok := downscale(candidate.Bytes, mime, width, height); ok {
			data, mime, width, height = small, smallMime, w, h
			sum = sha256.Sum256(data)
			hash = hex.EncodeToString(sum[:])
		}
		alt := "Agent session image"
		if candidate.Origin.Kind == "human_input" {
			alt = "Human-supplied session image"
		}
		add(bundle.DescriptionImage{ID: "att_" + hash[:32], State: bundle.AttachmentStored,
			ContentType: mime, Width: width, Height: height, Size: int64(len(data)),
			SHA256: hash, Alt: alt, Origins: []bundle.ImageOrigin{candidate.Origin}}, data)
		if at, kept := byHash[hash]; kept {
			byHash[original] = at
		}
	}
	return images, contents, notes
}

// A session trimmed from the generation input cannot leave image bytes behind.
func pruneImageOrigins(b *bundle.Bundle) {
	readable := map[string]bool{}
	for _, session := range b.Sessions {
		readable[session.RootSessionID] = !session.Restricted
	}
	for i := range b.Attachments {
		a := &b.Attachments[i]
		a.Origins = slices.DeleteFunc(a.Origins, func(origin bundle.ImageOrigin) bool { return !readable[origin.Session] })
	}
	b.Attachments = slices.DeleteFunc(b.Attachments, func(a bundle.Attachment) bool { return a.SourceURL == "" && len(a.Origins) == 0 })
	b.Sources["attachments"] = len(b.Attachments) > 0
}
