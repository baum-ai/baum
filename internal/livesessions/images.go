package livesessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/vendors"
)

// ErrNoImage is the answer for an image a session's transcript does not
// hold.
var ErrNoImage = errors.New("livesessions: no such image")

// imageTypes are the images served, by media type: what the agents accept
// pasted, and nothing a browser would run.
var imageTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// promptImages is where a transcript holds the images pasted with one
// prompt: the records that carry them (ref), and each image's media type in
// order.
type promptImages struct {
	ref   string
	types []string
}

// pastedImage is one image a record carries, as base64 data.
type pastedImage struct {
	mediaType string
	data      string
}

// imageReader reads the images an agent's transcript holds.
type imageReader struct {
	// markers are what a line that matters holds, so the others, an image
	// making some megabytes long, are not decoded.
	markers [][]byte
	// images is the images a line carries and the ref of its records, or
	// none for a line that carries none.
	images func(raw []byte) (string, []pastedImage)
}

var imageReaders = map[vendors.Slug]imageReader{
	// A prompt's record holds its images as content blocks; its uuid is its
	// ref, the vendor id the graph has for the prompt.
	vendors.ClaudeCode: {markers: [][]byte{[]byte(`"type":"image"`)}, images: claudeImages},
	// The images go to the model in a user message of the turn, its ref, as
	// data URLs; the prompt's own record names only where the image was
	// saved, which does not last.
	vendors.Codex: {markers: [][]byte{[]byte(`"type":"input_image"`)}, images: codexImages},
}

func claudeImages(raw []byte) (string, []pastedImage) {
	var line struct {
		Type    string `json:"type"`
		UUID    string `json:"uuid"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &line) != nil || line.Type != "user" || len(line.Message.Content) == 0 ||
		line.Message.Content[0] != '[' {
		return "", nil
	}
	var blocks []struct {
		Type   string `json:"type"`
		Source struct {
			Type      string `json:"type"`
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if json.Unmarshal(line.Message.Content, &blocks) != nil {
		return "", nil
	}
	var images []pastedImage
	for _, b := range blocks {
		if b.Type == "image" && b.Source.Type == "base64" && slices.Contains(imageTypes, b.Source.MediaType) {
			images = append(images, pastedImage{mediaType: b.Source.MediaType, data: b.Source.Data})
		}
	}
	return line.UUID, images
}

func codexImages(raw []byte) (string, []pastedImage) {
	var line struct {
		Type    string `json:"type"`
		Payload struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				ImageURL string `json:"image_url"`
			} `json:"content"`
			Metadata struct {
				TurnID string `json:"turn_id"`
			} `json:"internal_chat_message_metadata_passthrough"`
		} `json:"payload"`
	}
	if json.Unmarshal(raw, &line) != nil || line.Type != "response_item" || line.Payload.Type != "message" ||
		line.Payload.Role != "user" || line.Payload.Metadata.TurnID == "" {
		return "", nil
	}
	var images []pastedImage
	for _, part := range line.Payload.Content {
		if part.Type != "input_image" {
			continue
		}
		header, data, ok := strings.Cut(part.ImageURL, ",")
		mediaType, base64ed := strings.CutSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		if ok && base64ed && strings.HasPrefix(header, "data:") && slices.Contains(imageTypes, mediaType) {
			images = append(images, pastedImage{mediaType: mediaType, data: data})
		}
	}
	return line.Payload.Metadata.TurnID, images
}

// eachImageLine calls fn with each line of the transcript file at path that
// holds one of markers, and its number, from 1, as the mappers count lines,
// until fn returns false.
func eachImageLine(path string, markers [][]byte, fn func(raw []byte, number int) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 1<<20)
	for number := 1; ; number++ {
		raw, err := reader.ReadBytes('\n')
		if len(raw) > 0 && slices.ContainsFunc(markers, func(m []byte) bool { return bytes.Contains(raw, m) }) &&
			!fn(raw, number) {
			return nil
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// findImages is the images pasted with each prompt of a Claude Code or
// Codex session, by the prompt's turn. Cursor's have none here.
func findImages(src Location, graph *session.Graph) (map[string]promptImages, error) {
	reader, ok := imageReaders[src.Vendor]
	if !ok || src.main == "" {
		return nil, nil
	}
	turnOf, err := imageRefs(src, graph)
	if err != nil {
		return nil, err
	}
	found := map[string]promptImages{}
	err = eachImageLine(src.main, reader.markers, func(raw []byte, _ int) bool {
		ref, images := reader.images(raw)
		turn, ok := turnOf[ref]
		if !ok || len(images) == 0 {
			return true
		}
		shown := found[turn]
		shown.ref = ref
		for _, image := range images {
			shown.types = append(shown.types, image.mediaType)
		}
		found[turn] = shown
		return true
	})
	if len(found) == 0 {
		return nil, err
	}
	return found, err
}

// imageRefs is the prompt each ref belongs to, by the prompt's turn. A
// Claude Code prompt's ref is the uuid the graph has for it. A Codex prompt
// is the rollout line the graph names, by its number in the file, whose
// record says the turn it opened, its ref.
func imageRefs(src Location, graph *session.Graph) (map[string]string, error) {
	refs := map[string]string{}
	if src.Vendor == vendors.ClaudeCode {
		for _, row := range graph.VendorIDs {
			if row.OurType == session.EntityUserInput && row.VendorIDKind == "uuid" {
				refs[row.VendorID] = row.OurID
			}
		}
		return refs, nil
	}
	byLine := map[string]string{}
	for _, row := range graph.VendorIDs {
		if row.OurType == session.EntityUserInput && row.VendorIDKind == "rollout.line" {
			byLine[row.VendorID] = row.OurID
		}
	}
	thread := ""
	markers := [][]byte{[]byte(`"type":"session_meta"`), []byte(`"type":"UserMessage"`)}
	err := eachImageLine(src.main, markers, func(raw []byte, number int) bool {
		var line struct {
			Type    string `json:"type"`
			Payload struct {
				ID     string `json:"id"`
				Type   string `json:"type"`
				TurnID string `json:"turn_id"`
				Item   struct {
					Type string `json:"type"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &line) != nil {
			return true
		}
		switch {
		case line.Type == "session_meta" && thread == "":
			thread = line.Payload.ID
		case line.Payload.Type == "item_completed" && line.Payload.Item.Type == "UserMessage" && line.Payload.TurnID != "":
			if turn, ok := byLine[fmt.Sprintf("%s:%d", thread, number)]; ok {
				refs[line.Payload.TurnID] = turn
			}
		}
		return true
	})
	return refs, err
}

