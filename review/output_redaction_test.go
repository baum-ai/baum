package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

// Both are detected by goredact's balanced profile: a database URL with
// credentials and an Anthropic-shaped provider key.
const (
	leakedDatabaseURL = "postgres://admin:Sup3rS3cretPassw0rd@db.internal.example.com:5432/app"
	leakedProviderKey = "sk-" + "ant-api03-Ab3dEf6hIj9kLm2nOp5qRs8tUv1wXy4zAb7dEf0hIj3kLm6nOp9qRs2tUv5wXy8zAb1dEf4hIj7kLm0nOp3qRs6tUv9wXy2zAb5dEf8hIjAA"
	redactionHeadSHA  = "3f786850e387550fdab836ed7e6dc881de23001b"
	redactionBaseSHA  = "89e6c98d92887913cadf06b2adb97f26cde4849b"
	// plantedSecret is a syntactically valid (but fake) GitHub personal access
	// token.
	plantedSecret = "ghp" + "_JTeKdNnFRIBXuDL7DxtpYlSXpfKtHF4vUCsM"
)

// The patch line carries a token, which bundle redaction masks before the
// agent sees it.
var redactionPatch = "@@ -1,1 +1,1 @@\n-token := \"" + plantedSecret + "\"\n+token := os.Getenv(\"TOKEN\")"

func testRedactor(t *testing.T) *bundlepkg.Redactor {
	t.Helper()
	redactor, err := bundlepkg.NewRedactor()
	checkNoError(t, err)
	return redactor
}

func newTestOutputRedactor(t *testing.T, redactor *bundlepkg.Redactor, bundle *bundlepkg.Bundle, checkout map[string]bool) *OutputRedactor {
	t.Helper()
	v, err := sharedValidator()
	checkNoError(t, err)
	o, err := NewOutputRedactor(v, redactor, bundle, checkout)
	checkNoError(t, err)
	return o
}

// maskedTokenLine is the removed patch line as the redacted bundle shows it.
func maskedTokenLine(t *testing.T) string {
	t.Helper()
	masked, count, err := testRedactor(t).RedactString(context.Background(), "token := \""+plantedSecret+"\"")
	checkNoError(t, err)
	checkEqual(t, 1, count)
	return masked
}

func requireNoLeakedSecret(t *testing.T, where string, body []byte) {
	t.Helper()
	for _, secret := range []string{"Sup3rS3cretPassw0rd", leakedProviderKey, plantedSecret} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("%s keeps %q: %s", where, secret, body)
		}
	}
}

// syntheticKeyLines returns a PEM private key block, one line per element,
// assembled at run time so no secret-shaped literal sits in the source.
func syntheticKeyLines() []string {
	label := "RSA " + "PRIVATE KEY"
	lines := []string{"-----BEGIN " + label + "-----"}
	for i := range 6 {
		lines = append(lines, strings.Repeat("Qz7"+"Lw3Kp"+string(rune('a'+i))+"Xb9", 8)[:64])
	}
	return append(lines, "-----END "+label+"-----")
}

func requireNoKeyLine(t *testing.T, where string, body []byte) {
	t.Helper()
	for _, line := range syntheticKeyLines() {
		if bytes.Contains(body, []byte(line)) {
			t.Fatalf("%s keeps key line %q: %s", where, line, body)
		}
	}
}

// keyedAnchors cites one reference of each type, each holding the provider
// key, so none of them names anything the bundle or checkout holds.
func keyedAnchors() []any {
	key := " " + leakedProviderKey
	return []any{
		map[string]any{"type": "file", "path": "deploy/" + leakedProviderKey + "/app.env"},
		map[string]any{"type": "pr_comment", "id": "comment" + key},
		map[string]any{"type": "review_thread", "id": "thread" + key},
		map[string]any{"type": "check", "id": "check" + key},
		map[string]any{"type": "bot_report", "id": "report" + key},
		map[string]any{"type": "prompt", "session": "session" + key, "index": 1, "input": "input" + key},
		map[string]any{"type": "session_event", "session": "session" + key, "event": "event" + key},
		map[string]any{"type": "issue", "system": "system" + key, "key": "ISSUE" + key},
	}
}

// A document the validator never normalized may carry keys the contract does
// not name; only the text of an excerpt line the bundle supplies is kept.
func TestUnknownKeysUnderBundleExcerptAreMasked(t *testing.T) {
	redactor := testRedactor(t)
	bundle := &bundlepkg.Bundle{
		PullRequest: bundlepkg.PullRequest{HeadSHA: redactionHeadSHA, BaseSHA: redactionBaseSHA, MergeBaseSHA: redactionBaseSHA},
		Diff:        []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Change: "modified", Patch: redactionPatch}},
	}
	checkNoError(t, bundlepkg.Redact(context.Background(), bundle, redactor))
	removed, _ := json.Marshal(maskedTokenLine(t))
	document := []byte(`{"sections":[{"tiles":[{"kind":"bug-fix","code":[{"path":"main.go","revision":"` + redactionHeadSHA + `","review_version":1,"Diff":"` + leakedDatabaseURL + `","diff":{"note":"` + leakedDatabaseURL + `","hunks":[{"header":"@@ -1,1 +1,1 @@","Header":"` + leakedDatabaseURL + `","lines":[{"kind":"remove","old_line":1,"text":` + string(removed) + `,"note":"` + leakedDatabaseURL + `","Text":"` + leakedDatabaseURL + `"},{"kind":"add","new_line":1,"text":"token := os.Getenv(\"TOKEN\")"}]}]}}]}]}]}`)
	o := newTestOutputRedactor(t, redactor, bundle, nil)
	redacted, err := o.Document(context.Background(), document)
	checkNoError(t, err)
	requireNoLeakedSecret(t, "document", redacted)
	checkEqual(t, 5, o.Masked())
	var doc map[string]any
	checkNoError(t, json.Unmarshal(redacted, &doc))
	hunk := doc["sections"].([]any)[0].(map[string]any)["tiles"].([]any)[0].(map[string]any)["code"].([]any)[0].(map[string]any)["diff"].(map[string]any)["hunks"].([]any)[0].(map[string]any)
	checkEqual(t, "@@ -1,1 +1,1 @@", hunk["header"])
	checkEqual(t, maskedTokenLine(t), hunk["lines"].([]any)[0].(map[string]any)["text"])
}

// Formatting and key order outlive redaction when nothing is masked, and a
// count of spans accumulates over calls.
func TestOutputRedactorReturnsCleanDocumentsByteForByte(t *testing.T) {
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	returned := []byte("{\n  \"z\": 1.50,\n  \"a\": \"plain text\"\n}")
	redacted, err := o.Document(context.Background(), returned)
	if err != nil || !bytes.Equal(redacted, returned) {
		t.Fatalf("%q %v", redacted, err)
	}
	checkEqual(t, 0, o.Masked())

	masked, err := o.Text(context.Background(), "dsn "+leakedDatabaseURL)
	checkNoError(t, err)
	requireNoLeakedSecret(t, "text", []byte(masked))
	redacted, err = o.Document(context.Background(), []byte("not json "+leakedProviderKey))
	checkNoError(t, err)
	requireNoLeakedSecret(t, "non-JSON output", redacted)
	checkEqual(t, 2, o.Masked())
}

func TestOutputRedactorFailsWithTheContext(t *testing.T) {
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	document := []byte(`{"summary":{"text":"` + leakedProviderKey + `"}}`)
	if _, err := o.Document(ctx, document); !errors.Is(err, context.Canceled) {
		t.Fatalf("redaction under a cancelled context: %v", err)
	}
	if _, err := o.Text(ctx, leakedProviderKey); !errors.Is(err, context.Canceled) {
		t.Fatalf("text redaction under a cancelled context: %v", err)
	}
}

func TestOutputRedactorMasksWithoutABundle(t *testing.T) {
	o := newTestOutputRedactor(t, testRedactor(t), nil, nil)
	document := []byte(`{"summary":{"text":"` + leakedProviderKey + `"}}`)
	redacted, err := o.Document(context.Background(), document)
	checkNoError(t, err)
	requireNoLeakedSecret(t, "document", redacted)
	checkEqual(t, 1, o.Masked())
}

