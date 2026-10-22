package decisions

import (
	"testing"

	"github.com/google/uuid"
)

func TestDecisionValidate(t *testing.T) {
	valid := Decision{Summary: "s", Category: CategoryScope, Who: WhoAgent, Turns: []int{1, 3}, Files: []string{"platform/a.go"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid decision: %v", err)
	}
	full := valid
	full.Trigger, full.DecidedTurn, full.Evidence, full.EvidenceTurn = TriggerFailure, 3, "use a gate", 1
	if err := full.Validate(); err != nil {
		t.Fatalf("decision with trigger, decided turn and evidence: %v", err)
	}
	for name, mutate := range map[string]func(*Decision){
		"empty summary":    func(d *Decision) { d.Summary = " " },
		"unknown who":      func(d *Decision) { d.Who = "team" },
		"unknown category": func(d *Decision) { d.Category = "misc" },
		"no turns":         func(d *Decision) { d.Turns = nil },
		"unordered turns":  func(d *Decision) { d.Turns = []int{3, 1} },
		"repeated turn":    func(d *Decision) { d.Turns = []int{1, 1} },
		"absolute file":    func(d *Decision) { d.Files = []string{"/home/dev/platform/a.go"} },
		"escaping file":    func(d *Decision) { d.Files = []string{"../a.go"} },
		"unclean file":     func(d *Decision) { d.Files = []string{"./a.go"} },
		"dot file":         func(d *Decision) { d.Files = []string{"."} },
		"turn zero":        func(d *Decision) { d.Turns = []int{0, 1} },
		"unknown trigger":  func(d *Decision) { d.Trigger = "boss" },
		"uncited decided":  func(d *Decision) { d.DecidedTurn = 2 },
		"evidence no turn": func(d *Decision) { d.Evidence = "use a gate" },
		"turn no evidence": func(d *Decision) { d.EvidenceTurn = 1 },
		"uncited evidence": func(d *Decision) { d.Evidence, d.EvidenceTurn = "use a gate", 2 },
	} {
		d := valid
		mutate(&d)
		if d.Validate() == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestIdentifiedKeepsExistingIDs(t *testing.T) {
	decisions := []Decision{{Summary: "kept", ID: "0f9e8d7c-0000-4000-8000-000000000001"}, {Summary: "new"}, {Summary: "other"}}
	got := Identified(decisions)
	if got[0].ID != decisions[0].ID {
		t.Errorf("existing ID changed to %q", got[0].ID)
	}
	if _, err := uuid.Parse(got[1].ID); err != nil || got[1].ID == got[2].ID {
		t.Errorf("new IDs = %q, %q", got[1].ID, got[2].ID)
	}
	if decisions[1].ID != "" {
		t.Error("Identified changed its argument")
	}
}
