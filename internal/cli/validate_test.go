package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const examplePath = "../../schema/review-example.json"

func TestValidateAcceptsTheExample(t *testing.T) {
	stdout, stderr, err := run(t, "internal", "validate", examplePath)
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, stderr)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("expected no output, got stdout %q stderr %q", stdout, stderr)
	}
}

func TestValidateReportsFindingsOnStderr(t *testing.T) {
	raw, err := os.ReadFile(examplePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "review.json")
	// The first mention of this risk is the attention ref that names it, so
	// the rename leaves the reference dangling.
	broken := strings.Replace(string(raw), `"rsk_retry_herd"`, `"rsk_renamed"`, 1)
	writeFile(t, path, broken)
	stdout, stderr, err := run(t, "internal", "validate", path)
	if err == nil || !strings.Contains(err.Error(), "is invalid") || !strings.Contains(err.Error(), "1 finding") {
		t.Fatalf("err = %v", err)
	}
	if stdout != "" || !strings.Contains(stderr, `attention ref "rsk_renamed"`) {
		t.Errorf("stdout %q stderr %q", stdout, stderr)
	}
}

func TestValidateWithBundleChecksBundleRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundle.json")
	writeFile(t, path, `{}`)
	_, stderr, err := run(t, "internal", "validate", examplePath, "--bundle", path)
	if err == nil {
		t.Fatal("an empty bundle should not match the example")
	}
	if !strings.Contains(stderr, "unknown assigned hunk") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestValidateMissingDocument(t *testing.T) {
	if _, _, err := run(t, "internal", "validate", filepath.Join(t.TempDir(), "absent.json")); err == nil || !strings.Contains(err.Error(), "read document") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateBadBundleNamesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundle.json")
	writeFile(t, path, "not json")
	_, _, err := run(t, "internal", "validate", examplePath, "--bundle", path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateNeedsOneDocument(t *testing.T) {
	if _, _, err := run(t, "internal", "validate"); err == nil || !strings.Contains(err.Error(), "accepts 1 arg") {
		t.Fatalf("validate without a document: %v", err)
	}
}
