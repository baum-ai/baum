package generate

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/store"
)

// Read the original assembly archive, rather than files exposed to the agent.
func capturedAttachments(path string, listed []bundle.Attachment) ([]store.Attachment, error) {
	if len(listed) == 0 {
		return nil, nil
	}
	if len(listed) > store.MaxAttachments {
		return nil, errors.New("too many captured attachments")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader := tar.NewReader(f)
	byPath := map[string]bundle.Attachment{}
	for _, a := range listed {
		byPath[a.Path] = a
	}
	out := make([]store.Attachment, 0, len(listed))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		a, ok := byPath[header.Name]
		if !ok || header.Typeflag != tar.TypeReg || header.Size > store.MaxAttachmentBytes || header.Size < 0 {
			return nil, errors.New("invalid captured attachment archive")
		}
		delete(byPath, header.Name)
		bytes, err := io.ReadAll(io.LimitReader(reader, store.MaxAttachmentBytes+1))
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(bytes)
		mime, _, _, valid := bundle.SniffImage(bytes)
		if !valid || mime != a.ContentType || hex.EncodeToString(hash[:]) != a.SHA256 {
			return nil, errors.New("captured attachment differs from assembled evidence")
		}
		out = append(out, store.Attachment{ID: a.ID, ContentType: mime, Bytes: bytes})
	}
	if len(byPath) != 0 {
		return nil, errors.New("captured attachment missing")
	}
	return out, nil
}
