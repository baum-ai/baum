package cli

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/review"
)

// TestFakeCursorAgentProcess is the fake cursor-agent: the scripts
// InstallFakeCursorAgent writes run this test binary with only this test.
func TestFakeCursorAgentProcess(t *testing.T) { generatetest.RunFakeCursorAgent(t) }

// TestGenerateWithTheCursorAgentBackend runs "baum internal generate --backend
// cursor-agent" with the real backend against a fake cursor-agent on PATH. Its
// first answer is rejected by the validator, its repair holds a secret, and
// what is stored is the repaired document, valid and masked.
func TestGenerateWithTheCursorAgentBackend(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake shell binaries")
	}
	_, stores := generateEnv(t)
	dir, runs := generatetest.InstallFakeCursorAgent(t, "TestFakeCursorAgentProcess",
		generatetest.FakeCursorPass{AttentionRefs: []string{"rsk_absent"}},
		generatetest.FakeCursorPass{SectionSummary: "The change reads " + assembletest.PlantedSecret + " from the settings."})
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	generateBackends = generate.DefaultBackendsFor

	stdout, stderr, err := run(t, append([]string{"internal", "generate", "--pr", "7", "--backend", "cursor-agent"}, stores...)...)
	if err != nil {
		t.Fatalf("generate: %v\n%s", err, stderr)
	}
	if len(runs()) != 2 {
		t.Fatalf("%d cursor-agent runs", len(runs()))
	}
	validator, err := review.NewDocumentValidator()
	if err != nil {
		t.Fatal(err)
	}
	if findings := validator.ValidateStored([]byte(stdout), nil); len(findings) > 0 {
		t.Errorf("the stored document is invalid: %v", findings)
	}
	if strings.Contains(stdout, assembletest.PlantedSecret) || !strings.Contains(stdout, "The change reads") {
		t.Error("the secret was not masked in the stored document")
	}
	var document struct {
		Review struct {
			Generator struct{ Model string } `json:"generator"`
		} `json:"review"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil || document.Review.Generator.Model != "Fake Sonnet" {
		t.Fatalf("document: %v %+v", err, document)
	}
	for _, want := range []string{"running cursor-agent 2026.10.16", "4 call(s), 3000 token(s)"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
}
