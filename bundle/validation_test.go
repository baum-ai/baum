package bundle

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/goldentest"
)

func TestValidationBundleGolden(t *testing.T) {
	for _, name := range []string{"extracted", "not-extracted", "restricted", "trimmed"} {
		t.Run(name, func(t *testing.T) {
			items := []SessionValidation{
				{ID: "val_head", Method: "browser", Checked: "Mobile layout", Outcome: "passed", EvidenceClass: "observed", CommitLabel: ValidationCommitLabel{State: "head", SHA: "aaaaaaa", Reason: "No intervening edits"}, EventIDs: []string{"browser"}},
				{ID: "val_old", Method: "test_run", Checked: "Old tests", Outcome: "inconclusive", EvidenceClass: "observed", CommitLabel: ValidationCommitLabel{State: "earlier", SHA: "bbbbbbb", Reason: "Earlier revision"}, EventIDs: []string{"test"}},
			}
			b := &Bundle{Sessions: []Session{{RootSessionID: "s", Validations: &items, NotVerified: []ValidationGap{{What: "Windows"}}, SetAside: []ValidationSetAside{{What: "Timeout", StatedReason: "Unrelated"}}}}}
			switch name {
			case "not-extracted":
				b.Sessions[0].Validations = nil
				b.Sessions[0].NotVerified = nil
				b.Sessions[0].SetAside = nil
				b.Sessions[0].ValidationUnavailable = "over_cost_cap"
			case "restricted":
				b.Sessions[0].Restricted = true
			case "trimmed":
				cutLowerPriorityValidations(b)
			}
			redactor, err := NewRedactor()
			if err != nil {
				t.Fatal(err)
			}
			if err := Redact(context.Background(), b, redactor); err != nil {
				t.Fatal(err)
			}
			goldentest.Golden(t, filepath.Join("testdata", "validation", name+".json"), b.Sessions)
		})
	}
}

func TestValidationTextRedactionAndSessionCap(t *testing.T) {
	redactor, err := NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	text := "Contact person@example.com"
	items := []SessionValidation{{Checked: text, Caveats: text, CommitLabel: ValidationCommitLabel{Reason: text}}}
	b := &Bundle{Sessions: []Session{{RootSessionID: "keep", Confidence: 1, Validations: &items, NotVerified: []ValidationGap{{What: text}}, SetAside: []ValidationSetAside{{What: text, StatedReason: text}}, ValidationEvents: []SessionEvent{{ID: "event", Summary: text}}}, {RootSessionID: "drop", Confidence: 0, Validations: &items}}}
	ApplyCaps(b, Caps{MaxSessions: 1})
	if len(b.Sessions) != 1 || b.Sessions[0].RootSessionID != "keep" {
		t.Fatal("session cap failed")
	}
	if err := Redact(context.Background(), b, redactor); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{items[0].Checked, items[0].Caveats, items[0].CommitLabel.Reason, b.Sessions[0].NotVerified[0].What, b.Sessions[0].SetAside[0].What, b.Sessions[0].SetAside[0].StatedReason, b.Sessions[0].ValidationEvents[0].Summary} {
		if value == text {
			t.Fatal("validation text escaped redaction")
		}
	}
}