// imageTypesByTurn is the media types of each prompt's images, as answers
// carry them.
func imageTypesByTurn(images map[string]promptImages) map[string][]string {
	if len(images) == 0 {
		return nil
	}
	out := make(map[string][]string, len(images))
	for turn, found := range images {
		out[turn] = found.types
	}
	return out
}

// Image is the n-th image (from 0) pasted with the prompt turn of the
// tracked session agent/id, and its media type, read from the session's
// transcript as Get reads it. An error is ErrNotTracked, an *Unavailable,
// ErrNoImage, or the context's.
func (t *Transcripts) Image(ctx context.Context, agent, id, turn string, n int) (string, []byte, error) {
	answer, err := t.Get(ctx, agent, id)
	if err != nil {
		return "", nil, err
	}
	found, path := answer.images[turn], answer.source
	reader, ok := imageReaders[answer.vendor]
	if !ok || found.ref == "" || n < 0 || n >= len(found.types) || path == "" {
		return "", nil, ErrNoImage
	}
	// A ref's images are counted across its records, in order.
	var image *pastedImage
	seen := 0
	err = eachImageLine(path, reader.markers, func(raw []byte, _ int) bool {
		if ctx.Err() != nil {
			return false
		}
		ref, images := reader.images(raw)
		if ref != found.ref {
			return true
		}
		if n-seen < len(images) {
			image = &images[n-seen]
			return false
		}
		seen += len(images)
		return true
	})
	if err != nil {
		return "", nil, err
	}
	if image == nil {
		return "", nil, ErrNoImage
	}
	data, err := base64.StdEncoding.DecodeString(image.data)
	if err != nil {
		return "", nil, ErrNoImage
	}
	return image.mediaType, data, nil
}
