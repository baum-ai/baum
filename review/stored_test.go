package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	bundlepkg "github.com/baum-ai/baum/bundle"
)

func TestShortenIdentifier(t *testing.T) {
	bounded := "rsk_" + strings.Repeat("a", MaxIdentifierLength-len("rsk_"))
	if got := ShortenIdentifier(bounded); got != bounded {
		t.Fatalf("an identifier of %d characters changed: %q", MaxIdentifierLength, got)
	}
	long := "fnd_the_apple_module_is_imported_statically_into_the_login_route_while_the_app_version_used_for_runtime_compatibility_remains_1_5_0"
	short := ShortenIdentifier(long)
	if utf8.RuneCountInString(long) != 131 || utf8.RuneCountInString(short) > MaxIdentifierLength {
		t.Fatalf("%d characters shortened to %d: %q", utf8.RuneCountInString(long), utf8.RuneCountInString(short), short)
	}
	if !regexp.MustCompile(`^fnd_[a-z0-9_]+_[0-9a-f]{12}$`).MatchString(short) || !strings.HasPrefix(long, strings.TrimSuffix(short, short[len(short)-13:])) {
		t.Fatalf("shortened identifier lost its prefix or pattern: %q", short)
	}
	if ShortenIdentifier(long) != short || ShortenIdentifier(short) != short {
		t.Fatal("shortening is not deterministic and idempotent")
	}
	// Truncation alone would make these two equal.
	shared := "rsk_" + strings.Repeat("x", 120)
	if ShortenIdentifier(shared+"_one") == ShortenIdentifier(shared+"_two") {
		t.Fatal("identifiers that share their first characters collide")
	}
	// A cut that ends in underscores drops them, so the result does not
	// double the separator before the hash.
	underscored := "ev_" + strings.Repeat("a", 110) + strings.Repeat("_", 30)
	if got := ShortenIdentifier(underscored); strings.Contains(got, "__") {
		t.Fatalf("separator doubled: %q", got)
	}
}

// Every identifier pattern the stored-document rule recognizes is bounded at
// MaxIdentifierLength, which ShortenIdentifier and the hosts that compare
// stored keys through it assume.
func TestSchemaBoundsIdentifiersAtMaxIdentifierLength(t *testing.T) {
	schema, err := storedSchema()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	var walk func(any, string)
	walk = func(node any, path string) {
		switch n := node.(type) {
		case map[string]any:
			if pattern, ok := n["pattern"].(string); ok && identifierPattern(pattern) != nil {
				found++
				if limit, _ := jsonInt(n["maxLength"]); limit != MaxIdentifierLength {
					t.Errorf("%s: identifier pattern %s has maxLength %v, want %d", path, pattern, n["maxLength"], MaxIdentifierLength)
				}
			}
			for key, child := range n {
				walk(child, path+"/"+key)
			}
		case []any:
			for _, child := range n {
				walk(child, path)
			}
		}
	}
	walk(schema, "")
	if found == 0 {
		t.Fatal("no identifier patterns found")
	}
}

var exampleIdentifier = regexp.MustCompile(`"((?:sec|rsk|ev|fnd|ar|beh|grp|dec|tile)_[a-z0-9_]+)"`)

// lengthen makes an identifier 131 characters or longer, as models wrote
// them before identifiers were bounded.
func lengthen(id string) string {
	return id + "_" + strings.Repeat("written_before_identifiers_were_bounded_", 4)[:max(131-len(id)-1, 0)]
}

