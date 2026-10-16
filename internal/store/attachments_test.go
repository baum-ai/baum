package store

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

const imageID = "att_0123456789abcdef0123456789abcdef"

func imageVersion(t *testing.T, k Key, n int) Version {
	t.Helper()
	v := version(k, n)
	var document map[string]any
	if err := json.Unmarshal(v.Document, &document); err != nil {
		t.Fatal(err)
	}
	document["index"] = map[string]any{"attachments": []any{map[string]any{"id": imageID}}}
	v.Document, _ = json.Marshal(document)
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatal(err)
	}
	v.Attachments = []Attachment{{imageID, "image/png", png}}
	return v
}
func TestVersionAttachmentAtomicityAndIsolation(t *testing.T) {
	s := opened(t)
	ctx := context.Background()
	key := Key{"owner/repo", 7}
	first := imageVersion(t, key, 1)
	if err := s.Put(ctx, key, Version{}, first); err != nil {
		t.Fatal(err)
	}
	got, err := s.Attachment(ctx, key, 0, imageID)
	if err != nil || !bytes.Equal(got.Bytes, first.Attachments[0].Bytes) {
		t.Fatal(got, err)
	}
	second := version(key, 2)
	if err = s.Put(ctx, key, first, second); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Attachment(ctx, key, 0, imageID); !errors.Is(err, ErrNotFound) {
		t.Fatal("latest exposed old image", err)
	}
	if _, err = s.Attachment(ctx, key, 1, imageID); err != nil {
		t.Fatal(err)
	}
	for _, other := range []Key{{"other/repo", 7}, {"owner/repo", 8}, {"enterprise.example/owner/repo", 7}} {
		if _, err = s.Attachment(ctx, other, 1, imageID); !errors.Is(err, ErrNotFound) {
			t.Fatal("image crossed repository", err)
		}
	}
	// The winning immutable version cannot acquire a loser's attachments.
	if err = s.Put(ctx, key, first, imageVersion(t, key, 2)); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = s.Attachment(ctx, key, 2, imageID); !errors.Is(err, ErrNotFound) {
		t.Fatal("conflicting image published", err)
	}
}
func TestAttachmentsRejectUnlistedUnsafeAndDuplicateWithoutVersion(t *testing.T) {
	s := opened(t)
	ctx := context.Background()
	key := Key{"owner/repo", 7}
	for _, mutate := range []func(*Version){func(v *Version) { v.Document = version(key, 1).Document }, func(v *Version) {
		v.Attachments[0].ContentType = "image/svg+xml"
		v.Attachments[0].Bytes = []byte(`<svg onload="alert(1)"></svg>`)
	}, func(v *Version) { v.Attachments = append(v.Attachments, v.Attachments[0]) }, func(v *Version) { v.Attachments[0].ID = "../secret" }, func(v *Version) { v.Attachments[0].Bytes = make([]byte, MaxAttachmentBytes+1) }} {
		v := imageVersion(t, key, 1)
		mutate(&v)
		if err := s.Put(ctx, key, Version{}, v); err == nil {
			t.Fatal("unsafe attachment accepted")
		}
		if _, err := s.Latest(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Fatal("failed attachment published document", err)
		}
	}
}
func TestOldVersionHasNoAttachmentsTable(t *testing.T) {
	s := opened(t)
	ctx := context.Background()
	key := Key{"owner/repo", 7}
	v := imageVersion(t, key, 1)
	v.Attachments = nil
	if err := s.Put(ctx, key, Version{}, v); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attachment(ctx, key, 1, imageID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
