package adaptive

import (
	"encoding/json"
	"testing"
)

func TestProtocolValidWorkAndPaths(t *testing.T) {
	for _, action := range []string{"catalog", "details", "code", "decisions", "checkpoint", "finish"} {
		data, _ := json.Marshal(Reply{Action: action, Reason: "A valid next step"})
		if _, err := Parse(data); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	for _, p := range []string{"", ".", "../secret", "/tmp/key", "a/../b", "a\\b", "a\x00b"} {
		if ValidPath(p) {
			t.Errorf("unsafe path %q", p)
		}
	}
	for _, p := range []string{"PRODUCT.md", "oss/internal/decisionlog", "a/b.go"} {
		if !ValidPath(p) {
			t.Errorf("literal path refused %q", p)
		}
	}
	for _, data := range []string{`{"action":"unknown","reason":"x"}`, `{"action":"finish","reason":"x","injected":1}`, `{"action":"finish","reason":"x"} {}`} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatalf("accepted invalid reply %s", data)
		}
	}
}
