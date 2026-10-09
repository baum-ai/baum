package redact

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactJSONMasksInsideStringsAndKeepsTheJSONValid(t *testing.T) {
	e := engine(t)
	value := map[string]any{
		"text":   "mail " + email + " and connect to " + plantedURL,
		"quoted": "a \"quoted\" " + email + "\nnext line\té",
		"key":    plantedKey,
		"count":  42,
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	out, summary, err := e.RedactJSON(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Findings == 0 {
		t.Fatal("nothing was found")
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("the redacted text is not JSON: %v\n%s", err, out)
	}
	for _, leaked := range []string{email, "Tr0ub4dor-3xkq", "MIIEpAIBAAKCAQEA7Qx1"} {
		if strings.Contains(string(out), leaked) {
			t.Errorf("%q survived: %s", leaked, out)
		}
	}
	quoted, _ := decoded["quoted"].(string)
	if decoded["count"] != float64(42) || !strings.HasPrefix(quoted, "a \"quoted\" ") ||
		!strings.HasSuffix(quoted, "\nnext line\té") {
		t.Errorf("unmasked content changed: %v", decoded)
	}
}

func TestRedactJSONKeepsTheEscapesASecretSpans(t *testing.T) {
	e := engine(t)
	// The key's newlines are escapes inside one string: the mask must leave
	// each backslash and the letter after it in place.
	data, err := json.Marshal([]string{plantedKey})
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := e.RedactJSON(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []string
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("the redacted text is not JSON: %v\n%s", err, out)
	}
	if strings.Contains(decoded[0], "MIIEpAIBAAKCAQEA7Qx1") || strings.Count(decoded[0], "\n") != 2 {
		t.Errorf("decoded = %q", decoded[0])
	}
}
