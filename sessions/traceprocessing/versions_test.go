package traceprocessing

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVersions(t *testing.T) {
	unknown := NewVersions()
	unknown.Add("")
	unknown.Add("unknown")
	unknown.Finalize()
	if unknown.Seen["unknown"] != 2 || unknown.Min != "unknown" || unknown.Max != "unknown" {
		t.Fatalf("unknown versions = %+v", unknown)
	}
	versions := NewVersions()
	for _, version := range []string{"unknown", "2.1.10", "2.1.9", "2.1.10"} {
		versions.Add(version)
	}
	versions.Finalize()
	if len(versions.Seen) != 3 || versions.Seen["unknown"] != 1 || versions.Seen["2.1.9"] != 1 || versions.Seen["2.1.10"] != 2 || versions.Min != "2.1.9" || versions.Max != "2.1.10" {
		t.Fatalf("versions = %+v", versions)
	}
	got, _ := json.Marshal(versions)
	if string(got) != `{"seen":{"2.1.10":2,"2.1.9":1,"unknown":1},"min":"2.1.9","max":"2.1.10"}` {
		t.Fatalf("JSON = %s", got)
	}
}

func TestCompareVersions(t *testing.T) {
	overflow := "1" + strings.Repeat("0", 100)
	for _, tt := range []struct {
		left, right string
		want        int
	}{
		{"2.1.9", "2.1.10", -1}, // numeric components
		{"1", "1.0", -1},        // missing component is empty
		{"1.0", "1.00", 0},      // equal numeric spellings
		{"1..2", "1.0.2", -1},   // malformed empty component
		{"1. 2", "1.2", -1},     // malformed whitespace component
		{"1.-2", "1.-1", -1},    // signed numeric components
		{overflow, "2", -1},     // overflow falls back to lexical
		{"2", "10", -1},         // deliberately non-transitive cycle
		{"10", "1a", -1},
		{"1a", "2", -1},
	} {
		if got := compareVersions(tt.left, tt.right); got != tt.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.left, tt.right, got, tt.want)
		}
	}
}