// preBoundDocument is the example document as a release before 2026-10-07.1
// could have stored it: every identifier, and every reference to one, over
// 128 characters, plus a long category, tile-local names and two risks whose
// identifiers share their first 124 characters. It returns the document and
// the same document with each long identifier replaced by its shortened form
// wherever it is a whole string value, which is what normalization must
// produce.
func preBoundDocument(t *testing.T) (long, want []byte) {
	t.Helper()
	encoded, err := json.Marshal(storedExample(t))
	if err != nil {
		t.Fatal(err)
	}
	encoded = exampleIdentifier.ReplaceAllFunc(encoded, func(match []byte) []byte {
		return []byte(`"` + lengthen(string(match[1:len(match)-1])) + `"`)
	})
	var doc map[string]any
	if err := json.Unmarshal(encoded, &doc); err != nil {
		t.Fatal(err)
	}
	section := firstSection(doc)
	risks := section["risks"].([]any)
	risks[0].(map[string]any)["category"] = lengthen("retry_storm_category")
	shared := "rsk_" + strings.Repeat("shared_prefix_", 9)[:120]
	// The second risk is one nothing references.
	twin := map[string]any{}
	for key, value := range risks[1].(map[string]any) {
		twin[key] = value
	}
	risks[1].(map[string]any)["id"], twin["id"] = shared+"_one", shared+"_two"
	section["risks"] = append(risks, twin)
	node := lengthen("worker")
	data := section["tiles"].([]any)[0].(map[string]any)["data"].(map[string]any)
	data["containers"].([]any)[0].(map[string]any)["id"] = node
	for _, raw := range data["edges"].([]any) {
		edge := raw.(map[string]any)
		if edge["from"] == "worker" {
			edge["from"] = node
		}
	}
	for _, raw := range data["sequences"].([]any)[0].(map[string]any)["steps"].([]any) {
		if step := raw.(map[string]any); step["from"] == "worker" {
			step["from"] = node
		}
	}
	if long, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	quoted := regexp.MustCompile(`"([a-z][a-z0-9_]{128,})"`)
	want = quoted.ReplaceAllFunc(long, func(match []byte) []byte {
		return []byte(`"` + ShortenIdentifier(string(match[1:len(match)-1])) + `"`)
	})
	if bytes.Equal(long, want) {
		t.Fatal("fixture has no long identifiers")
	}
	return long, want
}

func decodedJSON(t *testing.T, encoded []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestStoredLongIdentifiersAreShortenedConsistently(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	long, want := preBoundDocument(t)
	if err := validator.full.Validate(decodedJSON(t, long)); err == nil || !strings.Contains(err.Error(), "maxLength") {
		t.Fatalf("the current schema should reject the stored document as it is: %v", err)
	}
	normalized, err := ShortenStoredIdentifiers(long)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodedJSON(t, normalized), decodedJSON(t, want)) {
		t.Fatalf("normalization differs from replacing each long identifier with its shortened form:\n%s", normalized)
	}
	if findings := validator.ValidateStored(normalized, nil); findings != nil {
		t.Fatalf("normalized document rejected: %q", findings)
	}
	if findings := validator.ValidateStored(long, nil); findings != nil {
		t.Fatalf("stored document rejected: %q", findings)
	}
	doc := decodedJSON(t, normalized).(map[string]any)
	risks := firstSection(doc)["risks"].([]any)
	if first, last := risks[1].(map[string]any)["id"], risks[len(risks)-1].(map[string]any)["id"]; first == last {
		t.Fatalf("risks sharing a prefix share an identifier: %v", first)
	}
	// The prose that names an identifier is not a reference and is kept.
	if !strings.Contains(string(normalized), "Section sec_fixed_delay contains") {
		t.Fatal("prose naming an identifier changed")
	}
}

func TestShortenStoredIdentifiersKeepsValidDocumentBytes(t *testing.T) {
	encoded, err := json.Marshal(storedExample(t))
	if err != nil {
		t.Fatal(err)
	}
	// Formatting the normalizer would not reproduce shows it returns the
	// input itself.
	var indented bytes.Buffer
	if err := json.Indent(&indented, encoded, "", "  "); err != nil {
		t.Fatal(err)
	}
	got, err := ShortenStoredIdentifiers(indented.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, indented.Bytes()) {
		t.Fatal("a document no rule applies to was re-encoded")
	}
	if _, err := ShortenStoredIdentifiers([]byte(`["` + lengthen("rsk_a") + `"]`)); err == nil {
		t.Fatal("a document that is not an object was accepted")
	}
}