func TestOutputRedactionJoinsLineArrays(t *testing.T) {
	redactor := testRedactor(t)
	// redact runs texts as the lines of a model-authored code source, or of
	// an array under "lines" the contract does not describe, and returns the
	// stored texts and the masked-span count.
	redact := func(t *testing.T, described bool, texts []string) ([]string, int, []byte, []byte) {
		t.Helper()
		lines := make([]any, len(texts))
		for i, text := range texts {
			lines[i] = map[string]any{"kind": "add", "new_line": i + 1, "text": text}
		}
		hunks := []any{map[string]any{"header": fmt.Sprintf("@@ -0,0 +1,%d @@", len(texts)), "lines": lines}}
		value := map[string]any{"notes": map[string]any{"lines": lines}}
		if described {
			value = map[string]any{"sections": []any{map[string]any{"tiles": []any{map[string]any{"kind": "bug-fix", "code": []any{map[string]any{"path": "main.go", "revision": "c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff", "review_version": 1, "diff": map[string]any{"hunks": hunks}}}}}}}}
		}
		document, err := json.Marshal(value)
		checkNoError(t, err)
		o := newTestOutputRedactor(t, redactor, &bundlepkg.Bundle{}, nil)
		redacted, err := o.Document(context.Background(), document)
		checkNoError(t, err)
		var doc map[string]any
		checkNoError(t, json.Unmarshal(redacted, &doc))
		stored := doc["notes"]
		if described {
			stored = doc["sections"].([]any)[0].(map[string]any)["tiles"].([]any)[0].(map[string]any)["code"].([]any)[0].(map[string]any)["diff"].(map[string]any)["hunks"].([]any)[0]
		}
		var out []string
		for _, line := range stored.(map[string]any)["lines"].([]any) {
			out = append(out, line.(map[string]any)["text"].(string))
		}
		return out, o.Masked(), document, redacted
	}

	for _, described := range []bool{true, false} {
		t.Run(fmt.Sprintf("described %t", described), func(t *testing.T) {
			t.Run("private key", func(t *testing.T) {
				key := syntheticKeyLines()
				for _, line := range key {
					if masked, count, err := redactor.RedactString(context.Background(), line); err != nil || count != 0 || masked != line {
						t.Fatalf("a lone key line is masked, so the case proves nothing: %q %d %v", masked, count, err)
					}
				}
				texts := append([]string{"const key = `"}, key...)
				texts = append(texts, "`")
				out, masked, _, redacted := redact(t, described, texts)
				requireNoKeyLine(t, "document", redacted)
				checkEqual(t, 1, masked)
				checkEqual(t, len(texts), len(out))
				checkEqual(t, "const key = `", out[0])
				checkEqual(t, "`", out[len(out)-1])
				for i, line := range out[1 : len(out)-1] {
					checkEqual(t, len(key[i]), len(line))
				}
			})

			t.Run("key header without its footer", func(t *testing.T) {
				key := syntheticKeyLines()
				_, masked, _, redacted := redact(t, described, key[:3])
				requireNoKeyLine(t, "document", redacted)
				checkEqual(t, 1, masked)
			})

			t.Run("a secret on each of two lines", func(t *testing.T) {
				texts := []string{"dsn := \"" + leakedDatabaseURL + "\"", "key := \"" + leakedProviderKey + "\""}
				out, masked, _, redacted := redact(t, described, texts)
				requireNoLeakedSecret(t, "document", redacted)
				checkEqual(t, 2, masked)
				if !strings.HasPrefix(out[0], "dsn := \"postgres://admin:") || len(out[0]) != len(texts[0]) {
					t.Fatalf("the first secret is not masked in place: %q", out[0])
				}
				checkEqual(t, 2, len(out))
			})

			t.Run("plain code", func(t *testing.T) {
				texts := []string{"func main() {", "", "\tfmt.Println(\"hello\")", "}"}
				out, masked, document, redacted := redact(t, described, texts)
				checkEqual(t, 0, masked)
				if !bytes.Equal(document, redacted) {
					t.Fatalf("plain code changed:\n%s\n%s", document, redacted)
				}
				checkEqual(t, strings.Join(texts, "\n"), strings.Join(out, "\n"))
			})
		})
	}
}

// A line the bundle supplies keeps its text unless a key the model continues
// in its own lines reaches into it: a lone key header passes bundle
// redaction, and the body the model quotes after it is found only with it.
func TestOutputRedactionMasksBundleLinesAKeyReachesInto(t *testing.T) {
	redactor := testRedactor(t)
	ctx := context.Background()
	key := syntheticKeyLines()
	bundle := &bundlepkg.Bundle{PullRequest: bundlepkg.PullRequest{HeadSHA: redactionHeadSHA, BaseSHA: redactionBaseSHA, MergeBaseSHA: redactionBaseSHA},
		Diff: []bundlepkg.Hunk{{ID: testHunk, Path: "main.go", Change: "modified", Patch: "@@ -1,2 +1,2 @@\n-old := 1\n+new := 2\n " + key[0]}}}
	checkNoError(t, bundlepkg.Redact(ctx, bundle, redactor))
	if !strings.Contains(bundle.Diff[0].Patch, key[0]) {
		t.Fatalf("bundle redaction masks a lone key header, so the case proves nothing: %q", bundle.Diff[0].Patch)
	}
	lines := []any{
		map[string]any{"kind": "remove", "old_line": 1, "text": "old := 1"},
		map[string]any{"kind": "add", "new_line": 1, "text": "new := 2"},
		map[string]any{"kind": "context", "old_line": 2, "new_line": 2, "text": key[0]},
	}
	for i, line := range key[1:] {
		lines = append(lines, map[string]any{"kind": "add", "new_line": i + 3, "text": line})
	}
	source := map[string]any{"path": "main.go", "revision": redactionHeadSHA, "why": "Adds the key", "review_version": 1, "diff": map[string]any{"hunks": []any{map[string]any{"header": "@@ -1,2 +1,8 @@", "lines": lines}}}}
	document, err := json.Marshal(map[string]any{"sections": []any{map[string]any{"tiles": []any{map[string]any{"kind": "bug-fix", "code": []any{source}}}}}})
	checkNoError(t, err)
	o := newTestOutputRedactor(t, redactor, bundle, nil)
	redacted, err := o.Document(ctx, document)
	checkNoError(t, err)
	requireNoKeyLine(t, "document", redacted)
	checkEqual(t, 1, o.Masked())
	for _, kept := range []string{`"text":"old := 1"`, `"text":"new := 2"`} {
		if !bytes.Contains(redacted, []byte(kept)) {
			t.Fatalf("bundle line %s was masked: %s", kept, redacted)
		}
	}
}

// Undescribed fields and agent findings are part of the joined text too, and
// a string holding a newline is cut back out whole.
func TestOutputRedactionJoinsUndescribedText(t *testing.T) {
	redactor := testRedactor(t)
	key := syntheticKeyLines()
	findings := []any{}
	for _, line := range key {
		findings = append(findings, map[string]any{"text": line})
	}
	document, err := json.Marshal(map[string]any{
		"agent_reviews": []any{map[string]any{"findings": findings}},
		"notes":         []any{key[0] + "\n" + key[1], strings.Join(key[2:], "\n")},
	})
	checkNoError(t, err)
	o := newTestOutputRedactor(t, redactor, &bundlepkg.Bundle{}, nil)
	redacted, err := o.Document(context.Background(), document)
	checkNoError(t, err)
	requireNoKeyLine(t, "document", redacted)
	checkEqual(t, 2, o.Masked())
	var doc struct {
		Notes []string `json:"notes"`
	}
	checkNoError(t, json.Unmarshal(redacted, &doc))
	checkEqual(t, 2, len(doc.Notes))
	checkEqual(t, 1, strings.Count(doc.Notes[0], "\n"))
	checkEqual(t, len(key[0])+1+len(key[1]), len(doc.Notes[0]))
}

