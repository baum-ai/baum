package cursortranscript_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/baum-ai/baum/sessions/cursorsession"
	"github.com/baum-ai/baum/sessions/cursortranscript"
)

type hostedPayloadWire struct {
	SchemaVersion string `json:"schema_version"`
	Tree          struct {
		Composer struct {
			Data  map[string]json.RawMessage `json:"data"`
			Diffs json.RawMessage            `json:"diffs"`
		} `json:"composer"`
	} `json:"tree"`
	RawFields map[string]string `json:"raw_fields"`
}

func TestHostedTreePayloadRoundTripPreservesMappedGraph(t *testing.T) {
	store, err := cursortranscript.Open(filepath.Join("testdata", "store.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tree, err := store.Tree("root")
	if err != nil {
		t.Fatal(err)
	}
	want, _, err := cursorsession.Build(tree)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := cursortranscript.EncodeTreePayload(tree)
	if err != nil {
		t.Fatal(err)
	}
	var wire hostedPayloadWire
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Tree.Composer.Diffs != nil {
		t.Fatalf("nonempty diffs leaked into the strict v1 tree: %s", payload)
	}
	if _, present := wire.Tree.Composer.Data["codeBlockData"]; present {
		t.Fatalf("nonempty codeBlockData leaked into the strict v1 tree: %s", payload)
	}
	if wire.RawFields["root\x00diffs"] == "" || wire.RawFields["root\x00data.codeBlockData"] == "" {
		t.Fatalf("nonempty diff extensions are missing: %#v", wire.RawFields)
	}
	decoded, err := cursortranscript.DecodeHostedTreePayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	for i, composer := range tree.Composers() {
		decodedComposer := decoded.Composers()[i]
		if diff := cmp.Diff(composer.Diffs, decodedComposer.Diffs); diff != "" {
			t.Fatalf("composer %d diffs changed across payload round trip (-want +got):\n%s", i, diff)
		}
		if diff := cmp.Diff(composer.Data.CodeBlocks, decodedComposer.Data.CodeBlocks); diff != "" {
			t.Fatalf("composer %d code blocks changed across payload round trip (-want +got):\n%s", i, diff)
		}
	}
	got, _, err := cursorsession.Build(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("serialized Cursor tree mapped to a different session graph (-want +got):\n%s", diff)
	}
}

func TestHostedTreePayloadDistinguishesCapturedEmptyDiffsFromOldPayload(t *testing.T) {
	store, err := cursortranscript.Open(filepath.Join("testdata", "store.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tree, err := store.Tree("draft")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := cursortranscript.EncodeTreePayload(tree)
	if err != nil {
		t.Fatal(err)
	}
	var wire hostedPayloadWire
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.SchemaVersion != "1" || wire.Tree.Composer.Diffs != nil {
		t.Fatalf("new payload changed the strict v1 tree: %s", payload)
	}
	if _, present := wire.Tree.Composer.Data["codeBlockData"]; present {
		t.Fatalf("new payload put codeBlockData in the strict v1 tree: %s", payload)
	}
	if got := wire.RawFields["draft\x00diffs"]; got != "[]" {
		t.Fatalf("empty captured diffs extension = %q, want []", got)
	}
	decoded, err := cursortranscript.DecodeHostedTreePayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Composer.Diffs == nil || len(decoded.Composer.Diffs) != 0 {
		t.Fatalf("new payload diffs = %#v, want non-nil empty", decoded.Composer.Diffs)
	}

	old, err := cursortranscript.DecodeHostedTreePayload([]byte(`{"schema_version":"1","tree":{"composer":{"data":{"composerId":"old"},"bubbles":[]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if old.Composer.Diffs != nil {
		t.Fatalf("old payload diffs = %#v, want nil", old.Composer.Diffs)
	}
}

func TestHostedDecodeTreePayloadRejectsMalformedDiffExtension(t *testing.T) {
	_, err := cursortranscript.DecodeHostedTreePayload([]byte(`{"schema_version":"1","tree":{"composer":{"data":{"composerId":"old"},"bubbles":[]}},"raw_fields":{"old\u0000diffs":"null"}}`))
	if err == nil || !strings.Contains(err.Error(), "diffs") {
		t.Fatalf("DecodeTreePayload error = %v, want malformed diffs extension", err)
	}
}

func TestHostedDecodeTreePayloadRejectsNullNodes(t *testing.T) {
	for name, body := range map[string]string{
		"null bubble":          `{"schema_version":"1","tree":{"composer":{"data":{"composerId":"a"},"bubbles":[null]}}}`,
		"null unlisted bubble": `{"schema_version":"1","tree":{"composer":{"data":{"composerId":"a"},"bubbles":[],"unlisted_bubbles":[null]}}}`,
		"child without data":   `{"schema_version":"1","tree":{"composer":{"data":{"composerId":"a"},"bubbles":[]},"children":[{"composer":{"bubbles":[]}}]}}`,
		"null child":           `{"schema_version":"1","tree":{"composer":{"data":{"composerId":"a"},"bubbles":[]},"children":[null]}}`,
		"child without id":     `{"schema_version":"1","tree":{"composer":{"data":{"composerId":"a"},"bubbles":[]},"children":[{"composer":{"data":{},"bubbles":[]}}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := cursortranscript.DecodeHostedTreePayload([]byte(body)); err == nil {
				t.Fatal("DecodeTreePayload accepted the payload")
			}
		})
	}
}

func TestHostedDecodeTreePayloadRejectsUnknownVersion(t *testing.T) {
	_, err := cursortranscript.DecodeHostedTreePayload([]byte(`{"schema_version":"2","tree":null}`))
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("DecodeTreePayload error = %v", err)
	}
}
