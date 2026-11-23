package agentrequests

import (
	"errors"
	"testing"
)

func TestSetSettingsIfSavesNothingWhenRefused(t *testing.T) {
	s, _ := newStore(t)
	refused := errors.New("refused")
	if _, err := s.SetSettingsIf(Settings{CodexApprovals: true}, func(current Settings) error {
		if current.CodexApprovals {
			t.Error("the check read the new setting")
		}
		return refused
	}); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	if s.Settings().CodexApprovals || (&Store{Database: s.Database}).Settings().CodexApprovals {
		t.Fatal("a refused save was kept")
	}
	if _, err := s.SetSettingsIf(Settings{CodexApprovals: true}, func(Settings) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !(&Store{Database: s.Database}).Settings().CodexApprovals {
		t.Fatal("the setting was not saved")
	}
}