// A code line holding a newline is masked in place; it does not fail the run.
func TestOutputRedactionKeepsCodeLinesHoldingNewlines(t *testing.T) {
	redactor := testRedactor(t)
	key := syntheticKeyLines()
	lines := []any{
		map[string]any{"kind": "add", "new_line": 1, "text": key[0] + "\n" + key[1]},
		map[string]any{"kind": "add", "new_line": 2, "text": strings.Join(key[2:], "\n")},
	}
	source := map[string]any{"path": "main.go", "revision": "c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff", "review_version": 1, "diff": map[string]any{"hunks": []any{map[string]any{"header": "@@ -0,0 +1,2 @@", "lines": lines}}}}
	document, err := json.Marshal(map[string]any{"sections": []any{map[string]any{"tiles": []any{map[string]any{"kind": "bug-fix", "code": []any{source}}}}}})
	checkNoError(t, err)
	o := newTestOutputRedactor(t, redactor, &bundlepkg.Bundle{}, nil)
	redacted, err := o.Document(context.Background(), document)
	checkNoError(t, err)
	requireNoKeyLine(t, "document", redacted)
	checkEqual(t, 1, o.Masked())
	if !bytes.Contains(redacted, []byte(`\n`)) {
		t.Fatalf("the newlines inside the lines were lost: %s", redacted)
	}
}

// Only a pattern pinning the whole string to a closed alphabet closes it.
func TestClosedPattern(t *testing.T) {
	for pattern, closed := range map[string]bool{
		`^[a-z][a-z0-9_]*$`: true, `^[0-9a-f]{7,64}$`: true, `^h_[0-9a-f]{12}(-[0-9]+)?$`: true, `^SPEC-\d+$`: true,
		`^[0-9]{4}-[0-9]{2}-[0-9]{2}(\.[0-9]+)?$`: true, `^tile_[a-z0-9_]+$`: true,
		`\S`: false, `^\S`: false, `.+`: false, `^.+$`: false, `^[^/\s]+/[^/\s]+$`: false, `^[a-z]+`: false, `^\w+$`: true, `^\W+$`: false, ``: false, `^a|.+$`: false, `^(a|b)$`: true, `^(ev|rsk|dec)_[a-z0-9_]+$`: true,
		`^(a|.*)$`: false, `^(a|b)_(c|d)$`: false, `^(a|b)|c$`: false, `^(a|b|)$`: false, `^(?:a|b)$`: false,
	} {
		if closedPattern(pattern) != closed {
			t.Errorf("closedPattern(%q) = %t, want %t", pattern, !closed, closed)
		}
	}
}

// A reference that resolves keeps its exact text, whatever detection finds
// in it; the same text elsewhere is masked. The bundle here is not redacted,
// so its identifiers carry the key.
func TestOutputRedactionKeepsResolvedReferences(t *testing.T) {
	redactor := testRedactor(t)
	key := " " + leakedProviderKey
	bundle := &bundlepkg.Bundle{
		Diff:         []bundlepkg.Hunk{{ID: testHunk, Path: "src/main.go"}},
		Conversation: []bundlepkg.ConversationItem{{ID: "comment" + key}, {ID: "report" + key, BotReport: true}},
		Threads:      []bundlepkg.Thread{{ID: "thread" + key}},
		Checks:       []bundlepkg.Check{{ID: "check" + key}},
		Sessions:     []bundlepkg.Session{{RootSessionID: "session" + key, CommandEvents: []bundlepkg.CommandEvent{{ID: "event" + key}}}},
		References:   []bundlepkg.Reference{{Kind: "issue_key", Value: "ISSUE" + key}},
	}
	checkout := map[string]bool{"deploy/" + leakedProviderKey + "/app.env": true}
	document, err := json.Marshal(map[string]any{"sections": []any{map[string]any{"blocks": []any{map[string]any{"kind": "intent", "text": "Mentions comment" + key + ".", "anchors": keyedAnchors()}}}}})
	checkNoError(t, err)
	o := newTestOutputRedactor(t, redactor, bundle, checkout)
	redacted, err := o.Document(context.Background(), document)
	checkNoError(t, err)
	var doc map[string]any
	checkNoError(t, json.Unmarshal(redacted, &doc))
	block := doc["sections"].([]any)[0].(map[string]any)["blocks"].([]any)[0].(map[string]any)
	if strings.Contains(block["text"].(string), leakedProviderKey) {
		t.Fatalf("free text kept the key: %q", block["text"])
	}
	stored := block["anchors"].([]any)
	for i, want := range keyedAnchors() {
		got := stored[i].(map[string]any)
		for field, value := range want.(map[string]any) {
			text, ok := value.(string)
			if !ok || field == "type" {
				continue
			}
			// An issue's system and a prompt's input name nothing on their own.
			free := field == "system" || field == "input"
			if kept := got[field] == text; kept == free {
				t.Errorf("%s %s = %q, kept %t, want kept %t", want.(map[string]any)["type"], field, got[field], kept, !free)
			}
		}
	}
	// The free text, the issue's system and the prompt's input.
	checkEqual(t, 3, o.Masked())
}

func TestClosedValue(t *testing.T) {
	o := &outputRedaction{defs: map[string]any{"id": map[string]any{"type": "string", "pattern": "^sec_[a-z0-9_]+$"}}}
	for _, c := range []struct {
		schema map[string]any
		value  string
		kind   slotKind
	}{
		{map[string]any{"const": "logical"}, "logical", slotKept},
		{map[string]any{"const": "logical"}, "logical " + leakedProviderKey, slotFree},
		{map[string]any{"enum": []any{"feature", "fix"}}, "fix", slotKept},
		{map[string]any{"enum": []any{"feature", "fix"}}, "fix!", slotFree},
		{map[string]any{"$ref": "#/$defs/id"}, "sec_parser", slotClosed},
		{map[string]any{"$ref": "#/$defs/id"}, "sec_parser " + leakedProviderKey, slotFree},
		{map[string]any{"type": "string", "pattern": `\S`}, "anything", slotFree},
		{map[string]any{"type": "string", "format": "date-time"}, "2026-10-06T16:00:00Z", slotClosed},
		{map[string]any{"type": "string", "format": "date-time"}, "2026-10-06 " + leakedProviderKey, slotFree},
		{map[string]any{"anyOf": []any{map[string]any{"type": "string", "pattern": "^[0-9a-f]{7,64}$"}, map[string]any{"type": "null"}}}, redactionHeadSHA, slotClosed},
		{map[string]any{"anyOf": []any{map[string]any{"type": "string", "pattern": "^[0-9a-f]{7,64}$"}, map[string]any{"type": "null"}}}, "head", slotFree},
	} {
		if got, _ := o.closedValue(c.schema, c.value); got != c.kind {
			t.Errorf("closedValue(%v, %q) = %d, want %d", c.schema, c.value, got, c.kind)
		}
	}
	// The bound is the closing schema's maxLength, or the one around its
	// branch, and 0 without either.
	for _, c := range []struct {
		schema map[string]any
		limit  int
	}{
		{map[string]any{"type": "string", "pattern": "^[a-z_]+$", "maxLength": float64(128)}, 128},
		{map[string]any{"type": "string", "pattern": "^[a-z_]+$"}, 0},
		{map[string]any{"maxLength": float64(64), "anyOf": []any{map[string]any{"type": "string", "pattern": "^[a-z_]+$"}, map[string]any{"type": "null"}}}, 64},
		{map[string]any{"maxLength": float64(64), "anyOf": []any{map[string]any{"type": "string", "pattern": "^[a-z_]+$", "maxLength": float64(32)}}}, 32},
		{map[string]any{"type": "string", "format": "date-time"}, 0},
	} {
		value := "abc"
		if c.schema["format"] != nil {
			value = "2026-10-06T16:00:00Z"
		}
		if kind, limit := o.closedValue(c.schema, value); kind != slotClosed || limit != c.limit {
			t.Errorf("closedValue(%v) = %d, %d, want closed, %d", c.schema, kind, limit, c.limit)
		}
	}
}

