package review

import (
	"encoding/json"
	bundlepkg "github.com/baum-ai/baum/bundle"
	"reflect"
	"testing"
	"time"
)

func TestValidationRefresh(t *testing.T) {
	for _, tc := range []struct{ name, state, outcome, class, want string }{
		{"late extraction", "head", "passed", "observed", "observed"},
		{"new commit", "earlier", "passed", "observed", "asserted"},
		{"contradicted", "head", "passed", "contradicted", "failed"},
		{"failed earlier run", "earlier", "failed", "observed", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validator, b, published := refreshFixture(t)
			items := []bundlepkg.SessionValidation{{ID: "val_check", Method: "browser", Checked: "Mobile layout", Outcome: tc.outcome, EvidenceClass: tc.class, CommitLabel: bundlepkg.ValidationCommitLabel{State: tc.state, Reason: "fixture"}, EventIDs: []string{"event"}}}
			b.Sessions[0].Validations = &items
			b.Sessions[0].NotVerified = []bundlepkg.ValidationGap{{What: "Windows"}}
			b.Sessions[0].SetAside = []bundlepkg.ValidationSetAside{{What: "Timeout", StatedReason: "Pre-existing"}}
			refreshed, err := validator.Refresh(published, b, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			var doc, old reviewDocument
			if err := json.Unmarshal(refreshed, &doc); err != nil {
				t.Fatal(err)
			}
			json.Unmarshal(published, &old)
			if got := doc.Sections[0].Evidence[1].Status; got != tc.want {
				t.Fatalf("status %s, want %s", got, tc.want)
			}
			if len(doc.Index.Validation) != 1 || *doc.Review.Sources.SessionValidation != 1 || doc.Stats.Validation.SetAside != 1 || doc.Stats.Validation.NotVerified != 1 {
				t.Fatalf("validation not indexed: %s", refreshed)
			}
			if !reflect.DeepEqual(doc.Sections[0].Summary, old.Sections[0].Summary) || doc.Review.Head != old.Review.Head {
				t.Fatal("authored content changed")
			}
		})
	}
}

func TestValidationRestrictionAndLegacyDocuments(t *testing.T) {
	validator, b, published := refreshFixture(t)
	// The original event has no extracted validation; legacy observed evidence stays valid.
	if _, err := validator.Refresh(published, b, time.Now()); err != nil {
		t.Fatal(err)
	}
	b.Sessions[0].Restricted = true
	items := []bundlepkg.SessionValidation{{Checked: "secret"}}
	b.Sessions[0].Validations = &items
	b.Sessions[0].NotVerified = []bundlepkg.ValidationGap{{What: "secret"}}
	refreshed, err := validator.Refresh(published, b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var doc reviewDocument
	json.Unmarshal(refreshed, &doc)
	if len(doc.Index.Validation) != 0 || doc.Stats.Validation != nil || len(doc.Index.Sessions[0].NotVerified) != 0 {
		t.Fatal("restricted validation leaked")
	}
}

func TestValidationCitationResolvesNonCommandTool(t *testing.T) {
	b := &bundlepkg.Bundle{Sessions: []bundlepkg.Session{{RootSessionID: "s"}}}
	items := []bundlepkg.SessionValidation{{EventIDs: []string{"browser"}}}
	b.Sessions[0].Validations = &items
	if !resolveDocumentAnchor(documentAnchor{Type: "session_event", Session: new("s"), Event: new("browser")}, b, "") {
		t.Fatal("cited browser tool did not resolve")
	}
}

func TestValidationNonCommandEventIsIndexedAtActualTime(t *testing.T) {
	at := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	b := &bundlepkg.Bundle{Sessions: []bundlepkg.Session{{RootSessionID: "s", ValidationEvents: []bundlepkg.SessionEvent{{ID: "browser", Time: &at, Verb: "other", Summary: "browser check"}, {ID: "unknown", Verb: "other"}}}}}
	sessions, _, _ := sessionIndex(b)
	if len(sessions[0].Events) != 1 || sessions[0].Events[0].At != at.Format(time.RFC3339Nano) || sessions[0].Events[0].ID != "browser" {
		t.Fatalf("wrong indexed tool events: %+v", sessions)
	}
}