// A stored document from before identifiers were bounded, with 131-character
// and longer finding, risk and evidence identifiers and their references,
// refreshes, and the result is the refresh of the document with shortened
// identifiers.
func TestRefreshStoredLongIdentifiers(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	long, want := preBoundDocument(t)
	for _, id := range []string{lengthen("fnd_lintsage_7202"), lengthen("rsk_api_header"), lengthen("ev_api_curl")} {
		if !bytes.Contains(long, []byte(`"`+id+`"`)) || utf8.RuneCountInString(id) < 131 {
			t.Fatalf("fixture lacks the long identifier %q", id)
		}
	}
	doc := storedExample(t)
	header := doc["review"].(map[string]any)
	bundle := &bundlepkg.Bundle{Repository: "example-org/taskline", PullRequest: bundlepkg.PullRequest{Number: 212, State: "open", HeadSHA: header["head"].(string), BaseSHA: header["base"].(string), MergeBaseSHA: header["merge_base"].(string)}, Sources: bundlepkg.Sources{"diff": true}}
	now := time.Date(2026, 10, 16, 18, 34, 0, 0, time.UTC)
	refreshed, err := validator.Refresh(long, bundle, now)
	if err != nil {
		t.Fatalf("refresh of a stored document with long identifiers failed: %v", err)
	}
	expected, err := validator.Refresh(want, bundle, now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(refreshed, expected) {
		t.Fatalf("refresh differs from the refresh of the shortened document:\n%s\n%s", refreshed, expected)
	}
	if findings := validator.ValidateStored(refreshed, nil); findings != nil {
		t.Fatalf("refreshed document rejected: %q", findings)
	}
	if strings.Contains(string(refreshed), lengthen("rsk_api_header")) {
		t.Fatal("refreshed document kept a long identifier")
	}
	short := ShortenIdentifier(lengthen("rsk_api_header"))
	refreshedDoc := decodedJSON(t, refreshed).(map[string]any)
	for _, raw := range refreshedDoc["sections"].([]any) {
		section := raw.(map[string]any)
		if section["id"] != ShortenIdentifier(lengthen("sec_api")) {
			continue
		}
		reason := section["derived"].(map[string]any)["review_reason"].(map[string]any)
		attention := section["attention"].(map[string]any)["refs"].([]any)
		if reason["risk"] != short || attention[0] != short || section["risks"].([]any)[0].(map[string]any)["id"] != short {
			t.Fatalf("references to the shortened risk differ: reason %v, attention %v", reason["risk"], attention)
		}
		return
	}
	t.Fatal("section sec_api not found")
}

func TestRefreshReportsAStoredDocumentItCannotRepair(t *testing.T) {
	validator, err := sharedValidator()
	if err != nil {
		t.Fatal(err)
	}
	doc := storedExample(t)
	delete(doc, "stats")
	encoded, _ := json.Marshal(doc)
	_, err = validator.Refresh(encoded, &bundlepkg.Bundle{}, time.Now())
	var stored *StoredDocumentError
	if !errors.As(err, &stored) {
		t.Fatalf("error = %v, want a StoredDocumentError", err)
	}
}

func TestPreviousVersionOfShortensStoredIdentifiers(t *testing.T) {
	long, _ := preBoundDocument(t)
	previous, err := PreviousVersionOf(long, 3)
	if err != nil {
		t.Fatal(err)
	}
	var risk struct {
		Risks []struct {
			ID string `json:"id"`
		} `json:"risks"`
	}
	for _, section := range previous.Sections {
		if utf8.RuneCountInString(section.ID) > MaxIdentifierLength {
			t.Fatalf("previous section keeps a long identifier: %q", section.ID)
		}
		if err := json.Unmarshal(section.Content, &risk); err != nil {
			t.Fatal(err)
		}
		for _, r := range risk.Risks {
			if utf8.RuneCountInString(r.ID) > MaxIdentifierLength {
				t.Fatalf("previous section content keeps a long risk identifier: %q", r.ID)
			}
		}
	}
	if previous.Sections[0].ID != ShortenIdentifier(lengthen("sec_retry")) {
		t.Fatalf("first previous section = %q", previous.Sections[0].ID)
	}
}

// Normalization applies only to stored documents: a newly generated one
// whose identifier is over the bound is still rejected.
func TestGenerationStillBoundsIdentifierLength(t *testing.T) {
	validator, bundle, _ := refreshFixture(t)
	for length, tooLong := range map[int]bool{MaxIdentifierLength: false, MaxIdentifierLength + 1: true} {
		doc, err := decodeObject(authoredFixture(), "fixture")
		if err != nil {
			t.Fatal(err)
		}
		section := doc["sections"].([]any)[0].(map[string]any)
		section["risks"] = []any{map[string]any{"id": "rsk_" + strings.Repeat("a", length-len("rsk_")), "tier": "low", "category": "correctness", "source": "model", "text": "A risk.", "finding": nil, "anchors": []any{map[string]any{"type": "hunk", "id": testHunk}}}}
		authored, _ := json.Marshal(doc)
		_, findings := validator.Complete(authored, bundle, "", "fixture-model", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		if got := strings.Contains(findings, "maxLength"); got != tooLong {
			t.Errorf("risk identifier of %d characters: findings %q, want a maxLength finding: %t", length, findings, tooLong)
		}
	}
}