func TestOutputRedactionScansKeysWithTheirValues(t *testing.T) {
	redactor := testRedactor(t)
	key := syntheticKeyLines()
	redact := func(t *testing.T, value any) ([]byte, int) {
		t.Helper()
		document, err := json.Marshal(value)
		checkNoError(t, err)
		o := newTestOutputRedactor(t, redactor, &bundlepkg.Bundle{}, nil)
		redacted, err := o.Document(context.Background(), document)
		checkNoError(t, err)
		return redacted, o.Masked()
	}

	t.Run("a key and its value", func(t *testing.T) {
		redacted, masked := redact(t, map[string]any{"notes": map[string]any{key[0]: strings.Join(key[1:], "\n")}})
		requireNoKeyLine(t, "document", redacted)
		checkEqual(t, 1, masked)
		var doc map[string]map[string]string
		checkNoError(t, json.Unmarshal(redacted, &doc))
		for k, v := range doc["notes"] {
			checkEqual(t, len(key[0]), len(k))
			checkEqual(t, len(strings.Join(key[1:], "\n")), len(v))
		}
	})

	t.Run("adjacent keys", func(t *testing.T) {
		entries := []any{}
		for i, line := range key {
			entries = append(entries, map[string]any{line: i})
		}
		redacted, masked := redact(t, map[string]any{"notes": entries})
		requireNoKeyLine(t, "document", redacted)
		checkEqual(t, 1, masked)
		var doc map[string][]map[string]int
		checkNoError(t, json.Unmarshal(redacted, &doc))
		for i, entry := range doc["notes"] {
			for _, value := range entry {
				checkEqual(t, i, value)
			}
		}
	})

	t.Run("a masked key already taken", func(t *testing.T) {
		secret := "token " + leakedProviderKey
		maskedKey, count, err := redactor.RedactString(context.Background(), secret)
		checkNoError(t, err)
		checkEqual(t, 1, count)
		redacted, _ := redact(t, map[string]any{"notes": map[string]any{secret: "dropped", maskedKey: "kept"}})
		requireNoLeakedSecret(t, "document", redacted)
		var doc map[string]map[string]string
		checkNoError(t, json.Unmarshal(redacted, &doc))
		if len(doc["notes"]) != 1 || doc["notes"][maskedKey] != "kept" {
			t.Fatalf("notes = %v", doc["notes"])
		}
	})

	t.Run("keys the schema names and derived maps", func(t *testing.T) {
		document := []byte(`{"schema":"baum.review/1","summary":{"text":"Plain."},"stats":{"role_loc":{"code":3,"test":1},"evidence_summary":{"test":{"passed":1}}}}`)
		o := newTestOutputRedactor(t, redactor, &bundlepkg.Bundle{}, nil)
		redacted, err := o.Document(context.Background(), document)
		checkNoError(t, err)
		checkEqual(t, 0, o.Masked())
		if !bytes.Equal(redacted, document) {
			t.Fatalf("document changed:\n%s\n%s", document, redacted)
		}
	})
}

// A model that copies a token from the checkout into a value a lowercase
// pattern closes keeps no secret: the value is scanned on its own, whole and
// from after each underscore, and masked in place.
func TestOutputRedactionMasksSecretsInClosedPatternValues(t *testing.T) {
	const (
		token  = "sk_live_4ec3b2a1f9d84c7e6b5a3f2e1d0c9b8a"
		masked = "xx_xxxx_9xx9x9x9x9x99x9x9x9x9x9x9x9x9x9x"
	)
	redactor := testRedactor(t)
	// The mask is format-preserving: lowercase to x, digits to 9, separators kept.
	bare, count, err := redactor.RedactString(context.Background(), token)
	checkNoError(t, err)
	checkEqual(t, 1, count)
	checkEqual(t, masked, bare)
	// document puts values in a section's ID, a risk category and an
	// architecture edge, with its keys in the order a re-encoding writes them.
	document := func(id, category, edge string) string {
		return `{"sections":[{"id":"` + id + `","risks":[{"category":"` + category + `","id":"rsk_a"}],"tiles":[{"data":{"edges":[{"from":"api","to":"` + edge + `"}]},"id":"tile_a","kind":"architecture"}]}]}`
	}
	for _, c := range []struct {
		name, document, want string
		spans                int
	}{
		{"after the section prefix", document("sec_"+token, "security", "db"), document("sec_"+masked, "security", "db"), 1},
		{"after a later underscore", document("sec_auth_"+token, "security", "db"), document("sec_auth_"+masked, "security", "db"), 1},
		{"in a category", document("sec_a", token, "db"), document("sec_a", masked, "db"), 1},
		{"in an architecture edge", document("sec_a", "security", token), document("sec_a", "security", masked), 1},
		{"in several values", document("sec_"+token, token, token), document("sec_"+masked, masked, masked), 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := newTestOutputRedactor(t, redactor, &bundlepkg.Bundle{}, nil)
			redacted, err := o.Document(context.Background(), []byte(c.document))
			checkNoError(t, err)
			checkEqual(t, c.want, string(redacted))
			checkEqual(t, c.spans, o.Masked())
			checkEqual(t, c.spans, o.MaskedClosed())
		})
	}
}

// Values the schema fixes or closes keep their exact text when they hold
// nothing detected, and cost no count.
func TestOutputRedactionKeepsOrdinaryClosedValues(t *testing.T) {
	const (
		sha40 = "4ec3b2a1f9d84c7e6b5a3f2e1d0c9b8a7f6e5d4c"
		sha64 = "4ec3b2a1f9d84c7e6b5a3f2e1d0c9b8a7f6e5d4c3b2a1f0e9d8c7b6a5f4e3d2c"
	)
	document := `{"schema":"baum.review/1",` +
		`"review":{"head":"` + sha40 + `","base":"` + sha64 + `","generated_at":"2026-10-07T09:30:15.123Z"},` +
		`"sections":[{"id":"sec_auth_flow","title":"Auth","risks":[{"id":"rsk_token_reuse","category":"data_integrity","tier":"high","source":"model","finding":"fnd_reviewbot_7102"}],` +
		`"evidence":[{"id":"ev_retry_boundary"}],` +
		`"tiles":[{"kind":"architecture","id":"tile_session_summary_for_auth_flow","data":{"edges":[{"from":"api","to":"session_store"}]}}],` +
		`"blocks":[{"kind":"intent","text":"Plain.","anchors":[{"type":"hunk","hunk":"h_4ec3b2a1f9d8-2"}]}]}],` +
		`"index":{"attachments":[{"id":"att_4ec3b2a1f9d84c7e6b5a3f2e1d0c9b8a"}]}}`
	// A clean document comes back byte for byte, so any change shows.
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	redacted, err := o.Document(context.Background(), []byte(document))
	checkNoError(t, err)
	checkEqual(t, document, string(redacted))
	checkEqual(t, 0, o.Masked())
	checkEqual(t, 0, o.MaskedClosed())

	// A reference that resolves keeps its text whatever it holds.
	const secret = "sk_live_4ec3b2a1f9d84c7e6b5a3f2e1d0c9b8a"
	o = newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, map[string]bool{"deploy/" + secret + ".env": true})
	reference := `{"sections":[{"blocks":[{"kind":"intent","anchors":[{"type":"file","path":"deploy/` + secret + `.env"}]}]}]}`
	redacted, err = o.Document(context.Background(), []byte(reference))
	checkNoError(t, err)
	checkEqual(t, reference, string(redacted))
	checkEqual(t, 0, o.Masked())
	checkEqual(t, 0, o.MaskedClosed())
}

func TestOutputRedactorCountsClosedValuesAcrossCalls(t *testing.T) {
	const token = "sk_live_4ec3b2a1f9d84c7e6b5a3f2e1d0c9b8a"
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	for range 2 {
		_, err := o.Document(context.Background(), []byte(`{"sections":[{"risks":[{"category":"`+token+`"}]}]}`))
		checkNoError(t, err)
	}
	_, err := o.Document(context.Background(), []byte(`{"sections":[{"risks":[{"category":"auth"}],"blocks":[{"kind":"intent","text":"dsn `+leakedDatabaseURL+`"}]}]}`))
	checkNoError(t, err)
	checkEqual(t, 3, o.Masked())
	// Free text the model wrote does not count as a closed value.
	checkEqual(t, 2, o.MaskedClosed())
}

