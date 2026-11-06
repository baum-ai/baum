package generate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/store"
)

type nativeImageSource struct{ assembletest.Sessions }

func (s *nativeImageSource) ImageEvidence(context.Context, []bundle.Session, int, int64) ([]assemble.SessionImage, []string, error) {
	return []assemble.SessionImage{{Bytes: assembletest.PNG, Origin: bundle.ImageOrigin{Session: "claude-session-1", Turn: 1, Input: "p-1", Kind: "human_input", Position: 1}}}, nil, nil
}

func TestSessionImageSurvivesGenerationAndStoreReopen(t *testing.T) {
	f := newFixture(t)
	o := f.options(t)
	a := o.Assembler.(*assemble.Assembler)
	evidence := f.repo.Evidence()
	evidence.Images = nil
	evidence.PullRequest.Body = "A PR without image uploads."
	a.GitHub = &assembletest.GitHub{Evidence: evidence}
	a.Sessions = &nativeImageSource{assembletest.Sessions{Found: assembletest.SessionTree()}}
	var offered bundle.Attachment
	f.backend.RunFunc = func(_ context.Context, req agent.Request, _ int) (agent.Result, error) {
		raw, err := os.ReadFile(filepath.Join(req.Directory, "bundle.json"))
		if err != nil {
			return agent.Result{}, err
		}
		var b bundle.Bundle
		if err := json.Unmarshal(raw, &b); err != nil {
			return agent.Result{}, err
		}
		if len(b.Attachments) != 1 || b.Attachments[0].SourceURL != "" {
			t.Fatalf("native attachment not offered: %+v", b.Attachments)
		}
		offered = b.Attachments[0]
		data, err := os.ReadFile(filepath.Join(req.Directory, offered.Path))
		if err != nil || !bytes.Equal(data, assembletest.PNG) {
			t.Fatalf("native image not readable by model: %v", err)
		}
		doc, err := generatetest.Document(req.Directory, func(d map[string]any) {
			d["sections"].([]any)[0].(map[string]any)["blocks"] = []any{map[string]any{"kind": "images", "items": []any{map[string]any{"attachment": offered.ID}}, "source": []any{map[string]any{"type": "attachment", "id": offered.ID}}}}
		})
		return agent.Result{Document: doc, Model: "stub-model", Calls: 1, Tokens: 100}, err
	}
	result, err := generate.Run(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	s, err := store.Open("", root, true)
	if err != nil {
		t.Fatal(err)
	}
	key := store.Key{Repository: assembletest.Repository.String(), Number: assembletest.Number}
	version := store.Version{Metadata: result.Metadata, Document: result.Document, Attachments: result.Attachments}
	if err := s.Put(context.Background(), key, store.Version{}, version); err != nil {
		t.Fatal(err)
	}
	f.requireClean(t)
	reopened, err := store.Open("", root, false)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := reopened.Attachment(context.Background(), key, result.Metadata.Version, offered.ID)
	if err != nil || !bytes.Equal(saved.Bytes, assembletest.PNG) {
		t.Fatalf("saved session image lost after run cleanup/reopen: %v", err)
	}
	document, err := reopened.Latest(context.Background(), key)
	if err != nil || !bytes.Contains(document.Document, []byte(`"kind":"human_input"`)) {
		t.Fatalf("saved origin lost: %v", err)
	}
}
