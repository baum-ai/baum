package review

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

const testAttachment = "att_0123456789abcdef0123456789abcdef"

func imagesBundle() *bundlepkg.Bundle {
	return &bundlepkg.Bundle{Repository: "org/repo", PullRequest: bundlepkg.PullRequest{Number: 11, State: "open", HeadSHA: "aaaaaaa", BaseSHA: "bbbbbbb", MergeBaseSHA: "bbbbbbb", Body: "![Before](https://github.com/user-attachments/assets/1)"}, Sources: bundlepkg.Sources{"diff": true}, Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Role: bundlepkg.RoleCode, Change: "added"}},
		Attachments: []bundlepkg.Attachment{{ID: testAttachment, Path: "attachments/" + testAttachment + ".png", SourceURL: "https://github.com/user-attachments/assets/1", ContentType: "image/png", Alt: "Before", Heading: "Screenshots", Position: 1, Width: 640, Height: 480, SHA256: strings.Repeat("a", 64)}}}
}

// imagesAuthored is the authored fixture with one images block naming the
// given attachment, and evidence anchored to the given attachment anchor.
func imagesAuthored(t *testing.T, block map[string]any, anchor map[string]any) []byte {
	t.Helper()
	doc, err := decodeObject(authoredFixture(), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	section := doc["sections"].([]any)[0].(map[string]any)
	if block != nil {
		section["blocks"] = []any{block}
	}
	if anchor != nil {
		section["evidence"] = []any{map[string]any{"id": "ev_screenshot", "kind": "media", "claim": "The author shows the new screen", "status": "reported", "source": "author_human", "anchors": []any{anchor}}}
	}
	encoded, _ := json.Marshal(doc)
	return encoded
}

func imagesBlock(id string) map[string]any {
	return map[string]any{"kind": "images", "items": []any{map[string]any{"attachment": id, "caption": "Before"}}, "source": []any{map[string]any{"type": "pr_body"}}}
}

func completeImages(t *testing.T, authored []byte, bundle *bundlepkg.Bundle) ([]byte, string) {
	t.Helper()
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	return validator.Complete(authored, bundle, "", "fixture-model", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
}

// An images block may name only images the bundle lists.
func TestImagesBlockNamesOnlyListedAttachments(t *testing.T) {
	if _, findings := completeImages(t, imagesAuthored(t, imagesBlock(testAttachment), nil), imagesBundle()); findings != "" {
		t.Fatalf("listed attachment rejected: %s", findings)
	}
	unlisted := "att_ffffffffffffffffffffffffffffffff"
	_, findings := completeImages(t, imagesAuthored(t, imagesBlock(unlisted), nil), imagesBundle())
	if !strings.Contains(findings, unlisted) {
		t.Fatalf("unlisted attachment accepted: %q", findings)
	}
	bundle := imagesBundle()
	bundle.Attachments = nil
	if _, findings := completeImages(t, imagesAuthored(t, imagesBlock(testAttachment), nil), bundle); !strings.Contains(findings, testAttachment) {
		t.Fatalf("images block accepted without an attachments part: %q", findings)
	}
}

// index.attachments is filled from the bundle list when a version is stored,
// with only the fields the schema defines.
func TestCompleteIndexesStoredAttachments(t *testing.T) {
	published, findings := completeImages(t, imagesAuthored(t, imagesBlock(testAttachment), nil), imagesBundle())
	if findings != "" {
		t.Fatal(findings)
	}
	var doc struct {
		Index struct {
			Attachments []map[string]any `json:"attachments"`
		} `json:"index"`
	}
	if err := json.Unmarshal(published, &doc); err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{{"id": testAttachment, "alt": "Before", "heading": "Screenshots", "position": float64(1), "content_type": "image/png", "width": float64(640), "height": float64(480), "source_url": "https://github.com/user-attachments/assets/1"}}
	if !reflect.DeepEqual(doc.Index.Attachments, want) {
		t.Fatalf("index.attachments = %v", doc.Index.Attachments)
	}
}

// The source fill covers an images block that omits its source.
func TestImagesBlockWithoutSourceIsAnchoredToTheSection(t *testing.T) {
	block := imagesBlock(testAttachment)
	delete(block, "source")
	published, findings := completeImages(t, imagesAuthored(t, block, nil), imagesBundle())
	if findings != "" {
		t.Fatal(findings)
	}
	doc, _ := decodeObject(published, "published")
	source := doc["sections"].([]any)[0].(map[string]any)["blocks"].([]any)[0].(map[string]any)["source"]
	if !reflect.DeepEqual(source, []any{map[string]any{"type": "hunk", "id": testHunk}}) {
		t.Fatalf("source = %v", source)
	}
}

// An attachment anchor with an id the bundle does not list is unresolved; one
// with only a url resolves as before.
func TestAttachmentAnchorResolvesAgainstTheAttachmentList(t *testing.T) {
	for _, tc := range []struct {
		name   string
		anchor map[string]any
		status string
	}{
		{"listed", map[string]any{"type": "attachment", "url": "https://github.com/user-attachments/assets/1", "id": testAttachment}, "reported"},
		{"unlisted", map[string]any{"type": "attachment", "url": "https://github.com/user-attachments/assets/2", "id": "att_ffffffffffffffffffffffffffffffff"}, "asserted"},
		{"url only", map[string]any{"type": "attachment", "url": "https://github.com/user-attachments/assets/2"}, "reported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			published, findings := completeImages(t, imagesAuthored(t, nil, tc.anchor), imagesBundle())
			if findings != "" {
				t.Fatal(findings)
			}
			doc, _ := decodeObject(published, "published")
			evidence := doc["sections"].([]any)[0].(map[string]any)["evidence"].([]any)[0].(map[string]any)
			if evidence["status"] != tc.status {
				t.Fatalf("status = %v, want %s", evidence["status"], tc.status)
			}
		})
	}
}

// A stored version is checked against its own index: a refresh, whose bundle
// carries no attachments, keeps the images and the index, and a stored images
// block its index does not list is rejected.
func TestRefreshChecksImagesAgainstTheStoredIndex(t *testing.T) {
	published, findings := completeImages(t, imagesAuthored(t, imagesBlock(testAttachment), nil), imagesBundle())
	if findings != "" {
		t.Fatal(findings)
	}
	validator, _ := sharedValidator()
	bundle := imagesBundle()
	bundle.Attachments = nil
	refreshed, err := validator.Refresh(published, bundle, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := decodeObject(published, "published")
	after, _ := decodeObject(refreshed, "refreshed")
	if !reflect.DeepEqual(after["sections"].([]any)[0].(map[string]any)["blocks"], before["sections"].([]any)[0].(map[string]any)["blocks"]) ||
		!reflect.DeepEqual(after["index"].(map[string]any)["attachments"], before["index"].(map[string]any)["attachments"]) {
		t.Fatalf("refresh changed images or their index: %s", refreshed)
	}
	delete(before["index"].(map[string]any), "attachments")
	tampered, _ := json.Marshal(before)
	if _, err := validator.Refresh(tampered, bundle, time.Now()); err == nil || !strings.Contains(err.Error(), testAttachment) {
		t.Fatalf("stored images block outside its index accepted: %v", err)
	}
}

// A section the model leaves out is carried over by the platform, which drops
// the images the description no longer offers rather than rejecting the
// version for a section the model cannot repair.
func TestCarriedSectionDropsImagesNoLongerListed(t *testing.T) {
	published, findings := completeImages(t, imagesAuthored(t, imagesBlock(testAttachment), nil), imagesBundle())
	if findings != "" {
		t.Fatal(findings)
	}
	stored, _ := decodeObject(published, "published")
	content, _ := json.Marshal(stored["sections"].([]any)[0])
	bundle := imagesBundle()
	bundle.Attachments = nil
	bundle.Previous = &bundlepkg.PreviousVersion{Version: 1, Sections: []bundlepkg.PreviousSection{{ID: "sec_parser", Title: "Parser", Kind: "logical", Content: content}}}
	authored, _ := decodeObject(authoredFixture(), "fixture")
	section := authored["sections"].([]any)[0].(map[string]any)
	section["id"], section["title"] = "sec_rewrite", "Rewrite"
	encoded, _ := json.Marshal(authored)
	next, findings := completeImages(t, encoded, bundle)
	if findings != "" {
		t.Fatalf("version carrying an image the description dropped was rejected: %s", findings)
	}
	doc, _ := decodeObject(next, "next")
	carried := doc["sections"].([]any)[0].(map[string]any)
	if carried["id"] != "sec_parser" || len(carried["blocks"].([]any)) != 0 {
		t.Fatalf("carried section = %v", carried)
	}
	// The model's own sections still may not name an unlisted image.
	section["blocks"] = []any{imagesBlock(testAttachment)}
	encoded, _ = json.Marshal(authored)
	if _, findings := completeImages(t, encoded, bundle); !strings.Contains(findings, testAttachment) {
		t.Fatalf("model-authored unlisted image accepted: %q", findings)
	}
}