func TestDistinctFindings(t *testing.T) {
	for _, c := range []struct {
		ranges [][2]int
		count  int
	}{
		{nil, 0},
		{[][2]int{{0, 10}}, 1},
		// Copies that found the same secret overlap.
		{[][2]int{{4, 10}, {0, 10}}, 1},
		// Secrets side by side are apart.
		{[][2]int{{0, 20}, {21, 41}}, 2},
		{[][2]int{{0, 20}, {20, 41}}, 2},
	} {
		checkEqual(t, c.count, distinctFindings(c.ranges))
	}
}

// Tokens match as whole words of a respelling only.
func TestTokenMatcher(t *testing.T) {
	m := newTokenMatcher([]string{"he", "she_is", "hers", "akia_key", "x_y_io", ""})
	for text, want := range map[string]bool{
		"he": true, "a_he_b": true, "she_is_here": true, "ushers": false, "she": false, "ahishe": false,
		"ev_akia_key_2": true, "ev_akia_key2": false, "akia_ke": false, "": false, "h": false,
		"rsk_fix_y_io_timeout": false, "rsk_x_y_io_timeout": true, "x_y_io": true,
	} {
		if got := m.contains(text); got != want {
			t.Errorf("contains(%q) = %v, want %v", text, got, want)
		}
	}
	checkEqual(t, false, newTokenMatcher(nil).contains("anything"))
}

// Attention refs are closed by a pattern with a group of literal prefixes.
func TestOutputRedactionMasksSecretsInAttentionRefs(t *testing.T) {
	const (
		token  = "sk_live_4ec3b2a1f9d84c7e6b5a3f2e1d0c9b8a"
		masked = "xx_xxxx_9xx9x9x9x9x99x9x9x9x9x9x9x9x9x9x"
	)
	document := func(a, b string) string {
		return `{"sections":[{"attention":{"refs":["` + a + `","` + b + `"],"text":"Look."}}]}`
	}
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	redacted, err := o.Document(context.Background(), []byte(document("ev_"+token, "rsk_a_"+token)))
	checkNoError(t, err)
	checkEqual(t, document("ev_"+masked, "rsk_a_"+masked), string(redacted))
	checkEqual(t, 2, o.Masked())
	checkEqual(t, 2, o.MaskedClosed())
}

// A closed value over its maxLength cannot be valid and is masked whole,
// without the scan of its suffixes and, holding nothing detected, without a
// count; one at the bound is kept.
func TestOutputRedactionMasksOverlongClosedValuesWhole(t *testing.T) {
	document := func(category string) string {
		return `{"sections":[{"risks":[{"category":"` + category + `"}]}]}`
	}
	exact := strings.Repeat("a_", 64)
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	redacted, err := o.Document(context.Background(), []byte(document(exact)))
	checkNoError(t, err)
	checkEqual(t, document(exact), string(redacted))
	checkEqual(t, 0, o.Masked())
	checkEqual(t, 0, o.MaskedClosed())

	long := exact + "b"
	redacted, err = o.Document(context.Background(), []byte(document(long)))
	checkNoError(t, err)
	checkEqual(t, document(strings.Repeat("x_", 64)+"x"), string(redacted))
	checkEqual(t, 0, o.Masked())
	checkEqual(t, 0, o.MaskedClosed())

	// A few KiB of underscores are masked whole, not scanned after each.
	huge := strings.Repeat("a_", 2048)
	redacted, err = o.Document(context.Background(), []byte(document(huge)))
	checkNoError(t, err)
	checkEqual(t, document(strings.Repeat("x_", 2048)), string(redacted))
	checkEqual(t, 0, o.MaskedClosed())
}

// A closed value whose schema sets no maxLength may be valid at any length,
// so a long one is scanned rather than masked whole.
func TestOutputRedactionKeepsLongClosedValuesWithoutABound(t *testing.T) {
	// A hunk ID's pattern, ^h_[0-9a-f]{12}(-[0-9]+)?$, bounds no length.
	document := `{"sections":[{"hunks":["h_4ec3b2a1f9d8-` + strings.Repeat("7", 130) + `"]}]}`
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	redacted, err := o.Document(context.Background(), []byte(document))
	checkNoError(t, err)
	checkEqual(t, document, string(redacted))
	checkEqual(t, 0, o.Masked())
	checkEqual(t, 0, o.MaskedClosed())
	// The hunk ID was closed by its pattern, with no bound.
	closed := false
	for _, slot := range o.o.slots {
		closed = closed || strings.HasPrefix(slot.text, "h_") && slot.kind == slotClosed && slot.limit == 0
	}
	checkEqual(t, true, closed)
}

// A closed value without a maxLength is scanned again after at most
// closedCopyLimit runs of separators; one with more is masked whole, without
// a count, so its copies stay linear in its length.
func TestOutputRedactionCapsCopiesOfUnboundedClosedValues(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"v": map[string]any{"type": "string", "pattern": "^[a-z_]+$"}}}
	redactor := testRedactor(t)
	document := func(v string) string { return `{"v":"` + v + `"}` }
	within := strings.Repeat("ab_", closedCopyLimit) + "ab"
	o := &OutputRedactor{o: outputRedaction{redactor: redactor, schema: schema}}
	redacted, err := o.Document(context.Background(), []byte(document(within)))
	checkNoError(t, err)
	checkEqual(t, document(within), string(redacted))
	checkEqual(t, 0, o.MaskedClosed())

	over := strings.Repeat("ab_", closedCopyLimit+1) + "ab"
	redacted, err = o.Document(context.Background(), []byte(document(over)))
	checkNoError(t, err)
	checkEqual(t, document(strings.Repeat("xx_", closedCopyLimit+1)+"xx"), string(redacted))
	checkEqual(t, 0, o.Masked())
	checkEqual(t, 0, o.MaskedClosed())
}

// testFormatMask is the format-preserving mask written out for the tests:
// upper and lower case letters to X and x, digits to 9, the separators it
// keeps as they are, and any other character to *.
func testFormatMask(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z':
			return 'X'
		case r >= 'a' && r <= 'z':
			return 'x'
		case r >= '0' && r <= '9':
			return '9'
		case strings.ContainsRune("-_.:/+=@", r):
			return r
		}
		return '*'
	}, s)
}

// identifiersThenPrepare runs the host's order: Identifiers on the document
// as the model wrote it, Prepare, then Document on what Prepare returned.
func identifiersThenPrepare(t *testing.T, o *OutputRedactor, document string) (raw, prepared, redacted string) {
	t.Helper()
	v, err := sharedValidator()
	checkNoError(t, err)
	masked, err := o.Identifiers(context.Background(), []byte(document))
	checkNoError(t, err)
	normalized, _ := v.Prepare(masked, &bundlepkg.Bundle{}, "")
	final, err := o.Document(context.Background(), normalized)
	checkNoError(t, err)
	return string(masked), string(normalized), string(final)
}

// A secret the model writes into an identifier in a spelling Prepare
// respells (mixed case, hyphens) is one no rule detects once respelled, so it
// is masked as written, before Prepare, and Prepare respells the mask.
func TestOutputRedactionMasksIdentifiersBeforeNormalization(t *testing.T) {
	key := leakedProviderKey
	if len("sec_db_"+key) > 128 {
		t.Fatalf("the key is too long for these cases: %d", len(key))
	}
	// Prepare's respelling of the raw key is not detected: this is the leak.
	respelled := "sec_" + strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	_, count, err := testRedactor(t).RedactString(context.Background(), respelled)
	checkNoError(t, err)
	checkEqual(t, 0, count)

	document := func(id, category string) string {
		return `{"sections":[{"id":"` + id + `","risks":[{"category":"` + category + `","id":"Token-Reuse"}],"title":"T"}]}`
	}
	normalizedMask := func(prefix, value string) string {
		return prefix + strings.ToLower(strings.ReplaceAll(value, "-", "_"))
	}
	const stripe = "sk_live_4ec3b2a1f9d84c7e6b5a3f2e1d0c9b8a"
	for _, c := range []struct {
		name, id, category string
		wantID, wantCat    string
		values             int
	}{
		// One value written twice is scanned and counted once.
		{"raw in an ID and a category", key, key, testFormatMask(key), testFormatMask(key), 1},
		{"after a hyphen", "db-" + key, "Auth", "db-" + testFormatMask(key), "Auth", 1},
		// An underscore is a word character: only the copy after it exposes
		// the token to a rule that needs a word boundary.
		{"after an underscore", "Auth_" + stripe, "Auth", "Auth_" + testFormatMask(stripe), "Auth", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
			raw, prepared, redacted := identifiersThenPrepare(t, o, document(c.id, c.category))
			checkEqual(t, document(c.wantID, c.wantCat), raw)
			requireNoLeakedSecret(t, "prepared", []byte(prepared))
			if strings.Contains(prepared, stripe) || strings.Contains(prepared, respelled) {
				t.Fatalf("prepared keeps a respelled secret: %s", prepared)
			}
			var doc struct {
				Sections []struct {
					ID    string `json:"id"`
					Risks []struct {
						ID       string `json:"id"`
						Category string `json:"category"`
					} `json:"risks"`
				} `json:"sections"`
			}
			checkNoError(t, json.Unmarshal([]byte(prepared), &doc))
			section := doc.Sections[0]
			checkEqual(t, normalizedMask("sec_", c.wantID), section.ID)
			checkEqual(t, normalizedMask("", c.wantCat), section.Risks[0].Category)
			checkEqual(t, "rsk_token_reuse", section.Risks[0].ID)
			// Document finds nothing more in the respelled mask.
			checkEqual(t, prepared, redacted)
			checkEqual(t, c.values, o.Masked())
			checkEqual(t, c.values, o.MaskedClosed())
		})
	}
}

