package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
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
	plantedSecret = "ghp_JTeKdNnFRIBXuDL7DxtpYlSXpfKtHF4vUCsM"
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
		`\S`: false, `^\S`: false, `.+`: false, `^.+$`: false, `^[^/\s]+/[^/\s]+$`: false, `^[a-z]+`: false, `^\w+$`: true, `^\W+$`: false, ``: false, `^a|.+$`: false, `^(a|b)$`: false,
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
		closed bool
	}{
		{map[string]any{"const": "logical"}, "logical", true},
		{map[string]any{"const": "logical"}, "logical " + leakedProviderKey, false},
		{map[string]any{"enum": []any{"feature", "fix"}}, "fix", true},
		{map[string]any{"enum": []any{"feature", "fix"}}, "fix!", false},
		{map[string]any{"$ref": "#/$defs/id"}, "sec_parser", true},
		{map[string]any{"$ref": "#/$defs/id"}, "sec_parser " + leakedProviderKey, false},
		{map[string]any{"type": "string", "pattern": `\S`}, "anything", false},
		{map[string]any{"type": "string", "format": "date-time"}, "2026-10-06T16:00:00Z", true},
		{map[string]any{"type": "string", "format": "date-time"}, "2026-10-06 " + leakedProviderKey, false},
		{map[string]any{"anyOf": []any{map[string]any{"type": "string", "pattern": "^[0-9a-f]{7,64}$"}, map[string]any{"type": "null"}}}, redactionHeadSHA, true},
		{map[string]any{"anyOf": []any{map[string]any{"type": "string", "pattern": "^[0-9a-f]{7,64}$"}, map[string]any{"type": "null"}}}, "head", false},
	} {
		if got := o.closedValue(c.schema, c.value); got != c.closed {
			t.Errorf("closedValue(%v, %q) = %t, want %t", c.schema, c.value, got, c.closed)
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
