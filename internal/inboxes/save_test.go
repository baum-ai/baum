package inboxes

import (
	"errors"
	"testing"
)

// API.Save is PUT /api/inboxes' save: stale at another revision, Changed
// after a save only. State.Check refuses what Replace refuses.
func TestSaveAndCheck(t *testing.T) {
	h := newHarness(t)
	changed := 0
	api := &API{Store: Open(h.path), Changed: func() { changed++ }}
	inbox := Inbox{Name: "Mine", Color: "blue", What: "any", Risk: "any", Owners: []string{}, Repositories: []string{}}
	var stale *StaleError
	if _, err := api.Save(5, []Inbox{inbox}); !errors.As(err, &stale) || changed != 0 {
		t.Fatalf("%v, changed %d", err, changed)
	}
	state, err := api.Save(0, []Inbox{inbox})
	if err != nil || state.Revision != 1 || changed != 1 {
		t.Fatalf("%+v %v, changed %d", state, err, changed)
	}
	twice := []Inbox{state.Inboxes[0], inbox}
	var refused *InvalidError
	if err := state.Check(twice); !errors.As(err, &refused) || refused.Code != CodeDuplicateName {
		t.Errorf("a name twice: %v", err)
	}
	if _, err := api.Save(state.Revision, twice); err == nil || err.Error() != state.Check(twice).Error() {
		t.Errorf("Save and Check disagree: %v", err)
	}
	if err := state.Check(nil); err == nil {
		t.Error("an empty list")
	}
	other := inbox
	other.Name = "Other"
	if err := state.Check([]Inbox{state.Inboxes[0], other}); err != nil {
		t.Error(err)
	}
	if after, _ := api.Store.Read(); after.Revision != 1 || len(after.Inboxes) != 1 {
		t.Errorf("Check wrote: %+v", after)
	}
}