// A reference to an identifier holding a secret is masked as the identifier
// is, so it still resolves after Prepare.
func TestOutputRedactionMasksIdentifierReferencesAlike(t *testing.T) {
	key := leakedProviderKey
	document := func(id string) string {
		return `{"sections":[{"attention":{"refs":["` + id + `"],"text":"Look."},"evidence":[{"id":"` + id + `"}]}]}`
	}
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	masked, err := o.Identifiers(context.Background(), []byte(document(key)))
	checkNoError(t, err)
	checkEqual(t, document(testFormatMask(key)), string(masked))
	checkEqual(t, 1, o.Masked())
	checkEqual(t, 1, o.MaskedClosed())
}

// Ordinary identifiers in any spelling, and documents that are not JSON,
// come back byte for byte and cost no count.
func TestOutputRedactionKeepsOrdinaryAuthoredIdentifiers(t *testing.T) {
	document := `{"sections":[{"id":"Auth-Flow", "risks":[{"category":"Retry.Policy","id":"Token Reuse"}],` +
		`"tiles":[{"data":{"edges":[{"from":"API-Gateway","to":"Session.Store"}]},"id":"Tile-A","kind":"architecture"}],` +
		`"attention":{"refs":["EV-Retry"],"text":"Look."},"evidence":[{"id":"EV-Retry"}]}]}`
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	for _, input := range []string{document, "not json " + leakedProviderKey} {
		masked, err := o.Identifiers(context.Background(), []byte(input))
		checkNoError(t, err)
		checkEqual(t, input, string(masked))
	}
	checkEqual(t, 0, o.Masked())
	checkEqual(t, 0, o.MaskedClosed())
}

// The values scanned are those normalization respells, and no others.
func TestAuthoredIdentifiers(t *testing.T) {
	v, err := sharedValidator()
	checkNoError(t, err)
	var value any
	checkNoError(t, json.Unmarshal([]byte(`{"sections":[{"id":"Auth-Flow","title":"Title","hunks":["h_4ec3b2a1f9d8"],"risks":[{"category":"Retry.Policy","id":"Token Reuse","finding":"F-1"}],`+
		`"tiles":[{"data":{"edges":[{"from":"API","to":"DB"}]},"id":"Tile-A","kind":"architecture"}],`+
		`"attention":{"refs":["EV-Retry"],"text":"Look."},"unknown":"Key"}]}`), &value))
	var got []string
	for _, id := range authoredIdentifiers(value, v.strict) {
		got = append(got, fmt.Sprintf("%s>%s/%d", id.text, id.normalized, id.limit))
	}
	checkEqual(t, fmt.Sprint([]string{"API>api/128", "Auth-Flow>sec_auth_flow/128", "DB>db/128", "EV-Retry>ev_retry/128", "F-1>fnd_f_1/128", "Retry.Policy>retry_policy/128", "Tile-A>tile_a/128", "Token Reuse>rsk_token_reuse/128"}), fmt.Sprint(got))
}

// A raw identifier is bounded by its respelling: one Prepare respells within
// the bound is scanned however long it is as written, and one whose
// respelling is over the bound is masked whole, without a count, and Prepare
// respells the mask.
func TestOutputRedactionMasksOverlongAuthoredIdentifiersWhole(t *testing.T) {
	document := func(id string) string { return `{"sections":[{"id":"` + id + `","title":"T"}]}` }
	within := strings.Repeat("Auth--", 22)
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	raw, _, _ := identifiersThenPrepare(t, o, document(within))
	checkEqual(t, document(within), raw)

	over := strings.Repeat("Auth-", 26)
	raw, prepared, redacted := identifiersThenPrepare(t, o, document(over))
	checkEqual(t, document(strings.Repeat("Xxxx-", 26)), raw)
	if !strings.Contains(prepared, `"id":"sec_`+strings.Repeat("xxxx_", 25)+`xxxx"`) {
		t.Fatalf("prepared does not respell the mask: %s", prepared)
	}
	// Document masks the respelling, over its bound too, whole.
	if !strings.Contains(redacted, `"id":"xxx_`+strings.Repeat("xxxx_", 25)+`xxxx"`) {
		t.Fatalf("redacted keeps the over-long ID: %s", redacted)
	}
	checkEqual(t, 0, o.Masked())
	checkEqual(t, 0, o.MaskedClosed())
}

// preparedSection prepares a document and returns its first section.
func preparedSection(t *testing.T, document []byte) (string, map[string]any) {
	t.Helper()
	v, err := sharedValidator()
	checkNoError(t, err)
	prepared, _ := v.Prepare(document, &bundlepkg.Bundle{}, "")
	var doc struct {
		Sections []map[string]any `json:"sections"`
	}
	checkNoError(t, json.Unmarshal(prepared, &doc))
	return string(prepared), doc.Sections[0]
}

// A secret masked in one identifier is masked in every other spelling of it,
// upper-cased or already respelled, which no rule detects, so the prepared
// document holds no spelling of it.
func TestOutputRedactionMasksOtherSpellingsOfAnIdentifierSecret(t *testing.T) {
	key := leakedProviderKey
	token := identifierToken(key)
	// The mask of the key as Prepare respells it.
	maskedToken := identifierToken(testFormatMask(key))
	document := func(ref, id string) string {
		return `{"sections":[{"attention":{"refs":["` + ref + `"],"text":"Look."},"evidence":[{"id":"` + id + `"}]}]}`
	}
	for _, c := range []struct {
		name, ref, wantRaw, wantRef string
	}{
		// The ref resolves to the evidence it names once both are masked.
		{"upper-cased", strings.ToUpper(key), testFormatMask(strings.ToUpper(key)), "ev_" + maskedToken},
		// Masked whole, prefix and all, so it no longer resolves and
		// validation reports it.
		{"already respelled", "ev_" + token, testFormatMask("ev_" + token), "xx_" + maskedToken},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, count, err := testRedactor(t).RedactString(context.Background(), c.ref)
			checkNoError(t, err)
			checkEqual(t, 0, count)
			o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
			raw, err := o.Identifiers(context.Background(), []byte(document(c.ref, key)))
			checkNoError(t, err)
			checkEqual(t, document(c.wantRaw, testFormatMask(key)), string(raw))
			checkEqual(t, 2, o.Masked())
			checkEqual(t, 2, o.MaskedClosed())
			prepared, section := preparedSection(t, raw)
			checkEqual(t, "ev_"+maskedToken, section["evidence"].([]any)[0].(map[string]any)["id"])
			checkEqual(t, fmt.Sprint([]any{c.wantRef}), fmt.Sprint(section["attention"].(map[string]any)["refs"]))
			requireNoLeakedSecret(t, "prepared", []byte(prepared))
			for _, spelling := range []string{token, strings.ToLower(key), strings.ToUpper(key)} {
				if strings.Contains(prepared, spelling) {
					t.Fatalf("prepared keeps %q: %s", spelling, prepared)
				}
			}
		})
	}
}

