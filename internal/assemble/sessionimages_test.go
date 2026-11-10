package assemble

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/vendors"
)

func TestMergeSessionImagesDeduplicatesAndPreservesOrigins(t *testing.T) {
	limits := DefaultLimits()
	// A native image's content identity is shared even when several sessions
	// supplied it, and when a PR description supplied the same bytes.
	first := SessionImage{Bytes: assembletest.PNG, Origin: bundle.ImageOrigin{Session: "one", Turn: 1, Input: "p1", Position: 1, Kind: "human_input"}}
	second := first
	second.Origin = bundle.ImageOrigin{Session: "two", Turn: 2, Input: "p2", Tool: "tool", Position: 1, Kind: "tool_result"}
	merged, data, notes := mergeImages(nil, nil, []SessionImage{first, second}, limits)
	if len(merged) != 1 || len(merged[0].Origins) != 2 || len(notes) != 0 || !bytes.Equal(data[merged[0].ID], first.Bytes) {
		t.Fatalf("merge: %+v, %v", merged, notes)
	}
	prImage := merged[0]
	prImage.ID, prImage.SourceURL, prImage.Origins = "att_0123456789abcdef0123456789abcdef", "https://github.com/user-attachments/assets/1", nil
	merged, _, _ = mergeImages([]bundle.DescriptionImage{prImage}, map[string][]byte{prImage.ID: first.Bytes}, []SessionImage{first, second}, limits)
	if len(merged) != 1 || merged[0].ID != prImage.ID || len(merged[0].Origins) != 2 || merged[0].SourceURL != prImage.SourceURL {
		t.Fatalf("description/session merge lost provenance: %+v", merged)
	}
	limits.MaxImages = 0
	if got, _, notes := mergeImages(nil, nil, []SessionImage{first}, limits); len(got) != 0 || len(notes) == 0 {
		t.Fatalf("image count cap: %+v %v", got, notes)
	}
	limits = DefaultLimits()
	limits.MaxImageBytes = 1
	if got, _, notes := mergeImages(nil, nil, []SessionImage{first}, limits); len(got) != 0 || len(notes) == 0 {
		t.Fatalf("image byte cap: %+v %v", got, notes)
	}
}

func TestPruneImagesOfTrimmedAndRestrictedSessions(t *testing.T) {
	b := bundle.Bundle{Sources: bundle.Sources{}, Sessions: []bundle.Session{{RootSessionID: "kept"}, {RootSessionID: "restricted", Restricted: true}}, Attachments: []bundle.Attachment{
		{ID: "native", Origins: []bundle.ImageOrigin{{Session: "trimmed"}, {Session: "restricted"}}},
		{ID: "shared", SourceURL: "https://github.com/user-attachments/assets/1", Origins: []bundle.ImageOrigin{{Session: "trimmed"}, {Session: "kept"}}},
	}}
	pruneImageOrigins(&b)
	if len(b.Attachments) != 1 || b.Attachments[0].ID != "shared" || len(b.Attachments[0].Origins) != 1 || b.Attachments[0].Origins[0].Session != "kept" {
		t.Fatalf("ineligible image provenance retained: %+v", b.Attachments)
	}
}

func TestLocalImagesUseOnlyLinkedNativeSession(t *testing.T) {
	f := linktest.New(t)
	found, _, err := sessions.Load(context.Background(), f.Repo, f.Store)
	if err != nil || len(found) != 1 {
		t.Fatalf("load: %v, %d", err, len(found))
	}
	path := found[0].Path
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(map[string]any{"type": "user", "sessionId": linktest.SessionID, "uuid": "image-prompt", "cwd": f.Repo, "timestamp": "2026-07-01T10:29:00Z", "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "Use this before screenshot"},
		map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(assembletest.PNG)}},
	}}})
	_, err = file.Write(append(line, '\n'))
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	source := LocalSessions{Dir: f.Repo, Stores: f.Store}
	images, _, err := source.ImageEvidence(context.Background(), []bundle.Session{{RootSessionID: linktest.SessionID}}, 20, 1<<20)
	if err != nil || len(images) != 1 || images[0].Origin.Kind != "human_input" || images[0].Origin.Session != linktest.SessionID || !bytes.Equal(images[0].Bytes, assembletest.PNG) {
		t.Fatalf("native human image: %+v, %v", images, err)
	}
	for _, linked := range [][]bundle.Session{{{RootSessionID: linktest.SessionID, Restricted: true}}, {{RootSessionID: "unrelated"}}} {
		if got, _, err := source.ImageEvidence(context.Background(), linked, 20, 1<<20); err != nil || len(got) != 0 {
			t.Fatalf("ineligible images: %+v %v", got, err)
		}
	}
	source.Stores.Sources = []vendors.Slug{sessions.Entire}
	if got, _, err := source.ImageEvidence(context.Background(), []bundle.Session{{RootSessionID: linktest.SessionID}}, 20, 1<<20); err != nil || len(got) != 0 {
		t.Fatalf("Entire source used: %+v %v", got, err)
	}
}

func TestUniqueImageLimitKeepsOriginsFromLaterSessions(t *testing.T) {
	f := linktest.New(t)
	secondID := "aaaaaaaa-0000-4000-8000-000000000002"
	f.AddSession(t, secondID, f.Repo, "Inspect the screenshot")
	found, _, err := sessions.Load(context.Background(), f.Repo, f.Store)
	if err != nil || len(found) != 2 {
		t.Fatalf("sessions: %d %v", len(found), err)
	}
	for _, s := range found {
		var blocks []any
		blocks = append(blocks, map[string]any{"type": "text", "text": "Here is the screenshot"})
		repeats := 1
		if s.ID == linktest.SessionID {
			repeats = 25
		}
		for range repeats {
			blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(assembletest.PNG)}})
		}
		line, _ := json.Marshal(map[string]any{"type": "user", "sessionId": s.ID, "uuid": "image-ask", "cwd": f.Repo, "timestamp": "2026-07-01T10:29:00Z", "message": map[string]any{"role": "user", "content": blocks}})
		file, err := os.OpenFile(s.Path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = file.Write(append(line, '\n'))
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	source := LocalSessions{Dir: f.Repo, Stores: f.Store}
	images, notes, err := source.ImageEvidence(context.Background(), []bundle.Session{{RootSessionID: linktest.SessionID}, {RootSessionID: secondID}}, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	merged, _, _ := mergeImages(nil, nil, images, DefaultLimits())
	if len(merged) != 1 || len(merged[0].Origins) != 26 || merged[0].Origins[25].Session != secondID {
		t.Fatalf("repeated captures lost later provenance: %+v %v", merged, notes)
	}
}
