package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bundlepkg "github.com/lastpersonlabs/baum/bundle"
)

func storedExample(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "schema", "review-example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func validateStored(t *testing.T, doc map[string]any, bundle *bundlepkg.Bundle) []string {
	t.Helper()
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return validator.ValidateStored(encoded, bundle)
}

func requireFinding(t *testing.T, findings []string, want string) {
	t.Helper()
	for _, finding := range findings {
		if strings.Contains(finding, want) {
			return
		}
	}
	t.Errorf("no finding contains %q: %q", want, findings)
}

func firstSection(doc map[string]any) map[string]any {
	return doc["sections"].([]any)[0].(map[string]any)
}

func TestValidateStoredAcceptsTheExample(t *testing.T) {
	if findings := validateStored(t, storedExample(t), nil); findings != nil {
		t.Fatalf("example rejected: %q", findings)
	}
}

func TestValidateStoredNotJSON(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	for input, want := range map[string]string{
		"not json": "document is not valid JSON: invalid character",
		"[]":       "document is not valid JSON: json: cannot unmarshal array",
		"null":     "document must be an object",
	} {
		findings := validator.ValidateStored([]byte(input), nil)
		if len(findings) != 1 || !strings.Contains(findings[0], want) {
			t.Errorf("%q: findings = %q, want %q", input, findings, want)
		}
	}
}

func TestValidateStoredSchemaViolation(t *testing.T) {
	doc := storedExample(t)
	delete(doc, "stats")
	findings := validateStored(t, doc, nil)
	if len(findings) != 1 || !strings.Contains(findings[0], "stats") {
		t.Fatalf("findings = %q", findings)
	}
}

func TestValidateStoredDuplicateRiskID(t *testing.T) {
	doc := storedExample(t)
	risks := firstSection(doc)["risks"].([]any)
	id := risks[0].(map[string]any)["id"].(string)
	risks[1].(map[string]any)["id"] = id
	requireFinding(t, validateStored(t, doc, nil), "duplicate risk ID: "+id)
}

func TestValidateStoredUnresolvedAttentionRef(t *testing.T) {
	doc := storedExample(t)
	firstSection(doc)["attention"] = map[string]any{"refs": []any{"rsk_missing"}, "text": "Look here."}
	requireFinding(t, validateStored(t, doc, nil), `attention ref "rsk_missing"`)
}

func TestValidateStoredImageNotInOwnIndex(t *testing.T) {
	doc := storedExample(t)
	const unlisted = "att_ffffffffffffffffffffffffffffffff"
	section := firstSection(doc)
	section["blocks"] = append(section["blocks"].([]any), imagesBlock(unlisted))
	requireFinding(t, validateStored(t, doc, nil), unlisted)
	// A bundle listing the image does not rescue it: a stored document is
	// checked against its own index.
	bundle := &bundlepkg.Bundle{Attachments: []bundlepkg.Attachment{{ID: unlisted}}}
	requireFinding(t, validateStored(t, doc, bundle), unlisted)
}

func TestValidateStoredBundleRulesNeedABundle(t *testing.T) {
	doc := storedExample(t)
	if findings := validateStored(t, doc, nil); findings != nil {
		t.Fatalf("without a bundle: %q", findings)
	}
	findings := validateStored(t, doc, &bundlepkg.Bundle{})
	if len(findings) == 0 {
		t.Fatal("an empty bundle raised no findings")
	}
	requireFinding(t, findings, "unknown assigned hunk")
	failing := &bundlepkg.Bundle{Checks: []bundlepkg.Check{{ID: "chk_9001", Conclusion: "failure"}}}
	requireFinding(t, validateStored(t, doc, failing), "verified evidence cites a check that did not succeed")
	succeeding := &bundlepkg.Bundle{Checks: []bundlepkg.Check{{ID: "chk_9001", Conclusion: "success"}}}
	requireFinding(t, validateStored(t, doc, succeeding), "verified test evidence requires a test hunk or recognized report")
}

func TestValidateStoredRestrictedSession(t *testing.T) {
	doc := storedExample(t)
	// The example's intent block cites this session through a prompt anchor.
	bundle := &bundlepkg.Bundle{Sessions: []bundlepkg.Session{{RootSessionID: "785faefe-40b5-5521-9932-4dc2d659ec1d", Restricted: true}}}
	requireFinding(t, validateStored(t, doc, bundle), "anchor cites a restricted session")
	for _, finding := range validateStored(t, doc, nil) {
		if strings.Contains(finding, "restricted") {
			t.Errorf("restricted finding without a bundle: %q", finding)
		}
	}
}