// Identifiers that hold nothing detected never count, however long or
// non-ASCII they are as written: one respelled over its bound is masked
// whole and prepares as it would have otherwise, but masked, and one Prepare
// respells within its bound comes back byte for byte.
func TestOutputRedactionNeverCountsHarmlessIdentifiers(t *testing.T) {
	long := strings.Repeat("Token reuse across sessions ", 5)[:129]
	japanese := strings.Repeat("セキュリティ", 8)
	document := func(category, id string) string {
		return `{"sections":[{"risks":[{"category":"` + category + `","id":"` + id + `"}],"title":"T"}]}`
	}
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	raw, prepared, redacted := identifiersThenPrepare(t, o, document(japanese, long))
	checkEqual(t, document(japanese, testFormatMask(long)), raw)
	plain, section := preparedSection(t, []byte(document(japanese, long)))
	checkEqual(t, "uncategorized", section["risks"].([]any)[0].(map[string]any)["category"])
	normalized := "rsk_" + identifierToken(long)
	checkEqual(t, strings.Replace(plain, normalized, "rsk_"+identifierToken(testFormatMask(long)), 1), prepared)
	if !strings.Contains(redacted, `"id":"xxx_xxxxx_`) {
		t.Fatalf("the over-long ID is not masked: %s", redacted)
	}
	checkEqual(t, 0, o.Masked())
	checkEqual(t, 0, o.MaskedClosed())

	// The category alone, within its bound once respelled, is kept.
	o = newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	raw, _, _ = identifiersThenPrepare(t, o, document(japanese, "Token reuse"))
	checkEqual(t, document(japanese, "Token reuse"), raw)
	checkEqual(t, 0, o.MaskedClosed())

	// A secret after such a run, longer as written than the bound but not
	// once respelled, is found and counts.
	o = newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	category := strings.Repeat("セキュリティ", 2) + "-" + leakedProviderKey
	if len(category) <= 128 {
		t.Fatalf("the category is within the bound as written: %d", len(category))
	}
	raw, prepared, _ = identifiersThenPrepare(t, o, document(category, "Token reuse"))
	checkEqual(t, document(strings.Repeat("セキュリティ", 2)+"-"+testFormatMask(leakedProviderKey), "Token reuse"), raw)
	requireNoLeakedSecret(t, "prepared", []byte(prepared))
	checkEqual(t, 1, o.Masked())
	checkEqual(t, 1, o.MaskedClosed())
}

// leakedAccessKey is an AWS-shaped access key ID the test redactor detects.
const leakedAccessKey = "AKIA" + "Z3MQ5TNV2XWB7KDL"

// preparedOutput is what Prepare writes around one section, as these tests
// build it.
func preparedOutput(section string) string {
	return `{"relations":[],"schema":"baum.review/1","sections":[` + section + `]}`
}

// An identifier over its bound is still scanned: a secret found in it is
// counted, the value is masked whole, and every other spelling of the secret
// is masked whole and counted too.
func TestOutputRedactionScansIdentifiersOverTheirBound(t *testing.T) {
	m := strings.ToLower(testFormatMask(leakedAccessKey))
	lower := strings.ToLower(leakedAccessKey)
	document := `{"sections":[{"attention":{"refs":["ev_` + lower + `"],"text":"Look."},"evidence":[{"id":"` + leakedAccessKey + `-` + strings.Repeat("pad", 40) + `"}],"risks":[{"id":"` + lower + `-risk"}]}]}`
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	raw, _, redacted := identifiersThenPrepare(t, o, document)
	checkEqual(t, `{"sections":[{"attention":{"refs":["xx_`+m+`"],"text":"Look."},"evidence":[{"id":"`+testFormatMask(leakedAccessKey)+`-`+strings.Repeat("xxx", 40)+`"}],"risks":[{"id":"`+m+`-xxxx"}]}]}`, raw)
	// Prepare respells the evidence ID over its bound, and Document masks
	// it whole again, uncounted.
	checkEqual(t, preparedOutput(`{"attention":{"refs":["xx_`+m+`"],"text":"Look."},"blocks":[],"evidence":[{"anchors":[],"id":"xx_`+m+`_`+strings.Repeat("x", 120)+`"}],"hunks":[],"risks":[{"id":"rsk_`+m+`_xxxx"}]}`), redacted)
	checkEqual(t, 3, o.Masked())
	checkEqual(t, 3, o.MaskedClosed())

	// The same holds for a closed value Document alone scans over its
	// maxLength.
	const stripe = "sk_live_4ec3b2a1f9d84c7e6b5a3f2e1d0c9b8a"
	category := strings.Repeat("a", 100) + "_" + stripe
	o = newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	masked, err := o.Document(context.Background(), []byte(`{"sections":[{"risks":[{"category":"`+category+`"}]}]}`))
	checkNoError(t, err)
	checkEqual(t, `{"sections":[{"risks":[{"category":"`+testFormatMask(category)+`"}]}]}`, string(masked))
	checkEqual(t, 1, o.Masked())
	checkEqual(t, 1, o.MaskedClosed())

	// A mixed-case token behind an underscore, over the bound as written.
	o = newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	_, prepared, _ := identifiersThenPrepare(t, o, `{"sections":[{"risks":[{"category":"`+strings.Repeat("a", 100)+"_"+plantedSecret+`"}]}]}`)
	if strings.Contains(strings.ToLower(prepared), strings.ToLower(plantedSecret[4:])) {
		t.Fatalf("prepared keeps the token: %s", prepared)
	}
	checkEqual(t, 1, o.MaskedClosed())
}

// Two secrets side by side in one identifier are two findings, and each is a
// source on its own, so a spelling holding either alone is masked.
func TestOutputRedactionKeepsSideBySideFindingsApart(t *testing.T) {
	m := strings.ToLower(testFormatMask(leakedAccessKey))
	document := `{"sections":[{"attention":{"refs":["ev_` + strings.ToLower(leakedAccessKey) + `"],"text":"Look."},"evidence":[{"id":"` + leakedAccessKey + `-` + leakedAccessKey + `"}]}]}`
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	raw, _, redacted := identifiersThenPrepare(t, o, document)
	checkEqual(t, `{"sections":[{"attention":{"refs":["xx_`+m+`"],"text":"Look."},"evidence":[{"id":"`+testFormatMask(leakedAccessKey)+`-`+testFormatMask(leakedAccessKey)+`"}]}]}`, raw)
	checkEqual(t, preparedOutput(`{"attention":{"refs":["xx_`+m+`"],"text":"Look."},"blocks":[],"evidence":[{"anchors":[],"id":"ev_`+m+`_`+m+`"}],"hunks":[],"risks":[]}`), redacted)
	// Two findings in the evidence ID, one other spelling.
	checkEqual(t, 3, o.Masked())
	checkEqual(t, 2, o.MaskedClosed())
}

// A secret written only in free text is left to Document: an identifier
// that merely shares its words is no source of anything, so a clean run
// stays clean.
func TestOutputRedactionTakesNoSourcesFromFreeText(t *testing.T) {
	remote := "git" + "@github.com"
	document := `{"sections":[{"evidence":[{"id":"ev_noreply_github_com_author"}],"risks":[{"id":"rsk_git_github_com_ssh_remote"}],"title":"Push to ` + remote + ` as noreply` + "@github.com" + `"}]}`
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	_, _, redacted := identifiersThenPrepare(t, o, document)
	if !strings.Contains(redacted, `"id":"rsk_git_github_com_ssh_remote"`) || !strings.Contains(redacted, `"id":"ev_noreply_github_com_author"`) {
		t.Fatalf("an identifier was masked: %s", redacted)
	}
	checkEqual(t, 0, o.MaskedClosed())
}

// accessKey returns the i-th of a run of distinct AWS-shaped access key IDs.
func accessKey(i int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	key := []byte("AKIA" + "Z3MQ5TNV2XWB7KDL")
	for k := range 4 {
		key[19-k] = alphabet[i%32]
		i /= 32
	}
	return string(key)
}

