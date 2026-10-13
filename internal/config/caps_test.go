package config

import (
	"strings"
	"testing"
)

func TestEffectiveCapsAppliesOverrides(t *testing.T) {
	cfg := Default()
	cfg.Caps = map[string]int{"max_comments": 5}
	caps, err := cfg.EffectiveCaps()
	if err != nil {
		t.Fatal(err)
	}
	if caps["max_comments"] != 5 || caps["max_sessions"] != CapDefaults["max_sessions"] {
		t.Errorf("caps = %v", caps)
	}
	if CapDefaults["max_comments"] == 5 {
		t.Error("the override changed the shared defaults")
	}
}

func TestEffectiveCapsRejectsUnknownAndNegative(t *testing.T) {
	for name, caps := range map[string]map[string]int{
		"unknown":  {"max_sessoins": 3},
		"negative": {"max_sessions": -1},
	} {
		cfg := Default()
		cfg.Caps = caps
		_, err := cfg.EffectiveCaps()
		if err == nil || !strings.Contains(err.Error(), "cap") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
