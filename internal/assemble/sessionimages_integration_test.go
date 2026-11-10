package assemble_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
)

type imageSessions struct {
	assembletest.Sessions
	images []assemble.SessionImage
	called bool
}

func (s *imageSessions) ImageEvidence(_ context.Context, linked []bundle.Session, _ int, _ int64) ([]assemble.SessionImage, []string, error) {
	s.called = true
	return s.images, nil, nil
}

func TestSessionOnlyImagesReachGenerationArchive(t *testing.T) {
	repo := assembletest.NewRepo(t, assembletest.Options{})
	evidence := repo.Evidence()
	evidence.Images = nil
	evidence.PullRequest.Body = "A change with no uploaded images."
	source := &imageSessions{Sessions: assembletest.Sessions{Found: assembletest.SessionTree()}, images: []assemble.SessionImage{
		{Bytes: assembletest.PNG, Origin: bundle.ImageOrigin{Session: "claude-session-1", Turn: 1, Input: "p-1", Kind: "human_input", Position: 1}},
		{Bytes: assembletest.PNG, Origin: bundle.ImageOrigin{Session: "claude-session-1", Turn: 2, Input: "p-2", Tool: "t-1", ToolName: "view_image", Kind: "tool_result", Position: 1}},
	}}
	r, err := assembleWith(t, repo, evidence, nil, func(_ *assemble.Request, a *assemble.Assembler) { a.Sessions = source })
	if err != nil {
		t.Fatal(err)
	}
	attachments := r.result.Bundle.Attachments
	if !source.called || len(attachments) != 1 || len(attachments[0].Origins) != 2 || attachments[0].SourceURL != "" {
		t.Fatalf("session attachments: %+v", attachments)
	}
	dir := t.TempDir()
	archive := filepath.Join(t.TempDir(), "images.tar")
	if err := os.WriteFile(archive, r.attachments.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := bundle.UnpackAttachments(context.Background(), archive, dir, attachments, 1<<20); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "attachments"), 0700) })
	got, err := os.ReadFile(filepath.Join(dir, attachments[0].Path))
	if err != nil || !bytes.Equal(got, assembletest.PNG) {
		t.Fatalf("unpacked session image: %v", err)
	}
	// Facts never scan transcript images or write an archive.
	source.called = false
	_, err = assembleWith(t, repo, evidence, nil, func(req *assemble.Request, a *assemble.Assembler) {
		a.Sessions = source
		req.Facts = true
		req.Attachments = nil
		req.Checkout = nil
	})
	if err != nil || source.called {
		t.Fatalf("facts extracted images: %v", err)
	}
}