// Any number of sources is looked for in one pass: of a hundred keys in
// identifiers, the respelled copies of two are masked, as whole words only.
func TestOutputRedactionMatchesManySources(t *testing.T) {
	var evidence []string
	for i := range 100 {
		_, count, err := testRedactor(t).RedactString(context.Background(), accessKey(i))
		checkNoError(t, err)
		checkEqual(t, 1, count)
		evidence = append(evidence, `{"id":"`+accessKey(i)+`"}`)
	}
	lower := func(i int) string { return strings.ToLower(accessKey(i)) }
	refs := []string{"rsk_" + lower(42) + "_reuse", lower(99), "x" + lower(7), lower(8) + "x"}
	document := `{"sections":[{"attention":{"refs":["` + strings.Join(refs, `","`) + `"],"text":"Look."},"evidence":[` + strings.Join(evidence, ",") + `]}]}`
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	raw, err := o.Identifiers(context.Background(), []byte(document))
	checkNoError(t, err)
	var masked []string
	for i := range 100 {
		masked = append(masked, `{"id":"`+testFormatMask(accessKey(i))+`"}`)
	}
	// A key inside a longer word is not that key's spelling.
	wantRefs := []string{testFormatMask(refs[0]), testFormatMask(refs[1]), refs[2], refs[3]}
	checkEqual(t, `{"sections":[{"attention":{"refs":["`+strings.Join(wantRefs, `","`)+`"],"text":"Look."},"evidence":[`+strings.Join(masked, ",")+`]}]}`, string(raw))
	checkEqual(t, 102, o.Masked())
	checkEqual(t, 102, o.MaskedClosed())
}

// One text at two positions, within its bound at one (a category) and over
// it at the other (a section ID, which takes a prefix), is scanned once: kept
// when it holds nothing, counted once when it holds a secret.
func TestOutputRedactionScansATextAtTwoBoundsOnce(t *testing.T) {
	document := func(text string) string {
		return `{"sections":[{"id":"` + text + `","risks":[{"category":"` + text + `"}]}]}`
	}
	plain := strings.Repeat("b", 126)
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	raw, err := o.Identifiers(context.Background(), []byte(document(plain)))
	checkNoError(t, err)
	checkEqual(t, document(plain), string(raw))
	checkEqual(t, 0, o.MaskedClosed())

	secret := leakedProviderKey + "-bbbb"
	if len(identifierToken(secret)) != 126 {
		t.Fatalf("the respelling is %d bytes", len(identifierToken(secret)))
	}
	raw, err = o.Identifiers(context.Background(), []byte(document(secret)))
	checkNoError(t, err)
	// The key's rule takes the hyphenated tail as part of the key.
	checkEqual(t, document(testFormatMask(secret)), string(raw))
	checkEqual(t, 1, o.Masked())
	checkEqual(t, 1, o.MaskedClosed())
}

// A value long as written but short once respelled costs its length and a
// window per restart, not a copy of every suffix.
func TestOutputRedactionScansLongValuesInLinearWork(t *testing.T) {
	category := strings.Repeat("a-", 60) + "a" + strings.Repeat("*", 1<<20) + "z"
	document := `{"sections":[{"risks":[{"category":"` + category + `"}]}]}`
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	raw, err := o.Identifiers(context.Background(), []byte(document))
	checkNoError(t, err)
	checkEqual(t, document, string(raw))
	// The closed-value scan joins the value once plus a window per restart;
	// the source scan joins the document once.
	if bound := 2*len(document) + 128*closedScanWindow; o.o.scanned > bound {
		t.Fatalf("scanned %d bytes, over %d", o.o.scanned, bound)
	}
	checkEqual(t, 0, o.MaskedClosed())
}

// An address the redactor already masked reads as its own mask; finding it
// again is no finding, so it neither counts nor becomes a source.
func TestOutputRedactionIgnoresAlreadyMaskedFindings(t *testing.T) {
	address := "xxxxx.xxx" + "@xxxxxxx.xxx"
	document := `{"sections":[{"evidence":[{"id":"ev-author-` + address + `"}],"risks":[{"category":"xxx-xxxx` + "@xxxxxxx.xxx" + `","id":"Author"}],"title":"T"}]}`
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	raw, _, _ := identifiersThenPrepare(t, o, document)
	checkEqual(t, document, raw)
	checkEqual(t, 0, o.Masked())
	checkEqual(t, 0, o.MaskedClosed())
}

// A real address whose domain happens to read as a mask is still a finding:
// its local part does not.
func TestOutputRedactionCountsAddressesWithMaskLikeDomains(t *testing.T) {
	for _, id := range []string{"User@xxx.xxx", "ev-author-User@xxx.xxx"} {
		t.Run(id, func(t *testing.T) {
			document := `{"sections":[{"evidence":[{"id":"` + id + `"}],"title":"T"}]}`
			o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
			raw, err := o.Identifiers(context.Background(), []byte(document))
			checkNoError(t, err)
			if strings.Contains(string(raw), "User") {
				t.Fatalf("the address survives in %s", raw)
			}
			checkEqual(t, 1, o.MaskedClosed())
		})
	}
}

// A key glued to letters in its own identifier is a source also widened to
// that identifier's words, so the identifier's other spellings are masked.
func TestOutputRedactionWidensSourcesToTheirWords(t *testing.T) {
	lower := strings.ToLower(leakedAccessKey)
	for _, c := range []struct{ id, other string }{
		{"ev" + leakedAccessKey, "ev" + lower},
		{leakedAccessKey + "x", "rsk_" + lower + "x"},
	} {
		t.Run(c.id, func(t *testing.T) {
			document := `{"sections":[{"evidence":[{"id":"` + c.id + `"}],"risks":[{"id":"` + c.other + `"}]}]}`
			o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
			raw, err := o.Identifiers(context.Background(), []byte(document))
			checkNoError(t, err)
			var doc struct {
				Sections []struct {
					Evidence []struct{ ID string } `json:"evidence"`
					Risks    []struct{ ID string } `json:"risks"`
				} `json:"sections"`
			}
			checkNoError(t, json.Unmarshal(raw, &doc))
			if strings.Contains(strings.ToLower(doc.Sections[0].Evidence[0].ID), lower) {
				t.Fatalf("the key survives in %s", raw)
			}
			checkEqual(t, testFormatMask(c.other), doc.Sections[0].Risks[0].ID)
			checkEqual(t, 2, o.Masked())
			checkEqual(t, 2, o.MaskedClosed())
		})
	}
}

// A short secret is a source however few bytes it respells to, and one whose
// respelling is over 128 bytes, which no identifier within its bound could
// hold, is not.
func TestOutputRedactionSourceLengths(t *testing.T) {
	document := func(id, ref string) string {
		return `{"sections":[{"attention":{"refs":["` + ref + `"],"text":"Look."},"evidence":[{"id":"` + id + `"}]}]}`
	}
	// The keyword rule finds the value after --password.
	short := "--password Kx9$Lm2"
	o := newTestOutputRedactor(t, testRedactor(t), &bundlepkg.Bundle{}, nil)
	raw, err := o.Identifiers(context.Background(), []byte(document(short, "ev_kx9_lm2")))
	checkNoError(t, err)
	checkEqual(t, document("--password Xx9*Xx9", "xx_xx9_xx9"), string(raw))
	checkEqual(t, 2, o.MaskedClosed())

	// The bound is on the respelling: 128 bytes are kept, 129 are not, for
	// the finding and for it widened to its words.
	for _, c := range []struct {
		text  string
		r     [2]int
		wants []string
	}{
		{strings.Repeat("a", 128), [2]int{0, 128}, []string{strings.Repeat("a", 128)}},
		{strings.Repeat("a", 129), [2]int{0, 129}, nil},
		{"Pre-" + strings.Repeat("A", 123) + "-post", [2]int{10, 20}, []string{strings.Repeat("a", 10), strings.Repeat("a", 123)}},
		{"Pre-" + strings.Repeat("A", 129) + "-post", [2]int{10, 20}, []string{strings.Repeat("a", 10)}},
	} {
		o := &outputRedaction{sources: map[string]bool{}}
		o.addSources(c.text, c.r)
		checkEqual(t, fmt.Sprint(c.wants), fmt.Sprint(slices.Sorted(maps.Keys(o.sources))))
	}
}
