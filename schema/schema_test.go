package schema

import (
	"errors"
	"os"
	"regexp"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestRevision(t *testing.T) {
	rev, err := Revision()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.\d+$`).MatchString(rev) {
		t.Errorf("revision %q is not date-based", rev)
	}
}

func TestCompileRecordsID(t *testing.T) {
	compiled, err := Compile()
	if err != nil {
		t.Fatal(err)
	}
	if compiled.ID != URL {
		t.Errorf("$id = %q, want %q", compiled.ID, URL)
	}
}

func TestExampleValidates(t *testing.T) {
	example := readInstance(t, "review-example.json")
	if err := Validate(example); err != nil {
		var verr *jsonschema.ValidationError
		if errors.As(err, &verr) {
			t.Fatalf("review-example.json does not validate:\n%s", verr.GoString())
		}
		t.Fatal(err)
	}
	doc, _ := example.(map[string]any)
	if got := doc["schema"]; got != Name {
		t.Errorf("example schema field = %v, want %q", got, Name)
	}
}

func TestValidateRejectsWrongSchemaName(t *testing.T) {
	example := readInstance(t, "review-example.json")
	doc := example.(map[string]any)
	doc["schema"] = "baum.review/0"
	if err := Validate(doc); err == nil {
		t.Fatal("a document with another schema name must not validate")
	}
}

func readInstance(t *testing.T, name string) any {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatal(err)
	}
	return instance
}
